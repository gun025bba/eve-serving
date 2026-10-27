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
		{"secret in key name", func() { l.Info("x", secret, 1) }, []string{secret}},
	}
	for _, c := range cases {
		buf.Reset()
		c.log()
		for _, b := range c.bad {
			if strings.Contains(buf.String(), b) {
				t.Logf("LEAK %s: %s", c.name, strings.TrimSpace(buf.String()))
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
