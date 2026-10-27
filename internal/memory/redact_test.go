package memory

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/logging"
)

func TestMemoryRedactor_LogsNeverCarryMemoryOrSecrets(t *testing.T) {
	blk, err := Block([]Item{{ID: "a", Kind: "fact", Text: "고양이를 키운다", UpdatedAt: time.Now()}}, time.Now(), DefaultCapTokens)
	if err != nil || blk == "" {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	l := logging.New(&buf, logging.DEBUG)
	l.SetRedactor(RedactLog)
	l.Info("upstream_body", "system", "S\n\n"+blk, "err", errString("token sk-abcdefghijklmnopqrstuv leaked"), "n", 3)
	out := buf.String()
	if strings.Contains(out, "고양이") || strings.Contains(out, "sk-abcdefghijklmnop") {
		t.Fatalf("leak: %s", out)
	}
	if !strings.Contains(out, redactedMemory) || !strings.Contains(out, redactedSecret) || !strings.Contains(out, `"n":3`) {
		t.Fatalf("unexpected: %s", out)
	}
	buf.Reset()
	l.SetRedactor(nil)
	l.Info("x", "k", "sk-abcdefghijklmnopqrstuv")
	if !strings.Contains(buf.String(), "sk-abcdefghijklmnopqrstuv") {
		t.Fatal("redactor not removable (off must be unchanged)")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
