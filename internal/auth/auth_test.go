package auth

import (
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
	c := Chain{NewTailscale([]string{" Alice@GitHub ", ""}), APIKey{Key: "k1"}}
	cases := []struct {
		name, remote, login, bearer string
		method, outLogin            string
		err                         error
	}{
		{"loopback allowlisted", "127.0.0.1:5000", "alice@github", "", MethodTailscale, "alice@github", nil},
		{"loopback v6 case-insensitive", "[::1]:5000", "ALICE@github", "", MethodTailscale, "ALICE@github", nil},
		{"loopback not allowlisted", "127.0.0.1:5000", "mallory@github", "", "", "", ErrForbiddenIdentity},
		{"non-loopback header ignored", "100.64.0.9:5000", "alice@github", "", "", "", ErrNoCredentials},
		{"lan header ignored", "192.168.1.2:5000", "alice@github", "", "", "", ErrNoCredentials},
		{"api key", "100.64.0.9:5000", "", "k1", MethodAPIKey, "", nil},
		{"api key + spoofed header", "100.64.0.9:5000", "alice@github", "k1", MethodAPIKey, "", nil},
		{"bad key", "127.0.0.1:5000", "", "nope", "", "", ErrInvalidKey},
		{"nothing", "127.0.0.1:5000", "", "", "", "", ErrNoCredentials},
	}
	for _, tc := range cases {
		id, err := c.Authenticate(newReq(tc.remote, tc.login, tc.bearer))
		if !errors.Is(err, tc.err) && !(err == nil && tc.err == nil) {
			t.Errorf("%s: err %v want %v", tc.name, err, tc.err)
			continue
		}
		if err == nil && (id.Method != tc.method || id.Login != tc.outLogin) {
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
