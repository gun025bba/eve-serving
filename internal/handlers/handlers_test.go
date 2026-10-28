package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/proxy"
	"github.com/gun025bba/eve-serving/internal/queue"
)

const key = "test-key"

// fakeUpstream mimics mlx_lm.server.
func fakeUpstream(t *testing.T, delay time.Duration, seen *[]map[string]any, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"m1"}]}`)
		case "/v1/chat/completions":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if mu != nil {
				mu.Lock()
				*seen = append(*seen, b)
				mu.Unlock()
			}
			time.Sleep(delay)
			if b["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, c := range []string{"he", "llo"} {
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
					w.(http.Flusher).Flush()
				}
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
				return
			}
			fmt.Fprint(w, `{"id":"x","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		}
	}))
}

func newServer(t *testing.T, upURL string, mut func(*config.Config)) (*httptest.Server, *strings.Builder) {
	cfg := testDefault()
	cfg.AllowedOrigins = []string{testOrigin}
	cfg.Upstream, cfg.Model, cfg.MinFreeMemGB = upURL, "m1", 0
	cfg.StaticDir = t.TempDir()
	os.WriteFile(filepath.Join(cfg.StaticDir, "index.html"), []byte("<html>ui</html>"), 0o644)
	os.WriteFile(filepath.Join(cfg.StaticDir, "styles.css"), []byte("body{}"), 0o644)
	os.WriteFile(filepath.Join(cfg.StaticDir, "app.js"), []byte("1"), 0o644)
	if mut != nil {
		mut(&cfg)
	}
	logbuf := &strings.Builder{}
	var lmu sync.Mutex
	m := metrics.New(50)
	q := queue.New(cfg.MaxConc, cfg.MaxQueue, time.Duration(cfg.QueueTimeoutS*float64(time.Second)))
	s := &Server{Cfg: cfg, Key: key, Log: logging.New(lockedWriter{&lmu, logbuf}, logging.INFO), M: m, Mem: &metrics.MemSampler{Needle: "no-such-proc-xyz"}, Q: q,
		Up: proxy.New(proxy.Options{BaseURL: upURL, ConnectTimeout: time.Second, ReadTimeout: 5 * time.Second, Retries: 0, Backoff: time.Millisecond}), Start: time.Now()}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, logbuf
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func do(t *testing.T, method, url, body string, authz bool) (*http.Response, map[string]any) {
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if authz {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	return resp, m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestAuthAndErrors(t *testing.T) {
	up := fakeUpstream(t, 0, nil, nil)
	defer up.Close()
	ts, _ := newServer(t, up.URL, nil)
	r, m := do(t, "POST", ts.URL+"/v1/chat/completions", `{}`, false)
	if r.StatusCode != 401 || errCode(m) != "unauthenticated" || r.Header.Get("X-Request-ID") == "" {
		t.Fatal(r.StatusCode, m)
	}
	r, m = do(t, "POST", ts.URL+"/v1/chat/completions", `{"messages":[]}`, true)
	if r.StatusCode != 400 || errCode(m) != "bad_request" {
		t.Fatal(r.StatusCode, m)
	}
	r, m = do(t, "POST", ts.URL+"/v1/chat/completions", `not json`, true)
	if r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	r, _ = do(t, "GET", ts.URL+"/admin/config", "", false)
	if r.StatusCode != 401 {
		t.Fatal(r.StatusCode)
	}
}

func TestUpstreamUnavailable(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", nil)
	r, m := do(t, "GET", ts.URL+"/v1/models", "", true)
	if r.StatusCode != 503 || errCode(m) != "upstream_unavailable" {
		t.Fatal(r.StatusCode, m)
	}
	r, _ = do(t, "GET", ts.URL+"/ready", "", true)
	if r.StatusCode != 503 {
		t.Fatal(r.StatusCode)
	}
	// Only /health and POST /v1/pair are unauthenticated.
	for _, p := range []string{"/ready", "/stats", "/metrics", "/openapi.json", "/docs"} {
		if r, _ := do(t, "GET", ts.URL+p, "", false); r.StatusCode != 401 {
			t.Fatal(p, r.StatusCode)
		}
	}
}

func TestChatDefaultsAndLog(t *testing.T) {
	var seen []map[string]any
	var mu sync.Mutex
	up := fakeUpstream(t, 0, &seen, &mu)
	defer up.Close()
	ts, logs := newServer(t, up.URL, nil)
	r, m := do(t, "POST", ts.URL+"/v1/chat/completions", `{"messages":[{"role":"user","content":"x"}],"temperature":0.1}`, true)
	if r.StatusCode != 200 || m["id"] != "x" {
		t.Fatal(r.StatusCode, m)
	}
	b := seen[0]
	if b["model"] != "m1" || b["max_tokens"].(float64) != 8192 || b["temperature"].(float64) != 0.1 || b["top_p"].(float64) != 0.9 {
		t.Fatal(b)
	}
	if !strings.Contains(logs.String(), `"msg":"chat_completion"`) || !strings.Contains(logs.String(), `"completion_tokens":1`) {
		t.Fatal(logs.String())
	}
	r, m = do(t, "GET", ts.URL+"/v1/models", "", true)
	if r.StatusCode != 200 || m["object"] != "list" {
		t.Fatal(m)
	}
	r, m = do(t, "GET", ts.URL+"/ready", "", true)
	if r.StatusCode != 200 || m["ready"] != true {
		t.Fatal(m)
	}
	r, m = do(t, "GET", ts.URL+"/stats", "", true)
	if m["window_requests"].(float64) != 1 {
		t.Fatal(m)
	}
}

func TestStreaming(t *testing.T) {
	var seen []map[string]any
	var mu sync.Mutex
	up := fakeUpstream(t, 0, &seen, &mu)
	defer up.Close()
	ts, logs := newServer(t, up.URL, nil)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"x"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(resp.Header)
	}
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}
	resp.Body.Close()
	if lines[len(lines)-1] != "data: [DONE]" || len(lines) != 4 {
		t.Fatal(lines)
	}
	if so, _ := seen[0]["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Fatal(seen[0])
	}
	time.Sleep(20 * time.Millisecond)
	if !strings.Contains(logs.String(), `"stream":true`) || !strings.Contains(logs.String(), `"prompt_tokens":3`) {
		t.Fatal(logs.String())
	}
}

func TestQueueing(t *testing.T) {
	up := fakeUpstream(t, 150*time.Millisecond, nil, nil)
	defer up.Close()
	ts, _ := newServer(t, up.URL, func(c *config.Config) { c.MaxConc, c.MaxQueue = 2, 2 })
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := do(t, "POST", ts.URL+"/v1/chat/completions", `{"messages":[{"role":"user","content":"x"}]}`, true)
			codes <- r.StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	n := map[int]int{}
	for c := range codes {
		n[c]++
	}
	if n[200] != 4 || n[429] != 2 {
		t.Fatal(n)
	}
	mreq, _ := http.NewRequest("GET", ts.URL+"/metrics", nil)
	mreq.Header.Set("Authorization", "Bearer "+key)
	resp, _ := http.DefaultClient.Do(mreq)
	b, _ := io.ReadAll(resp.Body)
	for _, name := range []string{"llm_requests_total", "llm_queue_wait_seconds", "llm_errors_total{code=\"queue_full\"} 2", "llm_inflight_requests 0"} {
		if !strings.Contains(string(b), name) {
			t.Fatalf("metrics missing %s", name)
		}
	}
}

func TestStaticAndDocs(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", nil)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := c.Get(ts.URL + "/ui")
	if r.StatusCode != 307 || r.Header.Get("Location") != "/ui/" {
		t.Fatal(r.StatusCode, r.Header)
	}
	for p, ct := range map[string]string{"/ui/": "text/html", "/ui/styles.css?v=8": "text/css", "/ui/app.js": "text/javascript", "/openapi.json": "application/json", "/docs": "text/html"} {
		r, _ := do(t, "GET", ts.URL+p, "", strings.HasPrefix(p, "/openapi") || p == "/docs")
		if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), ct) {
			t.Fatal(p, r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
	r, _ = http.Get(ts.URL + "/nope")
	if r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
}

func TestCORS(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", func(c *config.Config) { c.CORSOrigins = []string{"http://ok"} })
	req, _ := http.NewRequest("OPTIONS", ts.URL+"/v1/chat/completions", nil)
	req.Header.Set("Origin", "http://ok")
	req.Header.Set("Access-Control-Request-Method", "POST")
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != 200 || r.Header.Get("Access-Control-Allow-Origin") != "http://ok" || !strings.Contains(r.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatal(r.StatusCode, r.Header)
	}
	req.Header.Set("Origin", "http://evil")
	r, _ = http.DefaultClient.Do(req)
	if r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("evil allowed")
	}
	ts2, _ := newServer(t, "http://127.0.0.1:1", nil)
	r, _ = http.Get(ts2.URL + "/health")
	if r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("cors should be off")
	}
}

func TestAdmin(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", nil)
	r, m := do(t, "POST", ts.URL+"/admin/log-level", `{"level":"debug"}`, true)
	if r.StatusCode != 200 || m["log_level"] != "DEBUG" {
		t.Fatal(m)
	}
	r, m = do(t, "GET", ts.URL+"/admin/config", "", true)
	if m["log_level"] != "DEBUG" || m["max_conc"].(float64) != 2 {
		t.Fatal(m)
	}
	r, _ = do(t, "POST", ts.URL+"/admin/log-level", `{"level":"LOUD"}`, true)
	if r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

func TestIdentityAuth(t *testing.T) {
	up := fakeUpstream(t, 0, nil, nil)
	defer up.Close()
	ts, logbuf := newServer(t, up.URL, func(c *config.Config) { c.AllowedUsers = []string{"Owner@GitHub"} })
	get := func(path, login, bearer string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if login != "" {
			req.Header.Set(testUserHeader, login)
			browserHeaders(req)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	if c, m := get("/whoami", "owner@github", ""); c != 200 || m["provider"] != "session" || m["user"] != "owner@github" {
		t.Fatal(c, m)
	}
	if c, _ := get("/v1/models", "owner@github", ""); c != 200 {
		t.Fatal(c)
	}
	if c, m := get("/v1/models", "someone@github", ""); c != 403 || errCode(m) != "identity_not_allowed" {
		t.Fatal(c, m)
	}
	// Forbidden identity header + valid key must not fall through to api-key.
	if c, m := get("/v1/models", "someone@github", key); c != 403 || errCode(m) != "identity_not_allowed" {
		t.Fatal("forbidden identity + valid key:", c, m)
	}
	if c, m := get("/v1/models", "", ""); c != 401 || errCode(m) != "unauthenticated" {
		t.Fatal(c, m)
	}
	if strings.Contains(logbuf.String(), `"reason":"no_credentials"`) {
		t.Fatal("no_credentials must not be logged at INFO/WARN")
	}
	if !strings.Contains(logbuf.String(), `"auth_rejected"`) || !strings.Contains(logbuf.String(), `"reason":"forbidden_identity"`) || strings.Contains(logbuf.String(), "someone@github") {
		t.Fatal("rejection log missing or leaks identity:", logbuf.String())
	}
	if c, m := get("/v1/me", "", key); c != 200 || m["provider"] != "api-key" {
		t.Fatal(c, m)
	}
	if c, m := get("/v1/models", "", "wrong"); c != 401 || errCode(m) != "invalid_api_key" {
		t.Fatal(c, m)
	}
	if !strings.Contains(logbuf.String(), `"user":"owner@github"`) || !strings.Contains(logbuf.String(), `"auth_provider":"session"`) || strings.Contains(logbuf.String(), key) {
		t.Fatal("log missing identity or leaked key:", logbuf.String())
	}
	mreq, _ := http.NewRequest("GET", ts.URL+"/metrics", nil)
	mreq.Header.Set("Authorization", "Bearer "+key)
	resp, _ := http.DefaultClient.Do(mreq)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `llm_auth_total{auth_method="session",result="ok"}`) {
		t.Fatal("auth metric missing")
	}
}

// Tailscale-User-* headers never authenticate, from any peer, allowlisted or
// not, and are stripped before handlers run (4.0.0).
func TestTailscaleHeaderNeverAuthorizes(t *testing.T) {
	up := fakeUpstream(t, 0, nil, nil)
	defer up.Close()
	cfg := testDefault()
	cfg.Upstream, cfg.AllowedUsers = up.URL, []string{"owner@github"}
	s := &Server{Cfg: cfg, Key: key, Log: logging.New(io.Discard, logging.INFO), M: metrics.New(5), Mem: &metrics.MemSampler{}, Q: queue.New(1, 1, time.Second),
		Up: proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: time.Second})}
	h := s.Handler()
	for _, remote := range []string{"127.0.0.1:4444", "[::1]:4444", "100.64.1.2:4444", "192.168.0.5:1"} {
		req := httptest.NewRequest("GET", "/whoami", nil)
		req.RemoteAddr = remote
		req.Header.Set("Tailscale-User-Login", "owner@github")
		req.Header.Set("Tailscale-User-Name", "Owner")
		browserHeaders(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), "unauthenticated") {
			t.Fatal(remote, rec.Code, rec.Body.String())
		}
	}
}

type denyAll struct{}

func (denyAll) Allow(context.Context, auth.Identity) bool { return false }

func TestRateLimitHook(t *testing.T) {
	up := fakeUpstream(t, 0, nil, nil)
	defer up.Close()
	cfg := testDefault()
	cfg.Upstream = up.URL
	s := &Server{Cfg: cfg, Key: key, Limiter: denyAll{}, Log: logging.New(io.Discard, logging.INFO), M: metrics.New(5), Mem: &metrics.MemSampler{}, Q: queue.New(1, 1, time.Second),
		Up: proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: time.Second})}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

// PM decision: /ui/ is unauthenticated, so it must serve only the static
// files on disk, byte-for-byte, with nothing from config or secrets injected.
func TestUIStaticServesNoData(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", nil)
	want := map[string]string{"/ui/": "<html>ui</html>", "/ui/styles.css": "body{}", "/ui/app.js": "1"}
	for p, body := range want {
		resp, err := http.Get(ts.URL + p) // no Authorization, no cookie
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(b) != body {
			t.Fatalf("%s: %d %q", p, resp.StatusCode, b)
		}
		if resp.Header.Get("Set-Cookie") != "" {
			t.Fatalf("%s set a cookie", p)
		}
		var hdr strings.Builder
		resp.Header.Write(&hdr)
		if strings.Contains(string(b)+hdr.String(), key) {
			t.Fatalf("%s leaked the API key", p)
		}
	}
	for _, p := range []string{"/ui/../config.yaml", "/ui/%2e%2e/config.yaml", "/ui/nope.json"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatalf("%s served outside static files", p)
		}
	}
}

// GET / (exact path) redirects to the UI without credentials; everything else under / stays 404, the host allowlist still applies.
func TestRootRedirectsToUI(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", nil)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(method, path, host string) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		if host != "" {
			req.Host = host
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for _, m := range []string{"GET", "HEAD"} {
		resp := get(m, "/", "")
		if resp.StatusCode != 302 || resp.Header.Get("Location") != "/ui/" {
			t.Errorf("%s /: %d Location=%q, want 302 /ui/", m, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if resp := get("GET", "/", "evil.example"); resp.StatusCode != 421 {
		t.Errorf("GET / with a foreign Host: %d, want 421 (host allowlist)", resp.StatusCode)
	}
	for _, c := range []struct{ m, p string }{{"POST", "/"}, {"GET", "/nope"}, {"GET", "/index.html"}, {"GET", "//"}} {
		if resp := get(c.m, c.p, ""); resp.StatusCode == 302 || resp.StatusCode == 200 {
			t.Errorf("%s %s: %d, want not a redirect/200", c.m, c.p, resp.StatusCode)
		}
	}
	// the UI itself is still served at /ui/ (the redirect target)
	if resp := get("GET", "/ui/", ""); resp.StatusCode != 200 {
		t.Errorf("GET /ui/: %d, want 200", resp.StatusCode)
	}
}

// The redirect target is fixed: nothing in the query string or forwarded headers can steer it
// (no open redirect), and the query is not carried over.
func TestRootRedirectIgnoresQueryAndHeaders(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", nil)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, q := range []string{"?next=https://evil.example", "?next=//evil.example/x&url=/v1/me", "?", "?%2F%2Fevil.example", "?a=1&a=2"} {
		for _, m := range []string{"GET", "HEAD"} {
			req, _ := http.NewRequest(m, ts.URL+"/"+q, nil)
			req.Header.Set("X-Forwarded-Host", "evil.example")
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("Origin", "https://evil.example")
			resp, err := noFollow.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 302 || resp.Header.Get("Location") != "/ui/" {
				t.Errorf("%s /%s: %d Location=%q, want 302 exactly /ui/", m, q, resp.StatusCode, resp.Header.Get("Location"))
			}
		}
	}
}

// The redirect must never be cached (it depends on whether the UI is served).
func TestRootRedirectNoStore(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", nil)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"/", "/?x=1"} {
		for _, m := range []string{"GET", "HEAD"} {
			req, _ := http.NewRequest(m, ts.URL+p, nil)
			resp, err := noFollow.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 302 {
				t.Fatalf("%s %s: %d, want 302", m, p, resp.StatusCode)
			}
			if got := resp.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s %s: Cache-Control=%q, want no-store", m, p, got)
			}
		}
	}
}

// With the UI disabled (no static dir) there is no target: GET / keeps answering the JSON 404.
func TestRootNoUI404(t *testing.T) {
	ts, _ := newServer(t, "http://127.0.0.1:1", func(c *config.Config) { c.StaticDir = "" })
	resp, m := do(t, "GET", ts.URL+"/", "", false)
	if resp.StatusCode != 404 || m["detail"] != "Not Found" {
		t.Fatalf("GET / without UI: %d %v, want 404 Not Found", resp.StatusCode, m)
	}
}
