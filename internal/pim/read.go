package pim

import (
	"context"
	"encoding/json"
	"time"
)

// stringArg reads an optional string argument. Missing keys yield an empty
// string; callers decide whether that is valid for their tool.
func stringArg(args map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := args[key]
	if !ok {
		return "", true
	}
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil
}

// readEvents validates the range and normalizes the same items for the model
// result and the UI card. The helper is never called with invalid arguments.
func readEvents(ctx context.Context, rd Reader, args map[string]json.RawMessage, loc *time.Location) (Events, map[string]any, error) {
	for k := range args {
		if k != "from" && k != "to" {
			return Events{}, nil, &Error{CodeInvalidArgs}
		}
	}
	fs, ok1 := stringArg(args, "from")
	ts, ok2 := stringArg(args, "to")
	from, e1 := time.Parse(time.RFC3339, fs)
	to, e2 := time.Parse(time.RFC3339, ts)
	if !ok1 || !ok2 || e1 != nil || e2 != nil || !to.After(from) {
		return Events{}, nil, &Error{CodeInvalidArgs}
	}
	clipped := false
	if to.Sub(from) > MaxRange {
		to, clipped = from.Add(MaxRange), true
	}
	ev, err := rd.ListEvents(ctx, from.In(loc), to.In(loc))
	if err != nil {
		return Events{}, nil, err
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
			return Events{}, nil, &Error{CodeUnavailable} // the helper broke its contract
		}
		e.Start, e.End, e.Title = s, en, clipTitle(e.Title)
		items = append(items, e)
	}
	ev.Events = items
	card := map[string]any{"kind": "calendar_events", "v": 1, "items": items,
		"range": map[string]string{"from": from.In(KST).Format(time.RFC3339), "to": to.In(KST).Format(time.RFC3339)}}
	return ev, card, nil
}

// readReminders validates the filters and normalizes the same items for the
// model result and the UI card.
func readReminders(ctx context.Context, rd Reader, args map[string]json.RawMessage, loc *time.Location) (Reminders, map[string]any, error) {
	var due *time.Time
	include := false
	for k, v := range args {
		switch k {
		case "due_before":
			s, ok := stringArg(args, k)
			t, err := time.Parse(time.RFC3339, s)
			if !ok || err != nil {
				return Reminders{}, nil, &Error{CodeInvalidArgs}
			}
			t = t.In(loc)
			due = &t
		case "include_done":
			if json.Unmarshal(v, &include) != nil {
				return Reminders{}, nil, &Error{CodeInvalidArgs}
			}
		default:
			return Reminders{}, nil, &Error{CodeInvalidArgs}
		}
	}
	rs, err := rd.ListReminders(ctx, due, include)
	if err != nil {
		return Reminders{}, nil, err
	}
	if len(rs.Reminders) > MaxReminders {
		rs.Reminders, rs.Truncated = rs.Reminders[:MaxReminders], true
	}
	items := make([]Reminder, 0, len(rs.Reminders))
	for _, r := range rs.Reminders {
		if r.Due != nil {
			d, ok := ToKST(*r.Due)
			if !ok {
				return Reminders{}, nil, &Error{CodeUnavailable}
			}
			r.Due = &d
		}
		r.Title = clipTitle(r.Title)
		items = append(items, r)
	}
	rs.Reminders = items
	var dueBefore any
	if due != nil {
		dueBefore = due.In(KST).Format(time.RFC3339)
	}
	card := map[string]any{"kind": "reminders", "v": 1, "due_before": dueBefore, "items": items}
	return rs, card, nil
}
