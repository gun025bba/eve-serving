package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
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
