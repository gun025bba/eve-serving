//go:build e2e

package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// HeaderE2EIdentity names the injected user; it is honoured only together
// with the (test) API key as Bearer token.
const HeaderE2EIdentity = "X-Eve-Test-Identity"

func init() {
	Register(ProviderTestIdentity, func(o Options) (Authenticator, error) {
		if o.APIKey == "" || len(o.TestUsers) == 0 {
			return nil, errors.New("needs the api key and GW_TEST_IDENTITY_USERS")
		}
		e := &e2eIdentity{key: o.APIKey, users: map[string]struct{}{}}
		for _, u := range o.TestUsers {
			if u = strings.ToLower(strings.TrimSpace(u)); u != "" {
				e.users[u] = struct{}{}
			}
		}
		return e, nil
	})
}

type e2eIdentity struct {
	key   string
	users map[string]struct{}
}

func (e *e2eIdentity) Authenticate(r *http.Request) (Identity, error) {
	u := strings.TrimSpace(r.Header.Get(HeaderE2EIdentity))
	if u == "" {
		return Identity{}, ErrNoCredentials
	}
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(tok)), []byte(e.key)) != 1 {
		return Identity{}, ErrInvalidKey
	}
	if _, ok := e.users[strings.ToLower(u)]; !ok {
		return Identity{}, ErrForbiddenIdentity
	}
	return Identity{User: u, Provider: ProviderTestIdentity}, nil
}

// CheckInjection refuses every configuration that could reach prod data or
// real users.
func CheckInjection(c InjectionCheck) error {
	fail := func(f string, a ...any) error {
		return fmt.Errorf("test identity injection refused: "+f, a...)
	}
	switch strings.ToLower(strings.TrimSpace(c.Env)) {
	case "", "prod":
		return fail("GW_ENV must be set and not prod (got %q)", c.Env)
	}
	if !c.TCPEnabled {
		return fail("needs the TCP listener")
	}
	h := strings.Trim(c.Host, "[]")
	if ip := net.ParseIP(h); !(h == "localhost" || (ip != nil && ip.IsLoopback())) {
		return fail("bind host %q is not loopback", c.Host)
	}
	if c.UnixSocket != "" {
		return fail("GW_UNIX_SOCKET must be unset")
	}
	for _, p := range c.Providers {
		if p == ProviderTailscale {
			return fail("tailscale-identity provider must be disabled")
		}
	}
	if c.DBURL != "" {
		u, err := url.Parse(c.DBURL)
		name := ""
		if err == nil {
			name = strings.TrimPrefix(u.Path, "/")
		}
		if !strings.Contains(name, "_test") {
			return fail("database name must contain _test")
		}
	}
	if len(c.Users) == 0 {
		return fail("GW_TEST_IDENTITY_USERS is empty")
	}
	prod := map[string]bool{}
	for _, p := range c.ProdUsers {
		prod[strings.ToLower(strings.TrimSpace(p))] = true
	}
	for _, u := range c.Users {
		if prod[strings.ToLower(strings.TrimSpace(u))] {
			return fail("a test identity overlaps the allowed_users (prod) allowlist")
		}
	}
	return nil
}
