// Package tools implements the web_search / web_fetch tool layer used by the
// chat handler: tool definitions, per-request session state (URL allowlist,
// citations, call caps), privacy guards, untrusted-content wrapping, the
// lenient <tool_call> parser and per-user rate limits.
package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Mode string

const (
	ModeOff   Mode = "off"
	ModeAuto  Mode = "auto"
	ModeForce Mode = "force"
)

// ParseMode accepts false|true|"off"|"auto"|"force" (true = auto).
func ParseMode(v any) (Mode, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return ModeAuto, true
		}
		return ModeOff, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "off", "false", "":
			return ModeOff, true
		case "auto", "true", "on":
			return ModeAuto, true
		case "force", "always":
			return ModeForce, true
		}
	}
	return "", false
}

// MaxQueryChars caps web_search queries (review 7.2-1 / §2.8-10).
const MaxQueryChars = 256

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsk-[a-z0-9_\-]{16,}`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{30,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-]{12,}`),
	regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|api[_-]?key|token)\s*[:=]\s*\S{4,}`),
	regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
	regexp.MustCompile(`(?i)\b[a-z0-9\-]+\.[a-z0-9\-]+\.ts\.net\b|\.ts\.net\b`),
	regexp.MustCompile(`(?i)\b[a-z0-9\-]+\.(?:internal|local|lan|home\.arpa)\b`),
	regexp.MustCompile(`\b\d{6}-[1-4]\d{6}\b`),        // KR resident registration number
	regexp.MustCompile(`\b(?:\d[ \-]?){15}\d\b`),      // card-like 16 digits
	regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`),        // long hex tokens
	regexp.MustCompile(`[A-Za-z0-9+/_\-]{40,}={0,2}`), // long base64-ish tokens
	regexp.MustCompile(`\b(?:10|127|192\.168|172\.(?:1[6-9]|2\d|3[01]))\.\d{1,3}\.\d{1,3}(?:\.\d{1,3})?\b|\b100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3}\b`), // private / tailnet IPs
}

// CheckQuery returns "" if q may be sent to the search provider, otherwise a
// short reason ("empty", "too_long", "secret_pattern").
func CheckQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return "empty"
	}
	if len([]rune(q)) > MaxQueryChars {
		return "too_long"
	}
	for _, p := range secretPatterns {
		if p.MatchString(q) {
			return "secret_pattern"
		}
	}
	return ""
}

// Hash returns a short sha256 prefix for logs (never log raw queries/URLs).
func Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// CanonURL normalizes a URL for allowlist comparison ("" if not http(s)).
func CanonURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	p := u.Port()
	if (u.Scheme == "http" && p == "80") || (u.Scheme == "https" && p == "443") {
		p = ""
	}
	if p != "" {
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		h += ":" + p
	} else if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	u.Host = h
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `\]\[(){}]+`)

// ExtractURLs finds http(s) URLs in user-typed text.
func ExtractURLs(s string) []string {
	var out []string
	for _, m := range urlRe.FindAllString(s, 50) {
		m = strings.TrimRight(m, ".,;:!?。、")
		if c := CanonURL(m); c != "" {
			out = append(out, m)
		}
	}
	return out
}

// Domain returns the lowercase host of u.
func Domain(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// RateLimiter is a per-user sliding-window counter per tool kind.
type RateLimiter struct {
	mu     sync.Mutex
	limits map[string]int
	hits   map[string][]time.Time
	now    func() time.Time
}

func NewRateLimiter(perMin map[string]int) *RateLimiter {
	return &RateLimiter{limits: perMin, hits: map[string][]time.Time{}, now: time.Now}
}

func (r *RateLimiter) Allow(user, kind string) bool {
	if r == nil {
		return true
	}
	lim := r.limits[kind]
	if lim <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := user + "\x00" + kind
	now := r.now()
	h := r.hits[k][:0]
	for _, t := range r.hits[k] {
		if now.Sub(t) < time.Minute {
			h = append(h, t)
		}
	}
	if len(h) >= lim {
		r.hits[k] = h
		return false
	}
	r.hits[k] = append(h, now)
	return true
}
