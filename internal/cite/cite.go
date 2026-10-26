// Package cite strips citation markers ([n]) that do not refer to a search
// result actually returned during the current request.
//
// The rule mirrors the eval runner's reference (eval/tools/citation_guard.py
// on eval/ko-baseline-v1 @ f89e348) plus the confirmed single-space change:
//
//   - Code is never modified. "```" opens a fenced block (anywhere, not only
//     at line start) that runs to the next "```" or, if unclosed, to the end
//     of the text. A single backtick opens inline code that must close with a
//     backtick on the same line; otherwise the backtick is plain text. "“"
//     is an empty inline span. Tildes are not special.
//   - Outside code, a marker is '[' + one or more digits + ']'. Consecutive
//     markers with nothing between them ([1][2][5]) form one run.
//   - Index form: if the character directly before a run's first '[' is an
//     ASCII letter, ASCII digit, '_', ')' or ']', the whole run is kept
//     (a[1], f(x)[2], m[0][7]). Any other character (Hangul, whitespace,
//     punctuation) or the start of a text piece makes it a citation run. The
//     text right after a code span counts as a start.
//   - In a citation run each marker is checked on its own: kept if its number
//     is in the valid set, removed otherwise. [0] is therefore always removed
//     (sources are numbered from 1), and with no tool calls every citation
//     marker is removed.
//   - If every marker of a run is removed, the single space or tab directly
//     before the run is removed too. If any marker survives, it is kept.
//   - Removing a marker can join the text on either side of it (e.g. "A[9]B"
//     becomes "AB"); this is intended.
//
// Filter is incremental (each rune is processed once) with bounded
// lookahead, and chunk-invariant: any split of the input gives the same
// output as feeding it whole. Invalid UTF-8 becomes U+FFFD. Bounded divergences from the reference:
// a digit sequence longer than maxDigits is not a marker, and an inline-code
// candidate longer than maxInline bytes without a closing backtick is
// resolved as plain text.
package cite

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxDigits = 64
	maxInline = 16 << 10
)

const (
	modeText = iota
	modeFence
	modeInline
)

type Filter struct {
	valid   map[int]bool
	Removed int // markers dropped so far

	out     strings.Builder
	partial []byte // incomplete UTF-8 tail of the last Write

	mode int
	bt   int             // pending backticks (0..2)
	inl  strings.Builder // inline-code candidate content
	prev rune            // previous char in the current text piece; 0 = start
	lead rune            // held single space/tab before a possible run

	mk       []rune // partial marker "[digits"
	mkPrev   rune   // char before the '[' of a run start
	inRun    bool
	runIndex bool
	runKept  bool
	runLead  rune
}

// New returns a filter that keeps citation markers whose number is in valid.
func New(valid []int) *Filter {
	f := &Filter{valid: map[int]bool{}}
	for _, n := range valid {
		if n > 0 { // [0] is never a valid source number
			f.valid[n] = true
		}
	}
	return f
}

// Sanitize filters a complete text and returns it with the removed count.
func Sanitize(s string, valid []int) (string, int) {
	f := New(valid)
	out := f.Write(s) + f.Flush()
	return out, f.Removed
}

// Write feeds a chunk and returns the text that is final now.
func (f *Filter) Write(s string) string {
	b := s
	if len(f.partial) > 0 {
		b = string(f.partial) + s
		f.partial = nil
	}
	for len(b) > 0 {
		r, size := utf8.DecodeRuneInString(b)
		if r == utf8.RuneError && size <= 1 {
			if !utf8.FullRuneInString(b) {
				f.partial = []byte(b)
				break
			}
			// Invalid byte: becomes U+FFFD (same in the shown and saved copy).
			f.feed(utf8.RuneError)
			b = b[1:]
			continue
		}
		f.feed(r)
		b = b[size:]
	}
	return f.take()
}

// Flush resolves all pending state at end of text.
func (f *Filter) Flush() string {
	if len(f.partial) > 0 {
		f.partial = nil
		f.feed(utf8.RuneError) // truncated UTF-8 tail

	}
	f.finish()
	return f.take()
}

func (f *Filter) take() string {
	s := f.out.String()
	f.out.Reset()
	return s
}

func isIndex(c rune) bool {
	return c < utf8.RuneSelf && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == ')' || c == ']')
}

// digitVal returns the value of a Unicode decimal digit (Python \d / int()).
func digitVal(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	v := 0
	for x := r; unicode.IsDigit(x - 1); x-- {
		v++
	}
	return v % 10
}

func (f *Filter) emitLead() {
	if f.lead != 0 {
		f.out.WriteRune(f.lead)
		f.lead = 0
	}
}

func (f *Filter) endRun() {
	// If no marker was kept, runLead is simply dropped with the run.
	f.inRun, f.runLead = false, 0
}

func (f *Filter) failMarker() {
	if f.inRun {
		f.endRun()
	} else {
		f.emitLead()
	}
	for _, x := range f.mk {
		f.out.WriteRune(x)
		f.prev = x
	}
	f.mk = nil
}

func (f *Filter) marker() {
	n := 0
	if len(f.mk)-1 > 9 {
		n = -1
	} else {
		for _, d := range f.mk[1:] {
			n = n*10 + digitVal(d)
		}
	}
	m := string(f.mk) + "]"
	f.mk = nil
	f.prev = ']'
	if !f.inRun {
		f.inRun, f.runKept = true, false
		f.runIndex = isIndex(f.mkPrev)
		f.runLead, f.lead = f.lead, 0
	}
	switch {
	case f.runIndex || f.valid[n]:
		if !f.runKept && f.runLead != 0 {
			f.out.WriteRune(f.runLead)
		}
		f.runKept = true
		f.out.WriteString(m)
	default:
		f.Removed++
	}
}

func (f *Filter) feed(r rune) {
	switch f.mode {
	case modeFence:
		if r == '`' {
			f.bt++
			if f.bt == 3 {
				f.out.WriteString("```")
				f.mode, f.bt, f.prev = modeText, 0, 0
			}
			return
		}
		f.out.WriteString(strings.Repeat("`", f.bt))
		f.bt = 0
		f.out.WriteRune(r)
		return
	case modeInline:
		switch {
		case r == '`':
			f.out.WriteByte('`')
			f.out.WriteString(f.inl.String())
			f.out.WriteByte('`')
			f.inl.Reset()
			f.mode, f.prev = modeText, 0
		case r == '\n' || f.inl.Len() >= maxInline:
			f.failInline()
			f.feed(r)
		default:
			f.inl.WriteRune(r)
		}
		return
	}
	// modeText
	if f.bt > 0 && r != '`' {
		if f.bt == 1 {
			f.bt = 0
			f.mode = modeInline
			f.feed(r)
			return
		}
		f.out.WriteString("``") // empty inline span
		f.bt, f.prev = 0, 0
	}
	if f.mk != nil {
		if unicode.IsDigit(r) && len(f.mk) <= maxDigits {
			f.mk = append(f.mk, r)
			return
		}
		if r == ']' && len(f.mk) > 1 {
			f.marker()
			return
		}
		f.failMarker()
	}
	if f.inRun {
		if r == '[' {
			f.mk = []rune{'['}
			return
		}
		f.endRun()
	}
	switch r {
	case '[':
		f.mk, f.mkPrev = []rune{'['}, f.prev
	case ' ', '\t':
		f.emitLead()
		f.lead, f.prev = r, r
	case '`':
		f.emitLead()
		f.bt++
		if f.bt == 3 {
			f.out.WriteString("```")
			f.mode, f.bt = modeFence, 0
		}
	default:
		f.emitLead()
		f.out.WriteRune(r)
		f.prev = r
	}
}

// failInline: the backtick had no closing on its line, so it is plain text
// and the held content is processed as text.
func (f *Filter) failInline() {
	s := f.inl.String()
	f.inl.Reset()
	f.mode = modeText
	f.out.WriteByte('`')
	f.prev = '`'
	for _, x := range s {
		f.feed(x)
	}
}

func (f *Filter) finish() {
	switch f.mode {
	case modeFence:
		f.out.WriteString(strings.Repeat("`", f.bt))
		f.bt = 0
		return
	case modeInline:
		f.failInline()
	}
	if f.bt > 0 {
		f.out.WriteString(strings.Repeat("`", f.bt)) // lone "`" is text, "``" an empty span
		f.bt, f.prev = 0, 0
	}
	if f.mk != nil {
		f.failMarker()
	}
	if f.inRun {
		f.endRun()
	}
	f.emitLead()
}
