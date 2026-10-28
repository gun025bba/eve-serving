package handlers

import (
	"net/http"
	"strings"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/selfdesc"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// selfDescState derives the self-description from the same predicates the
// request path uses (memoryOn/memoryFor/memoryActive, webEnabled and the
// resolved web mode), so it cannot drift from what the gateway really does.
// The user is the authenticated users.id (via memoryFor); nothing here looks
// at how the user signed in; the interactive scope (granted by the auth
// layer) decides whether the text is injected at all (injectSelfDescription).
func (s *Server) selfDescState(r *http.Request, mode tools.Mode, memOffer bool) selfdesc.State {
	st := selfdesc.State{Version: s.version()}
	webOn := mode == tools.ModeAuto || mode == tools.ModeForce
	switch {
	case !s.memoryOn() || !hasScope(r, auth.ScopeInteractive):
		st.Memory = selfdesc.MemoryUnavailable
	case !s.memoryFor(r):
		st.Memory = selfdesc.MemoryOff
	case !s.memoryActive(r):
		st.Memory = selfdesc.MemoryPending
	default:
		st.Memory = selfdesc.MemoryOn
		// memory_propose is offered whenever memory is active, web on or off,
		// unless the request's history cannot be checked (memoryOffered).
		st.Propose = memOffer
	}
	if s.webEnabled() {
		d := "꺼짐, 입력창 웹 검색 버튼으로 켬"
		if webOn {
			d = "켜짐"
		}
		st.Tools = append(st.Tools, selfdesc.Tool{Name: "웹 검색", Detail: d})
	}
	if st.Propose {
		st.Tools = append(st.Tools, selfdesc.Tool{Name: "기억 제안", Detail: memory.ToolName})
	}
	return st
}

// injectSelfDescription puts the Eve self-description at the start of the
// system prompt of the forwarded request. The memory block is appended later
// (injectMemory) to the end of the leading system run, so the order is:
// self-description, client system text, memory block. It is per request and
// never persisted. A first system message with non-string content is left
// alone and the text goes in a new leading system message.
//
// Only requests with the interactive scope (a person at a paired browser or
// app session) get it. Scripted/API clients keep their request body, and so
// their behavior and eval baselines, exactly as before. The decision uses the
// scope the auth layer granted, never the credential type.
func (s *Server) injectSelfDescription(r *http.Request, body map[string]any, mode tools.Mode, memOffer bool) {
	if !hasScope(r, auth.ScopeInteractive) {
		return
	}
	text := selfdesc.Build(s.selfDescState(r, mode, memOffer))
	msgs, _ := body["messages"].([]any)
	if len(msgs) > 0 {
		if m, ok := msgs[0].(map[string]any); ok && m["role"] == "system" {
			if c, ok := m["content"].(string); ok {
				cp := make(map[string]any, len(m))
				for k, v := range m {
					cp[k] = v
				}
				cp["content"] = text
				if strings.TrimSpace(c) != "" {
					cp["content"] = text + "\n\n" + c
				}
				out := append([]any{}, msgs...)
				out[0] = cp
				body["messages"] = out
				return
			}
		}
	}
	body["messages"] = append([]any{map[string]any{"role": "system", "content": text}}, msgs...)
}
