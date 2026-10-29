package selfdesc

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// sensitivePatterns describe material that must never appear in the text the
// gateway puts at the start of every interactive system prompt: credentials,
// connection strings, hostnames, IP addresses and file paths. The block is
// built from runtime state only, so this guards against a later edit (or a new
// Feature line) pasting such a value into the template.
var sensitivePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"api key or token prefix", regexp.MustCompile(`(?i)\b(sk-[a-z0-9_-]{8,}|ghp_[a-z0-9]{8,}|github_pat_[a-z0-9_]{8,}|gh[ousr]_[a-z0-9]{8,}|xox[abprs]-[a-z0-9-]{8,}|AKIA[0-9A-Z]{12,}|eyJ[a-z0-9_-]{8,}\.[a-z0-9_-]{8,})`)},
	{"bearer or authorization header", regexp.MustCompile(`(?i)\b(bearer\s+\S{6,}|authorization\s*:)`)},
	{"long hex or base64 secret", regexp.MustCompile(`[A-Fa-f0-9]{32,}|[A-Za-z0-9+/_-]{40,}={0,2}`)},
	{"secret assignment", regexp.MustCompile(`(?i)\b(api[_-]?key|secret|token|passwd|password|private[_ -]?key|credential)s?\s*[=:]\s*\S+`)},
	{"database DSN or URL with credentials", regexp.MustCompile(`(?i)\b(postgres(ql)?|mysql|mongodb|redis|amqp|sqlite)(\+\w+)?://|\w+://[^\s/@]+:[^\s/@]*@|\bsslmode=|\bdbname=|\bhost=\S|\bpassword=|\buser=\S`)},
	{"IPv4 address", regexp.MustCompile(`\b(\d{1,3}\.){3}\d{1,3}\b`)},
	{"IPv6 address", regexp.MustCompile(`(?i)\b([0-9a-f]{1,4}:){2,7}[0-9a-f]{0,4}\b|::1\b`)},
	{"URL", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://`)},
	{"hostname or domain", regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9-]*(\.[a-z0-9-]+)*\.(local|lan|internal|home|ts\.net|com|net|org|io|dev|app|ai|kr|co|me|cloud)\b|\blocalhost\b|\.ts\.net\b|\.local\b`)},
	{"host:port", regexp.MustCompile(`(?i)\b[a-z0-9.-]+:\d{2,5}\b`)},
	{"unix file path", regexp.MustCompile(`(^|[\s"'(=,;])(~|\.{1,2})?/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]*)*`)},
	{"home or system directory", regexp.MustCompile(`(?i)(~/|/users/|/home/|/opt/|/etc/|/usr/|/var/|/tmp/|/library/|/private/|/volumes/)`)},
	{"windows path", regexp.MustCompile(`(?i)\b[a-z]:\\`)},
	{"config or key file name", regexp.MustCompile(`(?i)\.(env|pem|key|crt|p12|plist|sock|db|sqlite|yaml|yml|toml|ini|conf|log)\b|\.db_env\b|\bid_(rsa|ed25519)\b`)},
}

// stateMatrix covers every combination of memory and web state, with and
// without memory_propose and with an extra enabled feature.
func stateMatrix() []State {
	var out []State
	for _, m := range []MemoryState{MemoryUnavailable, MemoryOff, MemoryPending, MemoryOn} {
		for _, w := range []WebState{WebUnavailable, WebOff, WebOn, WebBlocked} {
			for _, p := range []bool{false, true} {
				out = append(out, State{Memory: m, Web: w, Propose: p})
				out = append(out, State{Memory: m, Web: w, Propose: p, Features: []Feature{{Name: "캘린더", Line: "읽기 전용"}}})
			}
		}
	}
	return out
}

// TestBuild_NoSensitiveInfo asserts the self-description preamble and every
// generated variant contain no credentials, DSNs, hostnames, IPs or file paths.
func TestBuild_NoSensitiveInfo(t *testing.T) {
	texts := map[string]string{"header": header}
	for i, st := range stateMatrix() {
		texts[fmt.Sprintf("state-%d", i)] = Build(st)
	}
	for name, got := range texts {
		for _, p := range sensitivePatterns {
			if loc := p.re.FindStringIndex(got); loc != nil {
				t.Errorf("%s: %s found (%q) in\n%s", name, p.name, got[loc[0]:loc[1]], got)
			}
		}
	}
}

// TestBuild_NoRuntimeEnvironmentValues asserts that values of this process's
// environment (host name, home directory, credentials in env vars) are not in
// the text, whatever the state.
func TestBuild_NoRuntimeEnvironmentValues(t *testing.T) {
	var secrets []string
	if h, err := os.Hostname(); err == nil && len(h) >= 3 {
		secrets = append(secrets, h)
	}
	if h, err := os.UserHomeDir(); err == nil && len(h) >= 3 {
		secrets = append(secrets, h)
	}
	if wd, err := os.Getwd(); err == nil && len(wd) >= 3 {
		secrets = append(secrets, wd)
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		uk := strings.ToUpper(k)
		if len(v) >= 8 && (strings.Contains(uk, "KEY") || strings.Contains(uk, "TOKEN") || strings.Contains(uk, "SECRET") ||
			strings.Contains(uk, "PASSWORD") || strings.Contains(uk, "DATABASE_URL") || strings.Contains(uk, "DSN")) {
			secrets = append(secrets, v)
		}
	}
	for _, st := range stateMatrix() {
		got := Build(st)
		for _, s := range secrets {
			if strings.Contains(got, s) {
				t.Fatalf("runtime value found in the reference block: %.3s...", s)
			}
		}
	}
}

// TestSensitivePatterns_Detect makes sure the patterns above actually catch
// what they claim to, so the guard cannot silently go blind.
func TestSensitivePatterns_Detect(t *testing.T) {
	bad := map[string]string{
		"api key or token prefix":              "key sk-abcdefghijklmnop",
		"bearer or authorization header":       "Authorization: Bearer abcdef123456",
		"secret assignment":                    "password=hunter22",
		"database DSN or URL with credentials": "postgres://u:p@db:5432/x",
		"IPv4 address":                         "server 10.0.0.12",
		"IPv6 address":                         "fd7a:115c:a1e0::1",
		"hostname or domain":                   "mac-studio.local",
		"unix file path":                       "see /opt/eve-gw/config",
		"home or system directory":             "in ~/secrets",
		"config or key file name":              "edit .db_env",
	}
	for name, s := range bad {
		var hit bool
		for _, p := range sensitivePatterns {
			if p.name == name && p.re.MatchString(s) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("pattern %q did not match %q", name, s)
		}
	}
}
