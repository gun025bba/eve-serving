package handlers

// QA (PR #34 aa843e1): direct API rejects sensitive text with 422 sensitive_category,
// stores nothing and never echoes the text.
import (
	"strings"
	"testing"
)

func TestQA_SensitiveCategory422(t *testing.T) {
	e, fs := newMemEnv(t, true, &failLedger{})
	for _, txt := range []string{"나 HIV 양성이야", "우리 아빠 당뇨 있어", "지지 정당은 녹색당이야"} {
		r, b := e.call(t, "alice@example", "POST", "/v1/memory/items", `{"kind":"fact","text":"`+txt+`"}`)
		if r.StatusCode != 422 || !strings.Contains(string(b), "sensitive_category") || strings.Contains(string(b), txt) {
			t.Errorf("%q: %d %s", txt, r.StatusCode, b)
		}
	}
	r, b := e.call(t, "alice@example", "POST", "/v1/memory/items", `{"kind":"fact","text":"key sk-abcdefghijklmnopqrstuv"}`)
	if r.StatusCode != 422 || !strings.Contains(string(b), "secret_detected") {
		t.Errorf("secret: %d %s", r.StatusCode, b)
	}
	if d, _ := fs.Load(uidOf(t, e, "alice@example")); len(d.Items) != 0 {
		t.Errorf("stored %d", len(d.Items))
	}
}
