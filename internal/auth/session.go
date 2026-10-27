package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

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
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
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
	if _, ok := s.allowed[strings.ToLower(sess.Login)]; !ok {
		return Identity{}, ErrForbiddenIdentity
	}
	return Identity{User: sess.Login, Provider: ProviderSession, SessionID: sess.ID}, nil
}

// AllowedLogin reports whether login is in the allowlist (case-insensitive).
func AllowedLogin(allowed []string, login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	for _, a := range allowed {
		if login != "" && strings.ToLower(strings.TrimSpace(a)) == login {
			return true
		}
	}
	return false
}
