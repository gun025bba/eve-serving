package memory

import (
	"strings"

	"github.com/gun025bba/eve-serving/internal/tools"
)

const (
	redactedSecret = "[REDACTED]"
	redactedMemory = "[REDACTED:memory]"
)

// RedactLog is the log Redactor used while memory is on (memory.md §9.1):
//   - any value that carries the injected <user_memory ...> block or its
//     preamble is replaced whole, so memory text never reaches logs;
//   - registered secret values (tools Redactor) and the shared
//     SecretPatterns list are masked.
func RedactLog(s string) string {
	if s == "" {
		return s
	}
	if strings.Contains(s, "<user_memory") || strings.Contains(s, "</user_memory") || strings.Contains(s, Preamble) {
		return redactedMemory
	}
	s = tools.Default().Redact.Redact(s)
	for _, p := range tools.SecretPatterns {
		s = p.Re.ReplaceAllString(s, redactedSecret)
	}
	return s
}
