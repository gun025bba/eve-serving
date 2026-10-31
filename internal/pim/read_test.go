package pim

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise Exec, rather than its private helpers, to pin the public contract
// while argument validation and rendering move between functions.
func TestExecReadInvalidArgsDoNotCallHelper(t *testing.T) {
	for _, tt := range []struct{ name, args string }{
		{ToolListEvents, `{}`},
		{ToolListEvents, `{"from":null,"to":"2026-10-29T00:00:00Z"}`},
		{ToolListEvents, `{"from":12,"to":"2026-10-29T00:00:00Z"}`},
		{ToolListEvents, `{"from":"2026-10-28T00:00:00Z","to":"2026-10-28T00:00:00Z"}`},
		{ToolListReminders, `{"due_before":null}`},
		{ToolListReminders, `{"due_before":false}`},
		{ToolListReminders, `{"due_before":"tomorrow"}`},
		{ToolListReminders, `{"include_done":1}`},
		{ToolListReminders, `{"unknown":true}`},
		{ToolListReminders, `{} {}`},
		{"unknown_tool", `{}`},
	} {
		t.Run(tt.name+"/"+tt.args, func(t *testing.T) {
			f := &fakeReader{}
			out, o := Exec(t.Context(), f, tt.name, tt.args, time.UTC)
			if f.calls != 0 || o.ErrorCode != CodeInvalidArgs || o.Count != 0 || o.Card["kind"] != "error" ||
				out != `{"error_code":"invalid_arguments","status":"error"}` {
				t.Fatalf("calls=%d, outcome=%+v, output=%s", f.calls, o, out)
			}
		})
	}
}

func TestExecReadCapsKeepModelAndCardItemsEqual(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			f := &fakeReader{}
			args, key, cap := `{}`, "reminders", MaxReminders
			if name == ToolListEvents {
				args, key, cap = `{"from":"2026-10-28T00:00:00Z","to":"2026-10-29T00:00:00Z"}`, "events", MaxEvents
			}
			for i := 0; i < cap+1; i++ {
				id := fmt.Sprint(i)
				f.ev.Events = append(f.ev.Events, Event{ID: id, Title: "event", Start: "2026-10-28T00:00:00Z", End: "2026-10-28T01:00:00Z"})
				f.rs.Reminders = append(f.rs.Reminders, Reminder{ID: id, Title: "reminder"})
			}
			out, o := Exec(t.Context(), f, name, args, time.UTC)
			if f.calls != 1 || o.Status != "ok" || o.Count != cap || !o.Truncated {
				t.Fatalf("calls=%d, outcome=%+v", f.calls, o)
			}
			_, payload, _ := strings.Cut(out, "\n")
			payload, _, _ = strings.Cut(payload, "\n</untrusted_web_content>")
			var model struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal([]byte(payload), &model); err != nil {
				t.Fatal(err)
			}
			card, err := json.Marshal(o.Card["items"])
			if err != nil {
				t.Fatal(err)
			}
			if string(model.Data[key]) != string(card) || string(model.Data["truncated"]) != "true" {
				t.Fatalf("model and card differ: %s / %s", payload, card)
			}
		})
	}
}

func TestExecRemindersDefaultsAndFilters(t *testing.T) {
	for _, args := range []string{"", " \n", `{}`, `null`, `{"include_done":null}`} {
		f := &fakeReader{}
		out, o := Exec(t.Context(), f, ToolListReminders, args, time.UTC)
		if f.calls != 1 || f.due != nil || f.done || o.Status != "ok" ||
			!strings.Contains(out, `"reminders":[]`) || o.Card["due_before"] != nil {
			t.Fatalf("args=%q, reader=%+v, outcome=%+v, output=%s", args, f, o, out)
		}
	}
	f := &fakeReader{}
	_, o := Exec(t.Context(), f, ToolListReminders, `{"due_before":"2026-10-29T00:00:00Z","include_done":true}`, seoul)
	if o.Status != "ok" || f.due == nil || f.due.Location() != seoul || !f.done ||
		f.due.Format(time.RFC3339) != "2026-10-29T09:00:00+09:00" {
		t.Fatalf("reader=%+v, outcome=%+v", f, o)
	}
}

func TestExecBadReminderTimeDiscardsPartialResult(t *testing.T) {
	bad := "invalid-helper-time"
	f := &fakeReader{rs: Reminders{Reminders: []Reminder{{Title: "private valid item"}, {Title: "private invalid item", Due: &bad}}}}
	out, o := Exec(t.Context(), f, ToolListReminders, `{}`, time.UTC)
	want := FailOutcome(ToolListReminders, CodeUnavailable)
	if !reflect.DeepEqual(o, want) || out != `{"error_code":"pim_unavailable","status":"error"}` {
		t.Fatalf("outcome=%+v, output=%s", o, out)
	}
}
