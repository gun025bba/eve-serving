package handlers

import (
	"net/http"
	"strings"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
)

// Most handler tests act as a paired browser. Instead of pairing in every
// test, the test-only provider below turns testUserHeader into a session
// identity (Provider "session", allowlist enforced) exactly like the real
// cookie provider would; pair_test.go covers the real pairing + cookie path.
const testUserHeader = "X-Test-Session-User"

const providerTestUser = "test-session-user"

func init() {
	auth.Register(providerTestUser, func(o auth.Options) (auth.Authenticator, error) {
		return testUserAuth{allowed: o.AllowedUsers}, nil
	})
}

type testUserAuth struct{ allowed []string }

func (a testUserAuth) Authenticate(r *http.Request) (auth.Identity, error) {
	u := strings.TrimSpace(r.Header.Get(testUserHeader))
	if u == "" {
		return auth.Identity{}, auth.ErrNoCredentials
	}
	if !auth.AllowedLogin(a.allowed, u) {
		return auth.Identity{}, auth.ErrForbiddenIdentity
	}
	return auth.Identity{User: u, Provider: auth.ProviderSession}, nil
}

// testDefault is testDefault() plus the test provider and the Host that
// httptest.NewRequest uses.
func testDefault() config.Config {
	c := config.Default()
	c.Auth.Providers = []string{providerTestUser, auth.ProviderSession, auth.ProviderAPIKey}
	c.AllowedHosts = []string{"example.com"}
	return c
}
