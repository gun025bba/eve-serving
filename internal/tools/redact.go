package tools

import (
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
