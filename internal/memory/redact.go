package memory

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/gun025bba/eve-serving/internal/tools"
)

const (
	redactedSecret = "[REDACTED]"
	redactedMemory = "[REDACTED:memory]"
	// MaxLogValueBytes caps every redacted log value (Low: length cap).
	MaxLogValueBytes = 4096
	// minRegisteredRunes: shorter item texts are not masked (would mangle logs).
	minRegisteredRunes = 2
	maxRegistered      = 50000
)

// entryLine matches the injected per-item line form ({"kind":..,"text":..})
// even when it appears without the surrounding block.
var entryLine = regexp.MustCompile(`\{\s*"kind"\s*:\s*"[a-z_]+"\s*,\s*"text"\s*:`)

var reg struct {
	mu    sync.RWMutex
	set   map[string]struct{}
	texts []string // longest first
}

// RegisterText adds a memory item text to the log mask set (process-local,
// never persisted). Item text that leaks into an error string or a "text"
// field outside the block is then masked as [REDACTED:memory].
func RegisterText(t string) {
	t = strings.TrimSpace(t)
	if utf8.RuneCountInString(t) < minRegisteredRunes {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.set == nil {
		reg.set = map[string]struct{}{}
	}
	if _, ok := reg.set[t]; ok || len(reg.set) >= maxRegistered {
		return
	}
	reg.set[t] = struct{}{}
	reg.texts = append(reg.texts, t)
	sort.SliceStable(reg.texts, func(i, j int) bool { return len(reg.texts[i]) > len(reg.texts[j]) })
}

func registerDoc(d Doc) {
	for _, it := range d.Items {
		RegisterText(it.Text)
	}
}

// unescapeForMatch undoes the JSON/HTML escapes that could hide markers
// (\u003c, \u003e, \u0026, \", \\) so matching sees the plain form.
var unescaper = strings.NewReplacer(`\u003c`, "<", `\u003C`, "<", `\u003e`, ">", `\u003E`, ">", `\u0026`, "&",
	`\\u003c`, "<", `\\u003e`, ">", `&lt;`, "<", `&gt;`, ">", `\"`, `"`, `\\"`, `"`)

// RedactLog is the log Redactor (memory.md §9.1). It is installed always;
// memory-specific masks only ever match while memory produced data.
//   - a value carrying the <user_memory> block, its preamble, or the per-item
//     line form (also when JSON-escaped) is replaced whole;
//   - registered memory item texts are masked as [REDACTED:memory];
//   - registered secret values and SecretPatterns are masked as [REDACTED];
//   - the result is capped at MaxLogValueBytes.
func RedactLog(s string) string {
	if s == "" {
		return s
	}
	u := unescaper.Replace(s)
	if strings.Contains(u, "<user_memory") || strings.Contains(u, "</user_memory") || strings.Contains(u, Preamble) || entryLine.MatchString(u) {
		return redactedMemory
	}
	reg.mu.RLock()
	for _, t := range reg.texts {
		if strings.Contains(s, t) {
			s = strings.ReplaceAll(s, t, redactedMemory)
		}
		if u != s && strings.Contains(u, t) {
			reg.mu.RUnlock()
			return redactedMemory // escaped form of an item text
		}
	}
	reg.mu.RUnlock()
	s = tools.Default().Redact.Redact(s)
	for _, p := range tools.SecretPatterns {
		s = p.Re.ReplaceAllString(s, redactedSecret)
	}
	if len(s) > MaxLogValueBytes {
		cut := MaxLogValueBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…[truncated]"
	}
	return s
}
