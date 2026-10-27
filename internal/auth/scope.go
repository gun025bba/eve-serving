package auth

// Scopes are granted to a principal by the provider that authenticated it.
// Features check scopes only; which credential produced them (session
// cookie, Bearer key, ...) is known only inside this package.
const (
	// ScopeInteractive: a person at a paired, interactive client (browser or
	// app session). Bearer API keys never get it.
	ScopeInteractive = "interactive"
	// ScopeAdmin: operator-level reads such as GET /v1/feedback/export.
	// Granted to allowlisted paired sessions and to the API key.
	ScopeAdmin = "admin"
)

// HasScope reports whether the authentication layer granted scope s.
func (id Identity) HasScope(s string) bool {
	for _, x := range id.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

// NeedsCSRF reports whether a state-changing request from this principal
// must pass the CSRF check. Only the Bearer API key is exempt (a header
// credential the browser never attaches on its own); every other provider,
// including unknown ones, is checked (fail-closed).
func (id Identity) NeedsCSRF() bool {
	return id.Provider != ProviderAPIKey
}
