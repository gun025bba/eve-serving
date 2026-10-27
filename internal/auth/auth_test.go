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
		{"forbidden identity + valid key", "127.0.0.1:5000", "mallory@github", "k1", "", "", ErrForbiddenIdentity},
		{"forbidden identity + bad key", "127.0.0.1:5000", "mallory@github", "nope", "", "", ErrForbiddenIdentity},
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
	if _, err := c.Authenticate(newReq("127.0.0.1:1", "alice@github", "")); !errors.Is(err, ErrForbiddenIdentity) {
		t.Fatal("empty allowlist + trusted header must be forbidden:", err)
	}
	if _, err := c.Authenticate(newReq("127.0.0.1:1", "alice@github", "k1")); !errors.Is(err, ErrForbiddenIdentity) {
		t.Fatal("empty allowlist + trusted header + key must be forbidden:", err)
	}
	if id, err := c.Authenticate(newReq("127.0.0.1:1", "", "k1")); err != nil || id.Provider != ProviderAPIKey {
		t.Fatal("no header + key:", id, err)
	}
}

func TestBuildTailscaleNeedsAllowlist(t *testing.T) {
	for _, users := range [][]string{nil, {}, {"", "  "}} {
		if _, err := Build([]string{ProviderTailscale, ProviderAPIKey}, Options{APIKey: "k1", AllowedUsers: users}); err == nil {
			t.Fatalf("allowed_users %q: want startup error", users)
		}
	}
	if _, err := Build([]string{ProviderAPIKey}, Options{APIKey: "k1"}); err != nil {
		t.Fatal("api-key only must build:", err)
	}
}

func TestTailscaleLoopbackAndCommaJoined(t *testing.T) {
	c, err := Build([]string{ProviderTailscale, ProviderAPIKey}, Options{APIKey: "k1", AllowedUsers: []string{"a@x", "b@y"}})
	if err != nil {
		t.Fatal(err)
	}
	// Non-loopback: header ignored, valid key wins as api-key (handler maps to 200).
	if id, err := c.Authenticate(newReq("100.64.0.9:5000", "mallory@github", "k1")); err != nil || id.Provider != ProviderAPIKey || id.User != "api-key" {
		t.Fatal("non-loopback + forbidden login + key:", id, err)
	}
	for _, v := range []string{"a@x, b@y", "a@x,b@y", "a@x b@y"} {
		if _, err := c.Authenticate(newReq("127.0.0.1:5000", v, "k1")); !errors.Is(err, ErrForbiddenIdentity) {
			t.Fatalf("%q: err %v want forbidden", v, err)
		}
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

func TestPresentButEmptyOrDuplicateHeaders(t *testing.T) {
	c, err := Build([]string{ProviderTailscale, ProviderAPIKey}, Options{APIKey: "k1", AllowedUsers: []string{"alice@github"}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(h map[string][]string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		for k, v := range h {
			r.Header[http.CanonicalHeaderKey(k)] = v
		}
		return r
	}
	cases := []struct {
		name string
		h    map[string][]string
		want error
	}{
		{"empty login + valid key", map[string][]string{HeaderTailscaleLogin: {""}, "Authorization": {"Bearer k1"}}, ErrForbiddenIdentity},
		{"blank login + valid key", map[string][]string{HeaderTailscaleLogin: {"   "}, "Authorization": {"Bearer k1"}}, ErrForbiddenIdentity},
		{"duplicate login", map[string][]string{HeaderTailscaleLogin: {"alice@github", "alice@github"}}, ErrForbiddenIdentity},
		{"empty authorization", map[string][]string{"Authorization": {""}}, ErrInvalidKey},
		{"blank authorization", map[string][]string{"Authorization": {"  "}}, ErrInvalidKey},
		{"duplicate authorization", map[string][]string{"Authorization": {"Bearer k1", "Bearer k1"}}, ErrInvalidKey},
	}
	for _, tc := range cases {
		if _, err := c.Authenticate(mk(tc.h)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v want %v", tc.name, err, tc.want)
		}
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
