package pim

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// calReader is a helper stand-in with calendars.
type calReader struct {
	listOnly
	cals []Calendar
	err  error
}

func (c calReader) ListCalendars(context.Context) (Calendars, error) {
	return Calendars{Calendars: c.cals}, c.err
}

var cals = calReader{cals: []Calendar{{ID: "cal_1", Name: "개인", Writable: true}, {ID: "cal_ro", Name: "공휴일", Writable: false}}}

func viewOf(t *testing.T, p Proposal) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(p.View, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// testNow = 2026-10-28 12:00 KST.
func TestAddEvent_ArgsStrict(t *testing.T) {
	ok := `"title":"치과","start":"2026-10-28T15:00:00+09:00"`
	// every case below must fail for the reason it names, so a case without a
	// calendar_id gets one (L-1: it is required; "no calendar_id" tests that).
	for name, args := range map[string]string{
		"no title":        `{"start":"2026-10-28T15:00:00+09:00"}`,
		"no start":        `{"title":"x"}`,
		"empty title":     `{"title":"  ","start":"2026-10-28T15:00:00+09:00"}`,
		"long title":      `{"title":"` + strings.Repeat("가", 201) + `","start":"2026-10-28T15:00:00+09:00"}`,
		"zone missing":    `{"title":"x","start":"2026-10-28T15:00:00"}`,
		"date only":       `{"title":"x","start":"2026-10-28"}`,
		"seconds":         `{"title":"x","start":"2026-10-28T15:00:30+09:00"}`,
		"fraction":        `{"title":"x","start":"2026-10-28T15:00:00.5+09:00"}`,
		"end seconds":     `{` + ok + `,"end":"2026-10-28T16:00:01+09:00"}`,
		"end before":      `{` + ok + `,"end":"2026-10-28T14:00:00+09:00"}`,
		"end equal":       `{` + ok + `,"end":"2026-10-28T15:00:00+09:00"}`,
		"too old":         `{"title":"x","start":"2026-10-27T11:59:00+09:00"}`,
		"too far":         `{"title":"x","start":"2027-10-29T12:01:00+09:00"}`,
		"8 days":          `{` + ok + `,"end":"2026-11-05T15:00:00+09:00"}`,
		"all day not 00":  `{` + ok + `,"all_day":true}`,
		"all day end mid": `{"title":"x","start":"2026-10-29T00:00:00+09:00","all_day":true,"end":"2026-10-30T12:00:00+09:00"}`,
		"all day 32 days": `{"title":"x","start":"2026-10-29T00:00:00+09:00","all_day":true,"end":"2026-11-30T00:00:00+09:00"}`,
		"null field":      `{` + ok + `,"location":null}`,
		"duplicate key":   `{` + ok + `,"title":"y"}`,
		"type error":      `{` + ok + `,"all_day":"yes"}`,
		"number title":    `{"title":5,"start":"2026-10-28T15:00:00+09:00"}`,
		"unknown key":     `{` + ok + `,"attendees":["a@b.c"]}`,
		"recurrence":      `{` + ok + `,"recurrence":"daily"}`,
		"long location":   `{` + ok + `,"location":"` + strings.Repeat("a", 201) + `"}`,
		"long notes":      `{` + ok + `,"notes":"` + strings.Repeat("a", 501) + `"}`,
		"newline notes":   `{` + ok + `,"notes":"a\nb"}`,
		"zero width":      `{"title":"a\u200bb","start":"2026-10-28T15:00:00+09:00"}`,
		"line sep title":  `{"title":"a\u2028b","start":"2026-10-28T15:00:00+09:00"}`,
		"para sep title":  `{"title":"a\u2029b","start":"2026-10-28T15:00:00+09:00"}`,
		"line sep notes":  `{` + ok + `,"notes":"a\u2028b"}`,
		"para sep loc":    `{` + ok + `,"location":"a\u2029b"}`,
		"line sep cal id": `{` + ok + `,"calendar_id":"cal\u2028_1"}`,
		"notes 501 runes": `{` + ok + `,"notes":"` + strings.Repeat("가", 501) + `"}`,
		"control":         `{"title":"a\u0007b","start":"2026-10-28T15:00:00+09:00"}`,
		"secret":          `{"title":"AKIAIOSFODNN7EXAMPLE","start":"2026-10-28T15:00:00+09:00"}`,
		"secret notes":    `{` + ok + `,"notes":"password: hunter2"}`,
		"empty cal":       `{` + ok + `,"calendar_id":" "}`,
		"trailing":        `{` + ok + `} {}`,
		"not object":      `[]`,
	} {
		if strings.HasPrefix(args, "{") && strings.HasSuffix(args, "}") && !strings.Contains(args, "calendar_id") && name != "trailing" {
			args = args[:len(args)-1] + `,"calendar_id":"cal_1"}`
		}
		if _, err := ProposeAddEvent(t.Context(), cals, args, testNow); CodeOf(err) != CodeInvalidArgs {
			t.Fatal(name, err)
		}
	}
	// L-1: calendar_id is required, with or without anything else
	for _, a := range []string{`{` + ok + `}`, `{` + ok + `,"all_day":false,"location":"x"}`} {
		if _, err := ProposeAddEvent(t.Context(), cals, a, testNow); CodeOf(err) != CodeInvalidArgs {
			t.Fatal("calendar_id must be required:", a, err)
		}
	}
	// calendars: unknown or read-only -> not_found
	for _, id := range []string{"nope", "cal_ro"} {
		if _, err := ProposeAddEvent(t.Context(), cals, `{`+ok+`,"calendar_id":"`+id+`"}`, testNow); CodeOf(err) != CodeNotFound {
			t.Fatal(id, err)
		}
	}
	if _, err := ProposeAddEvent(t.Context(), nil, `{`+ok+`,"calendar_id":"cal_1"}`, testNow); CodeOf(err) != CodeUnavailable {
		t.Fatal(err)
	}
	// defaults: timed -> +60 min, no location/notes; the card names the calendar and its id
	p, err := ProposeAddEvent(t.Context(), cals, `{`+ok+`,"calendar_id":"cal_1"}`, testNow)
	if err != nil {
		t.Fatal(err)
	}
	v := viewOf(t, p)
	if v["end"] != "2026-10-28T16:00:00+09:00" || v["all_day"] != false || v["location"] != nil || v["notes"] != nil || v["calendar_id"] != "cal_1" || v["calendar"] != "개인" {
		t.Fatal(string(p.View))
	}
	// all-day default: next day 00:00 (exclusive)
	p, err = ProposeAddEvent(t.Context(), cals, `{"title":"기념일","start":"2026-10-29T00:00:00+09:00","all_day":true,"calendar_id":"cal_1"}`, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if v = viewOf(t, p); v["end"] != "2026-10-30T00:00:00+09:00" || v["all_day"] != true || v["display"] != "10/29 (목) 종일" {
		t.Fatal(string(p.View))
	}
	// limits at the edge are accepted: 7 days, 31 days, now-1d, now+365d
	for _, a := range []string{
		`{` + ok + `,"end":"2026-11-04T15:00:00+09:00","calendar_id":"cal_1"}`,
		`{"title":"x","start":"2026-10-29T00:00:00+09:00","all_day":true,"end":"2026-11-29T00:00:00+09:00","calendar_id":"cal_1"}`,
		`{"title":"x","start":"2026-10-27T12:00:00+09:00","calendar_id":"cal_1"}`,
		`{"title":"x","start":"2027-10-28T12:00:00+09:00","calendar_id":"cal_1"}`,
		`{` + ok + `,"calendar_id":"cal_1","notes":"` + strings.Repeat("가", 500) + `"}`, // notes at exactly 500 runes
	} {
		if _, err := ProposeAddEvent(t.Context(), cals, a, testNow); err != nil {
			t.Fatal(a, err)
		}
	}
}

func TestAddEvent_NormalizedToKST(t *testing.T) {
	p, err := ProposeAddEvent(t.Context(), cals, `{"title":" Ｃａｆé 미팅 ","start":"2026-10-28T06:00:00Z","end":"2026-10-28T01:30:00-07:00","location":"  역삼  ","calendar_id":"cal_1"}`, testNow)
	if err != nil {
		t.Fatal(err)
	}
	v := viewOf(t, p)
	if v["start"] != "2026-10-28T15:00:00+09:00" || v["end"] != "2026-10-28T17:30:00+09:00" || v["title"] != "Café 미팅" || v["location"] != "역삼" {
		t.Fatal(string(p.View))
	}
	// payload (what is hashed and executed) is the normalized form, and re-parses to itself
	var pay Payload
	json.Unmarshal(p.Payload, &pay)
	if pay.Start != "2026-10-28T15:00:00+09:00" || pay.End != "2026-10-28T17:30:00+09:00" || pay.Method != "add_event" {
		t.Fatal(string(p.Payload))
	}
	if got, err := ParsePayload(ToolAddEvent, p.Payload); err != nil || got != pay {
		t.Fatal(got, err)
	}
	// a stored payload that is not canonical (a Z time) or has other tools' fields is refused
	for _, raw := range []string{
		`{"method":"add_event","title":"x","start":"2026-10-28T06:00:00Z","end":"2026-10-28T07:00:00Z"}`,
		`{"method":"add_event","title":"x","start":"2026-10-28T15:00:00+09:00","end":"2026-10-28T15:00:00+09:00"}`,
		`{"method":"add_event","title":"x","start":"2026-10-28T15:00:00+09:00","end":"2026-10-28T16:00:00+09:00","id":"r"}`,
		`{"method":"add_event","title":"x","start":"2026-10-28T15:00:00+09:00","end":"2026-10-28T16:00:00+09:00","list":"l"}`,
		`{"method":"add_event","title":"x","start":"2026-10-28T15:00:00+09:00","end":"2026-10-28T16:00:00+09:00","all_day":true}`,
		`{"method":"add_event","title":"x","start":"2026-10-28T15:00:00+09:00","end":"2026-10-28T16:00:00+09:00","extra":1}`,
	} {
		if _, err := ParsePayload(ToolAddEvent, json.RawMessage(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	// the other tools do not accept event fields
	if _, err := ParsePayload(ToolAddReminder, json.RawMessage(`{"method":"add_reminder","title":"x","start":"2026-10-28T15:00:00+09:00"}`)); err == nil {
		t.Fatal("reminder with event field")
	}
	// reminders_add normalizes its due the same way and needs zero seconds
	rp, err := ProposeAdd(`{"title":"우유 사기","due":"2026-10-28T09:00:00Z"}`, testNow)
	if err != nil || viewOf(t, rp)["due"] != "2026-10-28T18:00:00+09:00" {
		t.Fatal(err, string(rp.View))
	}
	if _, err := ProposeAdd(`{"title":"x","due":"2026-10-28T09:00:05Z"}`, testNow); CodeOf(err) != CodeInvalidArgs {
		t.Fatal(err)
	}
}

func TestAddEvent_DisplayFormat(t *testing.T) {
	at := func(s string) time.Time {
		x, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	for _, tc := range []struct {
		start, end string
		allDay     bool
		want       string
	}{
		{"2026-10-28T15:00:00+09:00", "2026-10-28T16:00:00+09:00", false, "10/28 (수) 15:00–16:00"},
		{"2026-10-28T23:00:00+09:00", "2026-10-29T01:00:00+09:00", false, "10/28 (수) 23:00 – 10/29 (목) 01:00"},
		{"2026-10-28T00:00:00+09:00", "2026-10-29T00:00:00+09:00", true, "10/28 (수) 종일"},
		{"2026-10-28T00:00:00+09:00", "2026-10-31T00:00:00+09:00", true, "10/28 (수) – 10/30 (금) 종일"},
		{"2027-01-05T09:00:00+09:00", "2027-01-05T10:00:00+09:00", false, "2027/1/5 (화) 09:00–10:00"},
		{"2026-12-31T23:00:00+09:00", "2027-01-01T01:00:00+09:00", false, "12/31 (목) 23:00 – 2027/1/1 (금) 01:00"},
		{"2026-10-28T06:00:00Z", "2026-10-28T07:05:00Z", false, "10/28 (수) 15:00–16:05"}, // zone in -> KST out
		{"2026-11-01T00:00:00+09:00", "2026-11-02T00:00:00+09:00", true, "11/1 (일) 종일"},
	} {
		if got := DisplayWhen(at(tc.start), at(tc.end), tc.allDay, testNow); got != tc.want {
			t.Fatalf("%s..%s: %q != %q", tc.start, tc.end, got, tc.want)
		}
	}
	if got := DisplayDue(at("2026-10-28T09:00:00Z"), testNow); got != "10/28 (수) 18:00" {
		t.Fatal(got)
	}
}

func TestAddEvent_ModelCannotSetView(t *testing.T) {
	base := `"title":"x","start":"2026-10-28T15:00:00+09:00"`
	for _, extra := range []string{`"verb":"update"`, `"display":"오늘"`, `"calendar":"가짜"`, `"payload_view":{"title":"y"}`, `"method":"add_reminder"`} {
		if _, err := ProposeAddEvent(t.Context(), cals, `{`+base+`,`+extra+`}`, testNow); CodeOf(err) != CodeInvalidArgs {
			t.Fatal(extra, err)
		}
	}
	for _, extra := range []string{`"verb":"update"`, `"display":"x"`, `"payload_view":{}`, `"method":"x"`} {
		if _, err := ProposeAdd(`{"title":"x",`+extra+`}`, testNow); CodeOf(err) != CodeInvalidArgs {
			t.Fatal(extra, err)
		}
		if _, err := ProposeComplete(t.Context(), listOnly{[]Reminder{{ID: "r1", Title: "t"}}}, `{"id":"r1",`+extra+`}`, testNow); CodeOf(err) != CodeInvalidArgs {
			t.Fatal(extra, err)
		}
	}
}

func TestPimCard_NoMethodKeyInView(t *testing.T) {
	rd := listOnly{[]Reminder{{ID: "r1", Title: "t", List: "l"}}}
	a, _ := ProposeAdd(`{"title":"x"}`, testNow)
	c, _ := ProposeComplete(t.Context(), rd, `{"id":"r1"}`, testNow)
	e, _ := ProposeAddEvent(t.Context(), cals, `{"title":"x","start":"2026-10-28T15:00:00+09:00","calendar_id":"cal_1"}`, testNow)
	for _, p := range []Proposal{a, c, e} {
		if v := viewOf(t, p); v["method"] != nil || v["verb"] == nil {
			t.Fatal(string(p.View))
		}
		if _, has := viewOf(t, p)["method"]; has {
			t.Fatal(string(p.View))
		}
		if !strings.Contains(string(p.Payload), `"method"`) {
			t.Fatal("payload lost its method", string(p.Payload))
		}
	}
}

func TestAddEvent_RevalidateAndExecute(t *testing.T) {
	p := Payload{Method: MethodAddEvent, Title: "x", Start: "2026-10-28T15:00:00+09:00", End: "2026-10-28T16:00:00+09:00", CalendarID: "cal_1"}
	if err := Revalidate(t.Context(), cals, ToolAddEvent, p); err != nil {
		t.Fatal(err)
	}
	gone := calReader{cals: []Calendar{{ID: "cal_1", Name: "개인", Writable: false}}}
	if err := Revalidate(t.Context(), gone, ToolAddEvent, p); CodeOf(err) != CodeNotFound {
		t.Fatal(err)
	}
	p.CalendarID = "" // never produced since L-1; a card without a calendar is refused, not defaulted
	if err := Revalidate(t.Context(), gone, ToolAddEvent, p); CodeOf(err) != CodeNotFound {
		t.Fatal(err)
	}
	if _, err := ParsePayload(ToolAddEvent, json.RawMessage(`{"method":"add_event","title":"x","start":"2026-10-28T15:00:00+09:00","end":"2026-10-28T16:00:00+09:00"}`)); CodeOf(err) != CodeInvalidArgs {
		t.Fatal("a stored event card without calendar_id must not parse:", err)
	}
	p.CalendarID = "cal_1"
	if err := Execute(t.Context(), nil, ToolAddEvent, p); CodeOf(err) != CodeUnavailable {
		t.Fatal(err)
	}
}
