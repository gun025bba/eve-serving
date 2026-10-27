package memory

import (
	"strings"
	"testing"
)

func TestRedactLog_EscapedBlockAndEntryLine(t *testing.T) {
	for _, s := range []string{
		`prompt: \u003cuser_memory nonce="x"\u003e`,
		`{"content":"\\u003cuser_memory nonce=\"a\"\\u003e"}`,
		`&lt;user_memory nonce="a"&gt;`,
		`{"kind":"fact","text":"something"}`,
		`{\"kind\":\"pref\",\"text\":\"x\"}`,
	} {
		if got := RedactLog(s); got != redactedMemory {
			t.Errorf("%q -> %q", s, got)
		}
	}
}

func TestRedactLog_RegisteredItemText(t *testing.T) {
	RegisterText("주말마다 북한산에 간다")
	got := RedactLog(`error: write failed for text 주말마다 북한산에 간다`)
	if strings.Contains(got, "북한산") || !strings.Contains(got, redactedMemory) {
		t.Fatal(got)
	}
	if RedactLog("평범한 로그") != "평범한 로그" {
		t.Fatal("ordinary text changed")
	}
}

func TestRedactLog_CleanRegistersCandidate(t *testing.T) {
	Clean("매일 아침 수영을 한다")
	if strings.Contains(RedactLog(`"text":"매일 아침 수영을 한다"`), "수영") {
		t.Fatal("candidate text not masked")
	}
}

func TestRedactLog_LengthCap(t *testing.T) {
	got := RedactLog(strings.Repeat("lorem ipsum ", 500))
	if len(got) > MaxLogValueBytes+32 || !strings.HasSuffix(got, "[truncated]") {
		t.Fatal(len(got))
	}
}
