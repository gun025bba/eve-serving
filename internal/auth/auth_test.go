package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/session"
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

func newReq(login, bearer, cookie string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	if login != "" {
		r.Header.Set(HeaderTailscaleLogin, login)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: session.CookieName, Value: cookie})
	}
	return r
}

func pairedManager(t *testing.T, logins ...string) (*session.Manager, map[string]string) {
	m, err := session.New(session.NewMemory(), []byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	toks := map[string]string{}
	for _, l := range logins {
		code, err := m.IssueCode(context.Background(), l)
		if err != nil {
			t.Fatal(err)
		}
		tok, _, err := m.Pair(context.Background(), code)
		if err != nil {
			t.Fatal(err)
		}
		toks[l] = tok
	}
	return m, toks
}

func TestChain(t *testing.T) {
	m, toks := pairedManager(t, "alice@github", "mallory@github")
	c, err := Build([]string{ProviderSession, ProviderAPIKey}, Options{APIKey: "k1", AllowedUsers: []string{" Alice@GitHub ", ""}, Sessions: m})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, login, bearer, cookie string
		method, outLogin            string
		err                         error
	}{
		{"session", "", "", toks["alice@github"], ProviderSession, "alice@github", nil},
		{"session + spoofed header keeps session user", "mallory@github", "", toks["alice@github"], ProviderSession, "alice@github", nil},
		{"session no longer allowlisted", "", "", toks["mallory@github"], "", "", ErrForbiddenIdentity},
		{"unknown session", "", "", "nope", "", "", ErrInvalidSession},
		{"tailscale header alone is nothing", "alice@github", "", "", "", "", ErrNoCredentials},
		{"tailscale header + key = key user", "alice@github", "k1", "", ProviderAPIKey, "api-key", nil},
		{"api key", "", "k1", "", ProviderAPIKey, "api-key", nil},
		{"bad key", "", "nope", "", "", "", ErrInvalidKey},
		{"nothing", "", "", "", "", "", ErrNoCredentials},
	}
	for _, tc := range cases {
		id, err := c.Authenticate(newReq(tc.login, tc.bearer, tc.cookie))
		if !errors.Is(err, tc.err) && !(err == nil && tc.err == nil) {
			t.Errorf("%s: err %v want %v", tc.name, err, tc.err)
			continue
		}
		if err == nil && (id.Provider != tc.method || id.User != tc.outLogin) {
			t.Errorf("%s: got %+v", tc.name, id)
		}
	}
}

func TestSessionWithoutStore(t *testing.T) {
	c := Chain{NewSession(nil, []string{"a@x"}), APIKey{Key: "k1"}}
	if _, err := c.Authenticate(newReq("a@x", "", "tok")); err == nil {
		t.Fatal("authenticated without a session store")
	}
}

func TestStripTailscale(t *testing.T) {
	r := newReq("alice@github", "", "")
	r.Header.Set("Tailscale-User-Name", "Alice")
	r.Header.Set("Tailscale-User-Profile-Pic", "x")
	r.Header.Set("Tailscale-User-Anything-New", "x")
	ctx := StripTailscale(r)
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "tailscale-user-") {
			t.Fatal("not stripped:", k)
		}
	}
	if TailscaleHint(ctx) != "alice@github" {
		t.Fatal("hint", TailscaleHint(ctx))
	}
}

func TestLoadKeyMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	os.WriteFile(p, []byte("abc"), 0o644)
	os.Chmod(p, 0o644)
	if _, err := LoadKey(p); err == nil {
		t.Fatal("0644 key file accepted")
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
	m, toks := pairedManager(t, "a@x")
	c, err := Build([]string{ProviderAPIKey, ProviderSession}, Options{APIKey: "k", AllowedUsers: []string{"a@x"}, Sessions: m})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := c.Authenticate(newReq("", "", toks["a@x"])); err != nil || id.Provider != ProviderSession {
		t.Fatal(id, err)
	}
	Register("test-fixed", func(Options) (Authenticator, error) { return fixed{}, nil })
	c, _ = Build([]string{"test-fixed"}, Options{})
	if id, _ := c.Authenticate(newReq("", "", "")); id.User != "fixed" {
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

func TestLoadKeyRefusesSymlink(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "k")
	if err := os.WriteFile(real, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "l")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if k, err := LoadKey(real); err != nil || k != "abc" {
		t.Fatal(k, err)
	}
}

type stubAuth struct {
	err   error
	calls *int
}

func (s stubAuth) Authenticate(*http.Request) (Identity, error) {
	*s.calls++
	if s.err == nil {
		return Identity{User: "next", Provider: "stub"}, nil
	}
	return Identity{}, s.err
}

func TestChainStopsOnRejection(t *testing.T) {
	for _, rej := range []error{ErrForbiddenIdentity, ErrInvalidKey} {
		var a, b int
		c := Chain{stubAuth{rej, &a}, stubAuth{nil, &b}}
		if _, err := c.Authenticate(httptest.NewRequest("GET", "/", nil)); !errors.Is(err, rej) || b != 0 {
			t.Fatalf("%v: err=%v next calls=%d", rej, err, b)
		}
		if RejectReason(rej) == "" {
			t.Fatal("empty reason")
		}
	}
	var a, b int
	c := Chain{stubAuth{ErrNoCredentials, &a}, stubAuth{nil, &b}}
	if id, err := c.Authenticate(httptest.NewRequest("GET", "/", nil)); err != nil || id.User != "next" {
		t.Fatal(id, err)
	}
}

func TestChainProviderError(t *testing.T) {
	boom := errors.New("boom")
	var a, b int
	c := Chain{stubAuth{boom, &a}, stubAuth{nil, &b}}
	_, err := c.Authenticate(httptest.NewRequest("GET", "/", nil))
	if !errors.Is(err, boom) || b != 0 || RejectReason(err) != "provider_error" {
		t.Fatalf("err=%v next=%d reason=%s", err, b, RejectReason(err))
	}
}

// Scopes and the CSRF requirement are decided here, in the auth layer; the
// handlers only call HasScope / NeedsCSRF and never compare providers.
func TestProviderScopes(t *testing.T) {
	m, toks := pairedManager(t, "alice@github")
	c, err := Build([]string{ProviderSession, ProviderAPIKey}, Options{APIKey: "k1", AllowedUsers: []string{"alice@github"}, Sessions: m})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := c.Authenticate(newReq("", "", toks["alice@github"]))
	if err != nil || !sess.HasScope(ScopeInteractive) || !sess.HasScope(ScopeAdmin) || !sess.NeedsCSRF() {
		t.Fatal("session principal", sess, err)
	}
	key, err := c.Authenticate(newReq("", "k1", ""))
	if err != nil || key.HasScope(ScopeInteractive) || !key.HasScope(ScopeAdmin) || key.NeedsCSRF() {
		t.Fatal("api-key principal", key, err)
	}
	// Unknown/zero principals: no scopes, and CSRF stays required (fail-closed).
	for _, id := range []Identity{{}, {User: "u", Provider: "cookie-session"}, {User: "u", Provider: ProviderTestIdentity}} {
		if id.HasScope(ScopeInteractive) || id.HasScope(ScopeAdmin) || !id.NeedsCSRF() {
			t.Fatal("unexpected grant", id)
		}
	}
}

// #131: an empty allowlist must fail closed. A valid paired session is refused
// (403-class ErrForbiddenIdentity, never a fall-through to the key) and nobody
// can be paired.
func TestEmptyAllowlistFailsClosed(t *testing.T) {
	m, toks := pairedManager(t, "alice@github")
	for _, users := range [][]string{nil, {}, {"", "  "}} {
		c, err := Build([]string{ProviderSession, ProviderAPIKey}, Options{APIKey: "k1", AllowedUsers: users, Sessions: m})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Authenticate(newReq("", "", toks["alice@github"])); !errors.Is(err, ErrForbiddenIdentity) {
			t.Fatalf("allowed %q: session err %v want forbidden", users, err)
		}
		if _, err := c.Authenticate(newReq("", "k1", toks["alice@github"])); !errors.Is(err, ErrForbiddenIdentity) {
			t.Fatalf("allowed %q: session + key err %v want forbidden (no fall-through)", users, err)
		}
		if AllowedLogin(users, "alice@github") {
			t.Fatalf("allowed %q: AllowedLogin true", users)
		}
	}
}

// #131: a comma- or whitespace-joined list is never a login.
func TestJoinedLoginsAreNeverLogins(t *testing.T) {
	for _, l := range []string{"a@x, b@y", "a@x,b@y", "a@x b@y", "a@x\tb@y", ",", ""} {
		if ValidLogin(l) {
			t.Errorf("ValidLogin(%q) = true", l)
		}
		if AllowedLogin([]string{l, "a@x", "b@y"}, l) {
			t.Errorf("AllowedLogin accepted joined login %q", l)
		}
	}
	if !ValidLogin("alice@github") || !AllowedLogin([]string{" Alice@GitHub "}, "ALICE@github") {
		t.Fatal("a plain login must still work")
	}
	// A malformed allowlist entry is ignored, not honoured: a session for the
	// joined name is refused even though the entry "matches" it literally.
	m, toks := pairedManager(t, "a@x,b@y")
	c, err := Build([]string{ProviderSession, ProviderAPIKey}, Options{APIKey: "k1", AllowedUsers: []string{"a@x,b@y"}, Sessions: m})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(newReq("", "", toks["a@x,b@y"])); !errors.Is(err, ErrForbiddenIdentity) {
		t.Fatalf("joined login session: err %v want forbidden", err)
	}
}
