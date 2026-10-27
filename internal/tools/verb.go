package tools

import "fmt"

// Confirmation-card verbs (ui-api-contract.md Q19, tool-platform.md §1.1).
// Q19 is read as "one verb per call (action)": each card carries exactly
// one verb, picked by the server from the tool's static, deploy-time list
// of allowed verbs (manifest `verbs`). Model output, tool args and user
// input never set it.
// The UI maps verb -> Korean button label and treats unknown values as run.
// Danger styling comes from scopes (Q13), not from the verb.
// Adding a value is a MINOR change.
const (
	VerbAdd    = "add"
	VerbUpdate = "update"
	VerbSend   = "send"
	VerbDelete = "delete"
	VerbRun    = "run"
)

// Verbs is the fixed enum, in display order.
var Verbs = []string{VerbAdd, VerbUpdate, VerbSend, VerbDelete, VerbRun}

// ValidVerb reports whether v is in the enum.
func ValidVerb(v string) bool {
	for _, x := range Verbs {
		if x == v {
			return true
		}
	}
	return false
}

// ValidateVerbs checks a tool's allowed-verb list: non-empty, every value
// in the enum, no duplicates.
func ValidateVerbs(vs []string) error {
	if len(vs) == 0 {
		return fmt.Errorf("verbs must list at least one of %v", Verbs)
	}
	seen := map[string]bool{}
	for _, v := range vs {
		if !ValidVerb(v) {
			return fmt.Errorf("verbs: %q is not one of %v", v, Verbs)
		}
		if seen[v] {
			return fmt.Errorf("verbs: duplicate %q", v)
		}
		seen[v] = true
	}
	return nil
}

// AllowsVerb reports whether v is in a tool's allowed list.
func AllowsVerb(allowed []string, v string) bool {
	for _, x := range allowed {
		if x == v {
			return true
		}
	}
	return false
}
