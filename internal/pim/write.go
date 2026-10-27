package pim

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gun025bba/eve-serving/internal/tools"
)

// Write verbs (#70). The model never writes: reminders_add and
// reminders_complete only build a confirmation card (pending_actions). The
// helper call happens when the owner approves that card in a browser session
// (interactive scope), and only while GW_PIM_WRITES_ENABLED is on and the
// helper itself runs with --writes-enabled. Neither flag is on by default.

const (
	ToolAddReminder      = "reminders_add"
	ToolCompleteReminder = "reminders_complete"
	ToolWriteVersion     = "1"
	ScopeWrite           = "reminders.write"

	// Same limits as the helper (Protocol.swift Limits).
	MaxTitleChars = 200
	MaxListChars  = 100
	MaxIDChars    = 256

	// ProposalTTL bounds how long a write card can wait for approval.
	ProposalTTL = time.Hour

	// Method names as logged: the only thing besides count and error code.
	MethodAddReminder      = "add_reminder"
	MethodCompleteReminder = "complete_reminder"
)

// Writer is the write surface behind approved cards. *Client implements it.
type Writer interface {
	AddReminder(ctx context.Context, title string, due *time.Time, list string) error
	CompleteReminder(ctx context.Context, id string) error
}

// WriteNames lists the write tools in a fixed order.
func WriteNames() []string { return []string{ToolAddReminder, ToolCompleteReminder} }

// IsWriteTool reports whether name is a pim write tool.
func IsWriteTool(name string) bool { return name == ToolAddReminder || name == ToolCompleteReminder }

// Method maps a write tool to its helper method (for logs and audit).
func Method(tool string) string {
	switch tool {
	case ToolAddReminder:
		return MethodAddReminder
	case ToolCompleteReminder:
		return MethodCompleteReminder
	}
	return ""
}

// Payload is what an approved card executes (and what payload_hash binds).
// For complete, Title/List are what the user saw on the card; Execute checks
// that the reminder behind ID still looks like that.
type Payload struct {
	Method string `json:"method"`
	ID     string `json:"id,omitempty"`
	Title  string `json:"title,omitempty"`
	Due    string `json:"due,omitempty"`
	List   string `json:"list,omitempty"`
}

// Proposal is a validated, not yet stored confirmation card.
type Proposal struct {
	Tool    string
	Verb    string
	Summary string
	Payload json.RawMessage
	View    json.RawMessage // exact values, shown verbatim on the card
}

// secretLike reports whether s matches the shared secret pattern list (the
// same one memory writes and the redactor use). Reminders sync to external
// accounts, so secret-looking text is never written.
func secretLike(s string) bool {
	for _, sp := range tools.SecretPatterns {
		if sp.Re.MatchString(s) {
			return true
		}
	}
	return false
}

// cleanText mirrors the helper's text rule: trimmed, non-empty, bounded, no
// control characters. Failing here means the helper would refuse it too.
func cleanText(s string, max int) (string, bool) {
	t := strings.TrimSpace(s)
	if t == "" || utf8.RuneCountInString(t) > max || !utf8.ValidString(t) {
		return "", false
	}
	for _, r := range t {
		if unicode.Is(unicode.Cc, r) {
			return "", false
		}
	}
	return t, true
}

func strictArgs(raw string, allowed ...string) (map[string]json.RawMessage, bool) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil, false
	}
next:
	for k := range m {
		for _, a := range allowed {
			if k == a {
				continue next
			}
		}
		return nil, false
	}
	return m, true
}

func strArg(m map[string]json.RawMessage, k string) (string, bool, bool) { // value, present, ok
	v, ok := m[k]
	if !ok {
		return "", false, true
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return "", true, false
	}
	return s, true, true
}

// ProposeAdd validates reminders_add arguments and builds the card.
func ProposeAdd(rawArgs string) (Proposal, error) {
	m, ok := strictArgs(rawArgs, "title", "due", "list")
	if !ok {
		return Proposal{}, &Error{CodeInvalidArgs}
	}
	p := Payload{Method: MethodAddReminder}
	ts, _, ok1 := strArg(m, "title")
	ds, hasDue, ok2 := strArg(m, "due")
	ls, hasList, ok3 := strArg(m, "list")
	if !ok1 || !ok2 || !ok3 {
		return Proposal{}, &Error{CodeInvalidArgs}
	}
	if p.Title, ok = cleanText(ts, MaxTitleChars); !ok || secretLike(p.Title) {
		return Proposal{}, &Error{CodeInvalidArgs}
	}
	if hasDue {
		t, err := time.Parse(time.RFC3339, ds)
		if err != nil {
			return Proposal{}, &Error{CodeInvalidArgs}
		}
		p.Due = t.Format(time.RFC3339)
	}
	if hasList {
		if p.List, ok = cleanText(ls, MaxListChars); !ok || secretLike(p.List) {
			return Proposal{}, &Error{CodeInvalidArgs}
		}
	}
	return build(ToolAddReminder, tools.VerbAdd, "리마인더 추가", p)
}

// ProposeComplete validates reminders_complete arguments and looks the
// reminder up, so the card shows its title rather than an opaque id.
func ProposeComplete(ctx context.Context, rd Reader, rawArgs string) (Proposal, error) {
	m, ok := strictArgs(rawArgs, "id")
	if !ok {
		return Proposal{}, &Error{CodeInvalidArgs}
	}
	id, _, ok1 := strArg(m, "id")
	if id, ok = cleanText(id, MaxIDChars); !ok1 || !ok {
		return Proposal{}, &Error{CodeInvalidArgs}
	}
	r, err := findOpenReminder(ctx, rd, id)
	if err != nil {
		return Proposal{}, err
	}
	return build(ToolCompleteReminder, tools.VerbUpdate, "리마인더 완료 처리",
		Payload{Method: MethodCompleteReminder, ID: id, Title: r.Title, List: r.List})
}

// findOpenReminder returns the not-done reminder with this id (first
// MaxReminders only, the same window reminders_list shows the model).
func findOpenReminder(ctx context.Context, rd Reader, id string) (Reminder, error) {
	if rd == nil {
		return Reminder{}, &Error{CodeUnavailable}
	}
	rs, err := rd.ListReminders(ctx, nil, false)
	if err != nil {
		return Reminder{}, err
	}
	for i, r := range rs.Reminders {
		if i >= MaxReminders {
			break
		}
		if r.ID == id && !r.Done {
			return r, nil
		}
	}
	return Reminder{}, &Error{CodeNotFound}
}

func build(tool, verb, summary string, p Payload) (Proposal, error) {
	pb, err := json.Marshal(p)
	if err != nil {
		return Proposal{}, &Error{CodeInvalidArgs}
	}
	view := map[string]any{"verb": verb, "method": p.Method, "title": p.Title}
	if p.Due != "" {
		view["due"] = p.Due
	}
	if p.List != "" {
		view["list"] = p.List
	}
	vb, _ := json.Marshal(view)
	return Proposal{Tool: tool, Verb: verb, Summary: summary, Payload: pb, View: vb}, nil
}

// ParsePayload re-validates a stored card payload for tool (the approve
// step trusts nothing it did not just check).
func ParsePayload(tool string, raw json.RawMessage) (Payload, error) {
	var p Payload
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || p.Method != Method(tool) {
		return p, &Error{CodeInvalidArgs}
	}
	switch tool {
	case ToolAddReminder:
		var ok bool
		if p.ID != "" {
			return p, &Error{CodeInvalidArgs}
		}
		if p.Title, ok = cleanText(p.Title, MaxTitleChars); !ok || secretLike(p.Title) {
			return p, &Error{CodeInvalidArgs}
		}
		if p.Due != "" {
			if _, err := time.Parse(time.RFC3339, p.Due); err != nil {
				return p, &Error{CodeInvalidArgs}
			}
		}
		if p.List != "" {
			if p.List, ok = cleanText(p.List, MaxListChars); !ok || secretLike(p.List) {
				return p, &Error{CodeInvalidArgs}
			}
		}
	case ToolCompleteReminder:
		if _, ok := cleanText(p.ID, MaxIDChars); !ok || p.Due != "" {
			return p, &Error{CodeInvalidArgs}
		}
	default:
		return p, &Error{CodeInvalidArgs}
	}
	return p, nil
}

// Revalidate checks, right before executing, that a complete card still
// describes the same open reminder (title and list as shown). Add cards need
// no check. A mismatch is CodeNotFound: nothing is written.
func Revalidate(ctx context.Context, rd Reader, tool string, p Payload) error {
	if tool != ToolCompleteReminder {
		return nil
	}
	r, err := findOpenReminder(ctx, rd, p.ID)
	if err != nil {
		return err
	}
	if r.Title != p.Title || r.List != p.List {
		return &Error{CodeNotFound}
	}
	return nil
}

// Execute performs one approved write. One attempt; the caller decides what
// an unknown outcome means (see OutcomeUnknown).
func Execute(ctx context.Context, w Writer, tool string, p Payload) error {
	if w == nil {
		return &Error{CodeUnavailable}
	}
	switch tool {
	case ToolAddReminder:
		var due *time.Time
		if p.Due != "" {
			t, err := time.Parse(time.RFC3339, p.Due)
			if err != nil {
				return &Error{CodeInvalidArgs}
			}
			due = &t
		}
		return w.AddReminder(ctx, p.Title, due, p.List)
	case ToolCompleteReminder:
		return w.CompleteReminder(ctx, p.ID)
	}
	return &Error{CodeInvalidArgs}
}
