//go:build !e2e

package auth

import (
	"strings"
	"testing"
)

func TestInjectionNotCompiledIn(t *testing.T) {
	err := CheckInjection(InjectionCheck{Env: "e2e", Host: "127.0.0.1", TCPEnabled: true, Users: []string{"e2e@example.test"}})
	if err == nil || !strings.Contains(err.Error(), "-tags e2e") {
		t.Fatal(err)
	}
	if _, err := Build([]string{ProviderTestIdentity}, Options{APIKey: "k", TestUsers: []string{"x"}}); err == nil {
		t.Fatal("test-identity provider registered in default build")
	}
}

func TestLogUserPseudonymizesTestIdentity(t *testing.T) {
	if got := LogUser(Identity{User: "a@x", Provider: ProviderTailscale}); got != "a@x" {
		t.Fatal(got)
	}
	got := LogUser(Identity{User: "e2e-alice@example.test", Provider: ProviderTestIdentity})
	if !strings.HasPrefix(got, "t-") || strings.Contains(got, "alice") || got != LogUser(Identity{User: "e2e-alice@example.test", Provider: ProviderTestIdentity}) {
		t.Fatal(got)
	}
}
