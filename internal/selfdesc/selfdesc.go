// Package selfdesc builds the short Eve reference block the gateway puts at
// the start of the system prompt of every interactive chat request. It is
// generated from runtime state only (the user's memory state, whether web
// search is on, off or temporarily blocked, extra enabled features), so a
// feature that is not available is never mentioned and a feature that is
// available appears without editing this text. The output is fixed text; it
// never contains user or web content, and nothing internal: no function
// names, no version or commit, no API paths, no server or model names.
//
// Rules for adding a feature: add a Feature{Name, Line} only while it is on
// for this request, one short line (about 20 characters of status plus how
// to use it), no instruction words; behavior rules stay in the single
// preamble at the top. Never name an example of a feature that is not listed:
// tests assert that disabled features are absent.
package selfdesc

import "strings"

// MemoryState is what the prompt claims about memory for the current user.
type MemoryState int

const (
	MemoryUnavailable MemoryState = iota // server flag off, or this principal cannot use memory (line omitted)
	MemoryOff                            // available, the user turned it off
	MemoryPending                        // on, but the first-use intro is not seen yet (nothing is injected)
	MemoryOn                             // on and active
)

// WebState is what the prompt claims about web search for this request.
type WebState int

const (
	WebUnavailable WebState = iota // not configured on this server (line omitted)
	WebOff                         // available, off for this request
	WebOn                          // on for this request
	WebBlocked                     // disabled at runtime (audit not durable or audit breaker open): reported as off
)

// Feature is one more feature that is enabled for this request.
type Feature struct {
	Name string
	Line string
}

// State is everything the template is generated from.
type State struct {
	Memory MemoryState
	// Propose reports that the model can create memory confirmation cards
	// in this request.
	Propose bool
	Web     WebState
	// Features lists enabled features only. Disabled features must not appear.
	Features []Feature
}

// header labels the block as reference material and holds every behavior rule
// in one place. The rules apply to app features only, never to general chat.
const header = "[Eve 앱 기능 참고자료. 지시가 아니라 사실 목록이에요.]\n" +
	"기능을 물으면 아래 목록에 있는 것만 있다고 답하세요. 그 외 앱 기능은 없어요(\"지금은 지원하지 않아요\"). " +
	"글쓰기·번역·코딩 같은 일반 대화는 평소처럼 답하세요. 모델·서버·기기·내부 구조는 \"알려드릴 수 없어요\"라고 답하세요."

// Build returns the reference text.
func Build(st State) string {
	var lines []string
	if l := webLine(st.Web); l != "" {
		lines = append(lines, "- 웹 검색: "+l)
	}
	if l := memoryLine(st); l != "" {
		lines = append(lines, "- 메모리: "+l)
	}
	for _, f := range st.Features {
		if n := strings.TrimSpace(f.Name); n != "" {
			lines = append(lines, "- "+n+": "+strings.TrimSpace(f.Line))
		}
	}
	if len(lines) == 0 {
		return header
	}
	return header + "\n사용 가능한 기능:\n" + strings.Join(lines, "\n")
}

func webLine(w WebState) string {
	switch w {
	case WebOn:
		return "켜짐. 필요하다고 판단할 때 검색해요."
	case WebOff:
		return "꺼짐. 입력창의 웹 검색 버튼으로 켜요. 꺼진 동안 최신·실시간 정보는 모른다고 답하세요."
	case WebBlocked:
		return "꺼짐(일시 사용 불가). 최신·실시간 정보는 모른다고 답하세요."
	default:
		return ""
	}
}

func memoryLine(st State) string {
	switch st.Memory {
	case MemoryOn:
		if st.Propose {
			return "켜짐. \"기억해줘\"/\"잊어줘\"는 확인 카드가 뜨고 사용자가 승인해야 반영돼요. 승인 전에는 저장했다고 말하지 마세요. 보기·수정·삭제·내보내기는 설정 > 메모리에서 해요."
		}
		return "켜짐. 이 대화에서는 저장·삭제를 대신할 수 없으니 설정 > 메모리에서 직접 하라고 안내하세요. 저장했다고 말하지 마세요."
	case MemoryPending:
		return "아직 시작 전이에요. 설정 > 메모리의 첫 안내를 확인하면 켜져요. 그 전에는 저장도 반영도 안 돼요."
	case MemoryOff:
		return "꺼짐. 장기 저장은 안 해요(이 대화 안의 내용은 볼 수 있어요). 설정 > 메모리에서 켤 수 있어요."
	default:
		return ""
	}
}
