// Package selfdesc builds the short Eve self-description the gateway puts at
// the start of the system prompt of every chat request. It is generated from
// runtime state only (build version, the user's memory state, enabled tools),
// so a feature that is switched off is never mentioned and a feature that is
// switched on appears without editing this text. The output is fixed gateway
// text plus a sanitized version string; it never contains user or web content.
package selfdesc

import (
	"regexp"
	"strings"
)

// MemoryState is what the prompt claims about memory for the current user.
type MemoryState int

const (
	MemoryUnavailable MemoryState = iota // server flag off, or this principal cannot use memory
	MemoryOff                            // available, the user turned it off
	MemoryPending                        // on, but the first-use intro is not seen yet (nothing is injected)
	MemoryOn                             // on and active
)

// Tool is one tool that is enabled for this request. Detail is optional
// (for example the function names, or a per-request state).
type Tool struct {
	Name   string
	Detail string
}

// State is everything the template is generated from.
type State struct {
	Version string
	Memory  MemoryState
	// Propose reports that the model can create memory confirmation cards
	// in this request (the memory_propose tool is attached).
	Propose bool
	// Tools lists enabled tools only. Disabled features must not appear.
	Tools []Tool
}

const header = "[Eve 자기 설명(게이트웨이 자동 생성). 기능을 물으면 이 내용만 근거로 답하고, 없는 기능을 있다고 하지 마세요.]"

const memoryUI = "설정 > 메모리에서 보기·추가·수정·삭제·내보내기 가능(API: /v1/memory)."

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,39}$`)

// Build returns the self-description text.
func Build(st State) string {
	var b strings.Builder
	b.WriteString(header)
	if v := strings.TrimSpace(st.Version); versionRe.MatchString(v) {
		b.WriteString("\n- 버전: " + v)
	}
	b.WriteString("\n- 메모리: " + memoryLine(st))
	b.WriteString("\n- 도구: " + toolsLine(st.Tools))
	return b.String()
}

func memoryLine(st State) string {
	switch st.Memory {
	case MemoryOn:
		if st.Propose {
			return "켜짐. 선호·사실을 기억해 대화에 반영해요. \"기억해줘\"는 확인 카드를 띄우고 사용자가 승인해야 저장돼요(\"잊어줘\"는 삭제 카드). " + memoryUI
		}
		return "켜짐. 선호·사실을 기억해 대화에 반영해요. 이 요청에서는 채팅 제안이 불가하니 직접 하도록 안내하세요. " + memoryUI
	case MemoryPending:
		return "켜져 있지만 첫 사용 안내를 확인하기 전이라 반영되지 않고 기억 제안도 못 해요. 사용자가 메모리 첫 안내(설정 > 메모리)를 열어 확인하면 시작돼요. " + memoryUI
	case MemoryOff:
		return "꺼짐. 아무것도 기억·저장하지 않아요. 사용자가 설정 > 메모리에서 켤 수 있어요."
	default:
		return "이 연결에서는 사용할 수 없어요."
	}
}

func toolsLine(tools []Tool) string {
	if len(tools) == 0 {
		return "없음"
	}
	parts := make([]string, 0, len(tools))
	for _, t := range tools {
		if t.Detail != "" {
			parts = append(parts, t.Name+"("+t.Detail+")")
		} else {
			parts = append(parts, t.Name)
		}
	}
	return strings.Join(parts, ", ")
}
