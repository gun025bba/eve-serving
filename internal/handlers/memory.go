package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/store"
)

// Personalization memory endpoints (docs/architecture/memory.md §4). They
// exist only when memory.enabled is true; otherwise every path answers the
// same 404 as before this feature.
//
//	GET    /v1/memory              all items (no-store, no CORS)
//	POST   /v1/memory/items        add (user typed: confidence "user")
//	PATCH  /v1/memory/items/{id}   text / pinned / expires_at
//	DELETE /v1/memory/items/{id}   delete (tombstone)
//	DELETE /v1/memory              delete all
//	GET    /v1/memory/export       JSON download (audited, no-store)
//
// The user is always the authenticated identity (never a path/body field).
// Writes go through CSRF + exact Origin checks in authed(). Item text never
// reaches logs or audit rows: audit carries ids and an HMAC only.

func (s *Server) memoryOn() bool {
	_, ok := s.Store.(store.UserIDResolver)
	return s.Cfg.Memory.Enabled && s.Memory != nil && ok
}

// memUID resolves the authenticated login to users.id, the only key the
// memory store accepts. On failure it returns 0, which the store rejects
// with ErrBadUserID before touching disk (-> 500 internal_error).
func (s *Server) memUID(r *http.Request) int64 {
	res, ok := s.Store.(store.UserIDResolver)
	if !ok {
		return 0
	}
	id, err := res.UserID(r.Context(), user(r))
	if err != nil {
		s.Log.Error("memory_user_id_failed", "request_id", rid(r), "err", err.Error())
		return 0
	}
	return id
}

// noStore sets Cache-Control: no-store before the handler runs, so error
// responses (4xx/5xx) and 204s carry it too, not just success bodies.
func noStore(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h(w, r)
	}
}

func (s *Server) memoryRoutes(mux *http.ServeMux) {
	if !s.memoryOn() {
		return
	}
	mux.HandleFunc("GET /v1/memory", s.authed(noStore(s.browserOnly(s.getMemory))))
	mux.HandleFunc("DELETE /v1/memory", s.authed(noStore(s.browserOnly(s.deleteAllMemory))))
	mux.HandleFunc("GET /v1/memory/export", s.authed(noStore(s.browserOnly(s.exportMemory))))
	mux.HandleFunc("POST /v1/memory/items", s.authed(noStore(s.browserOnly(s.addMemoryItem))))
	mux.HandleFunc("PATCH /v1/memory/items/{id}", s.authed(noStore(s.browserOnly(s.patchMemoryItem))))
	mux.HandleFunc("DELETE /v1/memory/items/{id}", s.authed(noStore(s.browserOnly(s.deleteMemoryItem))))
	// Approve exists only while memory is on (off: same 404 as before).
	mux.HandleFunc("POST /v1/actions/{id}/approve", s.persisted(noStore(s.approveAction)))
}

// browserOnly refuses /v1/memory* to a principal without the interactive
// scope (e.g. an API key) -> 403 scope_denied; memory is fixed off for it.
func (s *Server) browserOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireScope(w, r, auth.ScopeInteractive) {
			return
		}
		h(w, r)
	}
}

func (s *Server) memHMAC(text string) string {
	m := hmac.New(sha256.New, s.MemoryAuditKey)
	m.Write([]byte(text))
	return "hmac-sha256:" + hex.EncodeToString(m.Sum(nil))
}

// memAudit writes the audit row first; a failure refuses the write
// (fail-closed, memory.md §5 S5-M4).
func (s *Server) memAudit(r *http.Request, ev, status string, ids []string, hmacHex string) bool {
	if s.Audit == nil {
		return true
	}
	e := audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: ev, Tool: memory.ToolPropose, Scope: memory.ScopeWrite,
		Status: status, PayloadSHA256: hmacHex, Detail: map[string]string{"via": "ui"}}
	if len(ids) > 0 {
		e.Detail["item_ids"] = strings.Join(ids, ",")
	}
	if err := s.Audit.Append(r.Context(), user(r), e); err != nil {
		s.M.AuditWriteFail.Inc()
		s.Log.Error("audit_write_failed", "event", ev, "err", err.Error())
		return false
	}
	return true
}

func (s *Server) memErr(w http.ResponseWriter, r *http.Request, err error) {
	var rej *memory.RejectError
	switch {
	case errors.As(err, &rej):
		s.writeCode(w, r, rejectCode(rej))
	case errors.Is(err, memory.ErrNotFound):
		s.writeCode(w, r, "not_found")
	case errors.Is(err, memory.ErrConflict):
		s.writeCode(w, r, "conflict")
	case errors.Is(err, memory.ErrTooMany), errors.Is(err, memory.ErrFileTooLarge), errors.Is(err, memory.ErrTooLong):
		s.writeCode(w, r, "cap_exceeded")
	case errors.Is(err, memory.ErrInvalid), errors.Is(err, memory.ErrEmpty), errors.Is(err, memory.ErrMarkup):
		s.writeCode(w, r, "invalid")
	case errors.Is(err, memory.ErrUnsafeFile):
		w.Header().Set("Cache-Control", "no-store")
		s.fail(w, r, apierr.New(503, "memory_read_only", "Memory is read-only until it is restored", "server_error"))
	default:
		w.Header().Set("Cache-Control", "no-store")
		s.Log.Error("memory_error", "request_id", rid(r), "err", logTrunc(err.Error(), 200))
		s.fail(w, r, apierr.New(500, "internal_error", "Memory operation failed", "server_error"))
	}
}

type memoryView struct {
	Object    string        `json:"object"`
	UpdatedAt *time.Time    `json:"updated_at"`
	Items     []memory.Item `json:"items"`
}

func (s *Server) loadView(r *http.Request) (memoryView, error) {
	d, err := s.Memory.Load(s.memUID(r))
	if err != nil {
		return memoryView{}, err
	}
	v := memoryView{Object: "memory", Items: d.Items}
	if !d.UpdatedAt.IsZero() {
		v.UpdatedAt = &d.UpdatedAt
	}
	return v, nil
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadView(r)
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	writeNoStore(w, 200, v)
}

func (s *Server) exportMemory(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadView(r)
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	if !s.memAudit(r, "memory_export", "ok", nil, "") {
		s.memErr(w, r, errors.New("audit unavailable"))
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="memory.json"`)
	writeNoStore(w, 200, v)
}

func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		return false
	}
	return true
}

func (s *Server) addMemoryItem(w http.ResponseWriter, r *http.Request) {
	// user memory off: read/edit/delete/export stay available; new items
	// are refused (same as propose/approve).
	if !s.memoryFor(r) {
		s.writeCode(w, r, "kill_switch")
		return
	}
	var b struct {
		Kind      string     `json:"kind"`
		Text      string     `json:"text"`
		Pinned    *bool      `json:"pinned"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !decodeStrict(w, r, &b) {
		s.writeCode(w, r, "invalid")
		return
	}
	t, err := memory.Clean(b.Text)
	if err != nil {
		var rej *memory.RejectError
		if errors.As(err, &rej) {
			s.memAudit(r, "memory_write_denied", "denied", nil, "")
		}
		s.memErr(w, r, err)
		return
	}
	if !s.memAudit(r, "memory_add", "ok", nil, s.memHMAC(t)) {
		s.memErr(w, r, errors.New("audit unavailable"))
		return
	}
	it, err := memory.Add(s.Memory, s.memUID(r), memory.NewItem{Kind: b.Kind, Text: t, Pinned: b.Pinned, Confidence: "user", ExpiresAt: b.ExpiresAt}, time.Now())
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	writeNoStore(w, 201, it)
}

func (s *Server) patchMemoryItem(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if !decodeStrict(w, r, &raw) {
		s.writeCode(w, r, "invalid")
		return
	}
	var p memory.Patch
	for k, v := range raw {
		var err error
		switch k {
		case "text":
			err = json.Unmarshal(v, &p.Text)
		case "pinned":
			err = json.Unmarshal(v, &p.Pinned)
		case "expires_at":
			if string(v) == "null" {
				p.ClearExpires = true
			} else {
				err = json.Unmarshal(v, &p.ExpiresAt)
			}
		case "if_updated_at":
			err = json.Unmarshal(v, &p.IfUpdatedAt)
		default:
			err = errors.New("unknown field")
		}
		if err != nil {
			s.writeCode(w, r, "invalid")
			return
		}
	}
	h := ""
	if p.Text != nil {
		t, err := memory.Clean(*p.Text)
		if err != nil {
			s.memErr(w, r, err)
			return
		}
		h = s.memHMAC(t)
	}
	id := r.PathValue("id")
	if !s.memAudit(r, "memory_edit", "ok", []string{id}, h) {
		s.memErr(w, r, errors.New("audit unavailable"))
		return
	}
	it, err := memory.Edit(s.Memory, s.memUID(r), id, p, time.Now())
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	writeNoStore(w, 200, it)
}

func (s *Server) deleteMemoryItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.Memory.Load(s.memUID(r))
	if err != nil {
		s.memErr(w, r, err)
		return
	}
	found := false
	for _, it := range d.Items {
		found = found || it.ID == id
	}
	if !found {
		s.writeCode(w, r, "not_found")
		return
	}
	if !s.memAudit(r, "memory_delete", "ok", []string{id}, "") {
		s.memErr(w, r, errors.New("audit unavailable"))
		return
	}
	if err := memory.Delete(s.Memory, s.memUID(r), []string{id}, time.Now()); err != nil {
		s.memErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}

func (s *Server) deleteAllMemory(w http.ResponseWriter, r *http.Request) {
	if !s.memAudit(r, "memory_delete_all", "ok", nil, "") {
		s.memErr(w, r, errors.New("audit unavailable"))
		return
	}
	if err := s.Memory.Purge(s.memUID(r)); err != nil {
		s.memErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}

// injectMemory appends the user's memory block to the end of the system
// prompt of the forwarded request. Any error injects nothing (fail-closed).
// Proactive/scheduler runs do not come through here.
func (s *Server) injectMemory(r *http.Request, body map[string]any) {
	if !s.memoryActive(r) {
		return
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return
	}
	d, err := s.Memory.Load(s.memUID(r))
	if err != nil {
		s.Log.Warn("memory_inject_skipped", "request_id", rid(r), "reason", "load_failed")
		return
	}
	limit := s.Cfg.Memory.InjectCapTokens
	if limit == 0 {
		limit = memory.DefaultCapTokens
	}
	blk, err := memory.Block(d.Items, time.Now(), limit)
	if err != nil || blk == "" {
		return
	}
	// The block must be the very last text of the system prompt: append it to
	// the LAST message of the leading run of system messages, so no client
	// system text follows it. (Web mode later prepends tools.SystemPrompt;
	// tool output never enters a system message.)
	last := -1
	for i, mm := range msgs {
		m, ok := mm.(map[string]any)
		if !ok || m["role"] != "system" {
			break
		}
		last = i
	}
	if last < 0 {
		body["messages"] = append([]any{map[string]any{"role": "system", "content": blk}}, msgs...)
		return
	}
	m := msgs[last].(map[string]any)
	c, ok := m["content"].(string)
	if !ok {
		return // structured system content: do not touch (fail-closed)
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	cp["content"] = c + "\n\n" + blk
	out := append([]any{}, msgs...)
	out[last] = cp
	body["messages"] = out
}

// reviseMemoryCheck re-runs memory.propose creation validations on the
// successor payload before a revise is accepted (ui-api-contract §3.1).
// Returns "" when memory is off, the proposal is not a memory card, or it passes.
func (s *Server) reviseMemoryCheck(r *http.Request, id string, itemIDs []string) string {
	if !s.memoryOn() {
		return "" // feature off: revise behaves exactly as before
	}
	acts, err := s.Store.ListPendingActions(r.Context(), user(r))
	if err != nil {
		return ""
	}
	for _, a := range acts {
		if a.ID != id || a.Tool != memory.ToolPropose {
			continue
		}
		if !s.memoryFor(r) {
			return "kill_switch" // per-user memory.enabled off
		}
		p, err := store.FilterItems(a.Payload, itemIDs)
		if err != nil {
			return "invalid"
		}
		actor := a.Provenance.Actor
		if actor == "" {
			actor = "user"
		}
		err = memory.Revalidate(s.Memory, s.memUID(r), p, memory.ProposalCtx{Actor: actor, Tainted: a.Tainted})
		var rej *memory.RejectError
		switch {
		case err == nil:
			return ""
		case errors.Is(err, memory.ErrNotFound):
			return "item_gone"
		case errors.Is(err, memory.ErrTainted):
			return "tainted"
		case errors.Is(err, memory.ErrScheduler):
			return "scope_denied"
		case errors.As(err, &rej):
			return rejectCode(rej)
		default:
			return "invalid"
		}
	}
	return ""
}

// rejectCode keeps sensitive-category refusals distinguishable from secret
// refusals: 422 sensitive_category vs 422 secret_detected. Neither echoes
// the matched text or the category name (§3.9.4).
func rejectCode(rej *memory.RejectError) string {
	if strings.HasPrefix(rej.Reason, "sensitive_") {
		return "sensitive_category"
	}
	return "secret_detected"
}
