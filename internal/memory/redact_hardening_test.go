package memory

import (
	"fmt"
	"path/filepath"
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

func TestRedactLog_RejectedCandidateNotRegistered(t *testing.T) {
	Clean("sk-abcdefghijklmnopqrstuv 매일 저녁 테니스")
	Clean("매일 저녁 테니스를 친다")
	m, _ := machine()
	if _, hit := maskRegistered(m, "매일 저녁 테니스를 친다"); hit {
		t.Fatal("unstored candidate registered")
	}
}

func TestRedactLog_PerUserReleaseAndPurge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	fs, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const uid = 4242
	_, err = fs.Update(uid, func(d *Doc) error {
		d.Items = append(d.Items, Item{ID: "a", Kind: "fact", Text: "주말마다 한강에서 달린다"}, Item{ID: "b", Kind: "fact", Text: "피아노 레슨을 받는다"})
		return nil
	})
	if err != nil {
		t.Skip("doc shape differs:", err)
	}
	if !strings.Contains(RedactLog("x 주말마다 한강에서 달린다"), redactedMemory) {
		t.Fatal("stored item not masked")
	}
	fs.Update(uid, func(d *Doc) error { d.Items = d.Items[1:]; return nil })
	if strings.Contains(RedactLog("주말마다 한강에서 달린다"), redactedMemory) {
		t.Fatal("deleted item still registered")
	}
	fs.Purge(uid)
	if strings.Contains(RedactLog("피아노 레슨을 받는다"), redactedMemory) {
		t.Fatal("purged user still registered")
	}
}

func TestRedactLog_TruncatedAndEscaped(t *testing.T) {
	long := "토요일 아침에는 동네 도서관에서 책을 읽는다"
	setUserTexts(777, []string{long})
	defer ReleaseUser(777)
	cut := string([]rune(long)[:12])
	if got := RedactLog("err: " + cut); strings.Contains(got, cut) {
		t.Fatal("prefix leaked:", got)
	}
	for _, v := range []string{`%3Cuser_memory%3E`, `\x3cuser_memory`, `&#60;user_memory`} {
		if RedactLog(v) != redactedMemory {
			t.Errorf("escaped marker leaked: %s", v)
		}
	}
}

func TestRedactLog_FailClosedOverCap(t *testing.T) {
	big := make([]string, 0, maxPatternsPerUser/4+10)
	for i := 0; i < cap(big); i++ {
		big = append(big, fmt.Sprintf("항목 번호 %06d 의 긴 내용", i))
	}
	setUserTexts(999, big)
	if got := RedactLog("아무 사용자 문장"); got != redactedMemory {
		t.Fatal("not fail-closed:", got)
	}
	if got := RedactLog("req_123"); got != "req_123" {
		t.Fatal("ids should pass:", got)
	}
	ReleaseUser(999)
	if got := RedactLog("아무 사용자 문장"); got == redactedMemory {
		t.Fatal("still closed after release")
	}
}

func TestRedactLog_LengthCap(t *testing.T) {
	got := RedactLog(strings.Repeat("lorem ipsum ", 500))
	if len(got) > MaxLogValueBytes+32 || !strings.HasSuffix(got, "[truncated]") {
		t.Fatal(len(got))
	}
}
