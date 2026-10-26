package tools

import (
	"encoding/json"
	"strings"
	"sync"
)

// Redactor replaces known secret values with [REDACTED] before text leaves the
// gateway (model input, logs, DB, errors, SSE). In M1 no tool declares secrets,
// so the set is empty and Redact is the identity function.
type Redactor struct {
	mu   sync.RWMutex
	vals []string
}

const redacted = "[REDACTED]"

// Add registers a secret value (e.g. a freshly refreshed token) immediately.
// Values shorter than 8 bytes are ignored to avoid mangling ordinary text.
func (r *Redactor) Add(v string) {
	if r == nil || len(v) < 8 {
		return
	}
	r.mu.Lock()
	r.vals = append(r.vals, v)
	r.mu.Unlock()
}

// Redact returns s with every registered secret replaced.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.vals {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, redacted)
		}
	}
	return s
}

// RedactJSON redacts every string (keys and values) inside a JSON document.
// Used for model/tool-derived sub-documents only; callers pick which fields
// pass through it by struct origin, never by key name.
func (r *Redactor) RedactJSON(raw []byte) []byte {
	if r == nil || len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []byte(`null`)
	}
	out, err := json.Marshal(r.redactAny(v))
	if err != nil {
		return []byte(`null`)
	}
	return out
}

func (r *Redactor) redactAny(v any) any {
	switch x := v.(type) {
	case string:
		return r.Redact(x)
	case []any:
		for i := range x {
			x[i] = r.redactAny(x[i])
		}
		return x
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[r.Redact(k)] = r.redactAny(e)
		}
		return m
	}
	return v
}
