package memory

import (
	"regexp"
	"strings"
	"unicode"
)

// Structural political-disclosure rules (architect policy for #53, QA round
// 11). They run on a "folded" form of each sensitive variant (already NFKC,
// lower-cased and jamo-composed): every punctuation, symbol and emoji rune
// becomes a space and runs of spaces collapse to one, so '정의당·지지해',
// '"개혁신당" 지지해' and '국민의힘 👍 지지' all read as '<party> 지지…'.
//
// Two rule shapes:
//  1. party / candidate + support verb within ~3 words of the same clause.
//  2. vote number (N번, 찬성/반대) + vote verb, only in a public-election
//     context: an explicit public keyword (대선, 총선, 지방선거, 재보궐,
//     국민투표, 사전투표, 기호, an office + 선거 …), or a bare 투표/선거 that
//     is not qualified by a preceding noun ('학교 투표', 'MVP 투표',
//     '노조 위원장 선거' are not public; '지난 선거에', '투표는' are
//     ambiguous and treated as public).

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

// Rule lists, kept together so they can move to config later.
const (
	// current and recent parties, including common abbreviations.
	partyNames      = `더불어\s?민주당|더불어\s?민주연합|더민주|민주당|국민의\s?힘|국민의\s?미래|국힘|국짐|정의당|조국혁신당|조국당|개혁신당|개혁당|진보당|국민의당|녹색당|기본소득당|새로운\s?미래|새미래|자유통일당|우리공화당|노동당|사회민주당|녹색정의당|새누리당|한나라당|자유한국당|미래통합당`
	politicianNames = `이재명|윤석열|한동훈|조국|이준석|김문수|안철수|홍준표|문재인|박근혜|이낙연|오세훈|김동연|심상정|트럼프|바이든|해리스|trump|biden|harris`
	// a single-syllable surname directly before 후보 ('윤 후보 찍었어').
	surnameCandidate = `[김이박최정강조윤장임한오서신권황안송류홍전고문양손배백허남심노하곽성차주우구민유나진지엄채원천방공현함변염여추도소석선설마길연위표명기반왕금옥육인맹제모남궁]\s?후보`

	// vote-verb dictionary: support verbs; endings make them statements (not 지지율, 지지대, 지지층, 찍힌).
	supportVerbs = `(지지|찍|뽑)(었|았|어|음|을|고|겠|해|함|하|했|합|한다|하는|$|자\s?(야|임|이|예|라|였|입|$))` +
		`|표\s?(줬|줌|줄|준|주었|줘|던졌|던짐)|밀어\s?(줬|줌|줄|준|주었|줘|주고|주는)|팬\s?(이야|임|이에요|입니다|이다|이라|이거든|$)|후원\s?(해|하|함|했)|응원\s?(해|하|함|했)` +
		`|당원\s?(이|임|으로)`

	// explicit public-election keywords.
	publicKeywords = `대통령\s?선거|국회의원\s?선거|지방\s?선거|재보궐\s?선거|재보궐|재보선|보궐\s?선거|보궐|대선|총선|지선|국민\s?투표|사전\s?투표|투표소|기호` +
		`|(시장|도지사|지사|구청장|군수|교육감|시의원|구의원|도의원|군의원)\s?선거`
	// bare vote words; public only when not qualified by a preceding noun.
	bareVote = `투표용지|투표|선거`
	// tokens before a bare vote word that do not qualify it (time words,
	// determiners, verb connectives, particles).
	unqualifiers = `^(나|저|난|전|내가|제가|나도|저도|나는|저는|우리|저희|우리가|저희가|가족|우리가족|엄마|아빠|엄마가|아빠가|남편|아내|와이프|남편이|아내가|부모님|부모님이|친구|친구가|동생|형|누나|언니|오빠)$|(지난|이번|다음|저번|그|그때|올해|작년|내년|어제|오늘|아까|이번엔|지난번|저번에|이번에)$|(하고|나고|가고|오고|끝나고|하면|해서|나서|가서|와서|하며|한|는|은|도|랑|에|고)$`
	// non-public votes named right before the vote word (kept for clarity;
	// also caught by the qualifier rule).
	voteExcludes = `(반장|동아리|회의|오디션|인기|동\s?대표|아파트|입주자|학생회|반\s?대표|팀\s?대표|학교|노조)(장|에서|의)?\s?(회장|대표|반장|위원장)?\s?(선거|투표)`
	// photo/video objects: '사진 찍었어' is not a vote.
	photoObjects = `(사진|영상|동영상|포스터|셀카|화보|광고|프로필)\s?(을|를|도|만)?\s?(\d+\s?번\s?)?(찍|뽑)`
	voteEndings  = `(었|았|음|을|고|겠|다|어( |$|요)|는)`
)

var (
	partyStmtRe = regexp.MustCompile(`(^|\s)(` + partyNames + `|` + politicianNames + `|` + surnameCandidate + `)\S{0,5}( \S{1,8}){0,3}? ?(` + supportVerbs + `)`)

	// stance statements without a vote number (moved from the old category
	// regex so politics lives in one place).
	stanceRe = regexp.MustCompile(`(^|\s)(나|저)\s?(는|도)?\s?(진보|보수)\s?(야|예요|이에요|입니다|주의자|성향)` +
		`|(지지|지지하는)\s?정당|정당\s?(지지\s?(해|하|함|했)|가입\s?(했|함|해\s?있|돼\s?있|되어\s?있)|당원\s?(이|임))|(^|\s)당원\s?(이야|이에요|입니다|이다|이라|임|이거든)|정치\s?성향|political (view|party|affiliation)|voted for` +
		`|(진보|보수|좌파|우파)\s?(쪽|측|성향)?\s?(의\s?)?(후보|정당|당)\s?(을|를|만)?\s?(찍|뽑|지지)` +
		`|\S+\s?(국회의원|대통령|도지사|구청장|군수|교육감)\s?(을|를)?\s?(지지\s?(해|하|함|했|합|한다)|찍었|뽑았)`)
	photoRe = regexp.MustCompile(photoObjects)

	publicRe   = regexp.MustCompile(publicKeywords)
	bareVoteRe = regexp.MustCompile(bareVote)
	unqualRe   = regexp.MustCompile(unqualifiers)
	excludeRe  = regexp.MustCompile(voteExcludes)

	// after a vote context: N번 (+ 에/으로/로) (+ 칸) + 찍/뽑 + ending, or
	// N번에 했다, or 찬성/반대 + 찍/뽑/했.
	voteTailRe = regexp.MustCompile(`^\S{0,3}( \S{1,6}){0,2}? ?(\d+ ?번 ?(에|으로|로)? ?((찍|뽑)` + voteEndings + `)` +
		`|\d+ ?번 ?(에|으로|로) ?했|\d+ ?번 ?(에|으로|로)? ?도장 ?찍` + voteEndings + `|(\d+ ?번 ?)?후보\S{0,3} ?((찍|뽑)` + voteEndings + `|지지\s?(해|하|함|했))|(찬성|반대)\S{0,2} ?((찍|뽑)` + voteEndings + `|했|투표\s?했))`)
	// N번에 도장 찍었어 with nothing before it (a ballot stamp, not a document).
	dojangRe = regexp.MustCompile(`^\d+ ?번 ?(에|으로|로)? ?도장 ?찍` + voteEndings)

	// first-person past statements stay disclosures even as a question.
	pastStmtRe = regexp.MustCompile(`(찍었|뽑았|했어|했음|했다)`)
)

// voteInContext reports whether a vote-number disclosure follows a public
// (or unqualified bare) vote word.
func voteInContext(f string) bool {
	f = excludeRe.ReplaceAllString(f, " _ ")
	for _, loc := range publicRe.FindAllStringIndex(f, -1) {
		if voteTailRe.MatchString(strings.TrimLeft(f[loc[1]:], " ")) {
			return true
		}
	}
	for _, loc := range bareVoteRe.FindAllStringIndex(f, -1) {
		before := strings.TrimRight(f[:loc[0]], " ")
		if before != "" {
			// glued to the previous word ('인기투표') or qualified by a noun.
			if loc[0] == len(before) {
				continue
			}
			toks := strings.Fields(before)
			if !unqualRe.MatchString(toks[len(toks)-1]) {
				continue
			}
		}
		if voteTailRe.MatchString(strings.TrimLeft(f[loc[1]:], " ")) {
			return true
		}
	}
	return dojangRe.MatchString(f)
}

// politicsStructural reports whether a folded variant discloses a vote or a
// party preference. original is the raw text: a question is not a vote
// disclosure ('이 서류 2번에 도장 찍어야 해?') unless it is a first-person
// past statement ('사전투표 2번 찍었어?').
//
// TODO(architect): choseong (ㅁㅈㄷ) and English-mixed forms ('I voted for
// 민주당', 'election에서') are out of scope for this round.
func politicsStructural(folded, original string) bool {
	folded = photoRe.ReplaceAllString(folded, " _ ")
	if partyStmtRe.MatchString(folded) || partyStmtRe.MatchString(joinSyllables(folded)) || stanceRe.MatchString(folded) {
		return true
	}
	if strings.HasSuffix(strings.TrimSpace(original), "?") && !pastStmtRe.MatchString(folded) && !publicRe.MatchString(folded) {
		return false
	}
	return voteInContext(folded)
}

// joinSyllables re-joins runs of single-rune tokens so '민 주 당 지지해'
// (from 민.주.당 / 민-주-당 / 민_주_당) reads '민주당 지지해'.
func joinSyllables(f string) string {
	toks := strings.Fields(f)
	var out []string
	run := ""
	for _, t := range toks {
		if len([]rune(t)) == 1 {
			run += t
			continue
		}
		if run != "" {
			out = append(out, run)
			run = ""
		}
		out = append(out, t)
	}
	if run != "" {
		out = append(out, run)
	}
	return strings.Join(out, " ")
}
