package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// argKID is the key id of the argument-hash key for log lines.
func (s *Server) argKID() string {
	if s.Web == nil {
		return ""
	}
	return s.Web.MAC.KID()
}

// personalTool classifies a tool name as a personal-data read (G1).
func (s *Server) personalTool(name string) bool { return tools.IsPersonalTool(name) }

// personalExposed reports whether personal-data tools are offered on r.
func (s *Server) personalExposed(r *http.Request) bool {
	return s.PersonalExposed != nil && s.PersonalExposed(r)
}

// assistantTexts are the contents of the assistant messages of a request.
func assistantTexts(msgs []any) []string {
	var out []string
	for _, x := range msgs {
		if m, ok := x.(map[string]any); ok && m["role"] == "assistant" {
			if t := strings.TrimSpace(textOf(m["content"])); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

type storedTool struct {
	Tool   string `json:"tool"`
	Status string `json:"status"`
}

// readsPersonal reports whether a stored assistant message used a personal
// data read: metadata.tools[] first, then (messages written before
// metadata.tools[] existed) its tool_calls rows. found tells whether either
// source had an answer.
func readsPersonal(m store.Message, calls []store.ToolCall) (reads, found bool) {
	var md struct {
		Tools *[]storedTool `json:"tools"`
	}
	if len(m.Metadata) > 0 && json.Unmarshal(m.Metadata, &md) == nil && md.Tools != nil {
		for _, t := range *md.Tools {
			if tools.IsPersonalTool(t.Tool) && t.Status == "ok" {
				return true, true
			}
		}
		return false, true
	}
	if len(calls) > 0 {
		for _, c := range calls {
			if tools.IsPersonalTool(c.Tool) { // any status: the read was attempted
				return true, true
			}
		}
		return false, true
	}
	return false, false
}

// personalHistory implements P④ and G1 ⓑ for one request: the protected texts
// are the contents of earlier assistant answers (in this request's history)
// that were built from a personal-data read; private is true when the history
// holds such an answer, or - while personal tools are exposed - an assistant
// turn the server cannot match with a stored message (a client-written turn
// cannot be shown to be free of personal data).
func (s *Server) personalHistory(r *http.Request, msgs []any, pr *chatPersist) (protected []string, private bool) {
	if !s.personalExposed(r) {
		return nil, false
	}
	want := assistantTexts(msgs)
	if len(want) == 0 {
		return nil, false
	}
	if pr == nil || pr.threadID == "" {
		return nil, true // nothing stored to check them against: fail closed
	}
	matched := map[string]bool{}
	before := ""
	for page := 0; page < 20; page++ {
		ms, more, err := s.Store.ListMessages(r.Context(), user(r), pr.threadID, 500, before)
		if err != nil {
			return protected, true
		}
		var ids []string
		for _, m := range ms {
			if m.Role == "assistant" {
				ids = append(ids, m.ID)
			}
		}
		var calls map[string][]store.ToolCall
		if ws, ok := s.Store.(store.WebStore); ok && len(ids) > 0 {
			var err error
			if calls, err = ws.ToolCallsByMessage(r.Context(), user(r), ids); err != nil {
				return protected, true
			}
		}
		for _, m := range ms {
			if m.Role != "assistant" {
				continue
			}
			t := strings.TrimSpace(m.Content)
			if t == "" {
				continue
			}
			reads, _ := readsPersonal(m, calls[m.ID])
			if reads {
				protected = append(protected, m.Content)
				private = true
			}
			matched[t] = true
		}
		if !more || len(ms) == 0 {
			break
		}
		before = ms[0].ID
	}
	for _, t := range want {
		if !matched[t] {
			private = true // not a message of this thread: unverifiable (G1 ⓑ)
			break
		}
	}
	return protected, private
}
