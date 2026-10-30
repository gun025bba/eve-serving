package tools

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// The server-side tool deny list (tools.denylist, #159) is the only exception
// to "every tool is used automatically when the model decides it is needed".
// A tool on the list is not offered to the model and a call that arrives
// anyway is refused. There is no per-user or per-chat switch. The default is
// the empty list. Names are the model-facing function names (web_search,
// web_fetch, memory_propose, the personal tools); "memory.propose" is accepted
// as an alias of memory_propose.

// ReasonDenylist is the block reason of a call refused by the deny list.
const ReasonDenylist = "tool_denylisted"

var (
	denyMu   sync.RWMutex
	denylist = map[string]bool{}

	toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)
	denyAlias  = map[string]string{"memory.propose": "memory_propose"}
)

// NormalizeDenylist validates and canonicalizes names (aliases resolved,
// duplicates dropped, sorted). A malformed name is an error: a typo must not
// silently leave a tool exposed.
func NormalizeDenylist(names []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !toolNameRe.MatchString(n) {
			return nil, fmt.Errorf("tools.denylist: invalid tool name %q", n)
		}
		if a, ok := denyAlias[n]; ok {
			n = a
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SetDenylist installs the deny list (startup; replaces the previous list).
// It returns the canonical list. An invalid list is rejected and changes
// nothing.
func SetDenylist(names []string) ([]string, error) {
	canon, err := NormalizeDenylist(names)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(canon))
	for _, n := range canon {
		m[n] = true
	}
	denyMu.Lock()
	denylist = m
	denyMu.Unlock()
	return canon, nil
}

// Denied reports whether name is on the server deny list.
func Denied(name string) bool {
	denyMu.RLock()
	defer denyMu.RUnlock()
	return denylist[name]
}

// KnownTool reports whether name is a tool this server knows: a registered
// web tool, the memory tool or a personal-data tool. Used to warn about a
// deny-list entry that matches nothing.
func KnownTool(name string) bool {
	if _, ok := Default().Reg.Get(name); ok {
		return true
	}
	return name == "memory_propose" || IsPersonalTool(name)
}
