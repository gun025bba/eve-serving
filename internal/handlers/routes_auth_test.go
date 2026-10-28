package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRouteAuthMatrix checks every route under Bearer, session cookie and no
// credentials. Session-only routes must answer 403 to Bearer; every
// authenticated route must answer 401 without credentials.
func TestRouteAuthMatrix(t *testing.T) {
	type rt struct {
		method, path string
		public       bool // no auth required
		sessionOnly  bool
	}
	routes := []rt{
		{"GET", "/whoami", false, false},
		{"GET", "/v1/me", false, false},
		{"GET", "/v1/settings", false, false},
		{"PATCH", "/v1/settings", false, true},
		{"GET", "/v1/memory", false, true},
		{"DELETE", "/v1/memory", false, true},
		{"GET", "/v1/memory/export", false, true},
		{"POST", "/v1/memory/items", false, true},
		{"PATCH", "/v1/memory/items/1", false, true},
		{"DELETE", "/v1/memory/items/1", false, true},
		{"POST", "/v1/actions/1/approve", false, true},
		{"POST", "/v1/chat/completions", false, false},
		{"GET", "/v1/models", false, false},
		{"GET", "/v1/threads", false, false},
		{"POST", "/v1/threads", false, false},
		{"POST", "/v1/threads/import", false, false},
		{"GET", "/v1/threads/1", false, false},
		{"PATCH", "/v1/threads/1", false, false},
		{"DELETE", "/v1/threads/1", false, false},
		{"GET", "/v1/threads/1/messages", false, false},
		{"GET", "/v1/threads/1/messages/1/stream", false, false},
		{"POST", "/v1/threads/1/messages/1/cancel", false, false},
		{"PUT", "/v1/messages/1/feedback", false, false},
		{"DELETE", "/v1/messages/1/feedback", false, false},
		{"GET", "/v1/feedback/export", false, false},
		{"GET", "/v1/search?q=x", false, false},
		{"GET", "/v1/actions", false, true},
		{"POST", "/v1/actions/1/reissue", false, true},
		{"POST", "/v1/proposals/1/revise", false, true},
		{"POST", "/v1/pair", true, false},
		{"DELETE", "/v1/session", false, true},
		{"GET", "/v1/push/vapid-public-key", false, true},
		{"POST", "/v1/push/subscriptions", false, true},
		{"DELETE", "/v1/push/subscriptions", false, true},
		{"GET", "/health", true, false},
		{"GET", "/ready", false, false},
		{"GET", "/metrics", false, false},
		{"GET", "/stats", false, false},
		{"GET", "/admin/config", false, false},
		{"POST", "/admin/log-level", false, false},
		{"GET", "/openapi.json", false, false},
		{"GET", "/docs", false, false},
		{"GET", "/", true, false}, // 302 -> /ui/, public; the exact path only ("GET /{$}")
	}
	// Guard: the table must equal, as a set, every route registered anywhere
	// in the package (server.go, memory.go, ...), not just a count.
	registered := registeredRoutes(t)
	inTable := map[string]bool{}
	for _, rc := range routes {
		inTable[rc.method+" "+normRoute(rc.path)] = true
	}
	for r := range registered {
		if !inTable[r] {
			t.Errorf("route %q registered but missing from the matrix", r)
		}
	}
	for r := range inTable {
		if !registered[r] {
			t.Errorf("matrix route %q is not registered", r)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	bearer := func(r *httpReq) { r.Header.Set("Authorization", "Bearer "+key) }
	for _, rc := range routes {
		e := newPairEnvMemory(t) // fresh env: DELETE /v1/session would revoke a shared cookie
		w := e.do("POST", "/v1/pair", `{"code":"`+e.code(t)+`"}`, nil)
		if w.Code != 204 {
			t.Fatal("pair", w.Code)
		}
		tok := w.Result().Cookies()[0].Value
		body := ""
		if rc.method != "GET" && rc.method != "DELETE" {
			body = "{}"
		}
		name := rc.method + " " + rc.path
		// unauthenticated
		if c := e.do(rc.method, rc.path, body, nil).Code; !rc.public && c != 401 {
			t.Errorf("%s unauth: %d, want 401", name, c)
		}
		// Bearer
		c := e.do(rc.method, rc.path, body, bearer).Code
		switch {
		case rc.sessionOnly && c != 403:
			t.Errorf("%s bearer: %d, want 403", name, c)
		case !rc.sessionOnly && !rc.public && (c == 401 || c == 403):
			t.Errorf("%s bearer: %d, want allowed", name, c)
		}
		// session
		if c := e.do(rc.method, rc.path, body, withCookie(tok)).Code; !rc.public && (c == 401 || c == 403) {
			t.Errorf("%s session: %d, want allowed", name, c)
		}
	}
}

func TestStaticNoListingNoSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>ui</html>"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "assets"), 0o755))
	must(os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("js"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "withindex"), 0o755))
	must(os.WriteFile(filepath.Join(root, "withindex", "index.html"), []byte("sub"), 0o644))
	must(os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET"), 0o644))
	must(os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt")))
	must(os.Symlink(outside, filepath.Join(root, "leakdir")))
	h := staticFiles(root)
	for path, want := range map[string]int{
		"/":                   200,
		"/assets/app.js":      200,
		"/withindex/":         200,
		"/assets/":            404,
		"/assets":             404,
		"/leak.txt":           404,
		"/leakdir/secret.txt": 404,
		"/leakdir/":           404,
		"/nope":               404,
	} {
		w := newRec()
		h.ServeHTTP(w, newReq("GET", path))
		if w.Code != want {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
		if strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "app.js\"") {
			t.Errorf("%s leaked: %q", path, w.Body.String())
		}
	}
}

type httpReq = http.Request

func newRec() *httptest.ResponseRecorder { return httptest.NewRecorder() }
func newReq(m, p string) *http.Request   { return httptest.NewRequest(m, p, nil) }

var (
	reHandle = regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`)
	reWild   = regexp.MustCompile(`\{[^}]+\}`)
	reNumSeg = regexp.MustCompile(`/[0-9]+(/|$)`)
)

// normRoute maps "/v1/threads/1?q=x" and "/v1/threads/{id}" to the same key.
func normRoute(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	p = strings.ReplaceAll(p, "{$}", "") // "GET /{$}" = exactly "/"
	p = reWild.ReplaceAllString(p, "*")
	for reNumSeg.MatchString(p) {
		p = reNumSeg.ReplaceAllString(p, "/*$1")
	}
	return p
}

// registeredRoutes collects every "METHOD /path" mux.HandleFunc in the
// package's non-test sources, minus the catch-all and the static UI.
func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range reHandle.FindAllStringSubmatch(string(src), -1) {
			if m[1] == "GET" && strings.HasPrefix(m[2], "/ui") {
				continue
			}
			out[m[1]+" "+normRoute(m[2])] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("no routes found")
	}
	return out
}
