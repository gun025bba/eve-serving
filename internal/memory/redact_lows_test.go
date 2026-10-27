package memory

import (
	"fmt"
	"strings"
	"testing"
)

func TestRedact_EscapeVariants(t *testing.T) {
	for _, s := range []string{"&#060;user_memory&#062;", "&#X3C;user_memory>", "%253Cuser_memory%253E", "%25253Cuser_memory", "&#x003c;/user_memory"} {
		if got := RedactLog(s); got != redactedMemory {
			t.Errorf("%q -> %q", s, got)
		}
	}
}

func TestRedact_OverCapIsPerUser(t *testing.T) {
	defer ReleaseUser(901)
	defer ReleaseUser(902)
	var d Doc
	for i := 0; i < MaxItems; i++ {
		d.Items = append(d.Items, Item{Text: distinctText(i, 290)})
	}
	registerDoc(901, d)
	registerDoc(902, Doc{Items: []Item{{Text: "bob의 비밀 일기 내용입니다"}}})
	if _, over := machine(); over {
		t.Fatal("global fail-closed triggered by one user")
	}
	if got := RedactLog("ordinary log line with spaces"); got != "ordinary log line with spaces" {
		t.Fatalf("other values masked: %q", got)
	}
	cp := []rune(d.Items[5].Text)
	if got := RedactLog("x " + string(cp[40:60])); !strings.Contains(got, "[REDACTED:memory]") {
		t.Fatalf("over-cap user text not masked: %q", got)
	}
	if got := RedactLog("bob의 비밀 일기 내용입니다"); !strings.Contains(got, "[REDACTED:memory]") {
		t.Fatalf("bob not masked: %q", got)
	}
}

func TestRegistryCapHookAndStats(t *testing.T) {
	defer ReleaseUser(903)
	var got []string
	OnRegistryCap = func(uid int64, level string) { got = append(got, fmt.Sprintf("%d %s", uid, level)) }
	defer func() { OnRegistryCap = nil }()
	var d Doc
	for i := 0; i < MaxItems; i++ {
		d.Items = append(d.Items, Item{Text: distinctText(i, 290)})
	}
	registerDoc(903, d)
	registerDoc(903, d) // no repeat warning
	if len(got) != 1 || got[0] != "903 degraded" {
		t.Fatal(got)
	}
	if dg, fc := RegistryStats(); dg < 1 || fc != 0 {
		t.Fatal(dg, fc)
	}
}

// distinctText yields n Hangul runes unique per (seed, position).
func distinctText(seed, n int) string {
	r := make([]rune, n)
	x := uint32(seed*7919 + 1)
	for j := range r {
		x = x*1664525 + 1013904223
		r[j] = rune(0xAC00 + int(x>>8)%11172)
	}
	return string(r)
}
