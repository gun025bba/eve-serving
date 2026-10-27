package memory

import (
	"regexp"
	"strings"
)

// Sensitive-category minimum deny list (memory.md §9.1). Items in these
// categories are never stored, even when the user asks: health and medical
// conditions, sexual orientation / sex life, religion, political views,
// criminal records, union membership, ethnicity, biometric and genetic data.
// The list is deliberately small and keyword based (Korean + English); it is
// a floor, not a classifier. A hit refuses the write with the same
// secret_detected code, so the reason is never echoed (ui-api-contract §3.3).
// Adding or changing an entry is a security review item.
var sensitiveCategories = []struct {
	Name string
	Re   *regexp.Regexp
}{
	// Korean rules avoid bare substrings that occur inside common words
	// (게이트/게이밍/게이지, 이전과/전과목, 추수감사절): they need a particle,
	// ending or first-person/verb context. Rules also run on the squashed
	// copy, so no rule relies on whitespace alone.
	{"health", regexp.MustCompile(`(?i)진단(을|를)?\s*받|질환|우울증|공황장애|조현병|양극성|정신과|항우울제|암\s*투병|에이즈|hiv|당뇨\s*(환자|진단)|(고혈압|저혈압|혈압|당뇨|갑상선|천식|간염|정신과)\s*약\s*(을|를)?\s*(먹|복용)|임신\s*중절|낙태|\bdiagnos(ed|is)\b|\bdepression\b|\bschizophreni|\bbipolar\b|\bchemotherapy\b|\bantidepressant`)},
	{"sexual_orientation", regexp.MustCompile(`(?i)동성애자|성소수자|게이($|[^가-힣]|야|예요|에요|입니다|이다|라서|인데|임|커플)|레즈비언|양성애자|트랜스젠더|성\s*정체성|성적\s*지향|\bgay\b|\blesbian\b|\bbisexual\b|\btransgender\b|sexual\s+orientation`)},
	{"religion", regexp.MustCompile(`(?i)(내|제|나의|저의)\s*(종교|신앙)|(종교|신앙)(는|은|이|가)\s*(불교|기독교|천주교|개신교|이슬람|가톨릭|원불교|유대교|힌두교)|(교회|성당|사찰|절|모스크)\s*(에|를|을)?\s*(다녀|다님|다닌|나가)|(기독교|천주교|개신교|불교|이슬람|가톨릭|원불교)\s*(신자|인|도)|신천지|여호와의\s*증인|\bmy\s+(religion|faith)\b|\bi\s*('m|am)\s+(a\s+)?(muslim|christian|catholic|buddhist|jewish|hindu)\b`)},
	{"politics", regexp.MustCompile(`(?i)(지지|지지하는)\s*정당|정당\s*(지지|가입|당원)|당원이|정치\s*성향|\bpolitical\s+(view|party|affiliation)|\bvoted\s+for\b`)},
	{"criminal", regexp.MustCompile(`(?i)(^|[^이예])전과\s*(가|자|기록|\d|있|\s있)|범죄\s*기록|(수감|복역)\s*(됐|되었|돼|되어|중이|했|한\s*적)|집행유예|유죄\s*판결|\bcriminal\s+record\b|\bconvicted\b|\bfelony\b`)},
	{"union", regexp.MustCompile(`(?i)노조\s*(가입|조합원)|노동조합\s*(가입|조합원)|\bunion\s+member`)},
	{"ethnicity", regexp.MustCompile(`(?i)(내|제|나의|저의)\s*(인종|민족)|(조선족|고려인|화교|혼혈)\s*(출신|이야|이에요|입니다|이다|인데|이라)|민족\s*출신|\bmy\s+(ethnicity|race)\b`)},
	{"biometric_genetic", regexp.MustCompile(`(?i)지문\s*(정보|데이터)|홍채|유전자\s*검사|dna\s*검사|\bfingerprint\b|\bgenetic\s+test`)},
	{"finance", regexp.MustCompile(`(?i)소득(은|이|이\s*있|\s*\d|\s*수준)|(내|제|나의|월|연)\s*소득|부채(는|가|\s*\d)|빚(이|은)\s*\d|신용\s*(점수|등급)|\bcredit\s+score\b|\bmy\s+(income|debt)\b`)},
	{"gov_id", regexp.MustCompile(`(?i)여권\s*번호|\bpassport\s*(no|number)|\b[mr]\d{8}\b|운전\s*면허\s*(증\s*)?번호|\bdriver'?s?\s+licen[cs]e\s*(no|number)|\b\d{2}-\d{2}-\d{6}-\d{2}\b|외국인\s*등록\s*(증\s*)?번호|\b\d{6}-?[5-8]\d{6}\b`)},
}

// CheckSensitive refuses text in a sensitive category. Rules run on the
// normalized text and its squashed copy (whitespace/zero-width removed).
func CheckSensitive(text string) error {
	low := strings.ToLower(Normalize(text))
	for _, v := range []string{low, squash(low)} {
		for _, c := range sensitiveCategories {
			if c.Re.MatchString(v) {
				return &RejectError{Reason: "sensitive_" + c.Name}
			}
		}
	}
	return nil
}
