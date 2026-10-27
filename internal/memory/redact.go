package memory

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gun025bba/eve-serving/internal/tools"
)

const (
	redactedSecret = "[REDACTED]"
	redactedMemory = "[REDACTED:memory]"
	// MaxLogValueBytes caps every redacted log value (Low: length cap).
	MaxLogValueBytes = 4096
	// minRegisteredRunes: shorter item texts are not masked (would mangle logs).
	minRegisteredRunes = 2
)

// entryLine matches the injected per-item line form ({"kind":..,"text":..})
// even when it appears without the surrounding block.
var entryLine = regexp.MustCompile(`\{\s*"kind"\s*:\s*"[a-z_]+"\s*,\s*"text"\s*:`)

// unescapeForMatch undoes the JSON/HTML escapes that could hide markers
// (\u003c, \u003e, \u0026, \", \\) so matching sees the plain form.
var unescaper = strings.NewReplacer(`%3C`, "<", `%3c`, "<", `%3E`, ">", `%3e`, ">", `\x3c`, "<", `\x3C`, "<", `\x3e`, ">", `\x3E`, ">",
	`&#60;`, "<", `&#x3c;`, "<", `&#x3C;`, "<", `\u003c`, "<", `\u003C`, "<", `\u003e`, ">", `\u003E`, ">", `\u0026`, "&",
	`\\u003c`, "<", `\\u003e`, ">", `&lt;`, "<", `&gt;`, ">", `\"`, `"`, `\\"`, `"`)

var numRefRe = regexp.MustCompile(`(?i)&#(x0*3c|0*60|x0*3e|0*62);?|%25(?:25)*(3c|3e)`)

// unescapeAll repeats the unescape until stable (max 4 rounds), and folds
// zero-padded/upper-case numeric references and nested %25 encodings.
func unescapeAll(s string) string {
	for i := 0; i < 4; i++ {
		n := numRefRe.ReplaceAllStringFunc(s, func(m string) string {
			l := strings.ToLower(m)
			if strings.HasSuffix(l, "3c") || strings.HasSuffix(l, "3c;") || strings.Contains(l, "60") {
				return "<"
			}
			return ">"
		})
		n = unescaper.Replace(n)
		if n == s {
			return n
		}
		s = n
	}
	return s
}

// RedactLog is the log Redactor (memory.md §9.1). It is installed always;
// memory-specific masks only ever match while memory produced data.
//   - a value carrying the <user_memory> block, its preamble, or the per-item
//     line form (also when JSON-escaped) is replaced whole;
//   - registered memory item texts are masked as [REDACTED:memory];
//   - registered secret values and SecretPatterns are masked as [REDACTED];
//   - the result is capped at MaxLogValueBytes.
func RedactLog(s string) string {
	if s == "" {
		return s
	}
	u := unescapeAll(s)
	if strings.Contains(u, "<user_memory") || strings.Contains(u, "</user_memory") || strings.Contains(u, Preamble) || entryLine.MatchString(u) {
		return redactedMemory
	}
	m, over := machine()
	if over && freeText(s) {
		return redactedMemory // fail closed: registry over cap
	}
	if m != nil {
		if _, hit := maskRegistered(m, u); hit && u != s {
			return redactedMemory // escaped form of an item text
		}
		s, _ = maskRegistered(m, s)
	}
	s = tools.Default().Redact.Redact(s)
	for _, p := range tools.SecretPatterns {
		s = p.Re.ReplaceAllString(s, redactedSecret)
	}
	if len(s) > MaxLogValueBytes {
		cut := MaxLogValueBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…[truncated]"
	}
	return s
}

// freeText: values that could carry user text (anything beyond a short
// identifier/number token) are masked in fail-closed mode.
func freeText(s string) bool {
	if len(s) > 64 {
		return true
	}
	for _, r := range s {
		if r > 0x7f || r == ' ' {
			return true
		}
	}
	return false
}
