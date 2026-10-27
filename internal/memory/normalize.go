package memory

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// invisible reports zero-width and bidi control characters
// (U+200B–U+200F, U+202A–U+202E, U+2060–U+2064, U+FEFF).
func invisible(r rune) bool {
	return (r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2060 && r <= 0x2064) || r == 0xFEFF
}

// Normalize is the stored form of item text (memory.md §2-3): NFKC, then
// C0/C1 controls (except newline), zero-width and bidi controls removed, then
// surrounding whitespace trimmed.
func Normalize(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	for _, r := range s {
		if r == '\n' {
			b.WriteRune(r)
			continue
		}
		if r == '\t' || r == '\r' {
			b.WriteRune(' ')
			continue
		}
		if invisible(r) || unicode.Is(unicode.Cc, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
