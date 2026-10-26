package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/store"
)

// API message status values (ui-api-contract.md §3.8).
const (
	apiGenerating = "generating"
	apiComplete   = "complete"
	apiFailed     = "failed"
)

// runDetached creates the assistant row (status generating), then runs gen
// with a context detached from the client (own timeout). Streaming requests
// subscribe to the run's event log; a disconnect ends only the subscription.
func (s *Server) runDetached(w http.ResponseWriter, r *http.Request, body map[string]any, pr *chatPersist, gen func(http.ResponseWriter, *http.Request)) {
	if err := pr.startPlaceholder(r); err != nil {
		s.fail(w, r, err)
		return
	}
	gctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), genTimeout)
	r2 := r.WithContext(gctx)
	stream, _ := body["stream"].(bool)
	if !stream {
		defer cancel()
		sw := &statusWriter{ResponseWriter: w}
		gen(sw, r2)
		pr.fail(r2, "upstream_error") // no-op when already saved
		return
	}
	g := s.hub().start(pr.user, pr.threadID, pr.msgID)
	meta, _ := json.Marshal(map[string]any{"thread_id": pr.threadID, "message_id": pr.msgID})
	g.Write([]byte("event: eve.meta\ndata: " + string(meta) + "\n\n"))
	go func() {
		defer cancel()
		defer s.hub().finish(g)
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("generation_panic", "request_id", rid(r2), "message_id", pr.msgID)
				pr.fail(r2, "internal_error")
			}
		}()
		gen(g, r2)
		code := g.emitError()
		if code == "" {
			code = "upstream_error"
		}
		pr.fail(r2, code)
	}()
	h := w.Header()
	pr.setHeaders(h)
	h.Set("X-Message-Id", pr.msgID)
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	if !g.follow(w, r, 0) {
		s.Log.Info("client_detached", "request_id", rid(r), "thread_id", pr.threadID, "message_id", pr.msgID)
	}
}

// streamMessage: GET /v1/threads/{id}/messages/{mid}/stream — replay events
// after Last-Event-ID (or ?after=) then follow live. Unknown, foreign, or
// mismatched ids are a uniform 404.
func (s *Server) streamMessage(w http.ResponseWriter, r *http.Request) {
	tid, mid := r.PathValue("id"), r.PathValue("mid")
	m, err := s.Store.GetMessage(r.Context(), user(r), mid)
	if err != nil || m.ThreadID != tid || m.Role != "assistant" {
		s.fail(w, r, notFound("Message"))
		return
	}
	after := 0
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw != "" {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 0 {
			s.fail(w, r, apierr.BadRequest("'after' / Last-Event-ID must be a non-negative integer"))
			return
		}
		after = n
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Message-Id", mid)
	if g := s.hub().get(user(r), mid); g != nil {
		w.WriteHeader(200)
		g.follow(w, r, after)
		return
	}
	// Run no longer in memory: send the saved message once, then [DONE].
	s.apiMessage(r, &m)
	b, _ := json.Marshal(m)
	w.WriteHeader(200)
	w.Write([]byte("event: eve.snapshot\ndata: " + string(b) + "\n\n"))
	w.Write([]byte("data: [DONE]\n\n"))
}

// apiMessage maps stored status to the API values and overlays the partial
// reply of a running generation.
func (s *Server) apiMessage(r *http.Request, m *store.Message) {
	switch m.Status {
	case store.StatusGenerating:
		if g := s.hub().get(user(r), m.ID); g != nil {
			c, rs := g.partialContent()
			m.Content = c
			m.Reasoning = nz(rs)
		}
		m.Status = apiGenerating
	case store.StatusIncomplete, store.StatusFailed:
		m.Status = apiFailed
		var md struct {
			ErrorCode string `json:"error_code"`
			Reason    string `json:"reason"`
		}
		_ = json.Unmarshal(m.Metadata, &md)
		code := md.ErrorCode
		if code == "" {
			code = md.Reason
		}
		if code == "" {
			code = "unknown"
		}
		m.ErrorCode = &code
	default:
		m.Status = apiComplete
	}
}
