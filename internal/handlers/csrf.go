package handlers

import (
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
)

// CSRF protection for browser writes (docs/security.md).
//
// State-changing methods (POST, PUT, PATCH, DELETE) from any identity other
// than an API key (Bearer) must carry:
//   - X-Eve-Request: 1
//   - Origin exactly equal to an entry of GW_ALLOWED_ORIGINS (missing/"null" rejected)
//   - Sec-Fetch-Site: same-origin, if the header is present
//   - Content-Type: application/json when the request has a body
//
// Failures return 403 {"error_code":"invalid"}; the reason is logged and
// counted in eve_csrf_rejected_total{reason}. An empty allowlist fails closed.

const (
	HeaderEveRequest = "X-Eve-Request"
	csrfInvalidBody  = `{"error_code":"invalid"}`
)

func isStateChanging(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func hasBody(r *http.Request) bool {
	return r.ContentLength > 0 || (r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody) || len(r.TransferEncoding) > 0
}

// csrfCheck returns "" when the request may proceed, else the rejection reason.
func (s *Server) csrfCheck(r *http.Request, id auth.Identity) string {
	if !isStateChanging(r.Method) || id.Provider == auth.ProviderAPIKey {
		return ""
	}
	if len(s.Cfg.AllowedOrigins) == 0 {
		return "no_allowlist"
	}
	if r.Header.Get(HeaderEveRequest) != "1" {
		return "missing_header"
	}
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return "missing_origin"
	}
	ok := false
	for _, a := range s.Cfg.AllowedOrigins {
		if o == a {
			ok = true
			break
		}
	}
	if !ok {
		return "origin_mismatch"
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" {
		return "fetch_site"
	}
	if hasBody(r) {
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/json" {
			return "content_type"
		}
	}
	return ""
}

func (s *Server) csrfReject(w http.ResponseWriter, r *http.Request, id auth.Identity, reason string) {
	s.M.CSRF.WithLabelValues(reason).Inc()
	s.Log.Warn("csrf_rejected", "request_id", rid(r), "method", r.Method, "path", r.URL.Path,
		"reason", reason, "auth_provider", id.Provider)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(csrfInvalidBody))
}

// csrfStartup warns when the UI is served but no Origin allowlist is set.
func (s *Server) csrfStartup() {
	if len(s.Cfg.AllowedOrigins) > 0 {
		return
	}
	if dir := config.Expand(s.Cfg.StaticDir); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			s.Log.Warn("csrf_allowlist_empty", "detail", "GW_ALLOWED_ORIGINS is empty while the UI is enabled; all browser writes will be rejected")
		}
	}
}

// corsDeniedSegments: any path containing one of these segments never gets
// CORS allow headers, including routes that don't exist yet.
var corsDeniedSegments = map[string]bool{
	"actions": true, "revise": true, "reissue": true, "token": true, "tokens": true,
	"confirm": true, "confirmations": true, "memory": true,
}

func corsDenied(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if corsDeniedSegments[strings.ToLower(seg)] {
			return true
		}
	}
	return false
}
