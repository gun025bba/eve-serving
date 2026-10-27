package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/push"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// Proactive P1 endpoints (docs/architecture/ui-api-contract.md §3.5–§3.7):
//
//	GET    /v1/actions                    pending proposals, no token fields
//	POST   /v1/actions/{id}/reissue       one-time confirm token (cap 3 per chain)
//	POST   /v1/proposals/{id}/revise      successor proposal + token
//	GET    /v1/push/vapid-public-key      {key, key_id}
//	POST   /v1/push/subscriptions         201
//	DELETE /v1/push/subscriptions         204
//
// Errors use the fixed contract body {"error_code":"<code>"} only. Every 404
// is byte-identical (body and headers, request id aside). Responses carry
// Cache-Control: no-store and bodies are never logged.

var errorStatus = map[string]int{
	"invalid": 400, "scope_denied": 403, "tainted": 403, "kill_switch": 403,
	"not_found": 404, "item_gone": 409, "conflict": 409, "expired": 410,
	"secret_detected": 422, "cap_exceeded": 422, "payload_mismatch": 422,
}

// ErrorCodes is the contract error_code enum (§3.3 / Q14).
func ErrorCodes() []string {
	return []string{"secret_detected", "cap_exceeded", "tainted", "scope_denied", "expired", "not_found",
		"item_gone", "conflict", "payload_mismatch", "kill_switch", "invalid"}
}

// writeCode writes {"error_code":code} with fixed headers.
func (s *Server) writeCode(w http.ResponseWriter, r *http.Request, code string) {
	st, ok := errorStatus[code]
	if !ok {
		code, st = "invalid", 400
	}
	s.M.Errs.WithLabelValues(code).Inc()
	body := []byte(`{"error_code":"` + code + `"}`)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(st)
	w.Write(body)
}

func writeNoStore(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}

// actionErr maps store errors to the contract enum; anything else is 500.
func (s *Server) actionErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.writeCode(w, r, "not_found")
	case errors.Is(err, store.ErrInvalid):
		s.writeCode(w, r, "invalid")
	case errors.Is(err, store.ErrConflict):
		s.writeCode(w, r, "conflict")
	case errors.Is(err, store.ErrExpired):
		s.writeCode(w, r, "expired")
	case errors.Is(err, store.ErrCapExceeded):
		s.writeCode(w, r, "cap_exceeded")
	case errors.Is(err, store.ErrItemGone):
		s.writeCode(w, r, "item_gone")
	default:
		w.Header().Set("Cache-Control", "no-store")
		s.fail(w, r, err)
	}
}

// view builds the token-free API object. Only ModelFields (summary,
// payload_view) pass through the redactor; ServerFields are never touched,
// whatever their key names — the split is by struct origin.
func view(a store.Action) store.Confirmation {
	c := a.View()
	c.ModelFields = redactModel(c.ModelFields)
	return c
}

func redactModel(m store.ModelFields) store.ModelFields {
	r := tools.Default().Redact
	m.Summary = r.Redact(m.Summary)
	m.PayloadView = r.RedactJSON(m.PayloadView)
	return m
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	if st := r.URL.Query().Get("status"); st != "" && st != "pending" {
		s.writeCode(w, r, "invalid")
		return
	}
	as, err := s.Store.ListPendingActions(r.Context(), user(r))
	if err != nil {
		s.actionErr(w, r, err)
		return
	}
	out := make([]store.Confirmation, 0, len(as))
	for _, a := range as {
		out = append(out, view(a))
	}
	writeNoStore(w, 200, map[string]any{"data": out})
}

func issued(a store.Action, tok string) store.IssuedConfirmation {
	return store.IssuedConfirmation{Confirmation: view(a), ConfirmToken: tok, TokenExpiresAt: *a.TokenExpiresAt}
}

func (s *Server) actionAudit(r *http.Request, ev string, a store.Action) {
	tainted := a.Tainted
	e := audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: ev, PendingActionID: a.ID, Tool: a.Tool,
		ToolVersion: a.ToolVersion, PayloadSHA256: a.PayloadHash, Tainted: &tainted, Status: a.Status}
	if a.ThreadID != nil {
		e.ThreadID = *a.ThreadID
	}
	s.appendAudit(r.Context(), user(r), e) // never the token or its hash
}

func (s *Server) reissueAction(w http.ResponseWriter, r *http.Request) {
	if s.denyAPIKey(w, r) {
		return
	}
	io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 1<<10))
	a, tok, err := s.Store.ReissueAction(r.Context(), user(r), r.PathValue("id"), s.now())
	if err != nil {
		s.actionErr(w, r, err)
		return
	}
	s.actionAudit(r, "action_token_issued", a)
	writeNoStore(w, 200, issued(a, tok))
}

// parseRevise accepts an empty body or exactly {"item_ids":[string,...]}.
func parseRevise(raw []byte) ([]string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var b struct {
		ItemIDs *[]string `json:"item_ids"`
	}
	if err := dec.Decode(&b); err != nil || dec.More() {
		return nil, false
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return nil, false
	}
	if v, ok := keys["item_ids"]; ok {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || b.ItemIDs == nil {
			return nil, false
		}
		if len(*b.ItemIDs) == 0 {
			return nil, false
		}
		return *b.ItemIDs, true
	}
	return nil, true // {} == empty body
}

func (s *Server) reviseProposal(w http.ResponseWriter, r *http.Request) {
	if s.denyAPIKey(w, r) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		s.writeCode(w, r, "invalid")
		return
	}
	ids, ok := parseRevise(raw)
	if !ok {
		s.writeCode(w, r, "invalid")
		return
	}
	if code := s.reviseMemoryCheck(r, r.PathValue("id"), ids); code != "" {
		s.writeCode(w, r, code)
		return
	}
	a, tok, err := s.Store.ReviseAction(r.Context(), user(r), r.PathValue("id"), ids, s.now())
	if err != nil {
		s.actionErr(w, r, err)
		return
	}
	s.actionAudit(r, "proposal_revised", a)
	writeNoStore(w, 200, issued(a, tok))
}

// pushEnabled is fail-closed: config, VAPID key, store and the kill switch
// (proactive_off / all_off, unreadable file = off) must all allow it.
func (s *Server) pushEnabled() bool {
	p := s.Cfg.Push
	return p.Enabled && p.VAPIDPublicKey != "" && p.VAPIDKeyID != "" && s.Store != nil && tools.Default().Kill.ProactiveAllowed()
}

func (s *Server) pushed(h http.HandlerFunc) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request) {
		if !s.pushEnabled() {
			s.writeCode(w, r, "not_found")
			return
		}
		h(w, r)
	})
}

func (s *Server) vapidKey(w http.ResponseWriter, r *http.Request) {
	writeNoStore(w, 200, map[string]string{"key": s.Cfg.Push.VAPIDPublicKey, "key_id": s.Cfg.Push.VAPIDKeyID})
}

func hashEndpoint(ep string) string {
	h := sha256.Sum256([]byte(ep))
	return hex.EncodeToString(h[:])
}

func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		VAPIDKeyID string `json:"vapid_key_id"`
	}
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, 8<<10), &b); err != nil {
		s.writeCode(w, r, "invalid")
		return
	}
	if push.ValidateEndpoint(b.Endpoint) != nil || push.ValidateKeys(b.Keys.P256dh, b.Keys.Auth) != nil ||
		b.VAPIDKeyID != s.Cfg.Push.VAPIDKeyID {
		s.writeCode(w, r, "invalid")
		return
	}
	res, err := s.Store.AddPushSubscription(r.Context(), user(r), store.PushSubscription{
		Endpoint: b.Endpoint, P256dh: b.Keys.P256dh, Auth: b.Keys.Auth, VAPIDKeyID: b.VAPIDKeyID})
	if err != nil {
		s.actionErr(w, r, err)
		return
	}
	eh := hashEndpoint(b.Endpoint)
	ev := audit.Event{At: time.Now().UTC(), Actor: audit.ActorUser, Event: "push_subscribed", Status: "ok",
		Detail: map[string]string{"endpoint_sha256": eh}}
	if res == store.PushOwnedByOther {
		// No transfer, no leak: same 201. Ops alert + audit with the hash only.
		ev.Event, ev.Status = "push_endpoint_conflict", "kept_existing_owner"
		s.M.Errs.WithLabelValues("push_endpoint_conflict").Inc()
		s.Log.Warn("ops_alert", "alert", "push_endpoint_conflict", "request_id", rid(r))
	}
	s.appendAudit(r.Context(), user(r), ev)
	writeNoStore(w, 201, map[string]string{"status": "subscribed"})
}

func (s *Server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, 8<<10), &b); err != nil || b.Endpoint == "" {
		s.writeCode(w, r, "invalid")
		return
	}
	if err := s.Store.DeletePushSubscription(r.Context(), user(r), b.Endpoint); err != nil {
		s.actionErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
