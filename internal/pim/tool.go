package pim

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/tools"
)

// Tool names. The tool registry only allows [a-z0-9_] names, so the design's
// calendar.list_events / reminders.list are calendar_list_events /
// reminders_list on the wire.
const (
	ToolListEvents    = "calendar_list_events"
	ToolListReminders = "reminders_list"
)

//go:embed manifests
var manifestFS embed.FS

var registry = func() *tools.Registry {
	sub, _ := fs.Sub(manifestFS, "manifests")
	r, err := tools.LoadFS(sub)
	if err != nil {
		panic("pim: manifests invalid: " + err.Error())
	}
	return r
}()

// Names lists the read tools in a fixed order (write verbs: WriteNames).
func Names() []string { return []string{ToolListEvents, ToolListReminders} }

// IsTool reports whether name is a pim tool (read or write verb).
func IsTool(name string) bool {
	return name == ToolListEvents || name == ToolListReminders || IsWriteTool(name)
}

// Manifest returns the validated manifest for a pim tool.
func Manifest(name string) (*tools.Manifest, bool) { return registry.Get(name) }

// ToolDefs returns the OpenAI-style function definitions for the given tools
// (from the validated manifests).
func ToolDefs(names []string) []any {
	var out []any
	for _, n := range names {
		m, ok := registry.Get(n)
		if !ok {
			continue
		}
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": m.Name, "description": m.Description, "parameters": json.RawMessage(m.Schema()),
		}})
	}
	return out
}

// Outcome is what may be logged or audited about one call: no content.
type Outcome struct {
	Tool      string
	Status    string // ok | error | timeout | denied
	ErrorCode string
	Count     int
	Truncated bool
	// Card is the UI card (§11.2): the list card on success, the error card on
	// failure. Stream-only, never stored.
	Card map[string]any
	// ActionID is the confirmation card of a write proposal (Status
	// "pending_confirmation"); there is no card of its own then.
	ActionID string
}

// Exec runs one read tool. loc sets the helper request timezone. The returned
// content is the tool message for the model (wrapped as untrusted).
func Exec(ctx context.Context, rd Reader, name, rawArgs string, loc *time.Location) (string, Outcome) {
	o := Outcome{Tool: name, Status: "error"}
	fail := func(code string) (string, Outcome) {
		o = FailOutcome(name, code)
		// the model keeps the existing form: status "error" whatever the UI status is
		b, _ := json.Marshal(map[string]any{"status": "error", "error_code": o.ErrorCode})
		return string(b), o
	}
	if rd == nil {
		return fail(CodeUnavailable)
	}
	var args map[string]json.RawMessage
	if strings.TrimSpace(rawArgs) == "" {
		rawArgs = "{}"
	}
	if json.Unmarshal([]byte(rawArgs), &args) != nil {
		return fail(CodeInvalidArgs)
	}
	var data any
	var err error
	switch name {
	case ToolListEvents:
		var ev Events
		ev, o.Card, err = readEvents(ctx, rd, args, loc)
		o.Count, o.Truncated, data = len(ev.Events), ev.Truncated, ev
	case ToolListReminders:
		var rs Reminders
		rs, o.Card, err = readReminders(ctx, rd, args, loc)
		o.Count, o.Truncated, data = len(rs.Reminders), rs.Truncated, rs
	default:
		return fail(CodeInvalidArgs)
	}
	if err != nil {
		return fail(CodeOf(err))
	}
	o.Status = "ok"
	b, _ := json.Marshal(map[string]any{"status": "ok", "data": data})
	// Titles and calendar names can be written by third parties (invites):
	// the whole result is untrusted data, wrapped like web results.
	return tools.WrapUntrusted(name, "", string(b)), o
}

// Denied is the tool result for a pim call that arrives while the tools are
// not exposed to this principal (hidden tools; nothing is executed).
func Denied() string { return `{"status":"denied","error_code":"kill_switch"}` }
