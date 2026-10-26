package store

import (
	"strings"
	"testing"
)

func TestSnippetEscapesAndMarks(t *testing.T) {
	got := Snippet(`<b>안녕</b> 하세요 Hello & bye`, "안녕 hello", 200)
	want := `&lt;b&gt;<mark>안녕</mark>&lt;/b&gt; 하세요 <mark>Hello</mark> &amp; bye`
	if got != want {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("가", 300) + "검색어" + strings.Repeat("나", 300)
	s := Snippet(long, "검색어", 60)
	if !strings.Contains(s, "<mark>검색어</mark>") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Fatalf("window: %q", s)
	}
	if strings.Count(strings.ReplaceAll(strings.ReplaceAll(s, "<mark>", ""), "</mark>", ""), "<") != 0 {
		t.Fatal("stray tag")
	}
}

func TestPreview(t *testing.T) {
	if Preview("a\n b   c", 10) != "a b c" || Preview("가나다라마", 3) != "가나다…" {
		t.Fatal(Preview("가나다라마", 3))
	}
}
