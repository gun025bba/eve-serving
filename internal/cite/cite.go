// Package cite strips citation markers ([n]) that do not refer to a search
// result actually returned during the current request.
//
// Matching rule (kept identical to the eval runner's emulation):
//   - A marker is '[' + 1-3 ASCII digits + ']'.
//   - It is a candidate only if the rune immediately before '[' (in the
//     ORIGINAL text, even if that rune belonged to a removed marker) is the
//     start of text, Unicode whitespace (unicode.IsSpace) or Unicode
//     punctuation (unicode.IsPunct). So "a[1]" and "값[1]" are kept, while
//     " [1]", "다.[1]", "요。[1]" and the second marker of "[1][2]" are candidates.
//   - A candidate is removed if its number (decimal, leading zeros ignored)
//     is not in the valid set. An empty set removes every candidate.
//   - Only the marker itself is removed; surrounding whitespace is untouched.
//   - Text inside fenced code blocks (a line whose first non-space content is
//     a run of >=3 backticks or tildes, up to the matching closing fence) and
//     inline code spans (backtick run of length k up to the next run of
//     exactly k backticks) is never modified.
//
// Filter is chunk-invariant: feeding text in any split produces the same
// output as feeding it whole.
package cite

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxDigits = 3

type Filter struct {
	valid map[int]bool
	// Removed counts markers dropped so far.
	Removed int

	buf       []rune // unprocessed lookahead (pending marker / delimiter run)
	partial   []byte // incomplete UTF-8 tail
	prev      rune   // previous original rune; 0 = start
	lineStart bool   // only spaces seen since last newline
	fence     rune   // '`' or '~' when inside a fenced block
	fenceLen  int
	inline    int // >0: inside inline code opened by this many backticks
}

// New returns a filter that keeps markers whose number is in valid.
func New(valid []int) *Filter {
	f := &Filter{valid: map[int]bool{}, lineStart: true}
	for _, n := range valid {
		f.valid[n] = true
	}
	return f
}

// Sanitize filters a complete text and returns it with the removed count.
func Sanitize(s string, valid []int) (string, int) {
	f := New(valid)
	out := f.Write(s) + f.Flush()
	return out, f.Removed
}

// Write feeds a chunk and returns text that is safe to emit now.
func (f *Filter) Write(s string) string {
	b := append(f.partial, s...)
	f.partial = nil
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(b) {
			f.partial = append([]byte{}, b...)
			break
		}
		f.buf = append(f.buf, r)
		b = b[size:]
	}
	var out strings.Builder
	f.process(&out, false)
	return out.String()
}

// Flush emits everything still buffered (an unterminated marker is kept).
func (f *Filter) Flush() string {
	var out strings.Builder
	f.process(&out, true)
	out.Write(f.partial)
	f.partial = nil
	return out.String()
}

func isBoundary(r rune) bool { return r == 0 || unicode.IsSpace(r) || unicode.IsPunct(r) }

func (f *Filter) emit(out *strings.Builder, rs []rune) {
	for _, r := range rs {
		out.WriteRune(r)
		f.advance(r)
	}
}

func (f *Filter) advance(r rune) {
	f.prev = r
	if r == '\n' {
		f.lineStart = true
	} else if r != ' ' && r != '\t' {
		f.lineStart = false
	}
}

func runLen(rs []rune, c rune) int {
	n := 0
	for n < len(rs) && rs[n] == c {
		n++
	}
	return n
}

// process consumes f.buf; when final is false it stops at an ambiguous tail.
func (f *Filter) process(out *strings.Builder, final bool) {
	for len(f.buf) > 0 {
		r := f.buf[0]
		// Inside a fenced block: only look for the closing fence.
		if f.fence != 0 {
			if r == f.fence && f.lineStart {
				n := runLen(f.buf, r)
				if n == len(f.buf) && !final {
					return
				}
				if n >= f.fenceLen {
					f.fence, f.fenceLen = 0, 0
				}
				f.emit(out, f.buf[:n])
				f.buf = f.buf[n:]
				continue
			}
			f.emit(out, f.buf[:1])
			f.buf = f.buf[1:]
			continue
		}
		if r == '`' || (r == '~' && f.inline == 0) {
			n := runLen(f.buf, r)
			if n == len(f.buf) && !final {
				return
			}
			switch {
			case f.inline == 0 && f.lineStart && n >= 3:
				f.fence, f.fenceLen = r, n
			case r == '`' && f.inline == 0:
				f.inline = n
			case r == '`' && f.inline == n:
				f.inline = 0
			}
			f.emit(out, f.buf[:n])
			f.buf = f.buf[n:]
			continue
		}
		if f.inline > 0 || r != '[' || !isBoundary(f.prev) {
			f.emit(out, f.buf[:1])
			f.buf = f.buf[1:]
			continue
		}
		// Candidate '[': need '[' digits{1,3} ']'.
		i, num := 1, 0
		for i < len(f.buf) && i <= maxDigits && f.buf[i] >= '0' && f.buf[i] <= '9' {
			num = num*10 + int(f.buf[i]-'0')
			i++
		}
		if i == len(f.buf) && i <= maxDigits && !final {
			return // may still become a marker
		}
		if i > 1 && i < len(f.buf) && f.buf[i] == ']' {
			m := f.buf[:i+1]
			if f.valid[num] {
				f.emit(out, m)
			} else {
				f.Removed++
				for _, x := range m { // track original text, emit nothing
					f.advance(x)
				}
			}
			f.buf = f.buf[i+1:]
			continue
		}
		f.emit(out, f.buf[:1])
		f.buf = f.buf[1:]
	}
}
