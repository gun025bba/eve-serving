package pim

import (
	"encoding/json"
	"regexp"
	"time"
	"unicode/utf8"
)

// UI format contract (design §11): what the gateway tells the web UI about a
// pim tool call. Everything here is derived from the same values the model
// gets, carries codes and counts instead of any internal detail, and is
// stream-only: cards are never written to the database.

// KST is the zone every time shown to the UI is normalized to (+09:00).
var KST = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Seoul"); err == nil {
		return l
	}
	return time.FixedZone("KST", 9*3600)
}()

// MaxTitleShown is the longest title the UI gets (longer: cut, then "…").
const MaxTitleShown = 200

// clipTitle keeps at most MaxTitleShown characters including the ellipsis.
func clipTitle(s string) string {
	if utf8.RuneCountInString(s) <= MaxTitleShown {
		return s
	}
	r := []rune(s)
	return string(r[:MaxTitleShown-1]) + "…"
}

// ToKST parses an RFC3339 time and formats it as +09:00.
func ToKST(s string) (string, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", false
	}
	return t.In(KST).Format(time.RFC3339), true
}

var codeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

// PublicCode returns code if it is a plain snake_case code, else the generic
// pim_unavailable: a code is the only error detail the UI ever gets.
func PublicCode(code string) string {
	if codeRe.MatchString(code) {
		return code
	}
	return CodeUnavailable
}

// StatusOf maps an error code to the tool_result status (§11.2): denial codes
// -> denied, timeout -> timeout, everything else -> error.
func StatusOf(code string) string {
	switch code {
	case "permission_denied", "scope_denied", "kill_switch", "tainted":
		return "denied"
	case CodeTimeout:
		return "timeout"
	}
	return "error"
}

// ErrorCard is the card of a failed call; error_code equals the one at the
// top level of the tool_result.
func ErrorCard(code string) map[string]any {
	return map[string]any{"kind": "error", "v": 1, "error_code": PublicCode(code)}
}

// FailOutcome is the outcome of a call that failed with code.
func FailOutcome(tool, code string) Outcome {
	code = PublicCode(code)
	return Outcome{Tool: tool, Status: StatusOf(code), ErrorCode: code, Card: ErrorCard(code)}
}

// StatusPending is the tool_result status of a write proposal that became a
// confirmation card.
const StatusPending = "pending_confirmation"

// PendingOutcome is the outcome of a write proposal that was stored.
func PendingOutcome(tool, actionID string) Outcome {
	return Outcome{Tool: tool, Status: StatusPending, Count: 1, ActionID: actionID}
}

// DeniedOutcome is the outcome of a call for a tool not exposed to this principal.
func DeniedOutcome(tool string) Outcome { return FailOutcome(tool, CodeKillSwitch) }

// ResultFields are the tool_result fields beyond object/id/tool (§11.1): a
// read success has count, truncated and card; any failure has error_code and
// an error card. PIM tools never carry sources or blocked_count.
func (o Outcome) ResultFields() map[string]any {
	out := map[string]any{"status": o.Status}
	if o.Status == StatusPending {
		out["action_id"] = o.ActionID // the confirmation event follows; no card here
		return out
	}
	if o.Status == "ok" {
		out["count"], out["truncated"], out["card"] = o.Count, o.Truncated, o.Card
		return out
	}
	out["error_code"], out["card"] = o.ErrorCode, o.Card
	return out
}

// readArgKeys are the arguments of the read tools (dates and booleans only).
var readArgKeys = map[string]map[string]bool{
	ToolListEvents:    {"from": true, "to": true},
	ToolListReminders: {"due_before": true, "include_done": true},
}

// PublicArgs is tool_call.args: a read tool's arguments as the model sent
// them (known keys, string or boolean values only); {} for everything else,
// so a write call never shows its text before the confirmation card.
func PublicArgs(tool, rawArgs string) map[string]any {
	out := map[string]any{}
	allowed := readArgKeys[tool]
	if allowed == nil {
		return out
	}
	var m map[string]any
	if json.Unmarshal([]byte(rawArgs), &m) != nil {
		return out
	}
	for k, v := range m {
		switch v.(type) {
		case string, bool:
			if allowed[k] {
				out[k] = v
			}
		}
	}
	return out
}
