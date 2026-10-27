package auth

import (
	"context"
	"net/http"
	"strings"
)

// HeaderTailscaleLogin is set by `tailscale serve`, but any local process can
// forge it on the loopback port. Since 4.0.0 it is NEVER used for identity or
// authorization; its value is kept only as the log field ts_login_hint.
const HeaderTailscaleLogin = "Tailscale-User-Login"

type tsHintKey struct{}

// StripTailscale removes every Tailscale-User-* header and returns a context
// carrying the login value as a log-only hint.
func StripTailscale(r *http.Request) context.Context {
	hint := strings.TrimSpace(r.Header.Get(HeaderTailscaleLogin))
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "tailscale-user-") {
			delete(r.Header, k)
		}
	}
	if len(hint) > 128 {
		hint = hint[:128]
	}
	return context.WithValue(r.Context(), tsHintKey{}, hint)
}

// TailscaleHint returns the log-only login hint ("" if none).
func TailscaleHint(ctx context.Context) string {
	v, _ := ctx.Value(tsHintKey{}).(string)
	return v
}
