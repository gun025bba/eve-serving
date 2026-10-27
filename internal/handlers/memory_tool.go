package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/store"
)

// Model-side memory.propose (memory.md §2, ui-api-contract §3.9.4–§3.9.5).
// The tool never writes: it creates a pending_actions card; the write only
// happens on POST /v1/actions/{id}/approve with a session token.

// withMemoryTool appends the memory_propose definition while memory is on;
// otherwise defs are returned unchanged (request bodies identical to before).
func (s *Server) withMemoryTool(defs []any) []any {
	if !s.memoryOn() {
		return defs
	}
	return append(append([]any{}, defs...), memory.ToolDef())
}

// historyTainted reports whether untrusted content is already in the
// conversation: any tool message or assistant tool_calls in the request
// history, or (persisted threads) any earlier message with web sources or a
// proactive origin. Any lookup error counts as tainted (fail-closed).
func (s *Server) historyTainted(r *http.Request, msgs []any, pr *chatPersist) bool {
	for _, mm := range msgs {
		m, ok := mm.(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "tool" || m["tool_calls"] != nil {
			return true
		}
	}
	if pr == nil || pr.threadID == "" {
		return false
	}
	ms, _, err := s.Store.ListMessages(r.Context(), user(r), pr.threadID, 1000, "")
	if err != nil {
		return true
	}
	var ids []string
	for _, m := range ms {
		if len(m.Sources) > 0 || strings.Contains(string(m.Metadata), `"proactive"`) {
			return true
		}
		ids = append(ids, m.ID)
	}
	ws, ok := s.Store.(store.WebStore)
	if !ok || len(ids) == 0 {
		return false
	}
	src, err := ws.Sources(r.Context(), user(r), ids)
	if err != nil {
		return true
	}
	for _, v := range src {
		if len(v) > 0 {
			return true
		}
	}
	return false
}

func toolResult(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// runMemoryPropose handles one model memory_propose call and returns the
// tool result for the model. Cards go out as SSE confirmation events.
func (s *Server) runMemoryPropose(r *http.Request, pr *chatPersist, args string, tainted bool, emit func(map[string]any)) string {
	deny := func(code string) string {
		emit(map[string]any{"object": "chat.completion.confirmation", "tool": memory.ToolPropose, "status": "denied", "error_code": code})
		s.appendAudit(r.Context(), user(r), audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: "denied",
			Tool: memory.ToolPropose, Scope: memory.ScopeWrite, Tainted: &tainted, Status: "denied", Detail: map[string]string{"error_code": code}})
		return toolResult(map[string]any{"status": "denied", "error": code})
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
		return deny("secret_detected")
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
// Registered only while memory is on; only memory.propose cards execute.
func (s *Server) approveAction(w http.ResponseWriter, r *http.Request) {
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
	now := time.Now().UTC()
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
		// Unknown, foreign, or no longer pending: same body for all (§3.7).
		s.writeCode(w, r, "not_found")
		return
	}
	if !now.Before(a.ExpiresAt) || a.TokenSHA256 == nil || a.TokenExpiresAt == nil || !now.Before(*a.TokenExpiresAt) ||
		store.TokenHash(b.ConfirmToken) != *a.TokenSHA256 {
		s.writeCode(w, r, "expired") // expired, never issued, replaced or wrong token
		return
	}
	if b.PayloadHash != a.PayloadHash || store.PayloadHash(a.Payload) != a.PayloadHash {
		s.writeCode(w, r, "payload_mismatch")
		return
	}
	if a.Tool != memory.ToolPropose {
		s.writeCode(w, r, "scope_denied")
		return
	}
	pc := memory.ProposalCtx{Actor: a.Provenance.Actor, Tainted: a.Tainted}
	if pc.Actor == "" {
		pc.Actor = "user"
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
		return "secret_detected"
	case errors.Is(err, memory.ErrTooMany), errors.Is(err, memory.ErrTooLong), errors.Is(err, memory.ErrFileTooLarge):
		return "cap_exceeded"
	case errors.Is(err, memory.ErrInvalid), errors.Is(err, memory.ErrEmpty):
		return "invalid"
	}
	return "_internal"
}
