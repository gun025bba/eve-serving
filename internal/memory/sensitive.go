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
	{"health", regexp.MustCompile(`(?i)진단(을|를)?\s*받|질환|우울증|공황장애|조현병|양극성|정신과|항우울제|암\s*투병|에이즈|hiv|당뇨\s*(환자|진단)|임신\s*중절|낙태|\bdiagnos(ed|is)\b|\bdepression\b|\bschizophreni|\bbipolar\b|\bchemotherapy\b|\bantidepressant`)},
	{"sexual_orientation", regexp.MustCompile(`(?i)동성애자|성소수자|게이|레즈비언|양성애자|트랜스젠더|성\s*정체성|성적\s*지향|\bgay\b|\blesbian\b|\bbisexual\b|\btransgender\b|sexual\s+orientation`)},
	{"religion", regexp.MustCompile(`(?i)(종교|신앙)(는|은|이|가)?\s|기독교인|천주교인|불교\s*신자|무슬림|이슬람\s*신자|신천지|여호와의\s*증인|\breligio(n|us)\b|\bmuslim\b`)},
	{"politics", regexp.MustCompile(`(?i)(지지|지지하는)\s*정당|정당\s*(지지|가입|당원)|당원이|정치\s*성향|\bpolitical\s+(view|party|affiliation)|\bvoted\s+for\b`)},
	{"criminal", regexp.MustCompile(`(?i)전과|범죄\s*기록|수감|복역|집행유예|유죄\s*판결|\bcriminal\s+record\b|\bconvicted\b|\bfelony\b`)},
	{"union", regexp.MustCompile(`(?i)노조\s*(가입|조합원)|노동조합\s*(가입|조합원)|\bunion\s+member`)},
	{"ethnicity", regexp.MustCompile(`(?i)인종|민족\s*출신|\bethnicity\b|\bracial\b`)},
	{"biometric_genetic", regexp.MustCompile(`(?i)지문\s*(정보|데이터)|홍채|유전자\s*검사|dna\s*검사|\bfingerprint\b|\bgenetic\s+test`)},
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
