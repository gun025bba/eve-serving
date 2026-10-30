package tools

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ParsedCall is a tool call recovered from model text.
type ParsedCall struct {
	Name string
	Args string
}

var tcRe = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)

// ParseTextToolCalls is the lenient parser for Qwen-style <tool_call>{...}</tool_call>
// blocks left in assistant content (only ever applied to assistant output).
// Returns at most limit calls; truncated/invalid blocks are counted in failures.
func ParseTextToolCalls(content string, limit int) (calls []ParsedCall, rest string, failures int) {
	if !strings.Contains(content, "<tool_call>") {
		return nil, content, 0
	}
	rest = content
	for _, m := range tcRe.FindAllStringSubmatch(content, -1) {
		var v struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(m[1]), &v); err != nil || !Default().IsExposed(v.Name) {
			failures++
			continue
		}
		args := string(v.Arguments)
		var s string
		if json.Unmarshal(v.Arguments, &s) == nil { // arguments given as a JSON string
			args = s
		}
		if len(calls) < limit {
			calls = append(calls, ParsedCall{Name: v.Name, Args: args})
		}
		rest = strings.Replace(rest, m[0], "", 1)
	}
	opens := strings.Count(content, "<tool_call>")
	if closes := strings.Count(content, "</tool_call>"); opens > closes {
		failures += opens - closes // truncated generation
		if i := strings.LastIndex(rest, "<tool_call>"); i >= 0 {
			rest = rest[:i]
		}
	}
	return calls, strings.TrimSpace(rest), failures
}

// StripDisabledToolCalls removes complete <tool_call>{...}</tool_call> blocks
// that name a registered tool the kill switch has switched off. Such a block
// is not a call (ParseTextToolCalls only knows the exposed tools), so without
// this it would reach the user as raw text. Blocks for unknown names and
// truncated blocks are left alone (they are ordinary text), and so is content
// without a match (returned unchanged).
func StripDisabledToolCalls(content string) string {
	if !strings.Contains(content, "<tool_call>") {
		return content
	}
	p := Default()
	out := tcRe.ReplaceAllStringFunc(content, func(block string) string {
		m := tcRe.FindStringSubmatch(block)
		var v struct {
			Name string `json:"name"`
		}
		if m == nil || json.Unmarshal([]byte(m[1]), &v) != nil {
			return block
		}
		if _, registered := p.Reg.Get(v.Name); registered && !p.IsExposed(v.Name) {
			return ""
		}
		return block
	})
	if out == content {
		return content
	}
	return strings.TrimSpace(out)
}

// SanitizeHistory makes client-supplied history safe to re-inject: raw tool
// results (role "tool") are replaced by metadata-only user notes, assistant
// tool_calls are dropped, and any system message that contains web-content
// delimiters is rejected (returns ok=false). Sources from metadata are listed
// by number/title/url only.
func SanitizeHistory(msgs []any) (out []any, ok bool) {
	for _, x := range msgs {
		m, isMap := x.(map[string]any)
		if !isMap {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "tool":
			out = append(out, map[string]any{"role": "user", "content": "[earlier web tool result omitted; only metadata is kept]"})
			continue
		case "system":
			if c, _ := m["content"].(string); delimRe.MatchString(SanitizeUntrusted(c)) {
				return nil, false
			}
		case "assistant":
			if _, has := m["tool_calls"]; has {
				cp := map[string]any{}
				for k, v := range m {
					if k != "tool_calls" {
						cp[k] = v
					}
				}
				if cp["content"] == nil {
					cp["content"] = ""
				}
				m = cp
			}
		}
		out = append(out, m)
	}
	return out, true
}
