package auth

import (
	"crypto/sha256"
	"encoding/hex"
)

// ProviderTestIdentity is the e2e-only identity injection provider. It is
// registered only in binaries built with -tags e2e (injection_e2e.go).
const ProviderTestIdentity = "test-identity"

// InjectionCheck carries what the e2e startup gate inspects.
type InjectionCheck struct {
	Env       string   // GW_ENV
	Host      string   // TCP bind host
	DBURL     string   // database URL ("" = no database)
	ProdUsers []string // the prod allowlist (allowed_users)
	Providers []string // configured auth providers
	Users     []string // GW_TEST_IDENTITY_USERS
}

// LogUser returns the user id to write to logs: injected test identities are
// pseudonymized; everything else is logged as before.
func LogUser(id Identity) string {
	if id.Provider == ProviderTestIdentity {
		h := sha256.Sum256([]byte("eve-test-identity:" + id.User))
		return "t-" + hex.EncodeToString(h[:6])
	}
	return id.User
}
