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

// Names lists the read tools in a fixed order.
func Names() []string { return []string{ToolListEvents, ToolListReminders} }

// IsTool reports whether name is a pim tool.
func IsTool(name string) bool { return name == ToolListEvents || name == ToolListReminders }

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
}

// Exec runs one read tool. now/loc anchor relative defaults. The returned
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
	str := func(k string) (string, bool, bool) { // value, present, ok
		v, ok := args[k]
		if !ok {
			return "", false, true
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			return "", true, false
		}
		return s, true, true
	}
	var data any
	switch name {
	case ToolListEvents:
		for k := range args {
			if k != "from" && k != "to" {
				return fail(CodeInvalidArgs)
			}
		}
		fs, _, ok1 := str("from")
		ts, _, ok2 := str("to")
		from, e1 := time.Parse(time.RFC3339, fs)
		to, e2 := time.Parse(time.RFC3339, ts)
		if !ok1 || !ok2 || e1 != nil || e2 != nil || !to.After(from) {
			return fail(CodeInvalidArgs)
		}
		clipped := false
		if to.Sub(from) > MaxRange {
			to, clipped = from.Add(MaxRange), true
		}
		ev, err := rd.ListEvents(ctx, from.In(loc), to.In(loc))
		if err != nil {
			return fail(CodeOf(err))
		}
		if len(ev.Events) > MaxEvents {
			ev.Events, ev.Truncated = ev.Events[:MaxEvents], true
		}
		ev.Truncated = ev.Truncated || clipped
		items := make([]Event, 0, len(ev.Events))
		for _, e := range ev.Events {
			s, ok1 := ToKST(e.Start)
			en, ok2 := ToKST(e.End)
			if !ok1 || !ok2 {
				return fail(CodeUnavailable) // the helper broke its contract
			}
			e.Start, e.End, e.Title = s, en, clipTitle(e.Title)
			items = append(items, e)
		}
		ev.Events = items
		o.Count, o.Truncated, data = len(items), ev.Truncated, ev
		o.Card = map[string]any{"kind": "calendar_events", "v": 1, "items": items,
			"range": map[string]string{"from": from.In(KST).Format(time.RFC3339), "to": to.In(KST).Format(time.RFC3339)}}
	case ToolListReminders:
		var due *time.Time
		include := false
		for k, v := range args {
			switch k {
			case "due_before":
				s, _, ok := str(k)
				t, err := time.Parse(time.RFC3339, s)
				if !ok || err != nil {
					return fail(CodeInvalidArgs)
				}
				t = t.In(loc)
				due = &t
			case "include_done":
				if json.Unmarshal(v, &include) != nil {
					return fail(CodeInvalidArgs)
				}
			default:
				return fail(CodeInvalidArgs)
			}
		}
		rs, err := rd.ListReminders(ctx, due, include)
		if err != nil {
			return fail(CodeOf(err))
		}
		if len(rs.Reminders) > MaxReminders {
			rs.Reminders, rs.Truncated = rs.Reminders[:MaxReminders], true
		}
		items := make([]Reminder, 0, len(rs.Reminders))
		for _, r := range rs.Reminders {
			if r.Due != nil {
				d, ok := ToKST(*r.Due)
				if !ok {
					return fail(CodeUnavailable)
				}
				r.Due = &d
			}
			r.Title = clipTitle(r.Title)
			items = append(items, r)
		}
		rs.Reminders = items
		o.Count, o.Truncated, data = len(items), rs.Truncated, rs
		var dueBefore any
		if due != nil {
			dueBefore = due.In(KST).Format(time.RFC3339)
		}
		o.Card = map[string]any{"kind": "reminders", "v": 1, "due_before": dueBefore, "items": items}
	default:
		return fail(CodeInvalidArgs)
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
