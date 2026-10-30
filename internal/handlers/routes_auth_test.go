package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
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
		{"POST", "/v1/briefing/preview", false, true},
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
		{"GET", "/", true, false},    // 302 -> /ui/, public; the exact path only ("GET /{$}")
		{"GET", "/ui", true, false},  // static UI (only mounted when static_dir is set; the matrix mux has one)
		{"GET", "/ui/", true, false}, // static UI subtree
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
	// Each matrix route must be served by a METHOD-qualified pattern of the
	// real mux (not the "/" catch-all, not a method-less pattern).
	realMux := muxWithUI(t)
	for _, rc := range routes {
		req := httptest.NewRequest(rc.method, rc.path, nil)
		_, pat := realMux.mux.Handler(req)
		if m := rePattern.FindStringSubmatch(pat); m == nil || m[1] != rc.method {
			t.Errorf("%s %s is served by mux pattern %q, want a %s-qualified pattern", rc.method, rc.path, pat, rc.method)
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
	rePattern = regexp.MustCompile(`^([A-Z]+) (/[^ ]*)$`)
	reWild    = regexp.MustCompile(`\{[^}]+\}`)
	reNumSeg  = regexp.MustCompile(`/[0-9]+(/|$)`)
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

// checkPatterns validates the patterns recorded by routeMux: each must be
// "METHOD /path" (a method-less pattern would answer every verb and bypass the
// matrix), registered once, with "/" as the only exception (404 catch-all).
func checkPatterns(patterns []string) []string {
	var bad []string
	seen := map[string]bool{}
	for _, p := range patterns {
		if p == "/" {
			continue
		}
		if !rePattern.MatchString(p) {
			bad = append(bad, "method-less or malformed pattern: "+p)
		}
		if seen[p] {
			bad = append(bad, "pattern registered twice: "+p)
		}
		seen[p] = true
	}
	return bad
}

func TestCheckPatterns(t *testing.T) {
	if got := checkPatterns([]string{"GET /a", "POST /a/{id}", "/"}); len(got) != 0 {
		t.Fatalf("valid set flagged: %v", got)
	}
	for _, p := range []string{"/x", "/x/", "GET/x", "get /x", "GET  /x", "{id}"} {
		if got := checkPatterns([]string{p}); len(got) != 1 {
			t.Errorf("%q not flagged: %v", p, got)
		}
	}
	if got := checkPatterns([]string{"GET /a", "GET /a"}); len(got) != 1 {
		t.Errorf("duplicate not flagged: %v", got)
	}
}

// TestRoutesAuthMux walks the real mux: every pattern the server registers
// (Handle, HandleFunc, helper functions, with or without a method) is recorded
// by routeMux and must be METHOD-qualified; and http.NewServeMux must have no
// other call site, so no route can be added behind the recorder's back.
func TestRoutesAuthMux(t *testing.T) {
	e, _ := newMemEnv(t, true, &failLedger{})
	m := srvOf(t, e).buildMux()
	if len(m.patterns) < 10 {
		t.Fatalf("only %d patterns recorded", len(m.patterns))
	}
	for _, b := range checkPatterns(m.patterns) {
		t.Error(b)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	sources := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sources[f] = string(src)
		calls += strings.Count(string(src), "http.NewServeMux(")
	}
	for _, b := range muxBypass(sources) {
		t.Error(b)
	}
	if calls != 1 {
		t.Errorf("http.NewServeMux() appears %d times in non-test sources, want exactly 1 (buildMux)", calls)
	}
}

// muxWithUI builds the real mux with the static UI mounted (static_dir set), so
// the UI patterns (GET /ui, GET /ui/) are part of what the matrix walks.
func muxWithUI(t *testing.T) *routeMux {
	t.Helper()
	e, _ := newMemEnv(t, true, &failLedger{})
	s := srvOf(t, e)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>ui</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Cfg.StaticDir = dir
	return s.buildMux()
}

// muxBypass reports every use of the routeMux.mux field outside the methods of
// *routeMux: s.mux.HandleFunc / Handle (or an alias of the field) would register
// a route without the recorder seeing it, so the matrix would never cover it.
func muxBypass(files map[string]string) []string {
	var bad []string
	fset := token.NewFileSet()
	for name, src := range files {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			return []string{name + ": " + err.Error()}
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				rt := fd.Recv.List[0].Type
				if st, ok := rt.(*ast.StarExpr); ok {
					rt = st.X
				}
				if id, ok := rt.(*ast.Ident); ok && id.Name == "routeMux" {
					continue // the recorder's own methods
				}
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if se, ok := n.(*ast.SelectorExpr); ok && se.Sel.Name == "mux" {
					bad = append(bad, fset.Position(se.Sel.Pos()).String()+": .mux used outside routeMux methods (bypasses the route recorder)")
				}
				return true
			})
		}
	}
	return bad
}

func TestMuxBypassDetected(t *testing.T) {
	const evil = `package handlers
func (s *Server) sneaky(mux *routeMux) { mux.mux.HandleFunc("GET /secret", nil) }
func (s *Server) alias(mux *routeMux)  { m := mux.mux; m.Handle("/x", nil) }`
	if got := muxBypass(map[string]string{"evil.go": evil}); len(got) != 2 {
		t.Fatalf("bypass not detected, got %v", got)
	}
	const fine = `package handlers
func (m *routeMux) Handle(p string, h http.Handler) { m.mux.Handle(p, h) }
func (s *Server) ok(mux *routeMux) { mux.HandleFunc("GET /a", nil) }`
	if got := muxBypass(map[string]string{"ok.go": fine}); len(got) != 0 {
		t.Fatalf("recorder methods flagged: %v", got)
	}
}

// registeredRoutes returns every METHOD-qualified pattern of the real mux
// (static UI included), minus the "/" catch-all.
func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, p := range muxWithUI(t).patterns {
		m := rePattern.FindStringSubmatch(p)
		if m == nil {
			if p != "/" {
				t.Errorf("registered pattern %q has no METHOD; it is not covered by the matrix", p)
			}
			continue
		}
		out[m[1]+" "+normRoute(m[2])] = true
	}
	if len(out) == 0 {
		t.Fatal("no routes found")
	}
	return out
}
