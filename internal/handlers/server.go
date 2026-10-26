// Package handlers wires the HTTP API: inference proxy, ops, admin, docs and static UI.
package handlers

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/proxy"
	"github.com/gun025bba/eve-serving/internal/queue"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed openapi.json
var openAPISpec []byte

func init() {
	// Don't depend on the host's mime database for UI assets.
	mime.AddExtensionType(".css", "text/css; charset=utf-8")
	mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
	mime.AddExtensionType(".json", "application/json")
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	mime.AddExtensionType(".svg", "image/svg+xml")
	mime.AddExtensionType(".png", "image/png")
	mime.AddExtensionType(".html", "text/html; charset=utf-8")
}

type Server struct {
	Cfg     config.Config
	Key     string             // API key; used to build Auth when Auth is nil
	Auth    auth.Authenticator // provider chain (auth.Build); nil = build from Cfg.Auth.Providers
	Limiter auth.RateLimiter   // per-user hook; nil = auth.NoopLimiter
	Log     *logging.Logger
	M       *metrics.Metrics
	Mem     *metrics.MemSampler
	Q       *queue.Queue
	Up      *proxy.Client
	Start   time.Time
	Version string
	Store   store.Store // nil = persistence disabled (thread endpoints -> 503)
	Web     *WebTools   // nil = web tools unavailable (also needs Cfg.Web.Enabled)
}

// uiCSP keeps the UI from loading external resources (no remote images =
// no markdown-image exfiltration channel).
// Neither inline scripts nor inline styles are allowed (UI v13 verified: no violations).
const uiCSP = "default-src 'self'; img-src 'self' data:; connect-src 'self'; script-src 'self'; style-src 'self'; " +
	"font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; manifest-src 'self'; worker-src 'self'"

func withCSP(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", uiCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

type ctxKey struct{}

func rid(r *http.Request) string {
	if v, ok := r.Context().Value(ctxKey{}).(string); ok {
		return v
	}
	return "-"
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// fail writes an apierr (or internal_error for anything else) and records it.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *apierr.Error
	if !errors.As(err, &e) {
		s.M.Errs.WithLabelValues("internal_error").Inc()
		s.Log.Error("internal_error", "request_id", rid(r), "path", r.URL.Path, "error", trunc(err.Error(), 300))
		writeJSON(w, 500, apierr.Body("internal_error", "Internal gateway error", "server_error", rid(r)))
		return
	}
	s.M.Errs.WithLabelValues(e.Code).Inc()
	s.M.Reqs.WithLabelValues(r.URL.Path, fmt.Sprint(e.Status)).Inc()
	s.M.Recent.Add(metrics.Sample{OK: false, Code: e.Code})
	s.Log.Warn("request_error", "request_id", rid(r), "path", r.URL.Path, "status", e.Status, "code", e.Code, "detail", e.Message)
	writeJSON(w, e.Status, apierr.Body(e.Code, e.Message, e.Type, rid(r)))
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// statusWriter records the status code; keeps Flush working for SSE.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) {
	if w.status == 0 {
		w.status = c
	}
	w.ResponseWriter.WriteHeader(c)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func authErr(err error) *apierr.Error {
	switch {
	case errors.Is(err, auth.ErrInvalidKey):
		return apierr.InvalidKey()
	case errors.Is(err, auth.ErrForbiddenIdentity):
		return apierr.ForbiddenIdentity()
	default:
		return apierr.Unauthenticated()
	}
}

// authed runs the provider chain, stores the Identity in the request context,
// applies the per-user rate-limit hook, and logs user/provider for the request.
// Handlers read the caller via auth.FromContext only.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		id, err := s.Auth.Authenticate(r)
		if err != nil {
			s.M.Auth.WithLabelValues("none", "denied").Inc()
			s.fail(w, r, authErr(err))
			return
		}
		s.M.Auth.WithLabelValues(id.Provider, "ok").Inc()
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		if !s.Limiter.Allow(r.Context(), id) {
			s.fail(w, r, apierr.New(429, "rate_limited", "Per-user rate limit exceeded", "rate_limit_error"))
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		h(sw, r)
		s.Log.Info("request", "request_id", rid(r), "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"user", id.User, "auth_provider", id.Provider, "ms", time.Since(t0).Milliseconds())
	}
}

// whoami reports the caller identity so the UI can detect identity mode.
func (s *Server) webDefault() string {
	if !s.webEnabled() {
		return "off"
	}
	return s.Cfg.Web.DefaultMode
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	writeJSON(w, 200, map[string]any{"authenticated": true, "user": id.User, "provider": id.Provider,
		"capabilities": map[string]any{"web_search": s.webEnabled(), "web_search_default": s.webDefault(), "persistence": s.Store != nil}})
}

// Handler returns the full router with middleware (request id, recovery, CORS).
func (s *Server) Handler() http.Handler {
	if s.Auth == nil {
		c, err := auth.Build(s.Cfg.Auth.Providers, auth.Options{APIKey: s.Key, AllowedUsers: s.Cfg.AllowedUsers})
		if err != nil {
			panic(err)
		}
		s.Auth = c
	}
	if s.Limiter == nil {
		s.Limiter = auth.NoopLimiter{}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /whoami", s.authed(s.whoami))
	mux.HandleFunc("GET /v1/me", s.authed(s.whoami))
	mux.HandleFunc("POST /v1/chat/completions", s.authed(s.chat))
	mux.HandleFunc("GET /v1/models", s.authed(s.models))
	mux.HandleFunc("GET /v1/threads", s.persisted(s.listThreads))
	mux.HandleFunc("POST /v1/threads", s.persisted(s.createThread))
	mux.HandleFunc("POST /v1/threads/import", s.persisted(s.importThreads))
	mux.HandleFunc("GET /v1/threads/{id}", s.persisted(s.getThread))
	mux.HandleFunc("PATCH /v1/threads/{id}", s.persisted(s.patchThread))
	mux.HandleFunc("DELETE /v1/threads/{id}", s.persisted(s.deleteThread))
	mux.HandleFunc("GET /v1/threads/{id}/messages", s.persisted(s.listMessages))
	mux.HandleFunc("PUT /v1/messages/{id}/feedback", s.persisted(s.putFeedback))
	mux.HandleFunc("DELETE /v1/messages/{id}/feedback", s.persisted(s.deleteFeedback))
	mux.HandleFunc("GET /v1/search", s.persisted(s.search))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /ready", s.ready)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /stats", s.stats)
	mux.HandleFunc("GET /admin/config", s.authed(s.adminConfig))
	mux.HandleFunc("POST /admin/log-level", s.authed(s.setLevel))
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(openAPISpec)
	})
	mux.HandleFunc("GET /docs", docs)
	if dir := config.Expand(s.Cfg.StaticDir); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			mux.HandleFunc("GET /ui", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
			})
			mux.Handle("GET /ui/", withCSP(http.StripPrefix("/ui", http.FileServer(http.Dir(dir)))))
		}
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"detail": "Not Found"})
	})
	return s.withCORS(s.withRecover(s.withRequestID(mux)))
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 128 {
			id = newID()
		}
		w.Header().Set("X-Request-ID", id)
		auth.StripUntrusted(r)
		next.ServeHTTP(w, r.WithContext(contextWith(r, id)))
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.fail(w, r, fmt.Errorf("panic: %v", v))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	origins := s.Cfg.CORSOrigins
	if len(origins) == 0 {
		return next
	}
	any := slices.Contains(origins, "*")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		if o != "" && (any || slices.Contains(origins, o)) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", "X-Request-ID, X-Thread-Id, X-Message-Id, X-User-Message-Id")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusOK)
				return
			}
		} else if o != "" && r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			http.Error(w, "Disallowed CORS origin", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "ok", "uptime_s": int(time.Since(s.Start).Seconds())})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	m := s.Mem.Sample(s.M)
	checks := map[string]bool{"upstream": false, "model_loaded": false}
	if resp, err := s.Up.Do(r.Context(), "GET", "/v1/models", nil, 3*time.Second); err == nil {
		var d struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.NewDecoder(resp.Body).Decode(&d) == nil {
			checks["upstream"] = resp.StatusCode == 200
			for _, x := range d.Data {
				if x.ID == s.Cfg.Model {
					checks["model_loaded"] = true
				}
			}
		}
		resp.Body.Close()
	}
	checks["memory_headroom"] = m.AvailGB >= s.Cfg.MinFreeMemGB
	ok := checks["upstream"] && checks["model_loaded"] && checks["memory_headroom"]
	st := 200
	if !ok {
		st = 503
	}
	writeJSON(w, st, map[string]any{"ready": ok, "checks": checks, "memory": m, "queue_depth": s.Q.Waiting(), "inflight": s.Q.Inflight(),
		"max_concurrency": s.Q.MaxConc(), "max_queue": s.Q.MaxQueue()})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	s.Mem.Sample(s.M)
	promhttp.HandlerFor(s.M.Reg, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	out := s.M.Recent.Stats()
	out["queue_depth"] = s.Q.Waiting()
	out["inflight"] = s.Q.Inflight()
	out["memory"] = s.Mem.Sample(s.M)
	out["uptime_s"] = int(time.Since(s.Start).Seconds())
	writeJSON(w, 200, out)
}

func (s *Server) adminConfig(w http.ResponseWriter, r *http.Request) {
	b, _ := json.Marshal(s.Cfg)
	var m map[string]any
	json.Unmarshal(b, &m)
	m["log_level"] = s.Log.Level().String()
	m["database"] = map[string]any{"configured": s.Cfg.Database.URL != "", "enabled": s.Store != nil, "auto_migrate": s.Cfg.Database.AutoMigrate}
	writeJSON(w, 200, m)
}

func (s *Server) setLevel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Level any `json:"level"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b)
	lv := strings.ToUpper(fmt.Sprint(b.Level))
	if !slices.Contains([]string{"DEBUG", "INFO", "WARNING", "ERROR"}, lv) {
		s.fail(w, r, apierr.BadRequest("level must be DEBUG|INFO|WARNING|ERROR"))
		return
	}
	l, _ := logging.ParseLevel(lv)
	s.Log.SetLevel(l)
	s.Log.Warn("log_level_changed", "new_level", lv)
	writeJSON(w, 200, map[string]string{"log_level": lv})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	resp, err := s.Up.Do(r.Context(), "GET", "/v1/models", nil, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer resp.Body.Close()
	var v any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		s.fail(w, r, apierr.New(502, "upstream_error", "Upstream returned non-JSON", "server_error"))
		return
	}
	writeJSON(w, resp.StatusCode, v)
}

func docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>Local LLM Gateway - Swagger UI</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css"></head>
<body><div id="swagger-ui"></div><script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url:'/openapi.json',dom_id:'#swagger-ui',persistAuthorization:true});</script></body></html>`)
}
