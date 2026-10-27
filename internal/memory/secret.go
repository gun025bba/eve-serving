package memory

import (
	"encoding/base64"
	"encoding/hex"
	"math"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/gun025bba/eve-serving/internal/tools"
	"golang.org/x/text/unicode/norm"
)

// RejectError explains why a text was refused. Reason is a fixed code safe to
// show the user; the matched text is never included.
type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return "memory: rejected (" + e.Reason + ")" }

var (
	runRe   = regexp.MustCompile(`[A-Za-z0-9+/=_\-]{20,}`)
	uuidRe  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	ulidRe  = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	hexRe   = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	cardRe  = regexp.MustCompile(`\d(?:[ \-]?\d){12,18}`)
	b64Cand = regexp.MustCompile(`[A-Za-z0-9+/_\-]{16,}={0,2}`)
	pctRe   = regexp.MustCompile(`%[0-9a-fA-F]{2}`)
)

// TODO(security): homoglyph folding (UTS#39 skeleton/confusables) is not
// done; NFKC only covers compatibility forms. Decoding is one level only
// (URL, base64, hex); nested encodings (e.g. base64 of URL-encoded) are not
// unwrapped. Both are tracked for the pre-enable list in PR #25.

// Entropy thresholds per character set and run length bucket (memory.md §7,
// S10-M2/S10-F1). Initial proposal values; the ROC over the §8.5 corpus has
// not been run yet, so they are deliberately conservative.
// TODO(config): 나중에 설정으로 옮김 (memory.secret_filter.*), after the ROC run.
const (
	EntropyMinRunLen  = 20  // shorter runs are never checked
	EntropyHexKeyLen  = 32  // hex this long is rejected outright (hex_key)
	hexShortMin       = 3.3 // hex 20–31 (must stay < 4.0; 32+ is the hex_key pattern)
	mixedShortMaxLen  = 24  // mixed 20–24: composition rule
	mixedShortClasses = 3.6 // ... entropy floor when 3+ char classes present
	mixed25           = 4.0 // mixed 25–32
	mixed25Classes    = 3.8
	mixed33           = 4.3 // mixed 33–48
	mixed33Classes    = 4.0
	mixed49           = 4.5 // mixed 49+
	mixed49Classes    = 4.2
)

func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	cnt := map[rune]int{}
	n := 0
	for _, r := range s {
		cnt[r]++
		n++
	}
	h := 0.0
	for _, c := range cnt {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

func hasClasses(s string) bool {
	var up, lo, dg bool
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			up = true
		case r >= 'a' && r <= 'z':
			lo = true
		case r >= '0' && r <= '9':
			dg = true
		}
	}
	return up && lo && dg
}

// runSuspicious applies the length-bucket rules to one ASCII run.
func runSuspicious(run string) bool {
	core := strings.Trim(run, "=-_/+")
	if uuidRe.MatchString(core) || ulidRe.MatchString(core) {
		return false
	}
	n := len(core)
	if n < EntropyMinRunLen {
		return false
	}
	if hexRe.MatchString(core) {
		return n >= EntropyHexKeyLen || shannon(core) >= hexShortMin
	}
	switch {
	case n <= mixedShortMaxLen:
		// Mixed 20–24: composition rule instead of entropy (S10-F1 fallback).
		return hasClasses(core) && shannon(core) >= mixedShortClasses
	case n <= 32:
		return shannon(core) >= mixed25 || (hasClasses(core) && shannon(core) >= mixed25Classes)
	case n <= 48:
		return shannon(core) >= mixed33 || (hasClasses(core) && shannon(core) >= mixed33Classes)
	default:
		return shannon(core) >= mixed49 || (hasClasses(core) && shannon(core) >= mixed49Classes)
	}
}

func luhn(d string) bool {
	sum, alt := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if alt {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}

// squash is the bypass-resistant copy: NFKC, whitespace/zero-width removed.
func squash(s string) string {
	s = norm.NFKC.String(s)
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || invisible(r) || unicode.Is(unicode.Cc, r) {
			return -1
		}
		return r
	}, s)
}

// variants returns the strings every rule is applied to: the original, the
// NFKC copy, the squashed copy, and one level of URL/base64/hex decoding.
func variants(s string) []string {
	n := norm.NFKC.String(s)
	sq := squash(s)
	out := []string{s, n, sq}
	for _, base := range []string{n, sq} {
		if pctRe.MatchString(base) {
			if d, err := url.QueryUnescape(base); err == nil {
				out = append(out, d)
			}
		}
		for _, c := range b64Cand.FindAllString(base, 20) {
			for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
				if d, err := enc.DecodeString(c); err == nil && printable(d) {
					out = append(out, string(d))
					break
				}
			}
			if len(c)%2 == 0 && hexRe.MatchString(c) {
				if d, err := hex.DecodeString(c); err == nil && printable(d) {
					out = append(out, string(d))
				}
			}
		}
	}
	return out
}

func printable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

// CheckSecret refuses text that looks like a credential, payment card or
// government id (memory.md §7). It never masks: a hit means the write is
// refused. Any internal failure refuses too (fail-closed).
func CheckSecret(text string) (err error) {
	defer func() {
		if recover() != nil {
			err = &RejectError{Reason: "filter_error"}
		}
	}()
	sq := squash(text)
	for _, v := range variants(text) {
		for _, p := range tools.SecretPatterns {
			if p.Re.MatchString(v) {
				return &RejectError{Reason: p.Name}
			}
		}
		for _, c := range cardRe.FindAllString(v, -1) {
			d := strings.NewReplacer(" ", "", "-", "").Replace(c)
			if len(d) >= 13 && len(d) <= 19 && luhn(d) {
				return &RejectError{Reason: "payment_card"}
			}
		}
		if v == sq && v != text && v != norm.NFKC.String(text) {
			continue // runs are checked on the original/NFKC/decoded copies only (§7)
		}
		for _, run := range runRe.FindAllString(v, -1) {
			if runSuspicious(run) {
				return &RejectError{Reason: "high_entropy"}
			}
			// URL paths: check each segment too (a long path is one run).
			for _, seg := range strings.Split(run, "/") {
				if len(seg) >= 20 && runSuspicious(seg) {
					return &RejectError{Reason: "high_entropy"}
				}
			}
		}
	}
	return nil
}
