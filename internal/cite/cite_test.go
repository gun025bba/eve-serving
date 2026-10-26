package cite

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/citation_cases.jsonl
var refCases string

type refCase struct {
	ID       string `json:"id"`
	Desc     string `json:"desc"`
	Text     string `json:"text"`
	Valid    []int  `json:"valid"`
	Expected string `json:"expected"`
	Removed  int    `json:"removed"`
}

func loadRef(t *testing.T) []refCase {
	var out []refCase
	sc := bufio.NewScanner(strings.NewReader(refCases))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var c refCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if len(out) != 15 {
		t.Fatalf("want 15 reference cases, got %d", len(out))
	}
	return out
}

// checkSplits asserts every 2-cut split and byte-by-byte feeding match.
func checkSplits(t *testing.T, in string, valid []int, want string, removed int) {
	t.Helper()
	b := []byte(in)
	for i := 0; i <= len(b); i++ {
		for j := i; j <= len(b); j++ {
			f := New(valid)
			got := f.Write(string(b[:i])) + f.Write(string(b[i:j])) + f.Write(string(b[j:])) + f.Flush()
			if got != want || f.Removed != removed {
				t.Fatalf("split %d/%d of %q: %q,%d want %q,%d", i, j, in, got, f.Removed, want, removed)
			}
		}
	}
	f := New(valid)
	got := ""
	for _, x := range b {
		got += f.Write(string([]byte{x}))
	}
	if got += f.Flush(); got != want || f.Removed != removed {
		t.Fatalf("bytewise %q: %q,%d want %q,%d", in, got, f.Removed, want, removed)
	}
}

func TestReferenceCases(t *testing.T) {
	for _, c := range loadRef(t) {
		t.Run(c.ID, func(t *testing.T) {
			got, n := Sanitize(c.Text, c.Valid)
			if got != c.Expected || n != c.Removed {
				t.Fatalf("%s (%s): got %q,%d want %q,%d", c.ID, c.Desc, got, n, c.Expected, c.Removed)
			}
			checkSplits(t, c.Text, c.Valid, c.Expected, c.Removed)
		})
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		valid   []int
		want    string
		removed int
	}{
		{"no tools removes all", "거절할게요. [1]", nil, "거절할게요.", 1},
		{"valid kept with space", "맑아요 [1].", []int{1}, "맑아요 [1].", 0},
		{"invalid removes one space", "맑아요 [3].", []int{1}, "맑아요.", 1},
		{"only one space removed", "맑아요  [3].", nil, "맑아요 .", 1},
		{"tab lead removed", "맑아요\t[3].", nil, "맑아요.", 1},
		{"partial run keeps space", "끝 [1][5]", []int{1}, "끝 [1]", 1},
		{"spaced markers are separate runs", "끝 [1] [3]", []int{1}, "끝 [1]", 1},
		{"zero always removed", "x [0]", []int{0, 1}, "x", 1},
		{"hangul is citation", "값[1]", nil, "값", 1},
		{"index forms kept", "a[1] _[2] 9[3] )[4] ][5]", nil, "a[1] _[2] 9[3] )[4] ][5]", 0},
		{"punctuation before is citation", "다.[1]「[2]」(출처[3])", nil, "다.「」(출처)", 3},
		{"non-ascii letter is citation", "é[1]", nil, "é", 1},
		{"joins text", "A。[9]B", nil, "A。B", 1},
		{"long number", "x [123456789012]", nil, "x", 1},
		{"fence mid line", "a ```[1]``` [1]", nil, "a ```[1]```", 1},
		{"after code span is start", "`x`[1]", nil, "`x`", 1},
		{"lone backtick is text", "a ` b [1]\nc", nil, "a ` b\nc", 1},
		{"double backtick empty span", "``[1]", nil, "``", 1},
		{"tilde not special", "~~~\n [4]", nil, "~~~\n", 1},
		{"failed marker then marker", "[1[2]", nil, "[1[2]", 0},
		{"unterminated", "x [12", nil, "x [12", 0},
		{"empty brackets", "x [] [a]", nil, "x [] [a]", 0},
		{"invalid utf8 normalized", "a\xff [1]", nil, "a\uFFFD", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := Sanitize(c.in, c.valid)
			if got != c.want || n != c.removed {
				t.Fatalf("Sanitize(%q) = %q,%d want %q,%d", c.in, got, n, c.want, c.removed)
			}
			checkSplits(t, c.in, c.valid, c.want, c.removed)
		})
	}
}

// Long backtick runs must not be buffered: output keeps up with input and
// total time stays linear.
func TestLongBacktickRunProgress(t *testing.T) {
	start := time.Now()
	f := New(nil)
	fed, emitted := 0, 0
	chunk := strings.Repeat("`", 5)
	for i := 0; i < 20000; i++ {
		emitted += len(f.Write(chunk))
		fed += len(chunk)
		if fed-emitted > 2 {
			t.Fatalf("after %d bytes only %d emitted", fed, emitted)
		}
	}
	emitted += len(f.Flush())
	if emitted != fed {
		t.Fatalf("emitted %d of %d", emitted, fed)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}

func TestLargeInputLinear(t *testing.T) {
	in := strings.Repeat("문장입니다 [3][1] a[2] `c [4]` ", 20000)
	start := time.Now()
	f := New([]int{1})
	for i := 0; i < len(in); i += 7 {
		j := i + 7
		if j > len(in) {
			j = len(in)
		}
		f.Write(in[i:j])
	}
	f.Flush()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}
