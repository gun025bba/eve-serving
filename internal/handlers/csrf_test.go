package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/proxy"
	"github.com/gun025bba/eve-serving/internal/queue"
)

// Placeholder origin; never a real host.
const testOrigin = "https://gateway.example.test"

// browserHeaders makes a request look like a same-origin UI fetch.
func browserHeaders(r *http.Request) {
	r.Header.Set(HeaderEveRequest, "1")
	r.Header.Set("Origin", testOrigin)
	if r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/json")
	}
}

func csrfServer(t *testing.T, upURL string, origins []string, logw io.Writer) *Server {
	cfg := config.Default()
	cfg.Upstream, cfg.Model, cfg.MinFreeMemGB = upURL, "m1", 0
	cfg.AllowedUsers = []string{"owner@example"}
	cfg.AllowedOrigins = origins
	cfg.StaticDir = t.TempDir()
	s := &Server{Cfg: cfg, Key: key, Log: logging.New(logw, logging.INFO), M: metrics.New(5), Mem: &metrics.MemSampler{Needle: "no-such-proc-xyz"},
		Q: queue.New(2, 2, time.Second), Up: proxy.New(proxy.Options{BaseURL: upURL, ConnectTimeout: time.Second, ReadTimeout: 5 * time.Second}), Start: time.Now()}
	return s
}

func TestCSRF(t *testing.T) {
	up := fakeUpstream(t, 0, nil, nil)
	defer up.Close()
	logbuf := &strings.Builder{}
	s := csrfServer(t, up.URL, []string{testOrigin}, logbuf)
	full := s.Handler()
	// Proactive-action routes (reject/revise/reissue/read) land in later PRs;
	// they go through the same authed() wrapper, so mount stubs on it.
	ok := s.authed(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := http.NewServeMux()
	h.Handle("/v1/chat/completions", full)
	h.Handle("/", ok)

	const chat = `{"messages":[{"role":"user","content":"hi"}]}`
	type tc struct {
		name, method, path, body string
		hdr                      map[string]string // "" value = delete
		tailscale, bearer        bool
		want                     int
		reason                   string
	}
	ui := func(extra map[string]string) map[string]string {
		m := map[string]string{"X-Eve-Request": "1", "Origin": testOrigin, "Content-Type": "application/json"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []tc{
		{"ui chat completions", "POST", "/v1/chat/completions", chat, ui(nil), true, false, 200, ""},
		{"ui chat sec-fetch same-origin", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Sec-Fetch-Site": "same-origin"}), true, false, 200, ""},
		{"chat no header", "POST", "/v1/chat/completions", chat, ui(map[string]string{"X-Eve-Request": ""}), true, false, 403, "missing_header"},
		{"chat header wrong value", "POST", "/v1/chat/completions", chat, ui(map[string]string{"X-Eve-Request": "true"}), true, false, 403, "missing_header"},
		{"chat no origin", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Origin": ""}), true, false, 403, "missing_origin"},
		{"chat null origin", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Origin": "null"}), true, false, 403, "missing_origin"},
		{"chat other origin", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Origin": "https://evil.example.test"}), true, false, 403, "origin_mismatch"},
		{"chat origin prefix", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Origin": testOrigin + ".evil.test"}), true, false, 403, "origin_mismatch"},
		{"chat origin trailing slash", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Origin": testOrigin + "/"}), true, false, 403, "origin_mismatch"},
		{"chat cross-site fetch", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Sec-Fetch-Site": "cross-site"}), true, false, 403, "fetch_site"},
		{"chat same-site fetch", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Sec-Fetch-Site": "same-site"}), true, false, 403, "fetch_site"},
		{"chat text/plain", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Content-Type": "text/plain"}), true, false, 403, "content_type"},
		{"chat form", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Content-Type": "application/x-www-form-urlencoded"}), true, false, 403, "content_type"},
		{"chat json charset ok", "POST", "/v1/chat/completions", chat, ui(map[string]string{"Content-Type": "application/json; charset=utf-8"}), true, false, 200, ""},
		{"chat bearer exempt", "POST", "/v1/chat/completions", chat, map[string]string{"Content-Type": "text/plain"}, false, true, 200, ""},
		{"reject ok", "POST", "/v1/actions/A1/reject", "", ui(map[string]string{"Content-Type": ""}), true, false, 200, ""},
		{"reject no origin", "POST", "/v1/actions/A1/reject", "", ui(map[string]string{"Origin": ""}), true, false, 403, "missing_origin"},
		{"revise ok", "POST", "/v1/actions/A1/revise", `{"payload":{}}`, ui(nil), true, false, 200, ""},
		{"revise no header", "POST", "/v1/actions/A1/revise", `{"payload":{}}`, ui(map[string]string{"X-Eve-Request": ""}), true, false, 403, "missing_header"},
		{"revise text/plain", "POST", "/v1/actions/A1/revise", `{"payload":{}}`, ui(map[string]string{"Content-Type": "text/plain"}), true, false, 403, "content_type"},
		{"reissue ok", "POST", "/v1/actions/A1/reissue", "", ui(map[string]string{"Content-Type": ""}), true, false, 200, ""},
		{"reissue cross-site", "POST", "/v1/actions/A1/reissue", "", ui(map[string]string{"Sec-Fetch-Site": "cross-site"}), true, false, 403, "fetch_site"},
		{"read ok", "POST", "/v1/messages/M1/read", "", ui(map[string]string{"Content-Type": ""}), true, false, 200, ""},
		{"read other origin", "POST", "/v1/messages/M1/read", "", ui(map[string]string{"Origin": "https://evil.example.test"}), true, false, 403, "origin_mismatch"},
		{"put no header", "PUT", "/v1/messages/M1/feedback", `{}`, ui(map[string]string{"X-Eve-Request": ""}), true, false, 403, "missing_header"},
		{"patch no origin", "PATCH", "/v1/threads/T1", `{}`, ui(map[string]string{"Origin": ""}), true, false, 403, "missing_origin"},
		{"delete no body ok", "DELETE", "/v1/threads/T1", "", ui(map[string]string{"Content-Type": ""}), true, false, 200, ""},
		{"delete no header", "DELETE", "/v1/threads/T1", "", ui(map[string]string{"X-Eve-Request": ""}), true, false, 403, "missing_header"},
		{"delete bearer exempt", "DELETE", "/v1/threads/T1", "", nil, false, true, 200, ""},
		{"get unchecked", "GET", "/v1/threads", "", nil, true, false, 200, ""},
		{"head unchecked", "HEAD", "/v1/threads", "", nil, true, false, 200, ""},
		{"options unchecked", "OPTIONS", "/v1/threads", "", nil, true, false, 200, ""},
		{"tailscale plus bad bearer not exempt", "POST", "/v1/actions/A1/reject", "", map[string]string{"Authorization": "Bearer wrong"}, true, false, 403, "missing_header"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body io.Reader
			if c.body != "" {
				body = strings.NewReader(c.body)
			}
			req := httptest.NewRequest(c.method, c.path, body)
			req = viaSocket(req)
			if c.tailscale {
				req.Header.Set("Tailscale-User-Login", "owner@example")
			}
			if c.bearer {
				req.Header.Set("Authorization", "Bearer "+key)
			}
			for k, v := range c.hdr {
				if v == "" {
					req.Header.Del(k)
				} else {
					req.Header.Set(k, v)
				}
			}
			before := csrfCount(s, c.reason)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status %d want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			if c.want == 403 {
				if strings.TrimSpace(rec.Body.String()) != `{"error_code":"invalid"}` {
					t.Fatalf("body %q", rec.Body.String())
				}
				if d := csrfCount(s, c.reason) - before; d != 1 {
					t.Fatalf("metric reason=%s delta %v", c.reason, d)
				}
				if !strings.Contains(logbuf.String(), `"reason":"`+c.reason+`"`) {
					t.Fatalf("reason not logged: %s", logbuf.String())
				}
			}
		})
	}
}

// Cookie-only requests carry no Bearer, so they are never exempt.
func TestCSRFCookieNotExempt(t *testing.T) {
	s := csrfServer(t, "http://127.0.0.1:1", []string{testOrigin}, io.Discard)
	s.Handler()
	req := httptest.NewRequest("POST", "/x", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "v"})
	browserHeaders(req)
	req.Header.Del(HeaderEveRequest)
	if got := s.csrfCheck(req, auth.Identity{User: "u", Provider: "cookie-session"}); got != "missing_header" {
		t.Fatal(got)
	}
}

func TestCSRFEmptyAllowlistFailsClosed(t *testing.T) {
	logbuf := &strings.Builder{}
	s := csrfServer(t, "http://127.0.0.1:1", nil, logbuf)
	h := s.Handler()
	if !strings.Contains(logbuf.String(), "csrf_allowlist_empty") {
		t.Fatal("no startup warning:", logbuf.String())
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	req = viaSocket(req)
	req.Header.Set("Tailscale-User-Login", "owner@example")
	browserHeaders(req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 || csrfCount(s, "no_allowlist") != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestCSRFAllowedOriginsEnv(t *testing.T) {
	c, err := config.Load("", func(k string) string {
		if k == "GW_ALLOWED_ORIGINS" {
			return testOrigin + ", https://other.example.test"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AllowedOrigins) != 2 || c.AllowedOrigins[0] != testOrigin {
		t.Fatal(c.AllowedOrigins)
	}
}

func csrfCount(s *Server, reason string) float64 {
	mfs, _ := s.M.Reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "eve_csrf_rejected_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == reason {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestCORSWildcardRefused(t *testing.T) {
	for _, v := range []string{"*", "https://a.example.test, *"} {
		_, err := config.Load("", func(k string) string {
			if k == "GW_CORS_ORIGINS" {
				return v
			}
			return ""
		})
		if err == nil {
			t.Fatalf("%q accepted", v)
		}
	}
}

func TestCORSRouteDeny(t *testing.T) {
	const o = "https://other.example.test"
	ts, _ := newServer(t, "http://127.0.0.1:1", func(c *config.Config) { c.CORSOrigins = []string{o} })
	cases := []struct {
		path   string
		denied bool
	}{
		{"/v1/actions", true}, {"/v1/actions/A1/reject", true}, {"/v1/actions/A1/revise", true},
		{"/v1/actions/A1/reissue", true}, {"/v1/future/X/revise", true}, {"/v1/future/X/reissue", true},
		{"/v1/tokens", true}, {"/v1/confirm/X", true}, {"/v1/Actions", true},
		{"/v1/chat/completions", false}, {"/v1/threads", false},
	}
	for _, c := range cases {
		for _, m := range []string{"OPTIONS", "GET", "POST"} {
			req, _ := http.NewRequest(m, ts.URL+c.path, nil)
			req.Header.Set("Origin", o)
			if m == "OPTIONS" {
				req.Header.Set("Access-Control-Request-Method", "POST")
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			r.Body.Close()
			got := r.Header.Get("Access-Control-Allow-Origin") != "" || r.Header.Get("Access-Control-Allow-Methods") != ""
			if c.denied && got {
				t.Fatalf("%s %s got CORS headers", m, c.path)
			}
			if c.denied && m == "OPTIONS" && r.StatusCode != 403 {
				t.Fatalf("%s preflight %d", c.path, r.StatusCode)
			}
			if !c.denied && !got {
				t.Fatalf("%s %s missing CORS headers", m, c.path)
			}
		}
	}
}
