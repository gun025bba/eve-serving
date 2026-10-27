package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/queue"
	"github.com/gun025bba/eve-serving/internal/session"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

type pairEnv struct {
	h   http.Handler
	mgr *session.Manager
	now time.Time
}

func newPairEnv(t *testing.T) *pairEnv {
	cfg := testDefault()
	cfg.Auth.Providers = []string{auth.ProviderSession, auth.ProviderAPIKey} // real providers only
	cfg.AllowedOrigins = []string{testOrigin}
	cfg.AllowedUsers = []string{"alice@example"}
	cfg.AllowedHosts = []string{"gw.example.test"}
	mgr, err := session.New(session.NewMemory(), []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	e := &pairEnv{mgr: mgr, now: time.Now()}
	mgr.Now = func() time.Time { return e.now }
	s := &Server{Cfg: cfg, Key: key, Sessions: mgr, Store: memstore.New(), Log: logging.New(io.Discard, logging.INFO), M: metrics.New(5),
		Mem: &metrics.MemSampler{Needle: "none-xyz"}, Q: queue.New(1, 1, time.Second), Start: time.Now()}
	e.h = s.Handler()
	return e
}

func (e *pairEnv) do(method, path, body string, mut func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "gw.example.test"
	if body != "" || method != "GET" {
		browserHeaders(r)
	}
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *pairEnv) code(t *testing.T) string {
	c, err := e.mgr.IssueCode(context.Background(), "alice@example")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func withCookie(tok string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: session.CookieName, Value: tok}) }
}

func TestPairIssuesSessionCookie(t *testing.T) {
	e := newPairEnv(t)
	w := e.do("POST", "/v1/pair", `{"code":"`+strings.ToLower(e.code(t))+`"}`, nil)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	cs := w.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != session.CookieName || !cs[0].HttpOnly || !cs[0].Secure || cs[0].SameSite != http.SameSiteStrictMode || cs[0].Path != "/" || len(cs[0].Value) < 43 {
		t.Fatalf("cookie %+v", cs)
	}
	tok := cs[0].Value
	// The session authenticates as the users.login it was paired for, and a
	// forged Tailscale header changes nothing.
	w = e.do("GET", "/v1/me", "", func(r *http.Request) {
		withCookie(tok)(r)
		r.Header.Set("Tailscale-User-Login", "mallory@example")
	})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user":"alice@example"`) || !strings.Contains(w.Body.String(), `"provider":"session"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Browser-session-only routes refuse Bearer.
	if w := e.do("GET", "/v1/actions", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }); w.Code != 403 || !strings.Contains(w.Body.String(), "scope_denied") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := e.do("GET", "/v1/actions", "", withCookie(tok)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Logout revokes it.
	if w := e.do("DELETE", "/v1/session", "", withCookie(tok)); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := e.do("GET", "/v1/me", "", withCookie(tok)); w.Code != 401 || !strings.Contains(w.Body.String(), "invalid_session") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPairCodeOneTimeAndExpiry(t *testing.T) {
	e := newPairEnv(t)
	c := e.code(t)
	if w := e.do("POST", "/v1/pair", `{"code":"`+c+`"}`, nil); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := e.do("POST", "/v1/pair", `{"code":"`+c+`"}`, nil); w.Code != 401 || !strings.Contains(w.Body.String(), "pair_failed") {
		t.Fatal("reused code", w.Code, w.Body.String())
	}
	c = e.code(t)
	e.now = e.now.Add(session.CodeTTL + time.Second)
	if w := e.do("POST", "/v1/pair", `{"code":"`+c+`"}`, nil); w.Code != 401 {
		t.Fatal("expired code", w.Code)
	}
}

func TestPairLockout(t *testing.T) {
	e := newPairEnv(t)
	good := e.code(t)
	for i := 0; i < session.MaxFailures; i++ {
		if w := e.do("POST", "/v1/pair", `{"code":"ZZZZ-ZZZZ"}`, nil); w.Code != 401 {
			t.Fatal(i, w.Code)
		}
	}
	// Locked: the right code fails too, with the same response, and it is
	// invalidated so it cannot be used after the lock ends either.
	w := e.do("POST", "/v1/pair", `{"code":"`+good+`"}`, nil)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "pair_failed") {
		t.Fatal(w.Code, w.Body.String())
	}
	e.now = e.now.Add(session.LockFor + time.Second)
	if w := e.do("POST", "/v1/pair", `{"code":"`+good+`"}`, nil); w.Code != 401 {
		t.Fatal("code survived lockout", w.Code)
	}
	if w := e.do("POST", "/v1/pair", `{"code":"`+e.code(t)+`"}`, nil); w.Code != 204 {
		t.Fatal("new code after lock", w.Code)
	}
}

func TestPairNeedsBrowserCSRFHeaders(t *testing.T) {
	e := newPairEnv(t)
	c := e.code(t)
	w := e.do("POST", "/v1/pair", `{"code":"`+c+`"}`, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestSessionLifetime(t *testing.T) {
	e := newPairEnv(t)
	w := e.do("POST", "/v1/pair", `{"code":"`+e.code(t)+`"}`, nil)
	tok := w.Result().Cookies()[0].Value
	// Used every 29 days: idle never trips, absolute (180 d) does.
	for d := 29; d < 180; d += 29 {
		e.now = e.now.Add(29 * 24 * time.Hour)
		if w := e.do("GET", "/v1/me", "", withCookie(tok)); w.Code != 200 {
			t.Fatal("day", d, w.Code)
		}
	}
	e.now = e.now.Add(30 * 24 * time.Hour) // past 180 d absolute
	if w := e.do("GET", "/v1/me", "", withCookie(tok)); w.Code != 401 {
		t.Fatal("absolute expiry", w.Code)
	}
	w = e.do("POST", "/v1/pair", `{"code":"`+e.code(t)+`"}`, nil)
	tok = w.Result().Cookies()[0].Value
	e.now = e.now.Add(30*24*time.Hour + time.Minute)
	if w := e.do("GET", "/v1/me", "", withCookie(tok)); w.Code != 401 {
		t.Fatal("idle expiry", w.Code)
	}
}

func TestHostAllowlist(t *testing.T) {
	e := newPairEnv(t)
	for host, want := range map[string]int{"gw.example.test": 200, "GW.example.test:443": 200, "localhost:8000": 200, "127.0.0.1": 200,
		"[::1]:8000": 200, "evil.example": 421, "127.0.0.1.nip.io": 421, "": 400} {
		w := e.do("GET", "/health", "", func(r *http.Request) { r.Host = host })
		if w.Code != want {
			t.Errorf("host %q: %d want %d", host, w.Code, want)
		}
	}
}

// newPairEnvMemory is newPairEnv with memory on (memory + approve routes
// registered) and an in-memory audit ledger, for the route auth matrix.
func newPairEnvMemory(t *testing.T) *pairEnv {
	t.Helper()
	cfg := testDefault()
	cfg.Auth.Providers = []string{auth.ProviderSession, auth.ProviderAPIKey}
	cfg.AllowedOrigins = []string{testOrigin}
	cfg.AllowedUsers = []string{"alice@example"}
	cfg.AllowedHosts = []string{"gw.example.test"}
	cfg.Memory.Enabled = true
	mgr, err := session.New(session.NewMemory(), []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	fs, err := memory.Open(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	st := memstore.New()
	enableUserMemory(t, st, "alice@example")
	e := &pairEnv{mgr: mgr, now: time.Now()}
	mgr.Now = func() time.Time { return e.now }
	s := &Server{Cfg: cfg, Key: key, Sessions: mgr, Store: st, Log: logging.New(io.Discard, logging.INFO), M: metrics.New(5),
		Mem: &metrics.MemSampler{Needle: "none-xyz"}, Q: queue.New(1, 1, time.Second), Start: time.Now(),
		Audit: &failLedger{}, Memory: fs, MemoryAuditKey: []byte(strings.Repeat("k", 32))}
	e.h = s.Handler()
	return e
}
