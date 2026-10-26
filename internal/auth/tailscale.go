package auth

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// ProviderTailscale trusts the Tailscale-User-Login header injected by
// `tailscale serve`. Since 4.0.0 the header is honoured ONLY on requests that
// arrived on the gateway's unix socket (GW_UNIX_SOCKET, 0700 dir / 0660
// socket), which only `tailscale serve` is configured to reach. A loopback TCP
// peer proves nothing (any local process can connect), so on TCP the identity
// headers are stripped and only API-key auth works. Logins must also be
// allowlisted (case-insensitive); an empty allowlist disables the provider.
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
	login := strings.TrimSpace(r.Header.Get(HeaderTailscaleLogin))
	if login == "" || !ViaTrustedSocket(r.Context()) || !t.Enabled() {
		return Identity{}, ErrNoCredentials
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

// StripUntrusted removes every Tailscale-User-* identity header unless the
// request arrived on the trusted unix socket.
func StripUntrusted(r *http.Request) {
	if ViaTrustedSocket(r.Context()) {
		return
	}
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "tailscale-user-") {
			delete(r.Header, k)
		}
	}
}

type trustedSocketKey struct{}

// WithTrustedSocket marks a connection context as accepted on the gateway's
// unix socket (set by http.Server.ConnContext of the unix listener only).
func WithTrustedSocket(ctx context.Context) context.Context {
	return context.WithValue(ctx, trustedSocketKey{}, true)
}

// ViaTrustedSocket reports whether the request came in on the unix socket.
func ViaTrustedSocket(ctx context.Context) bool {
	v, _ := ctx.Value(trustedSocketKey{}).(bool)
	return v
}
