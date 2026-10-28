package handlers

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/pim"
	"github.com/gun025bba/eve-serving/internal/store"
)

// Model-side memory.propose (memory.md §2, ui-api-contract §3.9.4–§3.9.5).
// The tool never writes: it creates a pending_actions card; the write only
// happens on POST /v1/actions/{id}/approve with a session token.

// withMemoryTool appends the memory_propose definition while memory is on;
// otherwise defs are returned unchanged (request bodies identical to before).
func (s *Server) withMemoryTool(defs []any, on bool) []any {
	if !on {
		return defs
	}
	return append(append([]any{}, defs...), memory.ToolDef())
}

// threadTaintKey is a server-only sticky flag in threads.settings, set when a
// web run is saved for the thread. It is never cleared (web retention purges
// sources, the flag stays) and is stripped from API responses.
const threadTaintKey = "memory_tainted"

// taintFallback holds threads whose sticky flag could not be written
// (fail-closed, process-local). Known limit (security Low #44): this set is
// not persisted, so it is lost on restart. After a restart taint is
// re-derived from persisted state (web sources, proactive messages, tool
// history); only a thread whose sole taint signal was a failed flag write
// in the previous process loses it. Persisting would need the same DB that
// just failed, so it is documented rather than added.
var taintFallback sync.Map

func (s *Server) markThreadTainted(r *http.Request, login, threadID string) {
	ts, ok := s.Store.(store.ThreadSettingsStore)
	if !ok || threadID == "" {
		return
	}
	err := ts.SetThreadSettings(r.Context(), login, threadID, json.RawMessage(`{"`+threadTaintKey+`":true}`))
	if err != nil {
		// retry once, then fail closed: remember the thread as tainted in
		// process memory so proposals stay denied even without the flag.
		err = ts.SetThreadSettings(r.Context(), login, threadID, json.RawMessage(`{"`+threadTaintKey+`":true}`))
	}
	if err != nil {
		taintFallback.Store(login+"\x00"+threadID, true)
		s.Log.Error("memory_taint_flag_failed", "request_id", rid(r))
	}
}

// memoryOffered: memory_propose is offered when the user's memory is active
// and the request history can be checked for untrusted web content. Without a
// server-side history (no thread_id, or thread_id "new": the thread is created
// by this very request, so every assistant turn in the body is client-supplied
// and nothing is stored, no sources, no sticky taint flag) the gateway cannot tell whether an earlier assistant turn the
// client sends back carries web content, so any such turn means "not offered"
// (fail-closed). The first message of a stateless conversation still offers it.
// The memory block itself is still injected (read-only).
func (s *Server) memoryOffered(r *http.Request, body map[string]any, pr *chatPersist) bool {
	if !s.memoryActive(r) {
		return false
	}
	if pr != nil && pr.threadID != "" && !pr.newThread {
		return true
	}
	msgs, _ := body["messages"].([]any)
	for _, mm := range msgs {
		if m, ok := mm.(map[string]any); ok && m["role"] == "assistant" {
			return false
		}
	}
	return true
}

// historyTainted decides taint from server state plus the request history:
// tool messages / tool_calls in the request; for persisted threads the
// sticky flag, or any message (all pages) with origin proactive or web
// sources. Lookup errors count as tainted. reasons lists every source.
func (s *Server) historyTainted(r *http.Request, msgs []any, pr *chatPersist) (bool, []string) {
	var reasons []string
	for i, mm := range msgs {
		m, ok := mm.(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "tool" || m["tool_calls"] != nil {
			reasons = append(reasons, fmt.Sprintf("request:msg:%d", i))
		}
	}
	if pr == nil || pr.threadID == "" {
		return len(reasons) > 0, reasons
	}
	if _, ok := taintFallback.Load(user(r) + "\x00" + pr.threadID); ok {
		reasons = append(reasons, "thread:flag_fallback")
	}
	if ts, ok := s.Store.(store.ThreadSettingsStore); ok {
		raw, err := ts.ThreadSettings(r.Context(), user(r), pr.threadID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return true, append(reasons, "lookup_error")
		}
		var st map[string]any
		if json.Unmarshal(raw, &st) == nil && st[threadTaintKey] == true {
			reasons = append(reasons, "thread:flag")
		}
	}
	ws, hasWS := s.Store.(store.WebStore)
	before := ""
	for page := 0; ; page++ {
		if page >= 1000 {
			return true, append(reasons, "scan_limit")
		}
		ms, more, err := s.Store.ListMessages(r.Context(), user(r), pr.threadID, 500, before)
		if errors.Is(err, store.ErrNotFound) && before == "" {
			break
		}
		if err != nil {
			return true, append(reasons, "lookup_error")
		}
		var ids []string
		for _, m := range ms {
			if m.Origin == store.OriginProactive {
				reasons = append(reasons, "thread:proactive:"+m.ID)
			}
			for _, src := range m.Sources {
				reasons = append(reasons, "thread:source:"+src.URL)
			}
			ids = append(ids, m.ID)
		}
		if hasWS && len(ids) > 0 {
			src, err := ws.Sources(r.Context(), user(r), ids)
			if err != nil {
				return true, append(reasons, "lookup_error")
			}
			for _, v := range src {
				for _, x := range v {
					reasons = append(reasons, "thread:source:"+x.URL)
				}
			}
		}
		if !more || len(ms) == 0 {
			break
		}
		before = ms[0].ID
	}
	return len(reasons) > 0, reasons
}

// taintDetail summarizes taint sources for audit (count + hash only).
func taintDetail(reasons []string) map[string]string {
	if len(reasons) == 0 {
		return nil
	}
	sorted := append([]string(nil), reasons...)
	sort.Strings(sorted)
	h := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return map[string]string{"taint_source_count": strconv.Itoa(len(reasons)), "taint_sources_sha256": hex.EncodeToString(h[:])}
}

func toolResult(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// runMemoryPropose handles one model memory_propose call and returns the
// tool result for the model. Cards go out as SSE confirmation events.
func (s *Server) runMemoryPropose(r *http.Request, pr *chatPersist, args string, taintReasons []string, emit func(map[string]any)) string {
	tainted := len(taintReasons) > 0
	deny := func(code string) string {
		d := map[string]string{"error_code": code}
		for k, v := range taintDetail(taintReasons) {
			d[k] = v
		}
		emit(map[string]any{"object": "chat.completion.confirmation", "tool": memory.ToolPropose, "status": "denied", "error_code": code})
		s.appendAudit(r.Context(), user(r), audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: "denied",
			Tool: memory.ToolPropose, Scope: memory.ScopeWrite, Tainted: &tainted, Status: "denied", Detail: d})
		return toolResult(map[string]any{"status": "denied", "error_code": code})
	}
	a, err := memory.ParseToolArgs(args)
	if err != nil {
		return deny("invalid")
	}
	c := memory.ProposalCtx{Actor: "user", Tainted: tainted}
	if pr != nil {
		c.ThreadID = pr.threadID
	}
	now := time.Now()
	var card store.Action
	switch a.Op {
	case "add":
		card, err = memory.AddProposal(a.Text, a.Kind, c, now)
	case "forget":
		if err = c.Check(); err == nil {
			var d memory.Doc
			if d, err = s.Memory.Load(s.memUID(r)); err == nil {
				ids := memory.MatchForget(d.Items, a.Query, now)
				card, err = memory.ForgetProposal(s.Memory, s.memUID(r), ids, len(ids) > 1, c, now)
			}
		}
	}
	var rej *memory.RejectError
	switch {
	case err == nil:
	case errors.Is(err, memory.ErrNoMatch):
		return toolResult(map[string]any{"status": "no_match"}) // no card (Q16)
	case errors.Is(err, memory.ErrTainted):
		return deny("tainted")
	case errors.Is(err, memory.ErrScheduler):
		return deny("scope_denied")
	case errors.As(err, &rej):
		return deny(rejectCode(rej))
	case errors.Is(err, memory.ErrTooLong), errors.Is(err, memory.ErrTooMany):
		return deny("cap_exceeded")
	default:
		return deny("invalid")
	}
	saved, err := s.Store.CreateAction(r.Context(), user(r), card)
	if err != nil {
		return deny("invalid")
	}
	s.actionAudit(r, "action_proposed", saved)
	v := view(saved)
	b, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(b, &m)
	emit(m)
	return toolResult(map[string]any{"status": "pending_confirmation", "action_id": saved.ID})
}

// approveAction: POST /v1/actions/{id}/approve {confirm_token, payload_hash}.
// Registered while memory is on or pim writes are on; only memory.propose
// and (when GW_PIM_WRITES_ENABLED) reminders_add/reminders_complete/calendar_add_event cards execute.
func (s *Server) approveAction(w http.ResponseWriter, r *http.Request) {
	if s.denyAPIKey(w, r) {
		return
	}
	var b struct {
		ConfirmToken string `json:"confirm_token"`
		PayloadHash  string `json:"payload_hash"`
	}
	if !decodeStrict(w, r, &b) || b.ConfirmToken == "" || b.PayloadHash == "" {
		s.writeCode(w, r, "invalid")
		return
	}
	ap, ok := s.Store.(store.ActionApprover)
	if !ok {
		s.writeCode(w, r, "not_found")
		return
	}
	id := r.PathValue("id")
	now := s.now()
	acts, err := s.Store.ListPendingActions(r.Context(), user(r))
	if err != nil {
		s.actionErr(w, r, err)
		return
	}
	var a *store.Action
	for i := range acts {
		if acts[i].ID == id {
			a = &acts[i]
		}
	}
	if a == nil {
		// Unknown or foreign: same 404 body (§3.7). Own but already
		// decided (approved/rejected/voided): 409 conflict (§3.9.5).
		st, err := ap.ActionStatus(r.Context(), user(r), id)
		switch {
		case err == nil && st == "expired":
			s.writeCode(w, r, "expired")
		case err == nil:
			s.writeCode(w, r, "conflict")
		default:
			s.writeCode(w, r, "not_found")
		}
		return
	}
	if !now.Before(a.ExpiresAt) || a.TokenSHA256 == nil || a.TokenExpiresAt == nil || !now.Before(*a.TokenExpiresAt) ||
		subtle.ConstantTimeCompare([]byte(store.TokenHash(b.ConfirmToken)), []byte(*a.TokenSHA256)) != 1 {
		s.writeCode(w, r, "expired") // expired, never issued, replaced or wrong token
		return
	}
	if subtle.ConstantTimeCompare([]byte(b.PayloadHash), []byte(a.PayloadHash)) != 1 || !store.PayloadHashMatches(a.Payload, a.PayloadHash) {
		s.writeCode(w, r, "payload_mismatch")
		return
	}
	if pim.IsWriteTool(a.Tool) {
		// Jarvis M1 #70: same card, token and payload-hash checks as above;
		// the executor is the eve-pim helper instead of the memory file.
		s.approvePimWrite(w, r, ap, a, now)
		return
	}
	if a.Tool != memory.ToolPropose {
		s.writeCode(w, r, "scope_denied")
		return
	}
	// per-user switch: effective = system.memory_available AND memory.enabled
	if !s.memoryFor(r) {
		s.writeCode(w, r, "kill_switch")
		return
	}
	pc := memory.ProposalCtx{Actor: a.Provenance.Actor, Tainted: a.Tainted, ActionID: a.ID, ActionCreated: a.CreatedAt}
	if pc.Actor == "" {
		pc.Actor = "user"
	}
	// An executed action stays 'approved' (no separate status value, no
	// migration). That state, not the item tombstones, is what blocks a
	// second Execute: tombstones expire after TombstoneTTL or a Purge, the
	// action state does not (security Low #52). Checked before Revalidate
	// and the memory_approve "ok" audit, so a refused replay is not logged as ok.
	if st, err := ap.ActionStatus(r.Context(), user(r), id); err == nil && st != "pending" {
		s.writeCode(w, r, "conflict")
		return
	}
	if code := memValidateCode(memory.Revalidate(s.Memory, s.memUID(r), a.Payload, pc)); code != "" {
		if code == "_internal" {
			s.memErr(w, r, errors.New("revalidate failed"))
			return
		}
		s.writeCode(w, r, code)
		return
	}
	var p memory.Payload
	json.Unmarshal(a.Payload, &p)
	var ids []string
	for _, it := range p.Items {
		if it.ID != "" {
			ids = append(ids, it.ID)
		}
	}
	if !s.memAudit(r, "memory_approve", "ok", ids, s.payloadHMAC(p)) {
		s.memErr(w, r, errors.New("audit unavailable"))
		return
	}
	if err := ap.ApproveAction(r.Context(), user(r), id, *a.TokenSHA256, now); err != nil {
		s.actionErr(w, r, err)
		return
	}
	approved := *a
	approved.Status = "approved"
	s.actionAudit(r, "approved", approved)
	var src *memory.Source
	if a.ThreadID != nil {
		src = &memory.Source{ThreadID: *a.ThreadID}
		if a.MessageID != nil {
			src.MessageID = *a.MessageID
		}
	}
	out, err := memory.Execute(s.Memory, s.memUID(r), a.Payload, pc, src, now)
	if err != nil {
		// never leave a card consumed but unexecuted
		if rerr := ap.ReleaseAction(r.Context(), user(r), id); rerr != nil {
			s.Log.Error("memory_approve_release_failed", "request_id", rid(r), "action_id", id)
		}
		if code := memValidateCode(err); code != "" && code != "_internal" {
			s.writeCode(w, r, code)
			return
		}
		s.memErr(w, r, err)
		return
	}
	if out == nil {
		out = []string{}
	}
	writeNoStore(w, 200, map[string]any{"status": "executed", "action_id": id, "result": map[string]any{"op": p.Op, "item_ids": out}})
}

// payloadHMAC hashes add texts (never stored in audit in clear).
func (s *Server) payloadHMAC(p memory.Payload) string {
	var t []string
	for _, it := range p.Items {
		t = append(t, it.Text)
	}
	if strings.Join(t, "") == "" {
		return ""
	}
	return s.memHMAC(strings.Join(t, "\n"))
}

// memValidateCode maps memory validation errors to the contract enum.
func memValidateCode(err error) string {
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
	case errors.Is(err, memory.ErrTooMany), errors.Is(err, memory.ErrTooLong), errors.Is(err, memory.ErrFileTooLarge):
		return "cap_exceeded"
	case errors.Is(err, memory.ErrInvalid), errors.Is(err, memory.ErrEmpty), errors.Is(err, memory.ErrMarkup):
		return "invalid"
	}
	return "_internal"
}

// denyAPIKey: approve/revise/reissue need the interactive scope (CSRF is
// checked by authed); a principal without it (e.g. an API key) gets 403
// scope_denied. Nothing auto-approves. Allowlist, not denylist: only the
// granted scope counts, never the auth provider.
func (s *Server) denyAPIKey(w http.ResponseWriter, r *http.Request) bool {
	return !s.requireScope(w, r, auth.ScopeInteractive)
}

// browserOnly gates a route to browser sessions with the memory/settings
// error shape ({"error_code":"scope_denied"}), so registration alone keeps
// Bearer out even if a handler forgets its own denyAPIKey check.
func (s *Server) browserOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.denyAPIKey(w, r) {
			return
		}
		h(w, r)
	}
}
