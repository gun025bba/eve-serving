package memory

import (
	"regexp"
	"strconv"
	"strings"
)

// Sensitive-category minimum deny list (memory.md §9.1). Items in these
// categories are never stored, even when the user asks: health and medical
// conditions, sexual orientation / sex life, religion, political views,
// criminal records, union membership, ethnicity, biometric and genetic data,
// personal finance and government ID numbers.
//
// Policy (one rule, enforced here only):
//   - Self statements in a sensitive category are REJECTED. There is no
//     "allow with a warning" path; the confirm card is never shown for them.
//   - Statements about a third party (가족·지인·동료 등 + a sensitive term)
//     are ALWAYS rejected, regardless of wording.
//   - Topic mentions (news, film, schedules, tax deadlines) are allowed.
//
// The list is keyword based (Korean + English); it is a floor, not a
// classifier. A hit refuses the write with the same secret_detected code,
// so the reason is never echoed (ui-api-contract §3.3). Adding or changing
// an entry is a security review item.
var sensitiveCategories = []struct {
	Name string
	Re   *regexp.Regexp
}{
	// Korean rules avoid bare substrings that occur inside common words
	// (게이트/게이밍/게이지, 이전과/전과목, 추수감사절, 아카이브): they need a
	// particle, ending or first-person/verb context. Rules also run on the
	// squashed copy, so no rule relies on whitespace alone.
	{"health", regexp.MustCompile(`(?i)진단(을|를)?\s*받|질환\s*(이|을|를|가)?\s*(있|앓|진단)|우울증|공황\s*장애|조현병|양극성\s*장애|항우울제|암\s*(투병|환자|진단|수술|이래|이에요|이야|이\s*있|에\s*걸)|에이즈|\bhiv\s*(양성|positive|감염|보균|환자|진단|\+)|\bhiv\s*(에\s*)?(걸|감염)|(우울|불안|불면)\S*\s*약\s*(을|를)?\s*(먹|복용)|성병\s*(에|이|을|를)?\s*(걸|감염|있|치료\s*받|진단)|장애\s*\d\s*(급|등급)|(지체|시각|청각|지적|정신|발달|뇌병변|신장)\s*장애\s*(가\s*있|인|야|이야|등급|판정|\d)|장애인\s*(이야|이에요|입니다|이다|등록|이라)|(당뇨|고혈압|저혈압|천식|갑상선\s*(질환|암|기능)|간염|치매|뇌전증|adhd|아토피)\s*(이|가|은|는)?\s*(있|환자|진단|앓|걸렸|판정|이야|이에요|입니다|이다|라서|인데)|(고혈압|저혈압|혈압|당뇨|갑상선|천식|간염|정신과|정신\s*건강의학과|adhd)\s*약\s*(을|를)?\s*(먹|복용)|정신과\s*(에|를|을)?\s*(다녀|다니|다님|다닌|진료\s*받|치료\s*받|상담\s*받)|임신\s*중절|낙태|\bi\s+(have|had)\s+(diabetes|cancer|hiv|aids|depression|adhd|asthma)|\bdiagnosed\b|\bmy\s+(diagnosis|depression|diabetes|cancer)\b|\bschizophreni|\bbipolar\s+disorder\b|\bchemotherapy\b|\bantidepressant`)},
	{"sexual_orientation", regexp.MustCompile(`(?i)동성애자|성소수자|게이($|[^가-힣]|야|예요|에요|입니다|이다|라서|인데|임|커플)|레즈비언|양성애자|트랜스젠더|성\s*정체성|성적\s*지향|\bgay\b|\blesbian\b|\bbisexual\b|\btransgender\b|sexual\s+orientation`)},
	{"religion", regexp.MustCompile(`(?i)(내|제|나의|저의)\s*(종교|신앙)|(종교|신앙)(는|은|이|가)\s*(불교|기독교|천주교|개신교|이슬람|가톨릭|원불교|유대교|힌두교)|(불교|기독교|천주교|개신교|이슬람교?|가톨릭|원불교|유대교|힌두교|하나님|하느님|부처님|예수님?|알라)\s*(을|를)?\s*(믿|섬기|신봉)|(교회|성당|사찰|절|모스크)\s*(에|를|을)?\s*(다녀|다님|다닌|다니|나가)|(기독교|천주교|개신교|불교|이슬람|가톨릭|원불교)\s*(신자|인|도)\s*(야|이야|예요|이에요|입니다|이다|인데|이라|임|$)|무슬림\s*(이|이야|이에요|입니다|이다|인데|이라|임)|(나|저)\s*(는|도)?\s*무슬림|신천지|여호와의\s*증인|\bmy\s+(religion|faith)\b|\bi\s*('m|am)\s+(a\s+)?(muslim|christian|catholic|buddhist|jewish|hindu)\b`)},
	{"politics", regexp.MustCompile(`(?i)(더불어\s*민주당|민주당|국민의\s*힘|정의당|조국혁신당|개혁신당|진보당|국민의당|녹색당|기본소득당|새누리당|한나라당|자유한국당|미래통합당)\s*(을|를|에|의|에게)?\s*(지지(해|하|함|했|자|합|한다|하는|해요|합니다)|찍|뽑|투표\s*(했|해|할)|당원|후원)|(나|저)\s*(는|도)?\s*(진보|보수)\s*(야|예요|이에요|입니다|주의자|성향)|(지지|지지하는)\s*정당|정당\s*(지지\s*(해|하|함|했)|가입|당원)|당원이|정치\s*성향|\bpolitical\s+(view|party|affiliation)|(선거|대선|총선|지선|투표)\s*(에서|에|때|날)?\s*(\S+\s*){0,2}?(\d+\s*번|기호\s*\d+|후보)\s*(을|를|에)?\s*(찍|뽑|투표)|\d+\s*번\s*(을|를)?\s*(찍었|뽑았)|\bvoted\s+for\b`)},
	{"criminal", regexp.MustCompile(`(?i)(^|[^이예])전과\s*(가|자|기록|\d|있|\s있)|범죄\s*기록|(수감|복역)\s*(됐|되었|돼|되어|중이|했|한\s*적)|(교도소|감옥|구치소|소년원)\s*(에서|에)?\s*(출소|나왔|복역|살았|수감|갔다\s*왔|다녀왔)|출소\s*(했|한|하고)|집행유예|유죄\s*판결|\bcriminal\s+record\b|\bconvicted\b|\bfelony\b|\bi\s+(was|went)\s+(in|to)\s+(prison|jail)\b`)},
	{"union", regexp.MustCompile(`(?i)노조\s*(가입|조합원)|노동조합\s*(가입|조합원)|\bunion\s+member`)},
	{"ethnicity", regexp.MustCompile(`(?i)(내|제|나의|저의)\s*(인종|민족)|(조선족|고려인|화교|혼혈)\s*(출신|이야|이에요|입니다|이다|인데|이라)|민족\s*출신|\bmy\s+(ethnicity|race)\b`)},
	{"biometric_genetic", regexp.MustCompile(`(?i)지문\s*(정보|데이터)|홍채\s*(정보|데이터|인식\s*데이터)|유전자\s*검사|dna\s*검사|\bfingerprint\b|\bgenetic\s+test`)},
	// finance: first-person or sentence-initial (implicit self) only; news
	// like "가구 소득이 3% 늘었다" / "국가 부채가" passes.
	{"finance", regexp.MustCompile(`(?i)(내|제|나의|저의|우리\s*집|우리\s*집의)\s*(월\s*|연\s*)?(소득|수입|연봉|월급|급여|부채|빚|재산|대출)|^\s*(월\s*|연\s*)?(소득|수입|연봉|월급|부채|빚)\s*(은|는|이|가)\s*(월\s*|연\s*)?\d|신용\s*(점수|등급)|\bcredit\s+score\b|\bmy\s+(income|salary|debt)\b`)},
	{"gov_id", regexp.MustCompile(`(?i)여권\s*번호|여권\s*[:：]?\s*[mr]\d{8}|\bpassport\s*(no|number|#)?\s*[:：]?\s*[mr]?\d{8}|운전\s*면허\s*(증\s*)?번호|\bdriver'?s?\s+licen[cs]e\s*(no|number)|\b\d{2}-\d{2}-\d{6}-\d{2}\b|외국인\s*등록\s*(증\s*)?번호`)},
}

// thirdPartyRe: a person other than the user as subject/possessor ...
var thirdPartyRe = regexp.MustCompile(`(?i)(^|[^가-힣])(엄마|아빠|어머니|아버지|어머님|아버님|부모님|형|누나|언니|오빠|남동생|여동생|동생|남편|아내|와이프|여자\s*친구|남자\s*친구|여친|남친|친구|동료|상사|팀장|사장님?|아들|딸|할머니|할아버지|이모|삼촌|고모|사촌|룸메이트?|걔|쟤|그\s*사람|그녀|이웃)\s*(가|는|은|이|도|의|께서|네|\s)|\bmy\s+(mom|mother|dad|father|parents?|wife|husband|partner|friend|brother|sister|boss|coworker|colleague|son|daughter|neighbou?r|roommate)\b|\b(he|she|they)\s+(is|are|has|have|was|were)\b`)

// ... together with any sensitive term (broader than the self rules,
// because third-party statements are always refused).
var thirdPartyTermRe = regexp.MustCompile(`(?i)불교\s*(를|을)?\s*(믿|신자|도)|기독교|천주교|개신교|이슬람|무슬림|가톨릭\s*(신자|믿)|(교회|성당|절|사찰)\s*(에|를|을)?\s*(다녀|다니|다님|나가|열심)|신자|(더불어\s*민주당|민주당|국민의\s*힘|정의당|조국혁신당|개혁신당|진보당|국민의당|녹색당|기본소득당|새누리당|한나라당|자유한국당|미래통합당)|힌두|유대교|당뇨|고혈압|우울증|공황|조현병|치매|정신과|암\s*(투병|환자|진단|이|에)|에이즈|\bhiv\b|투병|게이($|[^가-힣]|야|예요|에요|라|래|인|커플|임)|동성애|레즈|트랜스젠더|성소수자|전과|교도소|감옥|수감|출소|조선족|고려인|혼혈|정당|당원|지지자|노조|연봉|월급|소득|빚|부채|신용\s*(점수|등급)|임신|낙태|\b(muslim|christian|catholic|buddhist|jewish|hindu|gay|lesbian|diabetes|cancer|depression|prison|jail|convicted|salary|debt)\b`)

// foreignIDRe candidates are validated (date + 7th digit 5-8) to avoid
// matching arbitrary 13-digit numbers.
var foreignIDRe = regexp.MustCompile(`(^|[^0-9])(\d{2})(\d{2})(\d{2})-?([5-8])\d{6}($|[^0-9])`)

func validForeignID(s string) bool {
	for _, m := range foreignIDRe.FindAllStringSubmatch(s, -1) {
		mo, _ := strconv.Atoi(m[3])
		d, _ := strconv.Atoi(m[4])
		if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			return true
		}
	}
	return false
}

// sensitiveVariants: normalized lower-case text, its squashed copy, the
// compatibility-jamo composed copy and a leet-folded copy.
func sensitiveVariants(text string) []string {
	low := strings.ToLower(Normalize(text))
	out := []string{low, squash(low)}
	if j := strings.ToLower(Normalize(composeJamo(text))); j != low {
		out = append(out, j, squash(j))
	}
	if l := leetFold(low, leetRepl); l != low {
		out = append(out, l, leetFold(low, leetReplL))
	}
	return out
}

var (
	leetRepl  = strings.NewReplacer("4", "a", "@", "a", "3", "e", "1", "i", "!", "i", "0", "o", "5", "s", "$", "s", "7", "t")
	leetReplL = strings.NewReplacer("4", "a", "@", "a", "3", "e", "1", "l", "!", "i", "0", "o", "5", "s", "$", "s", "7", "t")
)

// leetFold folds leetspeak only inside Latin words (digits between letters),
// so numbers in Korean text stay intact.
func leetFold(s string, rep *strings.Replacer) string {
	return leetWordRe.ReplaceAllStringFunc(s, func(w string) string { return rep.Replace(w) })
}

var leetWordRe = regexp.MustCompile(`[a-z][a-z0-9@$!]*[0-9@$!][a-z0-9@$!]*|[0-9@$!]+[a-z][a-z0-9@$!]*`)

// composeJamo turns runs of Hangul compatibility jamo (ㄱ..ㅣ) into
// syllables: ㄱㅔㅇㅣ -> 게이.
func composeJamo(s string) string {
	const cho = "ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ"
	const jong = " ㄱㄲㄳㄴㄵㄶㄷㄹㄺㄻㄼㄽㄾㄿㅀㅁㅂㅄㅅㅆㅇㅈㅊㅋㅌㅍㅎ"
	ci := map[rune]int{}
	for i, r := range []rune(cho) {
		ci[r] = i
	}
	ji := map[rune]int{}
	for i, r := range []rune(jong) {
		if i > 0 {
			ji[r] = i
		}
	}
	isV := func(r rune) bool { return r >= 'ㅏ' && r <= 'ㅣ' }
	in := []rune(s)
	var b strings.Builder
	for i := 0; i < len(in); {
		l, okL := ci[in[i]]
		if okL && i+1 < len(in) && isV(in[i+1]) {
			v := int(in[i+1] - 'ㅏ')
			t := 0
			n := 2
			if i+2 < len(in) {
				if tj, ok := ji[in[i+2]]; ok && !(i+3 < len(in) && isV(in[i+3])) {
					t, n = tj, 3
				}
			}
			b.WriteRune(rune(0xAC00 + (l*21+v)*28 + t))
			i += n
			continue
		}
		b.WriteRune(in[i])
		i++
	}
	return b.String()
}

// CheckSensitive refuses text in a sensitive category or a sensitive
// statement about a third party.
func CheckSensitive(text string) error {
	vs := sensitiveVariants(text)
	for _, v := range vs {
		for _, c := range sensitiveCategories {
			if c.Re.MatchString(v) {
				return &RejectError{Reason: "sensitive_" + c.Name}
			}
		}
		if thirdPartyRe.MatchString(v) && thirdPartyTermRe.MatchString(v) {
			return &RejectError{Reason: "sensitive_third_party"}
		}
	}
	// passport-number shape anywhere when 여권/passport appears anywhere
	for _, v := range vs[:2] {
		if passportWordRe.MatchString(v) && passportNoRe.MatchString(v) {
			return &RejectError{Reason: "sensitive_gov_id"}
		}
	}
	if validForeignID(vs[0]) || validForeignID(vs[1]) {
		return &RejectError{Reason: "sensitive_gov_id"}
	}
	return nil
}

var (
	passportWordRe = regexp.MustCompile(`(?i)여권|passport`)
	passportNoRe   = regexp.MustCompile(`(?i)(^|[^a-z0-9])[a-z]\d{8}($|[^0-9])`)
)
