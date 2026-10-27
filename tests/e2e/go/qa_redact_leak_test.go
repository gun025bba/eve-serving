package memory

// QA (PR #35): log-leak checks beyond the PR test.
import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/logging"
)

func TestQA_RedactorLeakPaths(t *testing.T) {
	var buf bytes.Buffer
	l := logging.New(&buf, logging.DEBUG)
	l.SetRedactor(RedactLog)
	item := "고양이를 키운다"
	RegisterText(item) // as store load/Clean do in production (PR #39)
	long := strings.Repeat("주말마다 북한산 둘레길을 걷는다 ", 20)
	RegisterText(long)
	secret := "sk-abcdefghijklmnopqrstuv"
	hex := strings.Repeat("0f1e2d3c", 8)
	cases := []struct {
		name string
		log  func()
		bad  []string
	}{
		{"wrapped error with secret", func() { l.Error("memory_error", "err", fmt.Errorf("audit: %w", errors.New("key "+hex))) }, []string{hex}},
		{"map field", func() { l.Info("x", "m", map[string]string{"k": secret}) }, []string{secret}},
		{"secret in message", func() { l.Warn("bad " + secret) }, []string{secret}},
		{"bare item text in error (no block)", func() { l.Error("memory_error", "err", errors.New("write item \""+item+"\": disk full")) }, []string{item}},
		{"bare item text field", func() { l.Info("memory_write", "text", item) }, []string{item}},
		{"registered item text truncated in err", func() { l.Error("memory_error", "err", "write: "+long[:150]) }, []string{long[:60]}},
		{"secret in key name", func() { l.Info("x", secret, 1) }, []string{secret}},
	}
	for _, c := range cases {
		buf.Reset()
		c.log()
		for _, b := range c.bad {
			if strings.Contains(buf.String(), b) {
				t.Errorf("LEAK %s: %s", c.name, strings.TrimSpace(buf.String()))
			}
		}
	}
	buf.Reset()
	l.SetRedactor(nil)
	l.Info("x", "k", secret)
	if !strings.Contains(buf.String(), secret) {
		t.Error("off mode should not redact")
	}
}
