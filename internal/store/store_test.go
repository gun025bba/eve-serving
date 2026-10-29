package store

import (
	"encoding/json"
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

// The payload hash is over a canonical form: key order (what Postgres JSONB
// changes on storage) and insignificant whitespace do not matter, content does.
func TestPayloadHash_CanonicalKeyOrder(t *testing.T) {
	a := json.RawMessage(`{"op":"add","items":[{"text":"x","kind":"fact"}],"n":1.50}`)
	b := json.RawMessage(`{ "items": [ {"kind":"fact", "text":"x"} ], "n":1.50, "op":"add" }`)
	if PayloadHash(a) != PayloadHash(b) || !PayloadHashMatches(b, PayloadHash(a)) {
		t.Fatal("key order or whitespace changed the hash")
	}
	for _, c := range []string{
		`{"op":"add","items":[{"text":"y","kind":"fact"}],"n":1.50}`,
		`{"op":"add","items":[{"text":"x","kind":"fact"},{"text":"x","kind":"fact"}],"n":1.50}`, // array order and length matter
		`{"op":"add","items":[{"text":"x","kind":"fact"}],"n":1.5}`,
	} {
		if PayloadHash(json.RawMessage(c)) == PayloadHash(a) {
			t.Fatalf("different payload hashes equal: %s", c)
		}
	}
	if PayloadHash(json.RawMessage(`{"a":"<&>"}`)) != PayloadHash(json.RawMessage(`{"a":"\u003c\u0026\u003e"}`)) {
		t.Fatal("escaping must not change the hash")
	}
	// legacy hashes (compact bytes in stored order) still verify
	if !PayloadHashMatches(a, LegacyPayloadHash(a)) || PayloadHashMatches(a, LegacyPayloadHash(json.RawMessage(`{"x":1}`))) {
		t.Fatal("legacy verification")
	}
	// not JSON / trailing data: hashed as raw bytes, never panics
	if PayloadHash(json.RawMessage(`{"a":1} {"b":2}`)) == PayloadHash(json.RawMessage(`{"a":1}`)) || PayloadHash(json.RawMessage(`nope`)) == "" {
		t.Fatal("raw fallback")
	}
}
