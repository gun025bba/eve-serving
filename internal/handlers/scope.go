package handlers

import (
	"net/http"

	"github.com/gun025bba/eve-serving/internal/auth"
)

// hasScope reports whether the request's principal holds scope. Features
// look only at the principal (user + scopes) set by the auth middleware,
// never at how the request was authenticated.
func hasScope(r *http.Request, scope string) bool {
	id, ok := auth.FromContext(r.Context())
	return ok && id.HasScope(scope)
}

// requireScope writes 403 scope_denied and returns false when the
// principal lacks scope.
func (s *Server) requireScope(w http.ResponseWriter, r *http.Request, scope string) bool {
	if hasScope(r, scope) {
		return true
	}
	s.writeCode(w, r, "scope_denied")
	return false
}
