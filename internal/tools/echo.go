package tools

import (
	"bufio"
	_ "embed"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Echo filter (auto tool selection): a model-written query or URL must not
// carry protected text (memory facts, suspect tool output) that the user did
// not type in this request. The comparison is on normalized 4-grams:
// N(x) = lower-cased letters and digits of SanitizeUntrusted(x); a query is
// rejected when at least EchoGrams of its 4-grams occur in a single protected
// text but in none of the user's texts, or when it contains an e-mail,
// phone-like, 4+ digit or URL token that is protected and not user-typed.
//
// Rule ② (echo_token, §4.3.1 rev 6): a content token m of a protected text
// and a query token t reject the query when t is a substring of m or m is a
// substring of t (either direction, token against token, no particle list),
// unless t is a word the user typed. A user word is a token u of the user's
// texts with t == u, or one a prefix of the other and a length difference of
// at most EchoUserSlack runes (word boundaries; concatenated text is never
// searched). Rule ③ (echo_newtoken): the runes of the query tokens that are
// neither user words nor stopwords add up to EchoNewRunes-1 at most, and the
// query must reuse a content word of the user's texts when there is one.
const (
	EchoGrams    = 3
	EchoGramSize = 4
	// EchoNewRunes: a query whose new tokens add up to this many runes or more
	// is rejected (rule ③, 12 runes are allowed).
	EchoNewRunes = 13
	// EchoUserSlack is the longest length difference (runes) between a query
	// token and a user word that still counts as that word (rule ②/③).
	EchoUserSlack = 3
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
	prot     []echoText
	user     map[string]struct{}
	userT    map[string]struct{}
	userTok  []string // tokens of the user texts (word boundaries kept)
	userCont []string // the usable, non-stopword ones
}

type echoText struct {
	grams map[string]struct{}
	toks  map[string]struct{}
	cont  map[string]struct{} // content tokens M (rule ②)
}

// NewEcho builds the filter. protected are texts whose content must not be
// echoed into tool inputs; user are the user-role texts of this request.
func NewEcho(protected, user []string) *Echo {
	e := &Echo{user: map[string]struct{}{}, userT: map[string]struct{}{}}
	for _, u := range user {
		n := NormEcho(u, false)
		for g := range grams4(n) {
			e.user[g] = struct{}{}
		}
		for t := range echoTokens(u) {
			e.userT[t] = struct{}{}
		}
		for _, t := range splitTokens(u) {
			e.userTok = append(e.userTok, t)
			if usableToken(t) && !stopwords[t] {
				e.userCont = append(e.userCont, t)
			}
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
		e.prot = append(e.prot, echoText{grams: grams4(NormEcho(p, false)), toks: echoTokens(p), cont: contentTokens(p)})
	}
}

// Active reports whether any protected text is registered.
func (e *Echo) Active() bool { return e != nil && len(e.prot) > 0 }

// ProtectedCount is the number of protected texts (P) registered so far.
func (e *Echo) ProtectedCount() int {
	if e == nil {
		return 0
	}
	return len(e.prot)
}

// Rule ids returned by Check; all map to the public code query_rejected.
const (
	RuleEchoFilter   = "echo_filter"
	RuleEchoURL      = "echo_filter_url"
	RuleEchoToken    = "echo_token"
	RuleEchoNewToken = "echo_newtoken"
	RuleEchoNoToken  = "echo_notoken"
)

// Reject reports whether q (a search query) is refused by rules ①–③.
func (e *Echo) Reject(q string) bool { return e.Check(q) != "" }

// Check returns the id of the first rule that refuses q, or "".
func (e *Echo) Check(q string) string {
	if e == nil {
		return ""
	}
	nq := NormEcho(q, false)
	if e.Active() {
		if e.reject(grams4(nq), echoTokens(q)) {
			return RuleEchoFilter
		}
		if e.rejectToken(queryTokens(q)) {
			return RuleEchoToken
		}
	}
	return e.newTokens(q)
}

// userWord reports whether t counts as a word the user typed: equal to a
// token u of the user's texts, or a prefix of it (or it of t) with a length
// difference of at most EchoUserSlack runes.
func userWord(t string, toks []string) bool {
	nt := utf8.RuneCountInString(t)
	for _, u := range toks {
		if u == t {
			return true
		}
		d := utf8.RuneCountInString(u) - nt
		if d < 0 {
			d = -d
		}
		// the prefix relation needs two runes on the shorter side, so a query
		// spelled one rune at a time cannot hide behind a user word
		if d <= EchoUserSlack && min(nt, utf8.RuneCountInString(u)) >= 2 && (strings.HasPrefix(u, t) || strings.HasPrefix(t, u)) {
			return true
		}
	}
	return false
}

// rejectToken is rule ②: a query token that has a content token of P inside
// it, or sits inside one, and is not a user word.
func (e *Echo) rejectToken(qtoks []string) bool {
	for _, t := range qtoks {
		if userWord(t, e.userTok) {
			continue
		}
		for _, p := range e.prot {
			for m := range p.cont {
				if strings.Contains(m, t) || strings.Contains(t, m) {
					return true
				}
			}
		}
	}
	return false
}

// newTokens is rule ③: the runes of the query tokens that are neither user
// words nor stopwords may add up to EchoNewRunes-1; one-rune tokens count, so
// splitting or joining a query does not change the sum. The query must reuse
// a content word of the user's texts when there is one.
func (e *Echo) newTokens(q string) string {
	toks := splitTokens(q)
	sum, reuse := 0, false
	for _, t := range toks {
		if userWord(t, e.userTok) {
			if userWord(t, e.userCont) {
				reuse = true
			}
			continue
		}
		if stopwords[t] && utf8.RuneCountInString(t) >= 2 { // one-rune tokens always count
			continue
		}
		sum += utf8.RuneCountInString(t)
	}
	if sum >= EchoNewRunes {
		return RuleEchoNewToken
	}
	if !reuse && len(e.userCont) > 0 && len(toks) > 0 {
		return RuleEchoNoToken
	}
	return ""
}

// RejectURL applies rules ① and ② to everything after the host of rawURL
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
	if e.reject(grams4(NormEcho(rest, true)), toks) {
		return true
	}
	return e.rejectToken(queryTokens(rest))
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

// --- tokens (rules ② and ③) ---

//go:embed echo_stopwords.txt
var stopwordsRaw string

// stopwords is read once at startup from the embedded data file. Nothing
// changes it while the process runs.
var stopwords = mustStopwords(stopwordsRaw)

func parseStopwords(raw string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		w := strings.TrimSpace(sc.Text())
		if w == "" || strings.HasPrefix(w, "#") {
			continue
		}
		out = append(out, strings.ToLower(w))
	}
	return out
}

func mustStopwords(raw string) map[string]bool {
	m := map[string]bool{}
	for _, w := range parseStopwords(raw) {
		m[w] = true
	}
	if len(m) < 50 {
		panic("echo_stopwords: list missing or truncated")
	}
	return m
}

// StopwordCount is the size of the loaded stopword list.
func StopwordCount() int { return len(stopwords) }

// IsStopword reports whether w (already lower-cased) is on the list.
func IsStopword(w string) bool { return stopwords[w] }

var dateTimeTokRe = regexp.MustCompile(`^\d+(년|월|일|시|분|초|개|명|원|%|st|nd|rd|th)?$`)

// splitTokens splits s (NFKC, invisible characters removed, lower-cased) at
// every character that is neither letter nor digit.
func splitTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(SanitizeUntrusted(s)), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// usableToken: two runes or more, not a number or date/time pattern.
func usableToken(t string) bool {
	return utf8.RuneCountInString(t) >= 2 && !dateTimeTokRe.MatchString(t)
}

// queryTokens are the tokens of a query (or URL remainder) that rule ②
// compares: usable (two runes or more, not a number or date) and not
// stopwords, in order and deduplicated.
func queryTokens(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range splitTokens(q) {
		if usableToken(t) && !stopwords[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// contentTokens is the content token set M of a protected text: usable
// tokens that are not stopwords. No stems are made; the bidirectional match
// of rule ② covers particles, endings and compounds.
func contentTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range splitTokens(s) {
		if usableToken(t) && !stopwords[t] {
			out[t] = struct{}{}
		}
	}
	return out
}

// LintStopwords checks a stopword list: entities (names, places, brands)
// must not be stopwords. It returns one message per offending entry, sorted.
// entities is the reviewed canary set (testdata/echo_entities.txt). The
// heuristic part rejects what is certainly not a function or search word:
// digit-bearing entries, place-like (…동) entries, honorifics (님), very long
// entries, and duplicates.
func LintStopwords(raw string, entities map[string]bool) []string {
	var bad []string
	seen := map[string]bool{}
	for _, w := range parseStopwords(raw) {
		switch {
		case seen[w]:
			bad = append(bad, "duplicate: "+w)
		case entities[w]:
			bad = append(bad, "entity: "+w)
		case utf8.RuneCountInString(w) > 12:
			bad = append(bad, "too long: "+w)
		case strings.IndexFunc(w, unicode.IsDigit) >= 0:
			bad = append(bad, "digit: "+w)
		case utf8.RuneCountInString(w) >= 3 && strings.HasSuffix(w, "동"):
			bad = append(bad, "place-like: "+w)
		case strings.HasSuffix(w, "님"):
			bad = append(bad, "honorific/name-like: "+w)
		}
		seen[w] = true
	}
	sort.Strings(bad)
	return bad
}
