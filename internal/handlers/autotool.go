package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

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

// personalReady is the fail-closed precondition for offering personal-data
// read tools (§4.3.4 "G1 prerequisites"): the guard code that protects what
// they return must be able to run. That means automatic selection is on (the
// session with G1-G3 and the echo filter exists) and the store is the web
// store whose messages P④ and G1 ⓑ read back. Without it no personal tool is
// offered.
func (s *Server) personalReady() bool {
	if s.Personal == nil || !s.autoSelect() {
		return false
	}
	_, ok := s.Store.(store.WebStore)
	return ok
}

// personalExposed reports whether personal-data tools are offered on r.
func (s *Server) personalExposed(r *http.Request) bool {
	return s.personalReady() && len(dropDenied(s.Personal.Defs(r))) > 0
}

// defName is the function name of an OpenAI tool definition ("" if malformed).
func defName(d any) string {
	m, _ := d.(map[string]any)
	f, _ := m["function"].(map[string]any)
	n, _ := f["name"].(string)
	return n
}

// dropDenied removes the tools on the server deny list (tools.denylist) from
// definitions. A definition whose name cannot be read is dropped too (fail
// closed).
func dropDenied(defs []any) []any {
	var out []any
	for _, d := range defs {
		if n := defName(d); n != "" && !tools.Denied(n) {
			out = append(out, d)
		}
	}
	return out
}

// personalDefs are the personal tool definitions for r (nil when not offered).
func (s *Server) personalDefs(r *http.Request) []any {
	if !s.personalExposed(r) {
		return nil
	}
	return dropDenied(s.Personal.Defs(r))
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

// readsPersonal reports whether a stored assistant message is to be treated as
// a personal-data read (P④, G1 ⓑ). Order (design 4.3.4 M-f):
//  1. an explicit metadata.pim_read marker is followed;
//  2. metadata.tools[] (6.0.0 writes it on every assistant message): a PIM tool
//     with status ok means read;
//  3. no metadata.tools[] and created inside the 5.1.0 window [start, end):
//     assumed to be a read (5.1.0 recorded nothing), assumed=true;
//  4. otherwise not a read.
//
// tool_calls rows are not consulted: nothing says they hold PIM reads, they
// are deleted after 90 days, and a reconstruction is no input for a safety
// control.
func readsPersonal(m store.Message, start, end time.Time) (reads, assumed bool) {
	var md struct {
		Tools   *[]storedTool `json:"tools"`
		PIMRead *bool         `json:"pim_read"`
	}
	if len(m.Metadata) > 0 {
		_ = json.Unmarshal(m.Metadata, &md) // unparsable: treated as absent, so the window rule decides
	}
	if md.PIMRead != nil {
		return *md.PIMRead, false
	}
	if md.Tools != nil {
		for _, t := range *md.Tools {
			if tools.IsPersonalTool(t.Tool) && t.Status == "ok" {
				return true, false
			}
		}
		return false, false
	}
	if !m.CreatedAt.Before(start) && (end.IsZero() || m.CreatedAt.Before(end)) {
		return true, true
	}
	return false, false
}

// personalHistory implements P④ and G1 ⓑ for one request: the protected texts
// are the contents of earlier assistant answers (in this request's history)
// that were built from a personal-data read; private is true when the history
// holds such an answer, or - while personal tools are exposed - an assistant
// turn the server cannot match with a stored message (a client-written turn
// cannot be shown to be free of personal data). Messages of the 5.1.0 window
// without metadata.tools[] count as reads whether or not personal tools are
// offered on this request (design 4.3.4 M-f); the unmatched-turn rule only
// applies while they are.
func (s *Server) personalHistory(r *http.Request, msgs []any, pr *chatPersist) (protected []string, private bool) {
	exposed := s.personalExposed(r)
	want := assistantTexts(msgs)
	if len(want) == 0 {
		return nil, false
	}
	if pr == nil || pr.threadID == "" {
		return nil, exposed // nothing stored to check them against: fail closed
	}
	matched := map[string]bool{}
	start, end := s.Cfg.Tools.PIMWindow()
	windowHit := false
	if s.M != nil {
		defer func() {
			if windowHit {
				s.M.PIMWindowAssumed.Inc() // once per request, content-free
			}
		}()
	}
	before := ""
	for page := 0; page < 20; page++ {
		ms, more, err := s.Store.ListMessages(r.Context(), user(r), pr.threadID, 500, before)
		if err != nil {
			return protected, true
		}
		for _, m := range ms {
			if m.Role != "assistant" {
				continue
			}
			t := strings.TrimSpace(m.Content)
			if t == "" {
				continue
			}
			reads, assumed := readsPersonal(m, start, end)
			if reads {
				protected = append(protected, m.Content)
				private = true
			}
			if assumed {
				windowHit = true
			}
			matched[t] = true
		}
		if !more || len(ms) == 0 {
			break
		}
		before = ms[0].ID
	}
	for _, t := range want {
		if exposed && !matched[t] {
			private = true // not a message of this thread: unverifiable (G1 ⓑ)
			break
		}
	}
	return protected, private
}
