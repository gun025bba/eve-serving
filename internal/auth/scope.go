package auth

// ScopeInteractive is granted to a principal that is a person at a paired,
// interactive client (browser or app session). Bearer API keys never get it.
// Features check scopes only; which credential produced them (session
// cookie, Bearer key, ...) is known only inside this package.
const ScopeInteractive = "interactive"

// HasScope reports whether the authentication layer granted scope s.
func (id Identity) HasScope(s string) bool {
	for _, x := range id.Scopes {
		if x == s {
			return true
		}
	}
	return false
}
