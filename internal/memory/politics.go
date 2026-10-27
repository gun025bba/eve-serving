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
	partyNames      = `더불어\s?민주당|더불어\s?민주연합|더민주|민주당|국민의\s?힘|국민의\s?미래|국힘|국짐|정의당|조국혁신당|조국당|조혁당|개혁신당|개혁당|진보당|국민의당|녹색당|기본소득당|새로운미래당?|새로운\s미래당|새미래당?|자유통일당|우리공화당|노동당|사회민주당|사민당|녹색정의당|새누리당|한나라당|자유한국당|미래통합당`
	politicianNames = `이재명|윤석열|한동훈|조국|이준석|김문수|안철수|홍준표|문재인|박근혜|이낙연|오세훈|김동연|심상정|트럼프|바이든|해리스|trump|biden|harris`
	// a single-syllable surname directly before 후보 ('윤 후보 찍었어').
	surnameCandidate = `[김이박최정강조윤장임한오서신권황안송류홍전고문양손배백허남심노하곽성차주우구민유나진지엄채원천방공현함변염여추도소석선설마길연위표명기반왕금옥육인맹제모남궁]\s?후보`

	// short, ambiguous abbreviations: only with a vote verb directly after
	// (no words in between: '민주 방식으로 뽑았어' is not a party vote), never with
	// 지지/응원/후원 ('민주주의 지지해', '민주 시민 응원해' are not parties).
	shortPartyNames = `민주|더민`

	// vote-verb dictionary: support verbs; endings make them statements (not 지지율, 지지대, 지지층, 찍힌).
	supportVerbs = `(지지|찍|뽑)(었|았|어|음|을|고|겠|해|함|하|했|합|한다|하는|$|자\s?(야|임|이|예|라|였|입|$))` +
		`|` + ballotVerbs + `|밀어\s?(줬|줌|줄|준|주었|줘|주고|주는)|팬\s?(이야|임|이에요|입니다|이다|이라|이거든|$)|후원\s?(해|하|함|했)|응원\s?(해|하|함|했)` +
		`|당원\s?(이|임|으로)`

	// explicit public-election keywords.
	publicKeywords = `대통령\s?선거|국회의원\s?선거|지방\s?선거|재보궐\s?선거|재보궐|재보선|보궐\s?선거|보궐|대선|총선|지선|국민\s?투표|사전\s?투표|투표소|기호|election` +
		`|(시장|도지사|지사|구청장|군수|교육감|시의원|구의원|도의원|군의원)\s?선거`
	// bare vote words; public only when not qualified by a preceding noun.
	bareVote = `투표용지|투표|선거`
	// tokens before a bare vote word that do not qualify it (time words,
	// determiners, verb connectives, particles).
	// person subjects, time words and connectives that leave a bare
	// 투표/선거 unqualified (public). Any other preceding noun makes it
	// non-public by default (backlog: default-public needs PM sign-off).
	personSubjects = `(나|저|난|전|내|제|우리|저희|우리\s?가족|가족|엄마|어머니|아빠|아버지|부모님|남편|아내|와이프|신랑|부인|아들|딸|자식|애|형|오빠|누나|언니|동생|남동생|여동생|할머니|할아버지|시아버지|시어머니|시부모님|시댁|장인|장모|장인어른|장모님|처가|처남|처제|형부|제부|매형|올케|며느리|사위|손자|손녀|증조할머니|증조할아버지|외할머니|외할아버지|삼촌|이모|고모|외삼촌|사촌|조카|친척|남친|여친|남자친구|여자친구|애인|친구|동료|상사|선배|후배|옆집|이웃)`
	unqualifiers   = `^` + personSubjects + `(가|이|는|은|도|랑|이랑|하고|도요)?$` +
		`|^(지난|지난주|지난달|지난해|지지난|이번|다음|저번|그|그때|올해|작년|내년|어제|오늘|아까|이번엔|지난번|저번에|이번에)$|(하고|나고|가고|오고|끝나고|하면|해서|나서|가서|와서|하며|도|랑|에|(보|먹|마치|듣|놀|자|씻|끝내|끝나|마시|사|만나|일어나)고|후|전에|다음에)$`
	// known non-public qualifiers: a bare 투표/선거 right after one of these
	// nouns (optionally + 에서/의/별/내) is not a public election. Anything
	// else (pronouns, kinship, time words, verbs) leaves it public.
	nonPublicQualifiers = `^(반장|부반장|반|반별|학급|학교|학년|학생회|학생회장|전교|전교회장|동아리|동아리장|과대|과대표|회장|부회장|위원장|대표|팀|팀장|사내|회사|부서|직장|노조|조합|아파트|입주자|동대표|관리단|주민|오디션|아이돌|인기|팬|팬클럽|mvp|메뉴|점심|저녁|회식|장소|여행지|여행|온라인|게임|영화|노래|콘테스트|대회|축제|공연|반티|티|주차|이름|강아지|고양이|반려견|로고|디자인|안건|회의|가족회의|투표함|설문|교회|성당|절|집사|구역|셀|친구들|친구|동창|동호회|학원|유튜브|트위치|인스타|방송|라이브|채팅|채팅방|모임|단톡|단톡방|카톡|커뮤니티|카페|길드|클랜|리그|시상식|어워드|순위)(에서|의|별|내|용|끼리|끼리서)?$`
	// non-public votes named right before the vote word (kept for clarity;
	// also caught by the qualifier rule).
	voteExcludes = `(반장|동아리|회의|오디션|인기|동\s?대표|아파트|입주자|학생회|반\s?대표|팀\s?대표|학교|노조)(장|에서|의)?\s?(회장|대표|반장|위원장)?\s?(선거|투표)`
	// photo/video objects: '사진 찍었어' is not a vote.
	photoObjects = `(사진|영상|동영상|포스터|셀카|화보|광고|프로필)\s?(을|를|도|만)?\s?(\d+\s?번\s?)?(찍|뽑)`
	// vote-verb dictionary for the vote-number rule (beyond 찍/뽑 + ending).
	// Single source for every rule family (party, vote number, 국민투표).
	ballotVerbs = `(한\s?)?표\s?(를)?\s?(던졌|던짐|던지고|던질|줬|줌|주고|준|줄|주었|행사\s?(했|함|하고))|기표\s?(했|함|하고|할)|투표\s?(했|함|하고|할)` +
		// '지지' statements and a bare '한 표' count as vote verbs next to a
		// party, 기호, 국민투표 or public-election word.
		`|지지\s?(해|하|함|했|합|한다|할)|한\s?표($|\s|를|요|입니다|임)`
	voteParticles = `(에|으로|로|한테|에게|을|를|에서)`
	voteEndings   = `(었|았|음|을|고|겠|다|어( |$|요)|는)`
)

var (
	voteVerbs = `(찍|뽑)` + voteEndings + `|` + ballotVerbs

	partyStmtRe  = regexp.MustCompile(`(^|\s)(` + partyNames + `|` + politicianNames + `|` + surnameCandidate + `)\S{0,5}( \S{1,8}){0,3}? ?(` + supportVerbs + `)`)
	shortPartyRe = regexp.MustCompile(`(^|\s)(` + shortPartyNames + `)(에|을|를|한테|에게|은|는|도|만|쪽)? ?(` + voteVerbs + `)`)

	// stance statements without a vote number (moved from the old category
	// regex so politics lives in one place).
	stanceRe = regexp.MustCompile(`(^|\s)(나|저)\s?(는|도)?\s?(진보|보수)\s?(야|예요|이에요|입니다|주의자|성향)` +
		`|(지지|지지하는)\s?정당|정당\s?(지지\s?(해|하|함|했)|가입\s?(했|함|해\s?있|돼\s?있|되어\s?있)|당원\s?(이|임))|(^|\s)당원\s?(이야|이에요|입니다|이다|이라|임|이거든)|정치\s?성향|political (view|party|affiliation)|voted for` +
		`|(진보|보수|좌파|우파)\s?(쪽|측|성향)?\s?(의\s?)?(후보|정당|당)\s?(을|를|만)?\s?(찍|뽑|지지\s?(해|하|함|했|합|한))` +
		`|\S+\s?(국회의원|대통령|도지사|구청장|군수|교육감)\s?(을|를)?\s?(지지\s?(해|하|함|했|합|한다)|찍었|뽑았)`)
	photoRe = regexp.MustCompile(photoObjects)
	// 지지자/지지층 directly after a party or politician, as a statement
	// ('난 조국혁신당 지지층이야'); '민주당 지지자 많네' is not.
	partyBaseRe = regexp.MustCompile(`(^|\s)(` + partyNames + `|` + politicianNames + `)\s?(의\s?)?지지(자|층)\s?(이야|이에요|예요|입니다|이다|이라|이거든|임|이었|였|이고|이지|이셔|이세요|이시)`)
	// party membership glued to the name: '민주당원이야', '진보당원임'.
	partyMemberRe = regexp.MustCompile(`(^|\s)(` + partyNames + `)\s?(당)?원\s?(이야|이에요|이예요|입니다|이다|이라|이거든|이고|이었|임|이|으로|이\s?됐|됐|되었|됨|돼서|가입\s?(했|함|해서|해\s?있|돼|되었|됨|완료))`)
	// joining / leaving a party: '국힘 입당했어', '민주당에 가입했어', '탈당했어'.
	partyJoinRe   = regexp.MustCompile(`(^|\s)(` + partyNames + `)\s?(에|을|를|에서)?\s?((입당|탈당|가입|탈퇴)\s?(했|함|해\s?있|한\s?지|하고\s?왔|했다|했음|해버렸)|들어갔|들어감|나왔어|나왔음)`)
	partyPrefixRe = regexp.MustCompile(`^(` + partyNames + `)`)

	publicRe   = regexp.MustCompile(publicKeywords)
	bareVoteRe = regexp.MustCompile(bareVote)
	qualRe     = regexp.MustCompile(nonPublicQualifiers)
	unqualRe   = regexp.MustCompile(unqualifiers)
	excludeRe  = regexp.MustCompile(voteExcludes)

	// after a vote context: N번 (+ 에/으로/로) (+ 칸) + 찍/뽑 + ending, or
	// N번에 했다, or 찬성/반대 + 찍/뽑/했.
	voteTailRe = regexp.MustCompile(`^\S{0,3}( \S{1,6}){0,2}? ?(` +
		// N번 (+ 후보/정당/당) (+ particle) + vote verb
		`\d+ ?번 ?(후보|정당|당)? ?` + voteParticles + `? ?(` + voteVerbs + `)` +
		`|\d+ ?번 ?(에|으로|로) ?했|\d+ ?번 ?(에|으로|로)? ?도장 ?찍` + voteEndings +
		`|후보 ?` + voteParticles + `? ?(` + voteVerbs + `|지지\s?(해|하|함|했))` +
		// 찬성/반대 (+ particle) + vote verb
		`|(찬성|반대) ?` + voteParticles + `? ?(` + voteVerbs + `|했))`)
	// N번에 도장 찍었어 with nothing before it (a ballot stamp, not a document).
	dojangRe = regexp.MustCompile(`^\d+ ?번 ?(에|으로|로)? ?도장 ?찍` + voteEndings)

	// first-person past statements stay disclosures even as a question.
	pastStmtRe = regexp.MustCompile(`(찍었|뽑았|했어|했음|했다)`)
)

// relClauseRe: a relative clause describing the vote ('영화 고르는 투표',
// '이름 정하는 투표') marks it as non-public.
var relClauseRe = regexp.MustCompile(`(고르|정하|뽑|결정하|선택하|하)는$`)

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
			// glued to the previous word ('인기투표') or after a known
			// non-public qualifier noun ('학교 투표', 'MVP 투표').
			if loc[0] == len(before) {
				continue
			}
			toks := strings.Fields(before)
			last := toks[len(toks)-1]
			// Only the word right before the vote decides: a non-public noun
			// ('노조 선거', '교회 투표', '친구들끼리 투표') or a relative clause
			// ('고르는 투표') keeps it non-public; a person subject, time word or
			// connective ('나 투표', '영화 보고 투표') makes it public; any other
			// noun stays non-public by default (PM; backlog).
			if qualRe.MatchString(last) || relClauseRe.MatchString(last) || !unqualRe.MatchString(last) {
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
// TODO(architect): choseong (ㅁㅈㄷ), English-mixed forms ('I voted for
// 민주당') and inverted order ('찍었어 민주당', '지지하는 건 민주당') are out of scope for this round.
func politicsStructural(folded, original string) bool {
	folded = photoRe.ReplaceAllString(candSiteRe.ReplaceAllString(folded, " _ "), " _ ")
	// exemptions are judged once on the spaced original.
	base := photoRe.ReplaceAllString(candSiteRe.ReplaceAllString(foldPunct(strings.ToLower(original)), " _ "), " _ ")
	fp := firstPerson(base)
	// non-public organisation context with no major political word: weak
	// anchors (사전투표, 후보, N번 후보, 기호, 지지자) do not anchor.
	if !majorAnchorRe.MatchString(base) && !majorAnchorRe.MatchString(folded) && weakModified(base) {
		return false
	}
	news := !fp && isNewsReport(base)
	if !news && (generalPolitical(folded) || generalPolitical(joinSyllables(folded))) {
		return true
	}
	if (strings.Contains(folded, " ") || !strings.Contains(base, " ")) && defaultPolitical(folded, base, original, fp) {
		return true
	}
	stmt := actorRe.ReplaceAllString(folded, "$1_ ")
	if news {
		stmt = actorTokens(stmt)
		if !strings.Contains(folded, " ") {
			stmt = actorTokens(base) // spaceless variant has no token boundaries
		}
	}
	if partyStmtRe.MatchString(stmt) || partyStmtRe.MatchString(joinSyllables(stmt)) || partyMemberRe.MatchString(stmt) || partyJoinRe.MatchString(stmt) || partyBaseRe.MatchString(folded) || shortPartyRe.MatchString(folded) || stanceRe.MatchString(folded) {
		return true
	}
	if strings.HasSuffix(strings.TrimSpace(original), "?") && !pastStmtRe.MatchString(folded) && !publicRe.MatchString(folded) {
		return false
	}
	return voteInContext(folded)
}

// General rule (QA r16): a political anchor (party name incl.
// abbreviations, politician, 기호 N, or a public-election word) plus a
// vote / support / membership verb anywhere in the same sentence blocks,
// unless the sentence is a request for information.
const (
	// conjugation tail shared by verb stems: past, present, future,
	// honorific and reported forms (-었대, -는대, -신다, -겠).
	conj  = `(었|았|어|음|는다|는대|는데|겠|으셨|으신|으시|셨|신다|신대|을\s?(거|게|래|겁니다)|ㄹ\s?게)`
	hconj = `(했|함|한다|한대|합니다|하겠|하셨|하신|하시|할\s?(거|게|래|겁니다)|하고\s?(왔|있))`

	voteClass    = `(찍|뽑)` + conj + `|(투표|기표)\s?` + hconj + `|(찬성|반대)\s?표|한\s?표|표\s?(를)?\s?(줬|줌|준다|준대|주겠|주셨|드렸|던졌|던짐|던진다|행사)`
	supportClass = `지지\s?(` + hconj + `|해|해요|하는\s?(정당|당|후보|사람)|자\s?(야|임|이|예|라|였|입))` +
		`|밀어\s?(줬|줌|준다|준대|주겠|주셨|주고|주는|드렸)|밀었|미셨|(응원|지지)하는\s?(정당|당|후보)`
	memberClass = `(권리\s?)?당원\s?(이|임|으로|됐|되었|되셨|이\s?(됐|되)|가입)|(입당|탈당|탈퇴)\s?(` + hconj + `|해\s?있)|가입\s?(` + hconj + `|해\s?있|돼\s?있)|들어갔|들어가셨`
)

var (
	anchorRe = regexp.MustCompile(`(^|\s)(` + partyNames + `|` + politicianNames + `)|기호\s?\d|` + publicKeywords)
	// 'N번 후보/정당' is an anchor only outside a non-public context
	// ('동아리 2번 후보한테 투표했어' stays allowed).
	numCandRe   = regexp.MustCompile(`\d+\s?번\s?(후보|정당)`)
	verbClassRe = regexp.MustCompile(voteClass + `|` + supportClass + `|` + memberClass)
	// requests for information stay allowed ('입당 절차 알려줘', '뭐야?').
	infoRequestRe = regexp.MustCompile(`(알려|요약|정리|찾아|비교|설명|보여|번역)\s?(해\s?)?(줘|주세요|줄래|줄 수)|뭐야|뭐예요|무엇|어떻게|절차|방법|조건|언제|어디|누구|몇\s?(명|석|표|퍼센트)|기사|뉴스|일정`)
)

func generalPolitical(f string) bool {
	if !verbClassRe.MatchString(f) || infoRequestRe.MatchString(f) {
		return false
	}
	if anchorRe.MatchString(f) {
		return true
	}
	// judged on spaced variants only: the space-stripped variant cannot
	// show a qualifier token.
	return numCandRe.MatchString(f) && strings.Contains(f, " ") && !hasNonPublicQualifier(f)
}

// Default rule (QA r17): any political anchor blocks unless the sentence
// is an information request / question, a third-person news report, or a
// neutral topical mention, each only without a first-person or kin subject.
var (
	firstPersonRe  = regexp.MustCompile(`^(내|제|내가|제가|제게|내게)$|^` + strings.Replace(personSubjects, "(나|저|난|전|내|제|", "(나|저|난|전|", 1) + `(가|이|는|은|도|랑|이랑|하고|도요|네|의|께서|님|님이|님은|님도)?$`)
	newsSubjectRe  = regexp.MustCompile(`^((` + partyNames + `|` + politicianNames + `)(이|가|은|는|도|측|측은|측이)|(의원|의원들|대표|후보|후보들|정당|대통령|장관|위원장|대변인|정부|여당|야당|지도부|최고위원|원내대표|당선인|당선자|국회|청와대|대통령실|선관위|측)(이|가|들이|측이))$`)
	newsEndingRe   = regexp.MustCompile(`(대|래|대요|래요|답니다|랍니다)$`)
	questionWordRe = regexp.MustCompile(`(^|\s)(뭐|뭐야|뭐예요|뭔데|무슨|무엇|어떻게|어떤|어디|어디서|어디야|언제|언제야|누구|누가|누구야|몇|왜|얼마)`)
	requestEndRe   = regexp.MustCompile(`((알려|요약|정리|찾아|비교|설명|보여|번역|추천|분석|검색)\s?(해\s?)?(줘|주세요|줄래|줄\s?수\s?있어|주라|봐|봐줘)|해\s?줘|해\s?주세요|줘|주세요)$`)
	topicNounRe    = regexp.MustCompile(`(결과|토론|토론회|영상|공약|일정|포스터|디자인|로고|당사|의석|의석수|지지율|지지층|지지자|대변인|브리핑|뉴스|기사|여론조사|제도|역사|홈페이지|링크|주소|전화번호|창당|합당|분석|출구조사|개표|현수막|유세|선거법|후보\s?등록|공보물|사전투표율|투표율|날짜|휴일|공휴일|위치|공부|수업|과제|사진|동영상|짤|굿즈|나이|학력|경력|고향)`)
)

func firstPerson(f string) bool {
	for _, t := range strings.Fields(f) {
		if t == "나이" || t == "나이가" {
			continue
		}
		if firstPersonRe.MatchString(t) {
			return true
		}
	}
	return false
}

// strongAnchorRe: party, politician or public-election word (not 기호 / N번).
var strongAnchorRe = regexp.MustCompile(`(^|\s)(` + partyNames + `|` + politicianNames + `)|` + strings.Replace(publicKeywords, "|기호|", "|", 1))

func hasAnchor(f string) bool {
	if strongAnchorRe.MatchString(f) || regexp.MustCompile(`기호\s?\d`).MatchString(f) {
		return true
	}
	return numCandRe.MatchString(f) && !hasNonPublicQualifier(f)
}

// isNewsReport: a single-clause reported-speech sentence (…대/…래) whose
// subject is a party, politician or office, with no evaluative word.
func isNewsReport(f string) bool {
	if len(clauses(f)) > 1 || !newsEndingRe.MatchString(strings.TrimSpace(f)) || evaluativeRe.MatchString(f) {
		return false
	}
	if newsEventRe.MatchString(strings.TrimSpace(f)) || statRe.MatchString(f) {
		return true
	}
	for _, t := range strings.Fields(f) {
		if newsSubjectRe.MatchString(t) {
			return true
		}
	}
	return false
}

// clauses splits on connective endings (-는데/-은데/-인데/-고/-지만/
// -니까/-어서) and sentence breaks; every clause but the last is judged
// as a statement.
func clauses(f string) []string {
	var out []string
	for _, c := range clauseSplitRe.Split(f, -1) {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	return out
}

func statementClause(c string) bool {
	return voteStemRe.MatchString(c) || verbClassRe.MatchString(c) || evaluativeRe.MatchString(c) || pastStmtRe.MatchString(c)
}

func defaultPolitical(f, base, original string, fp bool) bool {
	if !hasAnchor(base) {
		return false
	}
	if hasNonPublicQualifier(base) && !strongAnchorRe.MatchString(base) {
		return false
	}
	// evaluative words and agreement-seeking endings ('최고지?',
	// '좋지 않아?', '-잖아') are stances, never exempt.
	if evaluatesAnchor(base) {
		return true
	}
	cs := clauses(base)
	for _, c := range cs[:len(cs)-1] {
		if statementClause(c) || firstPerson(c) {
			return true
		}
	}
	last := cs[len(cs)-1]
	question := strings.HasSuffix(strings.TrimSpace(original), "?")
	info := requestEndRe.MatchString(strings.TrimSpace(last)) || questionWordRe.MatchString(last)
	// info request/question: allowed even with a first-person word when the
	// clause has no vote/support/membership verb.
	// Med D: a first person plus a vote/support verb anywhere removes the
	// question exemption; a chosen number or evaluative word does too.
	if info && !(fp && (verbClassRe.MatchString(base) || voteStemRe.MatchString(base) || numChoiceRe.MatchString(base))) {
		return false
	}
	if fp {
		return true
	}
	if isNewsReport(base) {
		return false
	}
	if question && !verbClassRe.MatchString(last) {
		return false
	}
	if (topicNounRe.MatchString(base) || photoRe.MatchString(foldPunct(strings.ToLower(original)))) && !verbClassRe.MatchString(base) {
		return false
	}
	return true
}

var (
	evaluativeRe  = regexp.MustCompile(`최고|최악|좋다|좋아|좋지|좋네|좋음|좋은|좋겠|꼴좋|극혐|싫|별로|짱|멋지|멋있|괜찮|쓰레기|ㅋㅋ|ㅎㅎ|화이팅|파이팅|가즈아|사랑|훌륭|대단|잘했|잘한다|잘하|못했|못한다|망해|망했|망한|혐오|역겹|한심|속시원|통쾌|쌤통|꼬시|고소하|미워|미친|개념|답없|답이 없|믿음직|든든|실망|최애|호감|비호감|맘에\s?(들|듦|든)|마음에\s?(들|듦|든)|낫|답\s?없|옳|틀리|틀렸|잘한|해야\s?한|돼야\s?한|되어야\s?한`)
	statRe        = regexp.MustCompile(`(역대|사상|역사상)\s?(최고|최저)(치)?|(투표율|득표율|지지율|참여율|개표율|\d+\s?(%|퍼센트))\s?(이|가|은|는|도)?\s?(최고|최저)(치)?|최고치|최저치`)
	voteStemRe    = regexp.MustCompile(`(찍|뽑)($|을|으|으려|으며|었|았|어|아|고|겠|는|음)|지지\s?(하|해|했|할)|밀어\s?(줄|주|줬)|기표`)
	newsEventRe   = regexp.MustCompile(`(됐|되었|선출|부결|가결|통과|출범|확정|사퇴|해산|발표)\S*(대|래|대요|래요)$`)
	numChoiceRe   = regexp.MustCompile(`\d+\s?번`)
	agreeEndRe    = regexp.MustCompile(`(지|죠|잖아|잖아요|잖냐|지 않아|지 않냐|지 않아요|지 않니)\s?$`)
	clauseSplitRe = regexp.MustCompile(`(는데|은데|인데|던데|지만|니까|으니|어서|아서|고|으며|며|면서|다가)(\s|$)|[.!?\n]+`)
	actorRe       = regexp.MustCompile(`(^|\s)(` + partyNames + `|` + politicianNames + `)(이|가)\s`)
	candSiteRe    = regexp.MustCompile(`후보지(\s|$|[가는를은에의도로])|후보\s?(장소|부지|지역|날짜|곡|작)`)
	orgCompanyRe  = regexp.MustCompile(`^(사람|사람들|친구|친구들|애들|형|형들|선배|후배|동기|멤버)`)
	evalTargetRe  = regexp.MustCompile(`(포스터|디자인|로고|현수막|당사|카페|식당|맛집|건물|근처|앞|옆|노래|영상|사진|굿즈|홈페이지|날씨)`)
	majorAnchorRe = regexp.MustCompile(`(^|\s)(` + partyNames + `|` + politicianNames + `)|대통령\s?선거|국회의원\s?선거|지방\s?선거|재보궐|재보선|보궐|대선|총선|지선|국민\s?투표|election|(시장|도지사|지사|구청장|군수|교육감|시의원|구의원|도의원|군의원)\s?선거`)
	orgWordRe     = regexp.MustCompile(`^(학생회|학생회장|동아리|오디션|경로당|노인회|반장|부반장|학급|전교|과대|과대표|아파트|입주자|입주자대표|동대표|관리단|동호회|회사|사내|부서|학교|학년|교회|성당|부녀회|반상회|팬클럽|아이돌|조합|협회|클럽|길드|커뮤니티|카페|모임)(의|별|내|끼리|회장|장|선거|투표|후보|대표|회의|에서의)?$`)
)

// weakModified (QA r19): with only weak anchors (사전투표, 투표소, 기호,
// 후보, 지지자, N번, bare 투표/선거/회장/대표), the sentence is non-public
// when every weak word is modified by a noun phrase: a glued noun prefix
// ('산악회장', '팬카페투표'), or the nearest preceding non-number token is
// a noun (bare, genitive ~의, event ~에서, org suffix). Person subjects,
// time words, connectives and verb forms are not modifiers.
var (
	weakWordRe   = regexp.MustCompile(`사전\s?투표|투표소|투표|선거|회장|대표|후보|기호|지지자|당선`)
	numTokRe     = regexp.MustCompile(`^(\d+\s?번|기호|사전|번)\S{0,4}$`)
	verbFormRe   = regexp.MustCompile(`(다가|가지고|려고|러|데|고|서|면|는데|은데|었어|았어|했어|했고|어|아|요|다|지|게|며|니|자|던|는|은|을|ㄴ)$`)
	nounParticle = regexp.MustCompile(`(의|에서|에서의|에|별|내|용|끼리|들|들의)$`)
	locWordRe    = regexp.MustCompile(`^(근처|앞|옆|뒤|건너편|주변|부근|안|밖|쪽|가는|길|아침|점심|저녁|밤|새벽|오전|오후|주말|평일|출근|퇴근|출근길|퇴근길)(에|에서|의|때)?$`)
	orgSuffixRe  = regexp.MustCompile(`(회|단|부|회사|조합|카페|모임|지부|연합|클럽|동호회|협회|위원회|센터|교회|학교|아파트)(의|에서|에|별|내)?$`)
)

func weakModified(f string) bool {
	ts := strings.Fields(f)
	found := false
	for i, t := range ts {
		loc := weakWordRe.FindStringIndex(t)
		if loc == nil {
			continue
		}
		found = true
		if loc[0] > 0 && !strings.HasPrefix(t, "사전") && !numTokRe.MatchString(t[:loc[0]]) {
			continue // glued noun prefix
		}
		j := i - 1
		for j >= 0 && numTokRe.MatchString(ts[j]) {
			j--
		}
		if j < 0 || !nounModifier(ts[j]) {
			return false
		}
	}
	return found
}

func nounModifier(t string) bool {
	if len([]rune(t)) == 1 {
		return false // a single syllable is a surname ('윤 후보'), not a group
	}
	if locWordRe.MatchString(t) || unqualRe.MatchString(t) || firstPersonRe.MatchString(t) {
		return false
	}
	if orgSuffixRe.MatchString(t) || qualRe.MatchString(t) {
		return true
	}
	if nounParticle.MatchString(t) {
		t = nounParticle.ReplaceAllString(t, "")
		return t != "" && !unqualRe.MatchString(t) && !firstPersonRe.MatchString(t)
	}
	return !verbFormRe.MatchString(t)
}

// evaluatesAnchor: any evaluative word with an anchor is a stance (PM:
// '민주당 포스터 최고' blocks). Statistic superlatives ('투표율 역대
// 최고', '득표율 최저', '%') are facts and are stripped first.
func evaluatesAnchor(f string) bool {
	return evaluativeRe.MatchString(statRe.ReplaceAllString(f, " _ "))
}

func hasNonPublicQualifier(f string) bool {
	for _, t := range strings.Fields(f) {
		if qualRe.MatchString(t) || excludeRe.MatchString(t) {
			return true
		}
	}
	return false
}

// joinSyllables re-joins runs of 3+ single-rune tokens so '민 주 당 지지해'
// (from 민.주.당 / 민-주-당 / 민_주_당) reads '민주당 지지해'.
func joinSyllables(f string) string {
	toks := strings.Fields(f)
	var out []string
	var run []string
	flush := func() {
		if len(run) >= 3 {
			// split off leading standalone syllables ('나' in '나 민 주 당')
			// so the party name starts its own token.
			k := 0
			for ; k <= len(run)-2; k++ {
				if partyPrefixRe.MatchString(strings.Join(run[k:], "")) {
					break
				}
			}
			if k > len(run)-2 {
				k = 0
			}
			out = append(out, run[:k]...)
			out = append(out, strings.Join(run[k:], ""))
		} else {
			out = append(out, run...)
		}
		run = nil
	}
	for _, t := range toks {
		if len([]rune(t)) == 1 {
			run = append(run, t)
			continue
		}
		flush()
		out = append(out, t)
	}
	flush()
	return strings.Join(out, " ")
}

// actorTokens blanks third-person actor subjects (party/office + 이/가) so
// statement rules do not read a news actor as the user's own stance.
func actorTokens(f string) string {
	ts := strings.Fields(f)
	for i, t := range ts {
		if newsSubjectRe.MatchString(t) {
			ts[i] = "_"
		}
	}
	return strings.Join(ts, " ")
}
