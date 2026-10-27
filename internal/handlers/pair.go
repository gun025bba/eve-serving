package handlers

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/session"
)

// builtinHosts are always accepted Host values (port ignored).
var builtinHosts = []string{"localhost", "127.0.0.1", "::1"}

// withHost rejects any Host header outside the allowlist (DNS rebinding /
// misdirected requests): 400 when missing, 421 when not allowed.
func (s *Server) withHost(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, h := range append(append([]string{}, builtinHosts...), s.Cfg.AllowedHosts...) {
		if h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), ".")); h != "" {
			allowed[h] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := strings.ToLower(r.Host)
		if host, _, err := net.SplitHostPort(h); err == nil {
			h = host
		}
		h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
		switch {
		case h == "":
			s.M.Errs.WithLabelValues("host_missing").Inc()
			writeJSON(w, 400, apierr.Body("host_missing", "Host header required", "invalid_request_error", "-"))
		case !allowed[h]:
			s.M.Errs.WithLabelValues("host_not_allowed").Inc()
			s.Log.Warn("host_rejected", "path", r.URL.Path)
			writeJSON(w, http.StatusMisdirectedRequest, apierr.Body("host_not_allowed", "Host not allowed", "invalid_request_error", "-"))
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// sessionOnly: browser-session routes allow only a paired session identity
// (Provider == session); API key and any other provider get 403.
func sessionOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id, ok := auth.FromContext(r.Context()); !ok || id.Provider != auth.ProviderSession {
			writeJSON(w, 403, apierr.Body("scope_denied", apierr.ScopeDenied().Message, "permission_error", rid(r)))
			return
		}
		h(w, r)
	}
}

// pair redeems a one-time pairing code (POST /v1/pair, unauthenticated).
// The response only says success (204 + cookie) or failure (401 pair_failed).
// The browser CSRF checks still apply so another site cannot log the browser
// into a session.
func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	if reason := s.csrfCheck(r, auth.Identity{Provider: "pair"}); reason != "" {
		s.csrfReject(w, r, auth.Identity{Provider: "pair"}, reason)
		return
	}
	pairFailed := func() {
		s.M.Auth.WithLabelValues(auth.ProviderSession, "pair_failed").Inc()
		writeJSON(w, 401, apierr.Body("pair_failed", "Pairing failed", "authentication_error", rid(r)))
	}
	if s.Sessions == nil {
		pairFailed()
		return
	}
	var b struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil || len(b.Code) > 32 {
		pairFailed()
		return
	}
	tok, sess, err := s.Sessions.Pair(r.Context(), b.Code)
	if errors.Is(err, session.ErrPairFailed) {
		s.Log.Warn("pair_failed", "request_id", rid(r), "locked", s.Sessions.Locked(), "ts_login_hint", auth.TailscaleHint(r.Context()))
		pairFailed()
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: session.CookieName, Value: tok, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(session.AbsoluteTTL.Seconds())})
	s.M.Auth.WithLabelValues(auth.ProviderSession, "paired").Inc()
	s.Log.Info("paired", "request_id", rid(r), "session_id", sess.ID, "ts_login_hint", auth.TailscaleHint(r.Context()))
	w.WriteHeader(204)
}

// logout revokes the caller's own session and clears the cookie.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(session.CookieName); err == nil && s.Sessions != nil {
		if err := s.Sessions.Revoke(r.Context(), c.Value); err != nil && !errors.Is(err, session.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: session.CookieName, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}
