package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/store"
)

// API message status values (ui-api-contract.md §3.8).
const (
	apiGenerating = "generating"
	apiComplete   = "complete"
	apiFailed     = "failed"
)

type slotHeldKey struct{}

// acquire takes a model slot unless the detached run already holds one
// (admitted before the assistant row was created).
func (s *Server) acquire(ctx context.Context) (time.Duration, func(), error) {
	if ctx.Value(slotHeldKey{}) != nil {
		return 0, func() {}, nil
	}
	q, err := s.Q.Acquire(ctx)
	if err != nil {
		return 0, nil, err
	}
	s.M.QWait.Observe(q.Seconds())
	return q, s.Q.Release, nil
}

func tooMany(w http.ResponseWriter, code, msg string) *apierr.Error {
	w.Header().Set("Retry-After", "5")
	return apierr.New(429, code, msg, "rate_limit_error")
}

func noStoreSSE(h http.Header) {
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
}

// runDetached admits the request (per-user cap, hub cap, model queue — all
// failing with the same HTTP statuses as before), then creates the assistant
// row (status generating) and runs gen with a context detached from the
// client. Streaming requests subscribe to the run's event log; a disconnect
// ends only the subscription.
func (s *Server) runDetached(w http.ResponseWriter, r *http.Request, body map[string]any, pr *chatPersist, gen func(http.ResponseWriter, *http.Request)) {
	h := s.hub()
	switch err := h.reserve(pr.user); {
	case errors.Is(err, errUserCap):
		s.fail(w, r, tooMany(w, "too_many_generations", "Too many generations in progress for this user"))
		return
	case err != nil:
		s.fail(w, r, tooMany(w, "server_busy", "Too many generations in progress"))
		return
	}
	reserved := true
	defer func() {
		if reserved {
			h.unreserve(pr.user)
		}
	}()
	_, rel, err := s.acquire(r.Context())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.fail(w, r, err)
		return
	}
	released := false
	defer func() {
		if !released {
			rel()
		}
	}()
	if err := pr.startPlaceholder(r); err != nil {
		if errors.Is(err, store.ErrGenerating) {
			s.M.GenRejected.WithLabelValues("conflict").Inc()
		}
		s.fail(w, r, err)
		return
	}
	gctx, cancel := context.WithTimeout(context.WithValue(context.WithoutCancel(r.Context()), slotHeldKey{}, true), dur(h.cfg.TimeoutS))
	g, err := h.start(gctx, cancel, pr.user, pr.threadID, pr.msgID)
	if err != nil {
		cancel()
		s.fail(w, r, apierr.New(409, "generation_in_progress", "Message is already being generated", "invalid_request_error"))
		return
	}
	reserved, released = false, true
	pr.failCode = g.failCode
	r2 := r.WithContext(gctx)
	if err := g.subscribe(); err != nil { // the original request is subscriber #1
		panic("unreachable: new run has no subscribers")
	}
	defer g.unsubscribe()
	go g.watchIdle()
	stream, _ := body["stream"].(bool)

	done := make(chan struct{})
	run := func(out http.ResponseWriter) {
		defer close(done)
		defer cancel()
		defer rel()
		defer h.finish(g)
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("generation_panic", "request_id", rid(r2), "message_id", pr.msgID)
				g.mu.Lock()
				if g.cancelCode == "" {
					g.cancelCode = "internal_error"
				}
				g.mu.Unlock()
				pr.fail(r2, "internal_error")
			}
			g.final = apiFailed
			if m, err := s.Store.GetMessage(context.WithoutCancel(r2.Context()), pr.user, pr.msgID); err == nil && m.Status == store.StatusComplete {
				g.final = apiComplete
			}
			if stream {
				code := g.failCode()
				if code == "" {
					code = "upstream_error"
				}
				v := pr.ids()
				v["object"], v["created"], v["status"] = "chat.completion.persisted", time.Now().Unix(), g.final
				g.closeOut(code, v)
			}
		}()
		gen(out, r2)
		if stream {
			if code, status := g.emitError(); code != "" {
				s.M.GenStreamErr.WithLabelValues(code, strconv.Itoa(status)).Inc()
				s.Log.Warn("stream_error", "request_id", rid(r2), "message_id", pr.msgID, "code", code, "status", status)
				pr.fail(r2, code)
				return
			}
		}
		code := g.failCode()
		if code == "" {
			code = "upstream_error"
		}
		pr.fail(r2, code) // no-op when already saved
	}
	if !stream {
		// Synchronous: this handler stays subscribed until the run ends, so
		// the reply is complete even if the client has gone away.
		run(&statusWriter{ResponseWriter: w})
		return
	}
	meta, _ := json.Marshal(map[string]any{"thread_id": pr.threadID, "message_id": pr.msgID})
	g.emitMeta([]byte("event: eve.meta\ndata: " + string(meta) + "\n\n"))
	go run(g)
	hd := w.Header()
	pr.setHeaders(hd)
	hd.Set("X-Message-Id", pr.msgID)
	noStoreSSE(hd)
	w.WriteHeader(200)
	if !g.follow(w, r, 0) {
		s.Log.Info("client_detached", "request_id", rid(r), "thread_id", pr.threadID, "message_id", pr.msgID)
	}
}

// ownedAssistant resolves {id}/{mid} for the caller; anything else is the same 404.
func (s *Server) ownedAssistant(w http.ResponseWriter, r *http.Request) (store.Message, bool) {
	tid, mid := r.PathValue("id"), r.PathValue("mid")
	m, err := s.Store.GetMessage(r.Context(), user(r), mid)
	if err != nil || m.ThreadID != tid || m.Role != "assistant" {
		s.fail(w, r, notFound("Message"))
		return m, false
	}
	return m, true
}

// streamMessage: GET /v1/threads/{id}/messages/{mid}/stream — replay events
// after Last-Event-ID (or ?after=) then follow live.
func (s *Server) streamMessage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.ownedAssistant(w, r)
	if !ok {
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
	if g := s.hub().get(user(r), m.ID); g != nil {
		if err := g.subscribe(); err != nil {
			s.fail(w, r, tooMany(w, "too_many_subscribers", "Too many open streams for this message"))
			return
		}
		defer g.unsubscribe()
		noStoreSSE(w.Header())
		w.Header().Set("X-Message-Id", m.ID)
		w.WriteHeader(200)
		g.follow(w, r, after)
		return
	}
	// Run no longer in memory: send the saved message once, then [DONE].
	s.apiMessage(r, &m)
	b, _ := json.Marshal(m)
	noStoreSSE(w.Header())
	w.Header().Set("X-Message-Id", m.ID)
	w.WriteHeader(200)
	w.Write([]byte("event: eve.snapshot\ndata: " + string(b) + "\n\n"))
	w.Write([]byte("data: [DONE]\n\n"))
}

// cancelMessage: POST /v1/threads/{id}/messages/{mid}/cancel (CSRF-checked
// like every write). 204 for any owned assistant message: a running
// generation is stopped (status failed, error_code "cancelled"); otherwise
// nothing changes (idempotent).
func (s *Server) cancelMessage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.ownedAssistant(w, r)
	if !ok {
		return
	}
	if g := s.hub().get(user(r), m.ID); g != nil && g.cancel("cancelled") {
		s.Log.Info("generation_cancelled", "request_id", rid(r), "message_id", m.ID)
	}
	w.WriteHeader(http.StatusNoContent)
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
