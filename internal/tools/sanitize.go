package tools

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// SanitizeUntrusted normalizes text that comes from outside the gateway
// (search snippets, fetched pages) before it is wrapped for the model or
// inspected by the suspect rules: NFKC, then control, format (bidi, zero
// width, soft hyphen, tag characters) and variation-selector characters are
// removed. Newlines and tabs are kept.
func SanitizeUntrusted(s string) string {
	s = norm.NFKC.String(s)
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF, r >= 0xE0000 && r <= 0xE007F:
			return -1
		}
		return r
	}, s)
}

// tagRe matches look-alikes of the untrusted-content delimiters. It runs on
// sanitized text, so it also needs to cover spaced/underscore-less variants
// and the other wrapper tags the gateway uses.
var tagRe = regexp.MustCompile(`(?i)<\s*/?\s*(?:untrusted[\s_\-]*web[\s_\-]*content|user_memory|tool_call|tool_response|/?system)`)

// defang rewrites delimiter look-alikes so payload text cannot close or fake
// the wrapper.
func defang(s string) string {
	return tagRe.ReplaceAllStringFunc(s, func(m string) string {
		return "[" + strings.TrimLeft(strings.TrimSpace(m), "< \t/")
	})
}

// delimRe matches the untrusted-content delimiter only. It is what a client
// system message is checked against (the memory block legitimately contains
// its own wrapper tag, so the wider tagRe is not used there).
var delimRe = regexp.MustCompile(`(?i)<\s*/?\s*untrusted[\s_\-]*web[\s_\-]*content`)
