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
		if l, ok := NormalizeLogin(l); ok {
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
	login, ok := NormalizeLogin(sess.Login)
	if !ok {
		return Identity{}, ErrForbiddenIdentity
	}
	if _, ok := s.allowed[login]; !ok {
		return Identity{}, ErrForbiddenIdentity
	}
	return Identity{User: sess.Login, Provider: ProviderSession, SessionID: sess.ID, Scopes: []string{ScopeInteractive, ScopeAdmin}}, nil
}

// maxLoginLen bounds a login (an email-style identifier fits well within it).
const maxLoginLen = 254

// NormalizeLogin is the single decision function for logins: allowlist
// entries, paired users and session logins all go through it. It trims
// surrounding space and lower-cases ASCII letters, and accepts only a
// non-empty string of ASCII [A-Za-z0-9._@+-] (at most 254 bytes). Anything
// else is refused, so a joined list ("a@x, b@y"), whitespace, control,
// zero-width or bidi characters, and look-alikes that Unicode case folding
// would map onto ASCII (Kelvin sign U+212A -> "k", U+0130 -> "i") are never a
// login: they can neither be allowlisted nor match an allowlist entry.
// Compare normalized values; the returned string is the canonical form.
func NormalizeLogin(login string) (string, bool) {
	login = strings.TrimSpace(login)
	if login == "" || len(login) > maxLoginLen {
		return "", false
	}
	b := []byte(login)
	for i, c := range b {
		switch {
		case c >= 'A' && c <= 'Z':
			b[i] = c + ('a' - 'A')
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '@', c == '+', c == '-':
		default:
			return "", false
		}
	}
	return string(b), true
}

// ValidLogin reports whether login is acceptable to NormalizeLogin.
func ValidLogin(login string) bool {
	_, ok := NormalizeLogin(login)
	return ok
}

// AllowedLogin reports whether login is in the allowlist (case-insensitive,
// ASCII only). An empty allowlist, or a malformed login or entry, never
// matches (fail closed). It does not consult API keys: those authenticate
// independently of the allowlist.
func AllowedLogin(allowed []string, login string) bool {
	want, ok := NormalizeLogin(login)
	if !ok {
		return false
	}
	for _, a := range allowed {
		if got, ok := NormalizeLogin(a); ok && got == want {
			return true
		}
	}
	return false
}
