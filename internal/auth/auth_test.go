package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheck(t *testing.T) {
	cases := map[string]bool{"Bearer k1": true, "bearer k1": true, "Bearer k2": false, "k1": false, "": false, "Basic k1": false}
	for h, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		if got := Check(r, "k1"); got != want {
			t.Errorf("%q: got %v want %v", h, got, want)
		}
	}
}

func TestLoadKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	os.WriteFile(p, []byte("  abc\n"), 0o600)
	if k, err := LoadKey(p); err != nil || k != "abc" {
		t.Fatal(k, err)
	}
	os.WriteFile(p, []byte("\n"), 0o600)
	if _, err := LoadKey(p); err == nil {
		t.Fatal("empty key accepted")
	}
}

func newReq(remote, login, bearer string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	if login != "" {
		r.Header.Set(HeaderTailscaleLogin, login)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

func TestChain(t *testing.T) {
	c, err := Build([]string{ProviderTailscale, ProviderAPIKey}, Options{APIKey: "k1", AllowedUsers: []string{" Alice@GitHub ", ""}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, remote, login, bearer string
		method, outLogin            string
		err                         error
	}{
		{"loopback allowlisted", "127.0.0.1:5000", "alice@github", "", ProviderTailscale, "alice@github", nil},
		{"loopback v6 case-insensitive", "[::1]:5000", "ALICE@github", "", ProviderTailscale, "ALICE@github", nil},
		{"loopback not allowlisted", "127.0.0.1:5000", "mallory@github", "", "", "", ErrForbiddenIdentity},
		{"non-loopback header ignored", "100.64.0.9:5000", "alice@github", "", "", "", ErrNoCredentials},
		{"lan header ignored", "192.168.1.2:5000", "alice@github", "", "", "", ErrNoCredentials},
		{"api key", "100.64.0.9:5000", "", "k1", ProviderAPIKey, "api-key", nil},
		{"api key + spoofed header", "100.64.0.9:5000", "alice@github", "k1", ProviderAPIKey, "api-key", nil},
		{"bad key", "127.0.0.1:5000", "", "nope", "", "", ErrInvalidKey},
		{"nothing", "127.0.0.1:5000", "", "", "", "", ErrNoCredentials},
	}
	for _, tc := range cases {
		id, err := c.Authenticate(newReq(tc.remote, tc.login, tc.bearer))
		if !errors.Is(err, tc.err) && !(err == nil && tc.err == nil) {
			t.Errorf("%s: err %v want %v", tc.name, err, tc.err)
			continue
		}
		if err == nil && (id.Provider != tc.method || id.User != tc.outLogin) {
			t.Errorf("%s: got %+v", tc.name, id)
		}
	}
}

func TestEmptyAllowlistDisabled(t *testing.T) {
	ts := NewTailscale(nil)
	if ts.Enabled() {
		t.Fatal("enabled")
	}
	c := Chain{ts, APIKey{Key: "k1"}}
	if _, err := c.Authenticate(newReq("127.0.0.1:1", "alice@github", "")); err == nil {
		t.Fatal("empty allowlist authenticated")
	}
}

func TestStripUntrusted(t *testing.T) {
	r := newReq("100.64.0.9:1", "alice@github", "")
	StripUntrusted(r)
	if r.Header.Get(HeaderTailscaleLogin) != "" {
		t.Fatal("not stripped")
	}
	r = newReq("127.0.0.1:1", "alice@github", "")
	StripUntrusted(r)
	if r.Header.Get(HeaderTailscaleLogin) == "" {
		t.Fatal("loopback stripped")
	}
}

func TestRegistry(t *testing.T) {
	if _, err := Build([]string{"nope"}, Options{}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := Build(nil, Options{}); err == nil {
		t.Fatal("empty provider list accepted")
	}
	// order matters: api-key first still authenticates key callers
	c, err := Build([]string{ProviderAPIKey, ProviderTailscale}, Options{APIKey: "k", AllowedUsers: []string{"a@x"}})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := c.Authenticate(newReq("127.0.0.1:1", "a@x", "")); err != nil || id.Provider != ProviderTailscale {
		t.Fatal(id, err)
	}
	Register("test-fixed", func(Options) (Authenticator, error) { return fixed{}, nil })
	c, _ = Build([]string{"test-fixed"}, Options{})
	if id, _ := c.Authenticate(newReq("1.2.3.4:1", "", "")); id.User != "fixed" {
		t.Fatal(id)
	}
}

type fixed struct{}

func (fixed) Authenticate(*http.Request) (Identity, error) {
	return Identity{User: "fixed", Provider: "test-fixed"}, nil
}

func TestContextAndNoop(t *testing.T) {
	ctx := WithIdentity(context.Background(), Identity{User: "u", Provider: "p"})
	if id, ok := FromContext(ctx); !ok || id.User != "u" {
		t.Fatal(id)
	}
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("identity in empty ctx")
	}
	if !(NoopLimiter{}).Allow(ctx, Identity{}) {
		t.Fatal("noop denied")
	}
}

// Scopes are decided here, in the auth layer: a browser identity gets
// interactive, an API key never does. Features only call HasScope.
func TestProviderScopes(t *testing.T) {
	c, err := Build([]string{ProviderTailscale, ProviderAPIKey}, Options{APIKey: "k", AllowedUsers: []string{"a@x"}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Authenticate(newReq("127.0.0.1:1", "a@x", ""))
	if err != nil || !id.HasScope(ScopeInteractive) {
		t.Fatal("browser identity lacks interactive", id, err)
	}
	id, err = c.Authenticate(newReq("127.0.0.1:1", "", "k"))
	if err != nil || id.Provider != ProviderAPIKey || id.HasScope(ScopeInteractive) || len(id.Scopes) != 0 {
		t.Fatal("api key must have no scopes", id, err)
	}
	if (Identity{}).HasScope(ScopeInteractive) {
		t.Fatal("zero identity has scope")
	}
}
