package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/pim"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// Jarvis M1 read tools (#69). Exposure depends only on the principal's
// users.id (resolved from the auth context) and server settings — never on
// how the request was authenticated.

// pimUserID resolves the request principal to users.id (0 = unknown).
func (s *Server) pimUserID(ctx context.Context, login string) int64 {
	res, ok := s.Store.(store.UserIDResolver)
	if !ok || login == "" {
		return 0
	}
	id, err := res.UserID(ctx, login)
	if err != nil {
		return 0
	}
	return id
}

// pimOwnerFor is the shared exposure gate for every pim tool: server switch
// on, a helper client, users.id == pim.owner_user_id, and the user's own
// pim.enabled setting (default false, #71). Fail-closed. Decided by users.id
// and settings only, never by how the request authenticated.
func (s *Server) pimOwnerFor(ctx context.Context, login string) bool {
	if !s.Cfg.Pim.Enabled || s.Pim == nil || s.Cfg.Pim.OwnerUserID <= 0 {
		return false
	}
	if s.pimUserID(ctx, login) != s.Cfg.Pim.OwnerUserID {
		return false
	}
	u, err := s.userSettingsFor(ctx, login)
	return err == nil && u.Pim.Enabled
}

// pimToolsFor lists the pim read tools offered to this principal (see
// pimOwnerFor). Tools switched off by the kill switch are dropped.
func (s *Server) pimToolsFor(ctx context.Context, login string) []string {
	if !s.pimOwnerFor(ctx, login) {
		return nil
	}
	var out []string
	for _, n := range pim.Names() {
		if !tools.Default().Kill.Disabled(n) {
			out = append(out, n)
		}
	}
	return out
}

// pimWriter is the helper client as a Writer (nil when the configured
// reader cannot write, e.g. in tests).
func (s *Server) pimWriter() pim.Writer {
	w, _ := s.Pim.(pim.Writer)
	return w
}

// pimWriteToolsFor lists the write verbs offered to this principal: the
// owner gate plus GW_PIM_WRITES_ENABLED (default off). Offering a verb only
// lets the model propose a card; nothing is written before approval.
func (s *Server) pimWriteToolsFor(ctx context.Context, login string) []string {
	if !s.Cfg.Pim.WritesOn() || s.pimWriter() == nil || !s.pimOwnerFor(ctx, login) {
		return nil
	}
	var out []string
	for _, n := range pim.WriteNames() {
		if !tools.Default().Kill.Disabled(n) {
			out = append(out, n)
		}
	}
	return out
}

// pimWriteAllowed re-checks, at approve time, that this principal may still
// execute this write verb.
func (s *Server) pimWriteAllowed(ctx context.Context, login, tool string) bool {
	for _, n := range s.pimWriteToolsFor(ctx, login) {
		if n == tool {
			return true
		}
	}
	return false
}

func withPimTools(defs []any, names []string) []any {
	if len(names) == 0 {
		return defs
	}
	return append(append([]any{}, defs...), pim.ToolDefs(names)...)
}

// runPimTool executes one pim tool call. A call for a tool that is not
// exposed to this principal is denied without touching the helper. Write
// verbs only build a confirmation card (proposePimWrite); its event comes
// back as the third result so it can follow the tool_result. Logs carry
// method or tool, status, error code and count only.
func (s *Server) runPimTool(r *http.Request, pr *chatPersist, exposed []string, name, args string, tainted bool) (string, pim.Outcome, map[string]any) {
	ok := false
	for _, n := range exposed {
		ok = ok || n == name
	}
	if !ok {
		s.Log.Info("pim_tool", "request_id", rid(r), "tool", name, "status", "denied", "error_code", pim.CodeKillSwitch)
		return pim.Denied(), pim.DeniedOutcome(name), nil
	}
	if pim.IsWriteTool(name) {
		return s.proposePimWrite(r, pr, name, args, tainted)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	out, o := pim.Exec(ctx, s.Pim, name, args, time.Local)
	s.Log.Info("pim_tool", "request_id", rid(r), "tool", name, "status", o.Status, "error_code", o.ErrorCode, "count", o.Count, "truncated", o.Truncated)
	return out, o, nil
}

// pimToolEvents builds the two SSE events of a pim tool call (design §11.1),
// the existing chat.completion.tool_call / tool_result shapes: the model's
// arguments as-is for reads ({} for writes), then status, count/truncated and
// the card on success or error_code and an error card on failure. No sources,
// and the card is never stored.
func pimToolEvents(id, name, rawArgs string, round int, o pim.Outcome) (call, result map[string]any) {
	call = map[string]any{"object": "chat.completion.tool_call", "id": id, "tool": name, "args": pim.PublicArgs(name, rawArgs), "round": round}
	result = map[string]any{"object": "chat.completion.tool_result", "id": id, "tool": name}
	for k, v := range o.ResultFields() {
		result[k] = v
	}
	return call, result
}

func pimErrResult(code string) string {
	return toolResult(map[string]any{"status": "error", "error_code": code})
}

// proposePimWrite turns a model write call into a pending confirmation card.
// The card's payload_view carries the exact values that will be written plus
// the server-made display strings (design §11.4). The model only ever sees
// pending_confirmation and the action id; the write itself happens in
// approvePimWrite. A failed proposal takes the read-error path: an error
// outcome (status, error_code, error card) and no confirmation.
func (s *Server) proposePimWrite(r *http.Request, pr *chatPersist, name, args string, tainted bool) (string, pim.Outcome, map[string]any) {
	method := pim.Method(name)
	fail := func(code string) (string, pim.Outcome, map[string]any) {
		o := pim.FailOutcome(name, code)
		s.Log.Info("pim_write", "request_id", rid(r), "method", method, "stage", "propose", "status", o.Status, "error_code", o.ErrorCode)
		return pimErrResult(o.ErrorCode), o, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	now := s.now().UTC()
	var prop pim.Proposal
	var err error
	switch name {
	case pim.ToolAddReminder:
		prop, err = pim.ProposeAdd(args, now)
	case pim.ToolCompleteReminder:
		prop, err = pim.ProposeComplete(ctx, s.Pim, args, now)
	case pim.ToolAddEvent:
		prop, err = pim.ProposeAddEvent(ctx, s.Pim, args, now)
	default:
		err = &pim.Error{Code: pim.CodeInvalidArgs}
	}
	if err != nil {
		return fail(pim.CodeOf(err))
	}
	card := store.Action{Payload: prop.Payload}
	card.Tool, card.ToolVersion, card.Scopes = name, pim.ToolWriteVersion, []string{pim.ScopeFor(name)}
	card.PayloadHash = store.PayloadHash(prop.Payload)
	card.Provenance = store.Provenance{Actor: "user"}
	card.Tainted = tainted
	card.Status = "pending"
	card.CreatedAt, card.ExpiresAt = now, now.Add(pim.ProposalTTL)
	if pr != nil && pr.threadID != "" {
		t := pr.threadID
		card.ThreadID = &t
	}
	card.Summary, card.PayloadView = prop.Summary, prop.View
	// The card must show exactly what would be written. If the display
	// redactor would mask part of it (secret-like text), refuse instead of
	// showing something different from what is executed.
	if red := redactModel(card.ModelFields); red.Summary != card.Summary || !bytes.Equal(compactJSON(red.PayloadView), compactJSON(card.PayloadView)) {
		return fail(pim.CodeInvalidArgs)
	}
	saved, err := s.Store.CreateAction(ctx, user(r), card)
	if err != nil {
		return fail(pim.CodeUnavailable)
	}
	s.actionAudit(r, "action_proposed", saved)
	var m map[string]any
	if b, err := json.Marshal(view(saved)); err != nil || json.Unmarshal(b, &m) != nil {
		m = nil
	}
	s.Log.Info("pim_write", "request_id", rid(r), "method", method, "stage", "propose", "status", "pending_confirmation", "count", 1)
	return toolResult(map[string]any{"status": "pending_confirmation", "action_id": saved.ID}), pim.PendingOutcome(name, saved.ID), m
}

func compactJSON(b []byte) []byte {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return b
	}
	return buf.Bytes()
}

// approvePimWrite runs after approveAction has verified the session scope
// (interactive), the card owner, the confirm token and the payload hash.
// Order: gates -> re-validate -> audit row (fail-closed) -> claim the card
// (pending -> approved, one winner) -> one helper call. No retry on an
// unknown outcome: EventKit has no idempotency key, so the card stays
// consumed and the user checks Reminders. Definite refusals release the card.
// Logs and audit carry method, count and error code only.
func (s *Server) approvePimWrite(w http.ResponseWriter, r *http.Request, ap store.ActionApprover, a *store.Action, now time.Time) {
	login := user(r)
	method := pim.Method(a.Tool)
	logRes := func(status, code string, count int) {
		s.Log.Info("pim_write", "request_id", rid(r), "method", method, "stage", "approve", "status", status, "error_code", code, "count", count)
	}
	if !s.pimWriteAllowed(r.Context(), login, a.Tool) {
		logRes("denied", pim.CodeKillSwitch, 0)
		s.writeCode(w, r, "kill_switch")
		return
	}
	if st, err := ap.ActionStatus(r.Context(), login, a.ID); err != nil || st != "pending" {
		s.writeCode(w, r, "conflict")
		return
	}
	p, err := pim.ParsePayload(a.Tool, a.Payload)
	if err != nil {
		logRes("error", pim.CodeInvalidArgs, 0)
		s.writeCode(w, r, "invalid")
		return
	}
	vctx, vcancel := context.WithTimeout(r.Context(), 6*time.Second)
	err = pim.Revalidate(vctx, s.Pim, a.Tool, p)
	vcancel()
	if err != nil {
		code := pim.CodeOf(err)
		logRes("error", code, 0)
		if code == pim.CodeNotFound {
			s.writeCode(w, r, "item_gone")
			return
		}
		writeNoStore(w, http.StatusBadGateway, map[string]any{"status": "error", "action_id": a.ID, "error_code": code, "outcome_unknown": false})
		return
	}
	tainted := a.Tainted
	if s.Audit != nil {
		e := audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: "pim_write_approve", PendingActionID: a.ID, Tool: a.Tool,
			ToolVersion: a.ToolVersion, Scope: pim.ScopeFor(a.Tool), PayloadSHA256: a.PayloadHash, Tainted: &tainted, Status: "ok",
			Detail: map[string]string{"via": "ui", "method": method, "count": "1"}}
		if err := s.Audit.Append(r.Context(), login, e); err != nil {
			s.M.AuditWriteFail.Inc()
			s.Log.Error("audit_write_failed", "event", e.Event, "err", err.Error())
			logRes("error", "audit_unavailable", 0)
			s.fail(w, r, apierr.New(500, "internal_error", "PIM write failed", "server_error"))
			return
		}
	}
	if err := ap.ApproveAction(r.Context(), login, a.ID, *a.TokenSHA256, now); err != nil {
		s.actionErr(w, r, err)
		return
	}
	approved := *a
	approved.Status = "approved"
	s.actionAudit(r, "approved", approved)

	// The card is consumed: finish even if the client goes away.
	ectx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 8*time.Second)
	defer cancel()
	err = pim.Execute(ectx, s.pimWriter(), a.Tool, p)
	result := func(status, code string) {
		s.appendAudit(r.Context(), login, audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: "pim_write_result", PendingActionID: a.ID,
			Tool: a.Tool, ToolVersion: a.ToolVersion, Scope: pim.ScopeFor(a.Tool), PayloadSHA256: a.PayloadHash, Status: status,
			Detail: map[string]string{"method": method, "error_code": code}})
	}
	if err == nil {
		logRes("ok", "", 1)
		result("ok", "")
		writeNoStore(w, 200, map[string]any{"status": "executed", "action_id": a.ID, "result": map[string]any{"method": method, "count": 1}})
		return
	}
	code := pim.CodeOf(err)
	if pim.OutcomeUnknown(err) {
		// Do not release: a retry could create a second reminder.
		logRes("unknown", code, 0)
		result("unknown", code)
		writeNoStore(w, http.StatusBadGateway, map[string]any{"status": "error", "action_id": a.ID, "error_code": code, "outcome_unknown": true})
		return
	}
	if rerr := ap.ReleaseAction(r.Context(), login, a.ID); rerr != nil {
		s.Log.Error("pim_write_release_failed", "request_id", rid(r), "action_id", a.ID)
	}
	logRes("error", code, 0)
	result("error", code)
	switch code {
	case pim.CodeWritesDisabled:
		s.writeCode(w, r, "kill_switch")
	case pim.CodeNotFound:
		s.writeCode(w, r, "item_gone")
	default:
		writeNoStore(w, http.StatusBadGateway, map[string]any{"status": "error", "action_id": a.ID, "error_code": code, "outcome_unknown": false})
	}
}

// PimCardRetention is how long the text of a finished pim write card (event
// title, place, notes; reminder title) stays in pending_actions. The card's
// own life is at most an hour; the rest is time to look at a failed approve.
const PimCardRetention = 24 * time.Hour

// ScrubPimCards blanks payload and payload_view of pim write cards that ended
// more than PimCardRetention before now (I-2: calendar content must not sit in
// the database forever). It returns how many cards it scrubbed.
func (s *Server) ScrubPimCards(ctx context.Context, now time.Time) (int64, error) {
	sc, ok := s.Store.(store.ActionScrubber)
	if !ok {
		return 0, nil
	}
	return sc.ScrubActionContent(ctx, pim.WriteNames(), now.Add(-PimCardRetention))
}

// RunPimCardScrub scrubs now and then every interval until ctx is done. It
// runs whether or not pim is enabled: cards written while it was on must be
// cleaned up after it is switched off.
func (s *Server) RunPimCardScrub(ctx context.Context, interval time.Duration) {
	run := func() {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		n, err := s.ScrubPimCards(c, time.Now())
		if err != nil {
			s.Log.Error("pim_card_scrub_failed", "error", logTrunc(err.Error(), 200))
			return
		}
		if n > 0 {
			s.Log.Info("pim_card_scrub", "cards", n) // a count only, never content
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
