package tools

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func echoFor(mem, user string) *Echo { return NewEcho([]string{mem}, []string{user}) }

func echoForAll(mems []string, user string) *Echo { return NewEcho(mems, []string{user}) }

// Rev 7 §4.3.1 fixed counter examples: six rejected spellings and the normal
// query that must pass with both memory facts present.
func TestEcho_TokenRule_CounterExamples(t *testing.T) {
	cat, move := "고양이 이름은 나비", "이사 예정"
	cases := []struct {
		name  string
		mems  []string
		user  string
		q     string
		want  string
		extra string
	}{
		{"1 memory token longer than the query token", []string{"나비랑 산책"}, "사료 추천해줘", "나비 사료", RuleEchoToken, ""},
		{"2 place with the suffix cut off", []string{"역삼동 거주"}, "맛집 추천해줘", "역삼 맛집", RuleEchoToken, ""},
		{"3 name split by blanks", []string{cat}, "사료 추천해줘", "나 비 사료", RuleEchoConcat, ""},
		{"4 place split into single runes", []string{"역삼동 거주"}, "맛집 추천해줘", "역 삼 동 맛집", RuleEchoConcat, ""},
		{"5 prefix exemption with a place as the tail", []string{"역삼동 거주"}, "맛집 추천해줘", "맛집역삼동", RuleEchoToken, ""},
		{"6 prefix exemption with a name as the tail", []string{cat}, "사료 추천해줘", "사료나비", RuleEchoToken, ""},
		{"normal query with both facts present", []string{cat, move}, "고양이 사료 추천해줘", "고양이 사료 추천", "", ""},
		{"name as plain word is rejected", []string{cat}, "고양이 사료 추천해줘", "고양이 나비 사료", RuleEchoToken, ""},
		{"user typed the name", []string{cat}, "우리 고양이 나비 사료 추천해줘", "고양이 나비 사료", "", ""},
		{"boundary case: the joined text of U is not a source of exemption", []string{move}, "고양이 사료 추천해줘", "이사 준비", RuleEchoToken, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := echoForAll(c.mems, c.user).Check(c.q); got != c.want {
				t.Fatalf("Check(%q) = %q, want %q", c.q, got, c.want)
			}
		})
	}
}

// §6.9 TestEcho_ObfuscatedVariants: zero-width characters, full-width forms,
// NFD, case, blanks and punctuation inside the word (one character at a time
// too) are all rejected. No expectation was weakened to make this pass.
func TestEcho_ObfuscatedVariants(t *testing.T) {
	mem := "고양이 이름은 나비 그리고 담당의사는 Kimberly"
	for _, q := range []string{
		"나\u200b비 사료",       // zero-width space
		"나\u200d비 사료",       // zero-width joiner
		"\u2060나비\u2060 사료", // word joiner
		"ｋｉｍｂｅｒｌｙ 병원",       // full-width Latin
		"KIMBERLY 병원",       // case
		"kimberly 병원",
		"나ㅤ비 사료", // blank-like characters stay token boundaries
		"나 비 사료", "나.비 사료", "나-비 사료", "나,비 사료", "나_비 사료",
		"k i m b e r l y 병원", "k.i.m.b.e.r.l.y 병원", "k-i-m-b-e-r-l-y 병원",
		"사료 나   비", "사료 나\t비",
	} {
		if got := echoForAll([]string{mem}, "사료 병원 추천해줘").Check(q); got == "" {
			t.Errorf("not rejected: %q", q)
		}
	}
	// NFD: a decomposed Hangul spelling normalizes to the same token
	if got := echoForAll([]string{mem}, "사료 추천해줘").Check(norm.NFD.String("나비 사료")); got == "" {
		t.Error("NFD spelling not rejected")
	}
}

// Concatenation match (rev 7, H-2 a): the three conditions, one by one.
func TestEcho_ConcatMatch_BoundaryAndExempt(t *testing.T) {
	cat := "고양이 이름은 나비"
	cases := []struct {
		name, mem, user, q, want string
	}{
		{"match inside one token is rule 2, not concat", cat, "사료 추천해줘", "나비사료", RuleEchoToken},
		{"two tokens, boundary crossed", cat, "사료 추천해줘", "나 비 사료", RuleEchoConcat},
		{"three tokens", "역삼동 거주", "맛집 추천해줘", "역 삼 동 맛집", RuleEchoConcat},
		{"arbitrary cut: start and end in the middle of tokens", "역삼동 거주", "맛집 추천해줘", "가역 삼 동", RuleEchoConcat},
		{"stopword pieces count", "역삼동 거주", "맛집 추천해줘", "역 삼 동", RuleEchoConcat},
		{"one-rune pieces count", cat, "사료 추천해줘", "사료 나 비", RuleEchoConcat},
		{"every covered token is a user word: passes", "이사 예정", "고양이 사료 추천해줘", "고양이 사료 추천", ""},
		{"only some covered tokens are user words: rejected", "이사 예정", "사료 추천해줘", "고양이 사료 추천", RuleEchoConcat},
		{"user words joined by the query only", "이사 예정", "고양이 사료 추천해줘", "고양이 사료", ""},
		{"U is never joined: the user's text does not exempt a joined spelling", "나비 산책", "나 비 사료", "나비 사료", RuleEchoToken},
		{"the match must be a whole content token of P (one rune is not in M)", "나 이사", "사료 추천해줘", "이 사 사료", RuleEchoConcat},
		{"unrelated query", cat, "사료 추천해줘", "강아지 사료", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := echoForAll([]string{c.mem}, c.user).Check(c.q); got != c.want {
				t.Fatalf("Check(%q) = %q, want %q", c.q, got, c.want)
			}
		})
	}
	// m has one rune: it is not in M at all
	if got := echoForAll([]string{"나 산책"}, "사료 추천해줘").Check("나 사료"); got != "" {
		t.Errorf("one-rune memory token matched: %q", got)
	}
}

// Prefix tail (rev 7, H-2 b).
func TestEcho_PrefixTail(t *testing.T) {
	cases := []struct {
		name, mem, user, q, want string
	}{
		{"tail equals m (3 runes)", "역삼동 거주", "맛집 추천해줘", "맛집역삼동", RuleEchoToken},
		{"tail equals m (2 runes)", "고양이 이름은 나비", "사료 추천해줘", "사료나비", RuleEchoToken},
		{"tail is a substring of m", "역삼동 거주", "맛집 추천해줘", "맛집역삼", RuleEchoToken},
		{"tail contains m", "고양이 이름은 나비", "사료 추천해줘", "사료나비야", RuleEchoToken},
		{"overlap without a substring relation adds to the sum", "고양이 이름은 나비", "사료 추천해줘", "사료비야", ""},
		{"one-rune tail overlapping m adds one", "고양이 이름은 나비", "사료 추천해줘", "사료비", ""},
		{"tail unrelated to P", "고양이 이름은 나비", "고양이 사료 추천해줘", "고양이가 사료", ""},
		{"not a prefix exemption: token is a plain new token", "역삼동 거주", "맛집 추천해줘", "역삼동맛집", RuleEchoToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := echoForAll([]string{c.mem}, c.user).Check(c.q); got != c.want {
				t.Fatalf("Check(%q) = %q, want %q", c.q, got, c.want)
			}
		})
	}
	// overlapping runes are added to R_new: twelve pass, thirteen are rejected
	e := echoForAll([]string{"고양이 이름은 나비"}, "사료 추천해줘")
	tok := func(n int) string { return strings.Repeat("강", n) } // new tokens of n runes
	// base: "사료비야" has user word 사료 + tail 비야 (L=1 with 나비) => 1 rune
	if got := e.Check("사료비야 " + tok(11)); got != "" {
		t.Errorf("12 runes (1 + 11): %q", got)
	}
	if got := e.Check("사료비야 " + tok(12)); got != RuleEchoNewToken {
		t.Errorf("13 runes (1 + 12): %q", got)
	}
}

func TestEcho_NormalQueryCorpus(t *testing.T) {
	facts, queries := loadCorpus(t)
	// average memory (the fixture) and a cap-sized one (the fixture plus 200 generated facts)
	rels := []string{"동생", "삼촌", "이모", "친구", "동료", "선배", "후배", "이웃", "사촌", "조카"}
	names := []string{"서준", "민준", "지우", "서연", "도윤", "예준", "시우", "주원", "하준", "지호", "수아", "지안", "다은", "유나", "채원", "소율", "건우", "은우", "현우", "도현"}
	big := append([]string{}, facts...)
	for _, r := range rels {
		for _, n := range names {
			big = append(big, r+" 이름은 "+n)
		}
	}
	var res strings.Builder
	fmt.Fprintf(&res, "# Result of TestEcho_NormalQueryCorpus (design 6.1, 8.3 gate). Generated: ECHO_CORPUS_UPDATE=1 go test ./internal/tools -run TestEcho_NormalQueryCorpus\n")
	fmt.Fprintf(&res, "# The corpus (echo_normal_queries.txt) is synthetic: no production query or memory text. Gate: upper bound of the\n")
	fmt.Fprintf(&res, "# per-request rejection rate must stay at or below 10%%; a request holds at most 2 searches (max_per_turn), so\n")
	fmt.Fprintf(&res, "# per-request upper = 1-(1-per-query upper)^2.\n\n")
	for _, cfg := range []struct {
		name  string
		facts []string
	}{{"average", facts}, {"cap-sized", big}} {
		rej, total := 0, 0
		byRule := map[string]int{}
		var rejected []string
		for _, c := range queries {
			total++
			got := NewEcho(cfg.facts, []string{c.user}).Check(c.q)
			switch {
			case c.known && got == "":
				t.Errorf("%s: known false positive now passes (move it to Q): user %q query %q", cfg.name, c.user, c.q)
			case c.known:
				rej++
				byRule[got]++
				rejected = append(rejected, fmt.Sprintf("  %s\tuser=%q\tquery=%q", got, c.user, c.q))
			case got != "":
				rej++
				byRule[got]++
				t.Errorf("%s: normal query rejected (%s): user %q query %q", cfg.name, got, c.user, c.q)
			}
		}
		lo, hi := wilson(rej, total)
		reqHi := 1 - (1-hi)*(1-hi)
		rules := make([]string, 0, len(byRule))
		for r, n := range byRule {
			rules = append(rules, fmt.Sprintf("%s=%d", r, n))
		}
		sort.Strings(rules)
		fmt.Fprintf(&res, "memory %s (%d facts)\n  queries: %d\n  rejected: %d\n  per-query rate: %.2f%%\n  Wilson 95%% interval: [%.2f%%, %.2f%%]\n  per-request upper bound (2 searches): %.2f%%\n  by rule: %s\n  rejected items:\n%s\n\n",
			cfg.name, len(cfg.facts), total, rej, 100*float64(rej)/float64(total), 100*lo, 100*hi, 100*reqHi, strings.Join(rules, " "), strings.Join(rejected, "\n"))
		t.Logf("%s: normal-query rejection %d/%d (%.1f%%), Wilson 95%% [%.1f%%, %.1f%%], per-request upper %.1f%%", cfg.name, rej, total, 100*float64(rej)/float64(total), 100*lo, 100*hi, 100*reqHi)
		if total < 300 {
			t.Fatalf("corpus too small for the gate: %d (a corpus of 100 only bounds the rate at about 17%%)", total)
		}
		if reqHi > 0.10 {
			t.Errorf("%s: per-request upper bound %.1f%% is above the 10%% gate", cfg.name, 100*reqHi)
		}
	}
	const resPath = "testdata/echo_normal_queries_results.txt"
	if os.Getenv("ECHO_CORPUS_UPDATE") == "1" {
		if err := os.WriteFile(resPath, []byte(res.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want, err := os.ReadFile(resPath); err != nil || string(want) != res.String() {
		t.Errorf("%s is missing or stale: regenerate with ECHO_CORPUS_UPDATE=1 and commit it", resPath)
	}
}

type corpusCase struct {
	user, q string
	known   bool // a rejection the design accepts (rule 3 on a translated query); counted in the rate
}

// loadCorpus reads testdata/echo_normal_queries.txt (append-only): lines
// "F\t<memory fact>" give the synthetic memory fixture; "Q\t<user>\t<query>"
// are queries an honest model writes for that user text.
func loadCorpus(t *testing.T) (facts []string, qs []corpusCase) {
	t.Helper()
	raw, err := os.ReadFile("testdata/echo_normal_queries.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case f[0] == "F" && len(f) == 2:
			facts = append(facts, f[1])
		case f[0] == "Q" && len(f) == 3:
			qs = append(qs, corpusCase{f[1], f[2], false})
		case f[0] == "K" && len(f) == 3:
			qs = append(qs, corpusCase{f[1], f[2], true})
		default:
			t.Fatalf("bad corpus line %q", line)
		}
	}
	return
}

// wilson returns the 95% Wilson score interval for k of n.
func wilson(k, n int) (lo, hi float64) {
	if n == 0 {
		return 0, 1
	}
	z, p, nn := 1.959964, float64(k)/float64(n), float64(n)
	d := 1 + z*z/nn
	c := p + z*z/(2*nn)
	h := z * math.Sqrt(p*(1-p)/nn+z*z/(4*nn*nn))
	return (c - h) / d, (c + h) / d
}

// m contains t, t contains m, equal and unrelated; the result must not depend
// on any particle list (there is none) and must hold for endings and compounds.
func TestEcho_TokenRule_BidirectionalSubstring(t *testing.T) {
	cases := []struct {
		mem, q string
		reject bool
	}{
		{"나비", "나비", true},                  // equal
		{"나비랑", "나비", true},                 // m contains t (particle)
		{"역삼동", "역삼", true},                 // m contains t (suffix)
		{"나비", "나비가", true},                 // t contains m (particle)
		{"나비", "나비사료", true},                // t contains m (compound)
		{"나비", "새나비집", true},                // t contains m (inside)
		{"마라톤대회", "마라톤", true},              // m contains t
		{"gangnam", "gangnamstation", true}, // t contains m
		{"나비", "강아지", false},                // unrelated
		{"역삼동", "서초", false},                // unrelated
	}
	for _, c := range cases {
		e := NewEcho([]string{"메모: " + c.mem}, []string{"hello world"})
		got := e.rejectToken(queryTokens(c.q))
		if got != c.reject {
			t.Errorf("mem %q query %q: reject=%v want %v", c.mem, c.q, got, c.reject)
		}
	}
}

// Kept pinned name (§6.9 G5): endings and compounds are caught without lists.
func TestEcho_TokenRule_ParticleAndCompound(t *testing.T) {
	e := echoFor("고양이 이름은 나비", "고양이 사료 추천해줘")
	for _, q := range []string{"고양이 나비가 좋아하는 사료", "고양이 나비사료", "고양이 나비랑 사료", "고양이 나비의 사료"} {
		if got := e.Check(q); got != RuleEchoToken {
			t.Errorf("%q = %q, want echo_token", q, got)
		}
	}
}

func TestEcho_TokenRule_UserWordExempt(t *testing.T) {
	e := echoFor("고양이 이름은 나비, 사는 곳은 역삼동", "우리 나비 역삼동 맛집 추천해줘")
	for _, q := range []string{"나비 역삼동 맛집", "나비 역삼동", "역삼동 맛집 추천"} {
		if got := e.Check(q); got != "" {
			t.Errorf("%q = %q, want pass (the user wrote the words)", q, got)
		}
	}
}

// Exemption works on word boundaries, never on the joined text of U.
func TestEcho_UserWordExempt_WordBoundary(t *testing.T) {
	user := "고양이 사료 추천해줘"
	cases := []struct {
		name, mem, q string
		reject       bool
	}{
		{"across the boundary of the joined text", "이사 예정", "이사 준비", true},
		{"middle substring of a user word", "양이 이야기", "양이 사료", true},
		{"exactly the user word", "고양이 취미", "고양이 사료", false},
		{"user word plus one rune", "고양이 취미", "고양이가 사료", false},
		{"user word plus three runes", "고양이 취미", "고양이들에게 사료", false},
		{"user word plus four runes", "고양이 취미", "고양이였던것이다 사료", true},
		{"user word is the longer one (prefix, diff 2)", "추천 취미", "추천 사료", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := NewEcho([]string{c.mem}, []string{user})
			if got := e.rejectToken(queryTokens(c.q)); got != c.reject {
				t.Fatalf("reject=%v want %v", got, c.reject)
			}
		})
	}
	if userWord("이사", splitTokens("고양이 사료 추천해줘")) {
		t.Fatal("a substring that crosses the joined text is a user word")
	}
	if userWord("양이", splitTokens(user)) {
		t.Fatal("a middle substring is a user word")
	}
}

func TestEcho_NewTokenCap(t *testing.T) {
	cases := []struct {
		name, user, q, want string
	}{
		{"korean 12 runes", "고양이 사료 추천해줘", "고양이 가나다라 마바사아 자차카타", ""},
		{"korean 13 runes", "고양이 사료 추천해줘", "고양이 가나다라 마바사아 자차카타파", RuleEchoNewToken},
		{"english 12 runes", "healthy breakfast ideas", "healthy oatmeal honey", ""},
		{"english 13 runes", "healthy breakfast ideas", "healthy oatmeal banana", RuleEchoNewToken},
		{"no user content token reused", "고양이 사료 추천해줘", "가나다 마바사", RuleEchoNoToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NewEcho(nil, []string{c.user}).Check(c.q); got != c.want {
				t.Fatalf("Check(%q) = %q, want %q", c.q, got, c.want)
			}
		})
	}
}

func TestEcho_NewTokenRuneCap(t *testing.T) {
	e := NewEcho(nil, []string{"고양이 사료 추천해줘"})
	// the same 13 new runes: one long token, split in words, split in single runes
	for _, q := range []string{
		"고양이 가나다라마바사아자차카타파",
		"고양이 가나다 라마바 사아자 차카타파",
		"고양이 가 나 다 라 마 바 사 아 자 차 카 타 파",
	} {
		if got := e.Check(q); got != RuleEchoNewToken {
			t.Errorf("%q = %q, want echo_newtoken", q, got)
		}
	}
	// 12 runes pass in any shape
	for _, q := range []string{"고양이 가나다라마바사아자차카타", "고양이 가나다 라마바 사아자 차카타", "고양이 가 나 다 라 마 바 사 아 자 차 카 타"} {
		if got := e.Check(q); got != "" {
			t.Errorf("%q = %q, want pass", q, got)
		}
	}
	// user words and stopwords add nothing
	if got := e.Check("고양이 사료 추천해줘 고양이 사료 추천해줘 고양이사료 가나다라마바사아자차카타"); got != "" {
		t.Errorf("exempt tokens counted: %q", got)
	}
	if !IsStopword("the") {
		t.Fatal("stopword list lacks 'the'")
	}
	if got := NewEcho(nil, []string{"healthy breakfast"}).Check("healthy the the the the the oatmeal honey"); got != "" {
		t.Errorf("stopwords counted: %q", got)
	}
}

func TestEcho_StopwordFile(t *testing.T) {
	if StopwordCount() < 50 {
		t.Fatalf("stopword list too short: %d", StopwordCount())
	}
	raw, err := os.ReadFile("echo_stopwords.txt")
	if err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadFile("testdata/echo_entities.txt")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, w := range parseStopwords(string(ents)) {
		m[w] = true
	}
	if bad := LintStopwords(string(raw), m); len(bad) != 0 {
		t.Fatalf("stopword lint: %s", strings.Join(bad, "; "))
	}
	// the lint itself must catch what it is for
	if len(LintStopwords("나비\n역삼동\n", map[string]bool{"나비": true})) < 2 {
		t.Fatal("lint does not catch entities / place names")
	}
	for _, w := range []string{"나비", "역삼동", "땅콩", "알레르기"} {
		if IsStopword(w) {
			t.Errorf("%q must not be a stopword", w)
		}
	}
}

// §6.9 G2 cases at the filter level (any echo rule may be the one that fires); the handler-level test lives in
// internal/handlers (TestG2_MemoryNeverQueryMaterial).
func TestEcho_PersonalizationCases(t *testing.T) {
	for _, c := range []struct{ mem, user, q string }{
		{"고양이 이름은 나비", "고양이 사료 추천해줘", "고양이 나비 사료"},
		{"땅콩 알레르기가 있음", "간식 추천해줘", "땅콩 알레르기 간식"},
		{"땅콩 알레르기가 있음", "간식 추천해줘", "간식 알레르기"},
	} {
		if got := echoFor(c.mem, c.user).Check(c.q); got == "" {
			t.Errorf("mem %q q %q passed", c.mem, c.q)
		}
	}
}

// M-i rule (c): inside an exempt token (user word + tail) a word of P that
// starts in the user's word and ends after it is rejected, whatever the tail.
func TestEcho_PrefixTail_BoundaryCrossing(t *testing.T) {
	cat := "고양이 이름은 나비"
	cases := []struct {
		name, mem, user, q, want string
	}{
		{"user wrote one rune, query has the name", cat, "나 사료 추천해줘", "나비 사료", RuleEchoToken},
		{"user wrote the stem, query completes the memory word", cat, "고양 사료 추천해줘", "고양이 사료", RuleEchoToken},
		{"place completed by one rune", "역삼동 거주", "역삼 맛집 추천해줘", "역삼동 맛집", RuleEchoToken},
		{"memory word made of the last rune of the user word and the tail", "집이 멀다", "맛집 추천해줘", "맛집이", RuleEchoToken},
		{"user wrote the whole word: nothing crosses", cat, "고양이 사료 추천해줘", "고양이가 사료", ""},
		{"memory word inside the user word only", cat, "고양이 사료 추천해줘", "고양이는 사료", ""},
		{"tail equal to a function word and no memory word crosses", "역삼동 거주", "맛집 추천해줘", "맛집은 추천", ""},
		{"tail is a function word but a memory word crosses", "집은 넓다", "맛집 추천해줘", "맛집은 추천", RuleEchoToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := echoForAll([]string{c.mem}, c.user).Check(c.q); got != c.want {
				t.Fatalf("Check(%q) = %q, want %q", c.q, got, c.want)
			}
		})
	}
}

// A function word attached to a user word is not new text for the rule 3 sum
// (it used to count as one rune when it overlapped P); a content tail still is.
func TestEcho_FunctionTail_NotNewText(t *testing.T) {
	e := echoForAll([]string{"가방 정리"}, "사료 추천해줘")
	tok12 := strings.Repeat("강", 12)
	if got := e.Check("사료가 " + tok12); got != "" {
		t.Errorf("function word counted as new text: %q", got)
	}
	if got := e.Check("사료방 " + tok12); got != RuleEchoNewToken {
		t.Errorf("content tail overlapping P not counted: %q", got)
	}
}

// The list is closed: a change has to touch this test, i.e. be reviewed.
func TestEcho_FunctionTailList(t *testing.T) {
	want := "가 과 까지 나 는 도 라고 랑 로 를 만 만의 보다 부터 를 에 에게 에는 에도 에서 에서는 와 은 을 의 이 이나 이다 이라 이랑 으로 으로는 처럼 하고 께"
	got := make([]string, 0, len(functionTails))
	for w := range functionTails {
		got = append(got, w)
		if n := len([]rune(w)); n < 1 || n > 3 {
			t.Errorf("%q: a function tail has 1-3 runes", w)
		}
		if IsStopword(w) {
			t.Errorf("%q is also a stopword", w)
		}
	}
	wantSet := map[string]bool{}
	for _, w := range strings.Fields(want) {
		wantSet[w] = true
	}
	if len(got) != len(wantSet) {
		t.Errorf("list size %d, pinned %d", len(got), len(wantSet))
	}
	for _, w := range got {
		if !wantSet[w] {
			t.Errorf("%q is not on the pinned list", w)
		}
	}
	for _, w := range []string{"사료", "나비", "동", "비", "비야", "집"} {
		if IsFunctionTail(w) {
			t.Errorf("%q is a content word, not a function tail", w)
		}
	}
}
