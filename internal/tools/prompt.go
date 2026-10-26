package tools

import (
	"strings"
	"time"
)

// Gateway-owned Eve system prompt (proposal, feat/eve-system-prompt).
// It is fixed text plus a date; it never contains user or web content.

const evePromptBase = `당신은 Eve입니다. 사용자의 개인 서버에서 동작하는 AI 어시스턴트예요.
오늘 날짜는 {{DATE}}입니다(한국 시간). 날짜나 요일이 필요한 질문에는 이 날짜를 기준으로 답하세요.
사용자가 쓴 언어로 답하고, 한국어로 물으면 자연스러운 한국어로 답하세요.`

const evePromptWebOff = `지금은 웹 검색이 꺼져 있어서 실시간 정보를 조회할 수 없어요. 학습한 지식으로 답하되, 최신 정보가 필요한 질문(뉴스, 가격, 일정, 최근 출시 등)이면 답이 오래됐을 수 있다고 짧게 알려 주세요.
그럴 때는 "입력창의 웹 검색 버튼을 켜고 다시 물어보시면 검색해서 답할게요"처럼 안내하세요. 다른 검색 엔진을 직접 써 보라고 권하거나 "제 지식은 몇 년까지입니다"라고 말하지 마세요.`

const evePromptWebOn = `지금은 웹 검색이 켜져 있어요. web_search와 web_fetch 도구로 최신 정보를 찾아볼 수 있으니, 검색을 할 수 없다고 말하지 마세요.
최신 정보나 사실 확인이 필요한 질문이면 먼저 도구로 찾아본 뒤 답하고, 근거에는 [1], [2]처럼 출처 번호를 붙이세요. 검색이 필요 없는 질문(인사, 일반 지식, 글쓰기 등)은 바로 답해도 돼요.`

// WebPromptState says what the prompt should claim about web search.
type WebPromptState int

const (
	WebUnavailable WebPromptState = iota // GW_WEB_ENABLED=false: say nothing about search
	WebOff                               // available but off for this request: point to the toggle
	WebOn                                // auto/force: tools are attached
)

// EvePrompt returns the gateway system prompt for now (in loc) and web state.
func EvePrompt(now time.Time, loc *time.Location, web WebPromptState) string {
	d := now.In(loc)
	days := []string{"일", "월", "화", "수", "목", "금", "토"}
	p := strings.Replace(evePromptBase, "{{DATE}}", d.Format("2006년 1월 2일")+" ("+days[d.Weekday()]+")", 1)
	switch web {
	case WebOff:
		p += "\n\n" + evePromptWebOff
	case WebOn:
		p += "\n\n" + evePromptWebOn
	}
	return p
}

// WithSystemPrompt puts gateway text first and folds the client's leading
// system messages into the same system message after it, so there is one
// system message (Qwen templates only honour a leading one) and client
// instructions (persona tweaks, formatting) still apply.
func WithSystemPrompt(gw string, msgs []any) []any {
	parts := []string{gw}
	i := 0
	for ; i < len(msgs); i++ {
		m, ok := msgs[i].(map[string]any)
		if !ok || m["role"] != "system" {
			break
		}
		if c, _ := m["content"].(string); strings.TrimSpace(c) != "" {
			parts = append(parts, c)
		}
	}
	out := make([]any, 0, len(msgs)-i+1)
	out = append(out, map[string]any{"role": "system", "content": strings.Join(parts, "\n\n---\n추가 지시(클라이언트):\n")})
	return append(out, msgs[i:]...)
}
