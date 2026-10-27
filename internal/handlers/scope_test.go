package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
)

// Features decide on principal scopes only: the provider name (session,
// api-key, anything else) is ignored outside the auth layer.
func TestFeaturesUseScopesNotProvider(t *testing.T) {
	s := &Server{Cfg: testDefault(), Log: logging.New(io.Discard, logging.INFO), M: metrics.New(10)}
	req := func(id auth.Identity) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		return r.WithContext(auth.WithIdentity(r.Context(), id))
	}
	cases := []struct {
		name               string
		id                 auth.Identity
		interactive, admin bool
	}{
		{"session provider without scopes", auth.Identity{User: "alice@example", Provider: auth.ProviderSession}, false, false},
		{"api-key provider granted interactive", auth.Identity{User: "x", Provider: auth.ProviderAPIKey, Scopes: []string{auth.ScopeInteractive}}, true, false},
		{"unknown provider with admin", auth.Identity{User: "x", Provider: "other", Scopes: []string{auth.ScopeAdmin}}, false, true},
		{"both", auth.Identity{User: "x", Provider: "other", Scopes: []string{auth.ScopeInteractive, auth.ScopeAdmin}}, true, true},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		denied := s.denyAPIKey(w, req(c.id))
		if denied == c.interactive {
			t.Errorf("%s: denyAPIKey=%v", c.name, denied)
		}
		if denied && w.Code != 403 {
			t.Errorf("%s: deny status %d", c.name, w.Code)
		}
		if got := s.isAdmin(req(c.id)); got != c.admin {
			t.Errorf("%s: isAdmin=%v", c.name, got)
		}
	}
	if hasScope(httptest.NewRequest("GET", "/", nil), auth.ScopeInteractive) {
		t.Fatal("unauthenticated request has scope")
	}
}

// PATCH /v1/settings needs the interactive scope; without it -> 403 before
// the body is read.
func TestPatchSettingsNeedsInteractive(t *testing.T) {
	s := &Server{Cfg: testDefault(), Log: logging.New(io.Discard, logging.INFO), M: metrics.New(10)}
	r := httptest.NewRequest("PATCH", "/v1/settings", nil)
	r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{User: "alice@example", Provider: auth.ProviderSession}))
	w := httptest.NewRecorder()
	s.patchSettings(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// TestInteractiveOnlyUsesScope pins the actions/reissue/revise/push gate:
// any principal with the interactive scope passes regardless of provider; a
// principal without it (API key) gets the unchanged 403 scope_denied body.
// DELETE /v1/session (sessionOnly) stays the single provider-based exception.
func TestInteractiveOnlyUsesScope(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }
	run := func(mw func(http.HandlerFunc) http.HandlerFunc, id auth.Identity) (int, string) {
		r := httptest.NewRequest("GET", "/v1/actions", nil)
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		w := httptest.NewRecorder()
		mw(ok)(w, r)
		return w.Code, w.Body.String()
	}
	sess := auth.Identity{User: "u", Provider: auth.ProviderSession, Scopes: []string{auth.ScopeInteractive, auth.ScopeAdmin}}
	key := auth.Identity{User: "api-key", Provider: auth.ProviderAPIKey, Scopes: []string{auth.ScopeAdmin}}
	other := auth.Identity{User: "u", Provider: "other", Scopes: []string{auth.ScopeInteractive}}

	if c, _ := run(interactiveOnly, sess); c != 204 {
		t.Fatalf("session: %d", c)
	}
	if c, _ := run(interactiveOnly, other); c != 204 {
		t.Fatalf("interactive non-session provider should pass: %d", c)
	}
	c1, b1 := run(interactiveOnly, key)
	c2, b2 := run(sessionOnly, key)
	if c1 != 403 || !strings.Contains(b1, `"scope_denied"`) || c1 != c2 || b1 != b2 {
		t.Fatalf("api key: interactiveOnly %d %s vs sessionOnly %d %s", c1, b1, c2, b2)
	}
	if c, _ := run(sessionOnly, other); c != 403 {
		t.Fatalf("logout stays provider-based: %d", c)
	}
}
