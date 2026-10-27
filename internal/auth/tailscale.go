package auth

import (
	"net"
	"net/http"
	"strings"
)

// ProviderTailscale trusts the Tailscale-User-Login header injected by
// `tailscale serve`. The header is honoured only when the TCP peer is loopback
// (serve proxies from the local tailscaled) and only for allowlisted logins
// (case-insensitive). An empty allowlist disables the provider.
const ProviderTailscale = "tailscale-identity"

// HeaderTailscaleLogin is set by `tailscale serve` on proxied requests.
const HeaderTailscaleLogin = "Tailscale-User-Login"

func init() {
	Register(ProviderTailscale, func(o Options) (Authenticator, error) { return NewTailscale(o.AllowedUsers), nil })
}

type Tailscale struct{ allowed map[string]struct{} }

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
	vals, present := r.Header[HeaderTailscaleLogin]
	if !present || !IsLoopback(r.RemoteAddr) || !t.Enabled() {
		return Identity{}, ErrNoCredentials
	}
	// Present but empty/whitespace, or duplicated: reject instead of falling through.
	if len(vals) != 1 {
		return Identity{}, ErrForbiddenIdentity
	}
	login := strings.TrimSpace(vals[0])
	if login == "" {
		return Identity{}, ErrForbiddenIdentity
	}
	if _, ok := t.allowed[strings.ToLower(login)]; !ok {
		return Identity{}, ErrForbiddenIdentity
	}
	return Identity{User: login, Provider: ProviderTailscale}, nil
}

// IsLoopback reports whether a RemoteAddr ("ip:port") is loopback.
func IsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// StripUntrusted removes Tailscale identity headers from non-loopback requests.
func StripUntrusted(r *http.Request) {
	if !IsLoopback(r.RemoteAddr) {
		for _, h := range []string{HeaderTailscaleLogin, "Tailscale-User-Name", "Tailscale-User-Profile-Pic"} {
			r.Header.Del(h)
		}
	}
}
