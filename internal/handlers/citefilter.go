package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gun025bba/eve-serving/internal/cite"
)

// filterChunk rewrites delta.content of one SSE data line through cf.
//
//   - Lines whose content passes through unchanged keep their original bytes.
//   - On a chunk carrying a finish_reason, the filter is flushed and the held
//     tail is appended to that chunk's content, so it arrives before finish.
//   - Lines that are not parseable JSON pass through unfiltered. They also
//     contribute nothing to the saved copy (parseSSELine skips them), so the
//     shown and saved text stay consistent.
//
// The saved copy is always built by parsing the line this returns.
func filterChunk(l []byte, cf *cite.Filter) []byte {
	ls := strings.TrimRight(string(l), "\r")
	if !strings.HasPrefix(ls, "data: ") {
		return l
	}
	var v map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(ls[6:])), &v) != nil {
		return l
	}
	cs, _ := v["choices"].([]any)
	changed := false
	for _, c := range cs {
		cm, _ := c.(map[string]any)
		d, _ := cm["delta"].(map[string]any)
		str, has := d["content"].(string)
		o := str
		if has {
			o = cf.Write(str)
		}
		if fr, _ := cm["finish_reason"].(string); fr != "" {
			o += cf.Flush()
		}
		if o != str {
			if d == nil {
				d = map[string]any{}
				cm["delta"] = d
			}
			d["content"] = o
			changed = true
		}
	}
	if !changed {
		return l
	}
	b, _ := json.Marshal(v)
	return append([]byte("data: "), b...)
}

// tailChunk carries text the filter still held when the stream ended
// without a finish_reason chunk.
func tailChunk(tail string, model any) []byte {
	b, _ := json.Marshal(map[string]any{"id": "cite-tail", "object": "chat.completion.chunk", "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": tail}, "finish_reason": nil}}})
	return append([]byte("data: "), b...)
}

// citeObserve logs citations_removed and bumps the counter.
func (s *Server) citeObserve(r *http.Request, path string, removed int) {
	if removed > 0 {
		s.M.CitesRemoved.Add(float64(removed))
	}
	s.Log.Info("citation_filter", "request_id", rid(r), "path", path, "citations_removed", removed)
}

// filterMessageContent filters choices[*].message.content of a non-streamed
// response in place; it reports whether anything changed.
func filterMessageContent(v map[string]any, valid []int) (bool, int) {
	cs, _ := v["choices"].([]any)
	changed, removed := false, 0
	for _, c := range cs {
		cm, _ := c.(map[string]any)
		m, _ := cm["message"].(map[string]any)
		if str, ok := m["content"].(string); ok {
			o, n := cite.Sanitize(str, valid)
			removed += n
			if o != str {
				m["content"] = o
				changed = true
			}
		}
	}
	return changed, removed
}
