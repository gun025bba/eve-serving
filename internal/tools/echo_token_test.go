package tools

import (
	"os"
	"strings"
	"testing"
)

func echoFor(mem, user string) *Echo { return NewEcho([]string{mem}, []string{user}) }

// Rev 6 §4.3.1 fixed counter examples.
func TestEcho_TokenRule_CounterExamples(t *testing.T) {
	cases := []struct {
		name, mem, user, q, want string
	}{
		{"1 memory token longer than the query token", "나비랑 산책", "사료 추천해줘", "나비 사료", RuleEchoToken},
		{"2 place with the suffix cut off", "역삼동 거주", "맛집 추천해줘", "역삼 맛집", RuleEchoToken},
		{"3 normal query passes", "고양이 이름은 나비", "고양이 사료 추천해줘", "고양이 사료 추천", ""},
		{"4 name as plain word is rejected", "고양이 이름은 나비", "고양이 사료 추천해줘", "고양이 나비 사료", RuleEchoToken},
		{"5 user typed the name", "고양이 이름은 나비", "우리 고양이 나비 사료 추천해줘", "고양이 나비 사료", ""},
		{"6 boundary case: joined text of U is not a source of exemption", "이사 예정", "고양이 사료 추천해줘", "이사 준비", RuleEchoToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := echoFor(c.mem, c.user).Check(c.q); got != c.want {
				t.Fatalf("Check(%q) = %q, want %q", c.q, got, c.want)
			}
		})
	}
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
