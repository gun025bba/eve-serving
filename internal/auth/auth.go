// Package auth implements pluggable request authentication.
//
// An Authenticator inspects a request and either returns an Identity, reports
// that it does not apply (no credentials of its kind), or rejects the request.
// A Chain tries authenticators in order; the first success wins. New schemes
// (session cookie, OIDC, ...) only need to implement Authenticator.
package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

// Method names used in logs and the auth_method metric label.
const (
	MethodAPIKey    = "api_key"
	MethodTailscale = "tailscale"
	MethodNone      = "none"
)

// Identity describes an authenticated caller. Login is empty for API-key callers.
// It never contains secrets.
type Identity struct {
	Method string `json:"method"`
	Login  string `json:"login,omitempty"`
}

var (
	// ErrNoCredentials: the authenticator found nothing it handles; try the next one.
	ErrNoCredentials = errors.New("no credentials")
	// ErrInvalidKey: a Bearer token was presented but is wrong.
	ErrInvalidKey = errors.New("invalid api key")
	// ErrForbiddenIdentity: an identity was presented but is not allowed.
	ErrForbiddenIdentity = errors.New("identity not allowed")
)

// Authenticator authenticates a request.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

// Chain tries each authenticator in order. The first success is returned.
// If none succeed, the most specific error is returned (invalid key /
// forbidden identity beat ErrNoCredentials).
type Chain []Authenticator

func (c Chain) Authenticate(r *http.Request) (Identity, error) {
	var last error = ErrNoCredentials
	for _, a := range c {
		id, err := a.Authenticate(r)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, ErrNoCredentials) {
			last = err
		}
	}
	return Identity{Method: MethodNone}, last
}

// LoadKey reads and trims the key file. The key is never logged.
func LoadKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", fmt.Errorf("api key file %s is empty", path)
	}
	return k, nil
}

// Check reports whether r carries "Authorization: Bearer <key>".
func Check(r *http.Request, key string) bool {
	_, err := APIKey{Key: key}.Authenticate(r)
	return err == nil
}

// APIKey authenticates "Authorization: Bearer <key>" (scripts, server-to-server).
type APIKey struct{ Key string }

func (a APIKey) Authenticate(r *http.Request) (Identity, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return Identity{}, ErrNoCredentials
	}
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return Identity{}, ErrInvalidKey
	}
	tok = strings.TrimSpace(tok)
	if a.Key == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(a.Key)) != 1 {
		return Identity{}, ErrInvalidKey
	}
	return Identity{Method: MethodAPIKey}, nil
}

// HeaderTailscaleLogin is set by `tailscale serve` on proxied requests.
const HeaderTailscaleLogin = "Tailscale-User-Login"

// Tailscale trusts the Tailscale-User-Login header injected by `tailscale serve`.
// The header is honoured only when the TCP peer is loopback (serve proxies from
// the local tailscaled), and only for logins in Allowed (case-insensitive).
// An empty allowlist disables this authenticator.
type Tailscale struct{ allowed map[string]struct{} }

// NewTailscale builds the authenticator from a list of logins.
func NewTailscale(logins []string) *Tailscale {
	t := &Tailscale{allowed: map[string]struct{}{}}
	for _, l := range logins {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			t.allowed[l] = struct{}{}
		}
	}
	return t
}

// Enabled reports whether any login is allowed.
func (t *Tailscale) Enabled() bool { return t != nil && len(t.allowed) > 0 }

func (t *Tailscale) Authenticate(r *http.Request) (Identity, error) {
	login := strings.TrimSpace(r.Header.Get(HeaderTailscaleLogin))
	if login == "" || !IsLoopback(r.RemoteAddr) || !t.Enabled() {
		return Identity{}, ErrNoCredentials
	}
	if _, ok := t.allowed[strings.ToLower(login)]; !ok {
		return Identity{}, ErrForbiddenIdentity
	}
	return Identity{Method: MethodTailscale, Login: login}, nil
}

// IsLoopback reports whether a RemoteAddr ("ip:port") is a loopback address.
func IsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// StripUntrusted removes identity headers that did not come from loopback, so
// handlers further down never see a spoofed value.
func StripUntrusted(r *http.Request) {
	if !IsLoopback(r.RemoteAddr) {
		r.Header.Del(HeaderTailscaleLogin)
		r.Header.Del("Tailscale-User-Name")
		r.Header.Del("Tailscale-User-Profile-Pic")
	}
}
