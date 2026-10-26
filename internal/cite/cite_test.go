package cite

import "testing"

func TestSanitize(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		valid   []int
		want    string
		removed int
	}{
		{"no tools removes all", "거절할게요. [1]", nil, "거절할게요. ", 1},
		{"no tools start of text", "[2] 시작", nil, " 시작", 1},
		{"valid kept", "서울은 맑아요 [1].", []int{1, 2}, "서울은 맑아요 [1].", 0},
		{"invalid removed", "맑아요 [3].", []int{1, 2}, "맑아요 .", 1},
		{"mixed", "A [1] B [9] C [2]", []int{1, 2}, "A [1] B  C [2]", 1},
		{"consecutive", "끝.[1][2][5]", []int{2}, "끝.[2]", 2},
		{"consecutive all invalid", "끝 [1][2]", nil, "끝 ", 2},
		{"spaced run", "끝 [1] [3]", []int{1}, "끝 [1] ", 1},
		{"array index kept", "a[1] and x[3]", nil, "a[1] and x[3]", 0},
		{"hangul before kept", "값[1]", nil, "값[1]", 0},
		{"korean punctuation", "좋아요。[1] 네、[2]「[3]」", nil, "좋아요。 네、「」", 3},
		{"inline code", "use `a [1]` here [1]", nil, "use `a [1]` here ", 1},
		{"double backtick inline", "``x ` [1]`` [1]", nil, "``x ` [1]`` ", 1},
		{"fenced code", "x [1]\n```go\nv := m [1]\n```\ny [1]", nil, "x \n```go\nv := m [1]\n```\ny ", 2},
		{"tilde fence", "~~~\n [4]\n~~~\n [4]", nil, "~~~\n [4]\n~~~\n ", 1},
		{"unclosed fence", "```\n [1]", nil, "```\n [1]", 0},
		{"four digits not marker", "x [1234]", nil, "x [1234]", 0},
		{"non digit", "x [a] [] [1a]", nil, "x [a] [] [1a]", 0},
		{"unterminated", "x [12", nil, "x [12", 0},
		{"leading zero", "x [01]", []int{1}, "x [01]", 0},
		{"newline before", "줄\n[1]", nil, "줄\n", 1},
		{"after paren", "(출처 [1])", nil, "(출처 )", 1},
		{"link like", "[1](http://example.invalid)", nil, "(http://example.invalid)", 1},
		{"empty", "", nil, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := Sanitize(c.in, c.valid)
			if got != c.want || n != c.removed {
				t.Errorf("Sanitize(%q) = %q,%d want %q,%d", c.in, got, n, c.want, c.removed)
			}
		})
	}
}

// Every split point (and byte-by-byte) must equal the whole-text result.
func TestStreamingSplits(t *testing.T) {
	inputs := []struct {
		in    string
		valid []int
	}{
		{"답 [1][2] 그리고 [3]。[12]", []int{2}},
		{"x `[1]` ``` [1]\n```\n [1]\n```\n[1] a[1]", nil},
		{"끝.[1] [3] 한글[2]", []int{3}},
		{"~~~\n[1]\n~~~ [1]", nil},
	}
	for _, c := range inputs {
		want, wn := Sanitize(c.in, c.valid)
		b := []byte(c.in)
		for i := 0; i <= len(b); i++ {
			for j := i; j <= len(b); j++ {
				f := New(c.valid)
				got := f.Write(string(b[:i])) + f.Write(string(b[i:j])) + f.Write(string(b[j:])) + f.Flush()
				if got != want || f.Removed != wn {
					t.Fatalf("split %d/%d of %q: %q,%d want %q,%d", i, j, c.in, got, f.Removed, want, wn)
				}
			}
		}
		f := New(c.valid)
		got := ""
		for _, x := range b {
			got += f.Write(string([]byte{x}))
		}
		got += f.Flush()
		if got != want {
			t.Fatalf("bytewise %q: %q want %q", c.in, got, want)
		}
	}
}
