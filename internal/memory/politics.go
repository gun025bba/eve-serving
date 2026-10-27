package memory

import (
	"regexp"
	"strings"
	"unicode"
)

// Structural political-disclosure rules (QA round 10). They run on a
// "folded" form of each sensitive variant (already NFKC, lower-cased and
// jamo-composed): every punctuation, symbol and emoji rune becomes a space
// and runs of spaces collapse to one, so '정의당·지지해', '"개혁신당" 지지해'
// and '국민의힘 👍 지지' all read as '<party> 지지…'.

// foldPunct maps anything that is not a letter, digit or combining mark to a
// space and collapses whitespace.
func foldPunct(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// Rule lists (architect policy for #53; kept together so they can move to
// config later). Public elections and 기호 N번 are political; class (반장),
// club (동아리), meeting (회의) and other non-public votes are not.
const (
	partyNames      = `더불어\s?민주당|민주당|국민의\s?힘|정의당|조국혁신당|개혁신당|진보당|국민의당|녹색당|기본소득당|새누리당|한나라당|자유한국당|미래통합당`
	politicianNames = `이재명|윤석열|한동훈|조국|이준석|김문수|안철수|홍준표|문재인|박근혜|이낙연|오세훈|김동연|심상정|트럼프|바이든|해리스|trump|biden|harris`
	// verb endings that make 지지/찍/뽑 a statement (not 지지율, 지지대, 찍힌).
	stmtEnding = `(었|았|어|음|을|고|겠|해|함|자|했|합|한다|하는|$)`
	// public-election / ballot contexts (plus 기호, 도장, generic 투표·선거).
	voteContexts = `투표용지|사전투표|국민투표|투표|도장|기호|대통령\s?선거|국회의원\s?선거|지방\s?선거|재보궐\s?선거|재보궐|재보선|보궐\s?선거|보궐|대선|총선|지선|선거`
	// non-public votes / non-election numbered actions: never political.
	voteExcludes = `반장|동아리|회의|오디션|인기\s?투표|주차|사진|가챠|시험|학생회|반\s?대표|팀\s?대표`
	voteEndings  = `(었|았|음|을|고|겠|다|어( |$|요))`
)

var (
	// party or politician name, then within ~8 characters 지지/찍/뽑 + ending.
	partyStmtRe = regexp.MustCompile(`(^|\s)(` + partyNames + `|` + politicianNames + `)\S{0,4}( \S{0,7})?? ?(지지|찍|뽑)` + stmtEnding)

	// election context + N번 (+ 에/으로/로) (+ 도장) + 찍/뽑/했 + ending.
	voteCtx      = `(` + voteContexts + `)`
	voteEnding   = voteEndings
	voteNumberRe = regexp.MustCompile(voteCtx + `\S{0,3}( \S{1,6}){0,2}? ?\d+ ?번 ?(에|으로|로)? ?(도장 ?)?((찍|뽑)` + voteEnding + `|했)` +
		// N번에 도장 찍었어 (number before the 도장 context)
		`|\d+ ?번 ?(에|으로|로)? ?도장 ?찍` + voteEnding)

	// non-election contexts where a numbered vote is not political (security Low).
	voteExcludeRe = regexp.MustCompile(voteExcludes)
)

// politicsStructural reports whether a folded variant discloses a vote or a
// party preference. original is the raw text (questions are not disclosures
// for the vote-number rule: '이 서류 2번에 도장 찍어야 해?').
func politicsStructural(folded, original string) bool {
	if partyStmtRe.MatchString(folded) {
		return true
	}
	if strings.HasSuffix(strings.TrimSpace(original), "?") || voteExcludeRe.MatchString(folded) {
		return false
	}
	return voteNumberRe.MatchString(folded)
}
