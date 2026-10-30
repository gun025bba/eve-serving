package tools

import (
	"regexp"
	"sort"
	"unicode"
)

// Suspect rules R1-R7 detect instruction-like content in tool output. They
// never reach the model, the client or the logs as text: only the rule ids
// (e.g. "R1") are reported.
var (
	r1Re  = regexp.MustCompile(`(?i)(ignore|disregard|forget|override)\s+(all\s+|any\s+|the\s+|your\s+)?(previous|prior|above|earlier|preceding|system)\s+(instructions?|prompts?|rules?|messages?|directions?)|new\s+instructions?\s*:|you\s+are\s+now\s+|system\s+prompt|developer\s+message|(이전|위|앞선|기존)\s*(의\s*)?(지시|지침|명령|프롬프트|규칙).{0,8}(무시|잊|따르지)|시스템\s*프롬프트|(새로운|다음)\s*(지시|지침|명령)\s*[:：]`)
	r2Re  = regexp.MustCompile(`(?i)\b(web_fetch|web_search|memory_propose|memory\.propose|calendar_add_event|reminders?_add|tool_call|function_call)\b|call\s+the\s+(tool|function)|invoke\s+(the\s+)?(tool|function)|(일정|리마인더|메모리|기억)\s*(을|를)?\s*(추가|저장|등록)\s*(하세요|해라|해줘|하라)`)
	r3Re  = regexp.MustCompile(`(?i)<\s*/?\s*(untrusted[\s_\-]*web[\s_\-]*content|tool_call|tool_response|system|user_memory)\b|<\|[a-z_]+\|>|(^|\n)\s*(assistant|system|developer)\s*[:：]|\[/?(INST|SYS)\]`)
	r4aRe = regexp.MustCompile(`!\[[^\]]*\]\(\s*(https?:)?//`)
	r4bRe = regexp.MustCompile(`(?i)https?://[^\s)]*(\{[a-z_]+\}|%7b[a-z_]+%7d|<[a-z_ ]+>)`)
	r4cRe = regexp.MustCompile(`(?i)(send|post|append|include|leak|exfiltrate|upload)\s+(the\s+|this\s+|all\s+|your\s+)?(conversation|chat|history|memory|memories|user\s+data)[^.\n]{0,60}(url|link|http|query)|(대화|메모리|기억|기록)\s*(내용)?\s*(을|를)?[^.\n]{0,30}(URL|url|링크|주소)에?\s*(포함|붙여|넣어|보내)`)
	r6B64 = regexp.MustCompile(`[A-Za-z0-9+/_\-]{40,}={0,2}|\b[0-9a-fA-F]{40,}\b`)
	r6Kw  = regexp.MustCompile(`(?i)(decode|decrypt|execute|instruction|ignore|run\s+this|call|지시|명령|디코드|실행)`)
	r7Re  = regexp.MustCompile(`(忽略|无视|忽视|無視|无视掉)(之前|以上|先前|以前|上述|所有)?的?(所有)?(指令|指示|说明|提示|命令|規則|规则)|(以前|前|上記)の?(指示|命令|プロンプト)を?(無視|忘れ)|指示を無視|ignorar\s+(todas\s+)?las\s+instrucciones|ignorez\s+(toutes\s+)?les\s+instructions|ignoriere\s+(alle\s+)?(vorherigen\s+)?anweisungen|игнорируй\s+(все\s+)?(предыдущие\s+)?инструкции`)
)

// Suspicious returns the sorted ids of the rules text trips. text is the
// raw (unsanitized) content; R5 needs the invisible characters, the others
// run on the sanitized form.
func Suspicious(text string) []string {
	if text == "" {
		return nil
	}
	set := map[string]bool{}
	if hasHiddenChars(text) {
		set["R5"] = true
	}
	t := SanitizeUntrusted(text)
	if r1Re.MatchString(t) {
		set["R1"] = true
	}
	if r2Re.MatchString(t) {
		set["R2"] = true
	}
	if r3Re.MatchString(t) {
		set["R3"] = true
	}
	if r4aRe.MatchString(t) || r4bRe.MatchString(t) || r4cRe.MatchString(t) {
		set["R4"] = true
	}
	if r6B64.MatchString(t) && (r6Kw.MatchString(t) || set["R1"] || set["R2"]) {
		set["R6"] = true
	}
	if r7Re.MatchString(t) {
		set["R7"] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isPict approximates Unicode Extended_Pictographic (plus the regional
// indicators, skin-tone modifiers and keycap bases that legitimately carry
// emoji sequences). It is a deliberately small table, not UCD data.
func isPict(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF, r >= 0x2600 && r <= 0x27BF, r >= 0x2300 && r <= 0x23FF,
		r >= 0x2B00 && r <= 0x2BFF, r >= 0x2190 && r <= 0x21FF, r >= 0x25A0 && r <= 0x25FF,
		r >= 0x2900 && r <= 0x297F:
		return true
	}
	switch r {
	case 0xA9, 0xAE, 0x203C, 0x2049, 0x2122, 0x2139, 0x24C2, 0x3030, 0x303D, 0x3297, 0x3299:
		return true
	}
	return false
}

func isKeycapBase(r rune) bool { return (r >= '0' && r <= '9') || r == '#' || r == '*' }

func isBidiOrZW(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0x2060, 0x2061, 0x2062, 0x2063, 0x2064, 0xFEFF, 0x00AD, 0x034F, 0x061C, 0x180E:
		return true
	}
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// hasHiddenChars implements R5: bidi/zero-width/soft-hyphen/tag/variation
// characters, except where they are part of a genuine emoji sequence
// (VS16 after an emoji or keycap base, ZWJ between two pictographs, a
// flag-tag sequence after U+1F3F4).
func hasHiddenChars(s string) bool {
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		prev := func() rune {
			for j := i - 1; j >= 0; j-- {
				if rs[j] == 0xFE0F || (rs[j] >= 0x1F3FB && rs[j] <= 0x1F3FF) {
					continue
				}
				return rs[j]
			}
			return 0
		}
		switch {
		case r == 0xFE0F:
			if i > 0 && (isPict(rs[i-1]) || isKeycapBase(rs[i-1])) {
				continue
			}
			return true
		case r >= 0xFE00 && r <= 0xFE0E, r >= 0xE0100 && r <= 0xE01EF:
			return true
		case r >= 0xE0020 && r <= 0xE007E, r == 0xE007F, r >= 0xE0000 && r <= 0xE001F:
			// tag characters are legitimate only inside a subdivision flag
			// sequence: U+1F3F4 followed by tags up to U+E007F.
			j := i
			for j > 0 && rs[j-1] >= 0xE0020 && rs[j-1] <= 0xE007E {
				j--
			}
			if j > 0 && rs[j-1] == 0x1F3F4 && r != 0xE0000 {
				continue
			}
			return true
		case r == 0x200D:
			if i+1 < len(rs) && isPict(prev()) && isPict(rs[i+1]) {
				continue
			}
			return true
		case isBidiOrZW(r):
			return true
		case r != '\n' && r != '\t' && r != '\r' && unicode.IsControl(r):
			return true
		}
	}
	return false
}
