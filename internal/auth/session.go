package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/gun025bba/eve-serving/internal/session"
)

// ProviderSession authenticates the paired browser session cookie (4.0.0).
// The session is bound to users.id; Identity.User is that row's login. The
// login must still be in allowed_users (removing it locks the browser out).
const ProviderSession = "session"

// SessionValidator is implemented by *session.Manager.
type SessionValidator interface {
	Validate(ctx context.Context, token string) (session.Session, error)
}

func init() {
	Register(ProviderSession, func(o Options) (Authenticator, error) { return NewSession(o.Sessions, o.AllowedUsers), nil })
}

type Session struct {
	v       SessionValidator
	allowed map[string]struct{}
}

// NewSession: a nil validator (no database) never authenticates anyone.
func NewSession(v SessionValidator, allowed []string) *Session {
	s := &Session{v: v, allowed: map[string]struct{}{}}
	for _, l := range allowed {
		if l = strings.ToLower(strings.TrimSpace(l)); ValidLogin(l) {
			s.allowed[l] = struct{}{}
		}
	}
	return s
}

func (s *Session) Authenticate(r *http.Request) (Identity, error) {
	c, err := r.Cookie(session.CookieName)
	if err != nil || c.Value == "" || s.v == nil {
		return Identity{}, ErrNoCredentials
	}
	sess, err := s.v.Validate(r.Context(), c.Value)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return Identity{}, ErrInvalidSession
		}
		return Identity{}, err
	}
	if _, ok := s.allowed[strings.ToLower(sess.Login)]; !ok || !ValidLogin(sess.Login) {
		return Identity{}, ErrForbiddenIdentity
	}
	return Identity{User: sess.Login, Provider: ProviderSession, SessionID: sess.ID, Scopes: []string{ScopeInteractive, ScopeAdmin}}, nil
}

// ValidLogin reports whether login is one well-formed login: non-empty, with
// no comma, whitespace or control characters. A joined list such as
// "a@x, b@y" (a mistyped GW_ALLOWED_USERS or --user) is never a login, so it
// can neither be allowlisted nor match an allowlist entry.
func ValidLogin(login string) bool {
	if login == "" || strings.Contains(login, ",") {
		return false
	}
	for _, r := range login {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// AllowedLogin reports whether login is in the allowlist (case-insensitive).
// An empty allowlist, or a malformed login, is always false (fail closed).
func AllowedLogin(allowed []string, login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if !ValidLogin(login) {
		return false
	}
	for _, a := range allowed {
		if strings.ToLower(strings.TrimSpace(a)) == login {
			return true
		}
	}
	return false
}
