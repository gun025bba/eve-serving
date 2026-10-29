package tools

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Echo filter (auto tool selection): a model-written query or URL must not
// carry protected text (memory facts, suspect tool output) that the user did
// not type in this request. The comparison is on normalized 4-grams:
// N(x) = lower-cased letters and digits of SanitizeUntrusted(x); a query is
// rejected when at least EchoGrams of its 4-grams occur in a single protected
// text but in none of the user's texts, or when it contains an e-mail,
// phone-like, 4+ digit or URL token that is protected and not user-typed.
const (
	EchoGrams    = 3
	EchoGramSize = 4
	// MaxAutoQuery is the rune cap for model-written queries.
	MaxAutoQuery = 128
)

// NormEcho is N(x). When raw is true (URL parts) NFKC is not applied, only
// invisible characters are dropped.
func NormEcho(s string, raw bool) string {
	if raw {
		s = stripInvisible(s)
	} else {
		s = SanitizeUntrusted(s)
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || (r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF) {
			return -1
		}
		return r
	}, s)
}

func grams4(n string) map[string]struct{} {
	r := []rune(n)
	out := map[string]struct{}{}
	for i := 0; i+EchoGramSize <= len(r); i++ {
		out[string(r[i:i+EchoGramSize])] = struct{}{}
	}
	return out
}

var echoTokRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}|https?://[^\s<>"']+|\+?\d[\d\-\s]{6,}\d|\d{4,}`)

func echoTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	s = SanitizeUntrusted(s)
	for _, m := range echoTokRe.FindAllString(s, 200) {
		t := strings.ToLower(strings.TrimRight(m, ".,;:)"))
		if strings.HasPrefix(t, "http") {
			out[t] = struct{}{}
			continue
		}
		// phone-like / digit runs compare on digits only
		var d strings.Builder
		hasLetter := false
		for _, r := range t {
			if unicode.IsDigit(r) {
				d.WriteRune(r)
			} else if unicode.IsLetter(r) {
				hasLetter = true
			}
		}
		if !hasLetter && d.Len() >= 4 {
			out[d.String()] = struct{}{}
		} else {
			out[t] = struct{}{}
		}
	}
	return out
}

// Echo holds the protected texts and the user's own texts of one request.
type Echo struct {
	prot  []echoText
	user  map[string]struct{}
	userT map[string]struct{}
}

type echoText struct {
	grams map[string]struct{}
	toks  map[string]struct{}
}

// NewEcho builds the filter. protected are texts whose content must not be
// echoed into tool inputs; user are the user-role texts of this request.
func NewEcho(protected, user []string) *Echo {
	e := &Echo{user: map[string]struct{}{}, userT: map[string]struct{}{}}
	for _, u := range user {
		for g := range grams4(NormEcho(u, false)) {
			e.user[g] = struct{}{}
		}
		for t := range echoTokens(u) {
			e.userT[t] = struct{}{}
		}
	}
	e.AddProtected(protected...)
	return e
}

// AddProtected registers more protected texts (e.g. output of a suspect
// tool result).
func (e *Echo) AddProtected(texts ...string) {
	if e == nil {
		return
	}
	for _, p := range texts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		e.prot = append(e.prot, echoText{grams: grams4(NormEcho(p, false)), toks: echoTokens(p)})
	}
}

// Active reports whether any protected text is registered.
func (e *Echo) Active() bool { return e != nil && len(e.prot) > 0 }

// Reject reports whether q (a search query) echoes protected text.
func (e *Echo) Reject(q string) bool {
	if !e.Active() {
		return false
	}
	return e.reject(grams4(NormEcho(q, false)), echoTokens(q))
}

// RejectURL applies the filter to everything after the host of rawURL
// (path, query, fragment), percent-decoded and without NFKC.
func (e *Echo) RejectURL(rawURL string) bool {
	if !e.Active() {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return true // fail closed
	}
	rest := u.Path + " " + u.RawQuery + " " + u.Fragment
	for range 2 { // two decode passes catch double encoding
		if d, err := url.QueryUnescape(rest); err == nil {
			rest = d
		}
	}
	toks := echoTokens(rest)
	return e.reject(grams4(NormEcho(rest, true)), toks)
}

func (e *Echo) reject(qg map[string]struct{}, qt map[string]struct{}) bool {
	for _, p := range e.prot {
		n := 0
		for g := range qg {
			if _, inP := p.grams[g]; !inP {
				continue
			}
			if _, inU := e.user[g]; inU {
				continue
			}
			n++
		}
		if n >= EchoGrams {
			return true
		}
		for t := range qt {
			if _, inP := p.toks[t]; !inP {
				continue
			}
			if _, inU := e.userT[t]; inU {
				continue
			}
			return true
		}
	}
	return false
}
