package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONLines(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, INFO)
	l.Debug("hidden")
	l.Info("chat_completion", "request_id", "ab", "total_ms", 12, "ttft_ms", nil, "msg2", "한글")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines %q", lines)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	if m["level"] != "INFO" || m["msg"] != "chat_completion" || m["total_ms"].(float64) != 12 || m["ttft_ms"] != nil {
		t.Fatal(m)
	}
	l.SetLevel(DEBUG)
	l.Debug("shown")
	if !strings.Contains(buf.String(), "shown") {
		t.Fatal("debug not shown")
	}
	if _, err := ParseLevel("nope"); err == nil {
		t.Fatal("want err")
	}
}
