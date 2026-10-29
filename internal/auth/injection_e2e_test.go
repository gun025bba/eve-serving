//go:build e2e

package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okCheck() InjectionCheck {
	return InjectionCheck{Env: "e2e", Host: "127.0.0.1", DBURL: "postgres://u@127.0.0.1:5432/llmchat_test?sslmode=disable",
		ProdUsers: []string{"real@example.com"}, Providers: []string{ProviderAPIKey}, Users: []string{"e2e-alice@example.test"}}
}

func TestCheckInjectionRefusals(t *testing.T) {
	if err := okCheck(); CheckInjection(err) != nil {
		t.Fatal(CheckInjection(err))
	}
	cases := map[string]func(*InjectionCheck){
		"env unset":    func(c *InjectionCheck) { c.Env = "" },
		"env prod":     func(c *InjectionCheck) { c.Env = "PROD" },
		"non-loopback": func(c *InjectionCheck) { c.Host = "0.0.0.0" },
		"wildcard v6":  func(c *InjectionCheck) { c.Host = "::" },
		"prod db":      func(c *InjectionCheck) { c.DBURL = "postgres://u@127.0.0.1:5432/llmchat?sslmode=disable" },
		"overlap":      func(c *InjectionCheck) { c.Users = append(c.Users, "REAL@example.com") },

		"no test users": func(c *InjectionCheck) { c.Users = nil },
	}
	for name, mut := range cases {
		c := okCheck()
		mut(&c)
		if err := CheckInjection(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := okCheck()
	c.DBURL, c.Host = "", "::1"
	if err := CheckInjection(c); err != nil {
		t.Fatal("no-db + ::1:", err)
	}
}

func TestE2EIdentityNeedsKeyAndHeader(t *testing.T) {
	ch, err := Build([]string{ProviderTestIdentity, ProviderAPIKey}, Options{APIKey: "tk", TestUsers: []string{"e2e-alice@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	req := func(ident, bearer string) (Identity, error) {
		r := httptest.NewRequest("GET", "/", nil)
		if ident != "" {
			r.Header.Set(HeaderE2EIdentity, ident)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		return ch.Authenticate(r)
	}
	if id, err := req("e2e-alice@example.test", "tk"); err != nil || id.Provider != ProviderTestIdentity || id.User != "e2e-alice@example.test" {
		t.Fatal(id, err)
	}
	if _, err := req("e2e-alice@example.test", ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("header without key:", err)
	}
	if _, err := req("e2e-alice@example.test", "wrong"); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("wrong key:", err)
	}
	// unlisted identity is never injected; the valid key alone still yields only the key user
	if id, _ := req("mallory@example.test", "tk"); id.Provider == ProviderTestIdentity || id.User == "mallory@example.test" {
		t.Fatal("unlisted injected:", id)
	}
	if _, err := ch[0].Authenticate(func() *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set(HeaderE2EIdentity, "mallory@example.test")
		r.Header.Set("Authorization", "Bearer tk")
		return r
	}()); !errors.Is(err, ErrForbiddenIdentity) {
		t.Fatal("unlisted:", err)
	}
	if id, err := req("", "tk"); err != nil || id.Provider != ProviderAPIKey {
		t.Fatal("key only:", id, err)
	}
	if !strings.HasPrefix(LogUser(Identity{User: "e2e-alice@example.test", Provider: ProviderTestIdentity}), "t-") {
		t.Fatal("not pseudonymized")
	}
}
