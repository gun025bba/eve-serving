package tools

// Confirmation-card verbs (ui-api-contract.md Q19, tool-platform.md §1.1).
// payload_view.verb is filled by the server only from a tool's static,
// deploy-time verb; model output, tool args and user input never set it.
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
