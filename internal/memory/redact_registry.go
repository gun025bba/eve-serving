package memory

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// Log-mask registry (memory.md §9.1, security review #39).
//
//   - Per-user sets: registerDoc(uid, d) REPLACES the user's set with the
//     stored items, so deleted items are released; Purge drops the user.
//     Rejected candidates are never registered (only stored items are).
//   - Matching uses one Aho-Corasick automaton over all users' patterns,
//     rebuilt lazily after a change.
//   - Texts longer than shingleRunes are registered as overlapping
//     shingleRunes-rune windows, so a truncated or partial copy of an item is
//     still masked (redaction does not depend on where a caller cut).
//   - Per-user cap maxPatternsPerUser. A user whose windows exceed the cap is
//     re-registered with windows at stride shingleRunes/2 (only that user;
//     other users keep full windows), which still masks any copy of at least
//     shingleRunes+shingleRunes/2-1 runes. Only if even that set exceeds the
//     cap (impossible within MaxItems*MaxTextRunes) does the registry fail
//     closed for all free-text values.
const (
	shingleRunes       = 8
	maxPatternsPerUser = 20000
)

type userReg struct {
	texts    map[string]struct{}
	overflow bool
}

var reg struct {
	mu       sync.RWMutex
	users    map[int64]*userReg
	ac       *acMachine
	dirty    bool
	overflow int // users currently over cap
}

func patternsFor(t string) []string { return patternsStride(t, 1) }

func patternsStride(t string, stride int) []string {
	t = strings.TrimSpace(t)
	n := utf8.RuneCountInString(t)
	if n < minRegisteredRunes {
		return nil
	}
	if n <= shingleRunes {
		return []string{t}
	}
	r := []rune(t)
	out := make([]string, 0, n-shingleRunes+1)
	for i := 0; i+shingleRunes <= n; i += stride {
		out = append(out, string(r[i:i+shingleRunes]))
	}
	if (n-shingleRunes)%stride != 0 {
		out = append(out, string(r[n-shingleRunes:]))
	}
	return out
}

// setUserTexts replaces uid's registered texts.
func setUserTexts(uid int64, texts []string) {
	set, over := buildSet(texts, 1)
	if over {
		set, over = buildSet(texts, shingleRunes/2)
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.users == nil {
		reg.users = map[int64]*userReg{}
	}
	if old := reg.users[uid]; old != nil && old.overflow {
		reg.overflow--
	}
	if over {
		reg.overflow++
	}
	reg.users[uid] = &userReg{texts: set, overflow: over}
	reg.dirty = true
}

func buildSet(texts []string, stride int) (map[string]struct{}, bool) {
	set := map[string]struct{}{}
	for _, t := range texts {
		for _, p := range patternsStride(t, stride) {
			if len(set) >= maxPatternsPerUser {
				return set, true
			}
			set[p] = struct{}{}
		}
	}
	return set, false
}

func registerDoc(uid int64, d Doc) {
	texts := make([]string, 0, len(d.Items))
	for _, it := range d.Items {
		texts = append(texts, it.Text)
	}
	setUserTexts(uid, texts)
}

// ReleaseUser drops all registered texts of uid (Purge).
func ReleaseUser(uid int64) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if u := reg.users[uid]; u != nil {
		if u.overflow {
			reg.overflow--
		}
		delete(reg.users, uid)
		reg.dirty = true
	}
}

// RegisterText adds one text under the unscoped registry (uid 0). Kept for
// tests and callers without a user; production registers via registerDoc.
func RegisterText(t string) {
	reg.mu.RLock()
	var cur []string
	if u := reg.users[0]; u != nil {
		for k := range u.texts {
			cur = append(cur, k)
		}
	}
	reg.mu.RUnlock()
	setUserTexts(0, append(cur, t))
}

// machine returns the current automaton and whether any user overflowed.
func machine() (*acMachine, bool) {
	reg.mu.RLock()
	if !reg.dirty {
		m, o := reg.ac, reg.overflow > 0
		reg.mu.RUnlock()
		return m, o
	}
	reg.mu.RUnlock()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.dirty {
		var pats []string
		seen := map[string]struct{}{}
		for _, u := range reg.users {
			for p := range u.texts {
				if _, ok := seen[p]; !ok {
					seen[p] = struct{}{}
					pats = append(pats, p)
				}
			}
		}
		reg.ac = buildAC(pats)
		reg.dirty = false
	}
	return reg.ac, reg.overflow > 0
}

// acMachine is a byte-level Aho-Corasick automaton.
type acMachine struct {
	next []map[byte]int32
	fail []int32
	out  []int32 // longest pattern length ending here (0 = none)
}

func buildAC(pats []string) *acMachine {
	if len(pats) == 0 {
		return nil
	}
	m := &acMachine{next: []map[byte]int32{{}}, fail: []int32{0}, out: []int32{0}}
	for _, p := range pats {
		s := int32(0)
		for i := 0; i < len(p); i++ {
			c := p[i]
			n, ok := m.next[s][c]
			if !ok {
				n = int32(len(m.next))
				m.next = append(m.next, map[byte]int32{})
				m.fail = append(m.fail, 0)
				m.out = append(m.out, 0)
				m.next[s][c] = n
			}
			s = n
		}
		if int32(len(p)) > m.out[s] {
			m.out[s] = int32(len(p))
		}
	}
	q := []int32{}
	for _, n := range m.next[0] {
		q = append(q, n)
	}
	for len(q) > 0 {
		s := q[0]
		q = q[1:]
		for c, n := range m.next[s] {
			f := m.fail[s]
			for {
				if t, ok := m.next[f][c]; ok && t != n {
					m.fail[n] = t
					break
				}
				if f == 0 {
					m.fail[n] = 0
					break
				}
				f = m.fail[f]
			}
			if m.out[m.fail[n]] > m.out[n] {
				m.out[n] = m.out[m.fail[n]]
			}
			q = append(q, n)
		}
	}
	return m
}

// spans returns merged [start,end) byte ranges of all matches in s.
func (m *acMachine) spans(s string) [][2]int {
	if m == nil {
		return nil
	}
	var out [][2]int
	st := int32(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		for {
			if n, ok := m.next[st][c]; ok {
				st = n
				break
			}
			if st == 0 {
				break
			}
			st = m.fail[st]
		}
		// walk the fail chain for every pattern ending here (out holds the
		// longest; shorter ones are covered by merging overlapping windows).
		for f := st; f != 0; f = m.fail[f] {
			if l := int(m.out[f]); l > 0 {
				a, b := i+1-l, i+1
				if k := len(out); k > 0 && a <= out[k-1][1] {
					if a < out[k-1][0] {
						out[k-1][0] = a
					}
					if b > out[k-1][1] {
						out[k-1][1] = b
					}
				} else {
					out = append(out, [2]int{a, b})
				}
				break
			}
		}
	}
	return out
}

// maskRegistered replaces matched spans with redactedMemory.
func maskRegistered(m *acMachine, s string) (string, bool) {
	sp := m.spans(s)
	if len(sp) == 0 {
		return s, false
	}
	var b strings.Builder
	last := 0
	for _, x := range sp {
		if x[0] < last {
			x[0] = last
		}
		b.WriteString(s[last:x[0]])
		b.WriteString(redactedMemory)
		last = x[1]
	}
	b.WriteString(s[last:])
	return b.String(), true
}
