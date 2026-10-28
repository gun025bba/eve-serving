package pim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/gun025bba/eve-serving/internal/tools"
)

// Write verbs (#70, calendar_add_event per design §11.4). The model never
// writes: reminders_add, reminders_complete and calendar_add_event only build a confirmation card (pending_actions). The
// helper call happens when the owner approves that card in a browser session
// (interactive scope), and only while GW_PIM_WRITES_ENABLED is on and the
// helper itself runs with --writes-enabled. Neither flag is on by default.

const (
	ToolAddReminder      = "reminders_add"
	ToolCompleteReminder = "reminders_complete"
	ToolAddEvent         = "calendar_add_event"
	ToolWriteVersion     = "1"
	ScopeWrite           = "reminders.write"
	ScopeCalendarWrite   = "calendar.write"

	// Same limits as the helper (Protocol.swift Limits).
	MaxTitleChars    = 200
	MaxListChars     = 100
	MaxIDChars       = 256
	MaxLocationChars = 200
	MaxNotesChars    = 500
	MaxEventLen      = 7 * 24 * time.Hour
	MaxAllDayLen     = 31 * 24 * time.Hour

	// A new event may start from one day ago to one year ahead (§11.4).
	EventPast   = 24 * time.Hour
	EventFuture = 365 * 24 * time.Hour
	// DefaultEventLen is the length of a timed event without "end".
	DefaultEventLen = 60 * time.Minute

	// ProposalTTL bounds how long a write card can wait for approval.
	ProposalTTL = time.Hour

	// Method names as logged: the only thing besides count and error code.
	MethodAddReminder      = "add_reminder"
	MethodCompleteReminder = "complete_reminder"
	MethodAddEvent         = "add_event"
)

// EventWrite is one add_event helper request (already normalized to +09:00).
type EventWrite struct {
	Title      string
	Start, End string
	AllDay     bool
	Location   string
	Notes      string
	CalendarID string
}

// Writer is the write surface behind approved cards. *Client implements it.
type Writer interface {
	AddReminder(ctx context.Context, title string, due *time.Time, list string) error
	CompleteReminder(ctx context.Context, id string) error
	AddEvent(ctx context.Context, e EventWrite) error
}

// WriteNames lists the write tools in a fixed order.
func WriteNames() []string { return []string{ToolAddReminder, ToolCompleteReminder, ToolAddEvent} }

// IsWriteTool reports whether name is a pim write tool.
func IsWriteTool(name string) bool {
	return name == ToolAddReminder || name == ToolCompleteReminder || name == ToolAddEvent
}

// Method maps a write tool to its helper method (for logs and audit).
func Method(tool string) string {
	switch tool {
	case ToolAddReminder:
		return MethodAddReminder
	case ToolCompleteReminder:
		return MethodCompleteReminder
	case ToolAddEvent:
		return MethodAddEvent
	}
	return ""
}

// ScopeFor is the scope a write tool needs (its manifest scope).
func ScopeFor(tool string) string {
	if tool == ToolAddEvent {
		return ScopeCalendarWrite
	}
	return ScopeWrite
}

// Payload is what an approved card executes (and what payload_hash binds).
// For complete, Title/List/Due are what the user saw on the card; Execute
// checks that the reminder behind ID still looks like that. Times are always
// the +09:00 form. Method is internal and is not part of the card view.
type Payload struct {
	Method     string `json:"method"`
	ID         string `json:"id,omitempty"`
	Title      string `json:"title,omitempty"`
	Due        string `json:"due,omitempty"`
	List       string `json:"list,omitempty"`
	Start      string `json:"start,omitempty"`
	End        string `json:"end,omitempty"`
	AllDay     bool   `json:"all_day,omitempty"`
	Location   string `json:"location,omitempty"`
	Notes      string `json:"notes,omitempty"`
	CalendarID string `json:"calendar_id,omitempty"`
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

// cleanLabel is the text rule of the write cards (§11.4): trimmed, NFKC,
// 1..max characters, no control characters, no zero-width / format
// characters, no U+2028/U+2029, nothing secret-like. Failing here means the helper would
// refuse it too (it also rejects control characters), or the card could not
// show it faithfully.
func cleanLabel(s string, max int) (string, bool) {
	if secretLike(s) || !utf8.ValidString(s) {
		return "", false
	}
	t := norm.NFKC.String(strings.TrimSpace(s))
	t = strings.TrimSpace(t)
	if t == "" || utf8.RuneCountInString(t) > max || secretLike(t) {
		return "", false
	}
	for _, r := range t {
		// Zl/Zp (U+2028, U+2029) are line/paragraph separators that Cc and Cf
		// do not cover; a card or a synced event must not carry a line break.
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return "", false
		}
	}
	return t, true
}

// cleanText is the rule for opaque ids: trimmed, bounded, no control chars.
func cleanText(s string, max int) (string, bool) {
	t := strings.TrimSpace(s)
	if t == "" || utf8.RuneCountInString(t) > max || !utf8.ValidString(t) {
		return "", false
	}
	for _, r := range t {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return "", false
		}
	}
	return t, true
}

// strictArgs decodes a tool call's arguments as one JSON object with only the
// allowed keys. Unknown keys (this includes verb/display/calendar/
// payload_view, which a model can never set), duplicate keys, null values and
// trailing data are refused.
func strictArgs(raw string, allowed ...string) (map[string]json.RawMessage, bool) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		k, ok := kt.(string)
		if err != nil || !ok {
			return nil, false
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, false
		}
		if _, dup := m[k]; dup || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, false
		}
		known := false
		for _, a := range allowed {
			known = known || k == a
		}
		if !known {
			return nil, false
		}
		m[k] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		return nil, false // trailing data
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

// minuteTime parses RFC3339 with a zone (Z or +hh:mm required) whose seconds
// and fraction are zero, and returns it in +09:00.
func minuteTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.Second() != 0 || t.Nanosecond() != 0 {
		return t, false
	}
	return t.In(KST), true
}

func atKSTMidnight(t time.Time) bool {
	t = t.In(KST)
	return t.Hour() == 0 && t.Minute() == 0
}

func kst(t time.Time) string { return t.In(KST).Format(time.RFC3339) }

var weekdayKo = [...]string{"일", "월", "화", "수", "목", "금", "토"}

// dayLabel is "10/28 (수)", with the year in front only when it is not the
// current (KST) year.
func dayLabel(t, now time.Time) string {
	t, now = t.In(KST), now.In(KST)
	y := ""
	if t.Year() != now.Year() {
		y = fmt.Sprintf("%d/", t.Year())
	}
	return fmt.Sprintf("%s%d/%d (%s)", y, int(t.Month()), t.Day(), weekdayKo[t.Weekday()])
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.In(KST).Date()
	by, bm, bd := b.In(KST).Date()
	return ay == by && am == bm && ad == bd
}

// DisplayWhen is the server-made display string of an event (§11.4), always
// Asia/Seoul, 24 hour, no zone suffix. For all-day events end is the
// exclusive next-day 00:00, the shown last day is the day before it.
func DisplayWhen(start, end time.Time, allDay bool, now time.Time) string {
	start, end = start.In(KST), end.In(KST)
	if allDay {
		last := end.AddDate(0, 0, -1)
		if !last.After(start) {
			return dayLabel(start, now) + " 종일"
		}
		return dayLabel(start, now) + " – " + dayLabel(last, now) + " 종일"
	}
	if sameDay(start, end) {
		return fmt.Sprintf("%s %s–%s", dayLabel(start, now), start.Format("15:04"), end.Format("15:04"))
	}
	return fmt.Sprintf("%s %s – %s %s", dayLabel(start, now), start.Format("15:04"), dayLabel(end, now), end.Format("15:04"))
}

// DisplayDue is the display string of a reminder due time.
func DisplayDue(due, now time.Time) string {
	return fmt.Sprintf("%s %s", dayLabel(due, now), due.In(KST).Format("15:04"))
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ProposeAdd validates reminders_add arguments and builds the card.
func ProposeAdd(rawArgs string, now time.Time) (Proposal, error) {
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
	if p.Title, ok = cleanLabel(ts, MaxTitleChars); !ok {
		return Proposal{}, &Error{CodeInvalidArgs}
	}
	if hasDue {
		t, ok := minuteTime(ds)
		if !ok {
			return Proposal{}, &Error{CodeInvalidArgs}
		}
		p.Due = kst(t)
	}
	if hasList {
		if p.List, ok = cleanLabel(ls, MaxListChars); !ok {
			return Proposal{}, &Error{CodeInvalidArgs}
		}
	}
	return build(ToolAddReminder, tools.VerbAdd, "리마인더 추가", p, now, "")
}

// ProposeComplete validates reminders_complete arguments and looks the
// reminder up, so the card shows its title rather than an opaque id.
func ProposeComplete(ctx context.Context, rd Reader, rawArgs string, now time.Time) (Proposal, error) {
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
	p := Payload{Method: MethodCompleteReminder, ID: id, Title: r.Title, List: r.List}
	if r.Due != nil {
		t, ok := minuteTimeLoose(*r.Due)
		if !ok {
			return Proposal{}, &Error{CodeUnavailable}
		}
		p.Due = kst(t)
	}
	return build(ToolCompleteReminder, tools.VerbUpdate, "리마인더 완료 처리", p, now, "")
}

// minuteTimeLoose is for values read back from the helper: any RFC3339 time,
// normalized to +09:00 (seconds are kept, nothing is rejected for them).
func minuteTimeLoose(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return t, false
	}
	return t.In(KST), true
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

// writableCalendar looks a calendar id up in the helper's list_calendars; the
// calendar must exist and accept new events, else not_found.
func writableCalendar(ctx context.Context, rd Reader, id string) (Calendar, error) {
	if rd == nil {
		return Calendar{}, &Error{CodeUnavailable}
	}
	cs, err := rd.ListCalendars(ctx)
	if err != nil {
		return Calendar{}, err
	}
	for _, c := range cs.Calendars {
		if c.ID == id && c.Writable {
			return c, nil
		}
	}
	return Calendar{}, &Error{CodeNotFound}
}

type eventArgs struct {
	title, start, end, location, notes, calendarID string
	hasEnd, hasLoc, hasNotes, hasCal               bool
	allDay                                         bool
}

// ProposeAddEvent validates calendar_add_event arguments (§11.4) and builds
// the card. now anchors the allowed start window. A calendar_id is checked
// against the helper's writable calendars (rd); the card shows its name.
func ProposeAddEvent(ctx context.Context, rd Reader, rawArgs string, now time.Time) (Proposal, error) {
	bad := &Error{CodeInvalidArgs}
	m, ok := strictArgs(rawArgs, "title", "start", "end", "all_day", "location", "notes", "calendar_id")
	if !ok {
		return Proposal{}, bad
	}
	var a eventArgs
	var ok1, ok2, ok3, ok4, ok5, ok6 bool
	var hasTitle, hasStart bool
	a.title, hasTitle, ok1 = strArg(m, "title")
	a.start, hasStart, ok2 = strArg(m, "start")
	a.end, a.hasEnd, ok3 = strArg(m, "end")
	a.location, a.hasLoc, ok4 = strArg(m, "location")
	a.notes, a.hasNotes, ok5 = strArg(m, "notes")
	a.calendarID, a.hasCal, ok6 = strArg(m, "calendar_id")
	if !hasTitle || !hasStart || !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return Proposal{}, bad
	}
	if v, has := m["all_day"]; has && json.Unmarshal(v, &a.allDay) != nil {
		return Proposal{}, bad
	}
	p := Payload{Method: MethodAddEvent, AllDay: a.allDay}
	if p.Title, ok = cleanLabel(a.title, MaxTitleChars); !ok {
		return Proposal{}, bad
	}
	st, ok := minuteTime(a.start)
	if !ok || st.Before(now.Add(-EventPast)) || st.After(now.Add(EventFuture)) {
		return Proposal{}, bad
	}
	var en time.Time
	switch {
	case a.hasEnd:
		if en, ok = minuteTime(a.end); !ok {
			return Proposal{}, bad
		}
	case a.allDay:
		en = st.AddDate(0, 0, 1)
	default:
		en = st.Add(DefaultEventLen)
	}
	p.Start, p.End = kst(st), kst(en)
	if a.hasLoc {
		if p.Location, ok = cleanLabel(a.location, MaxLocationChars); !ok {
			return Proposal{}, bad
		}
	}
	if a.hasNotes {
		if p.Notes, ok = cleanLabel(a.notes, MaxNotesChars); !ok {
			return Proposal{}, bad
		}
	}
	// L-1: calendar_id is required. The event syncs to an external account, so
	// the card must always name the calendar it will be written to (looked up
	// in the helper's writable calendars below), never "the default".
	if !a.hasCal {
		return Proposal{}, bad
	}
	if p.CalendarID, ok = cleanText(a.calendarID, MaxIDChars); !ok {
		return Proposal{}, bad
	}
	if err := checkEvent(p); err != nil {
		return Proposal{}, err
	}
	c, err := writableCalendar(ctx, rd, p.CalendarID)
	if err != nil {
		return Proposal{}, err
	}
	return build(ToolAddEvent, tools.VerbAdd, "일정 추가", p, now, clipTitle(c.Name))
}

// checkEvent is the shape rule of an event payload: start before end, both
// +09:00 minutes (all-day: KST midnight), lengths within the limits.
func checkEvent(p Payload) error {
	st, ok1 := minuteTime(p.Start)
	en, ok2 := minuteTime(p.End)
	if !ok1 || !ok2 || !en.After(st) || p.Start != kst(st) || p.End != kst(en) {
		return &Error{CodeInvalidArgs}
	}
	if p.AllDay {
		if !atKSTMidnight(st) || !atKSTMidnight(en) || en.Sub(st) > MaxAllDayLen {
			return &Error{CodeInvalidArgs}
		}
	} else if en.Sub(st) > MaxEventLen {
		return &Error{CodeInvalidArgs}
	}
	return nil
}

// build makes the card from the payload alone: the verb view carries the
// values that will be executed plus server-made display strings (§11.4). The
// helper method name is internal and never in the view. calName is the
// calendar name of an event card.
func build(tool, verb, summary string, p Payload, now time.Time, calName string) (Proposal, error) {
	pb, err := json.Marshal(p)
	if err != nil {
		return Proposal{}, &Error{CodeInvalidArgs}
	}
	view := map[string]any{"verb": verb, "title": p.Title}
	var due time.Time
	if p.Due != "" {
		t, ok := minuteTimeLoose(p.Due)
		if !ok {
			return Proposal{}, &Error{CodeInvalidArgs}
		}
		due = t
	}
	switch tool {
	case ToolAddReminder, ToolCompleteReminder:
		view["due"] = nilIfEmpty(p.Due)
		view["display"] = nil
		if p.Due != "" {
			view["display"] = DisplayDue(due, now)
		}
		view["list"] = nilIfEmpty(p.List)
		if tool == ToolCompleteReminder {
			delete(view, "due")
			view["id"] = p.ID
		}
	case ToolAddEvent:
		st, _ := minuteTime(p.Start)
		en, _ := minuteTime(p.End)
		view["start"], view["end"], view["all_day"] = p.Start, p.End, p.AllDay
		view["display"] = DisplayWhen(st, en, p.AllDay, now)
		view["location"], view["notes"] = nilIfEmpty(p.Location), nilIfEmpty(p.Notes)
		view["calendar_id"], view["calendar"] = nilIfEmpty(p.CalendarID), calName
	}
	vb, _ := json.Marshal(view)
	return Proposal{Tool: tool, Verb: verb, Summary: summary, Payload: pb, View: vb}, nil
}

// ParsePayload re-validates a stored card payload for tool (the approve
// step trusts nothing it did not just check). It must be the canonical form
// the proposal stored.
func ParsePayload(tool string, raw json.RawMessage) (Payload, error) {
	var p Payload
	bad := &Error{CodeInvalidArgs}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || p.Method != Method(tool) {
		return p, bad
	}
	in := p
	event := p.Start != "" || p.End != "" || p.AllDay || p.Location != "" || p.Notes != "" || p.CalendarID != ""
	switch tool {
	case ToolAddReminder:
		var ok bool
		if p.ID != "" || event {
			return p, bad
		}
		if p.Title, ok = cleanLabel(p.Title, MaxTitleChars); !ok {
			return p, bad
		}
		if p.Due != "" {
			t, ok := minuteTime(p.Due)
			if !ok {
				return p, bad
			}
			p.Due = kst(t)
		}
		if p.List != "" {
			if p.List, ok = cleanLabel(p.List, MaxListChars); !ok {
				return p, bad
			}
		}
	case ToolCompleteReminder:
		if _, ok := cleanText(p.ID, MaxIDChars); !ok || event {
			return p, bad
		}
		if p.Due != "" {
			t, ok := minuteTimeLoose(p.Due)
			if !ok {
				return p, bad
			}
			p.Due = kst(t)
		}
	case ToolAddEvent:
		var ok bool
		if p.ID != "" || p.Due != "" || p.List != "" {
			return p, bad
		}
		if p.Title, ok = cleanLabel(p.Title, MaxTitleChars); !ok {
			return p, bad
		}
		if p.Location != "" {
			if p.Location, ok = cleanLabel(p.Location, MaxLocationChars); !ok {
				return p, bad
			}
		}
		if p.Notes != "" {
			if p.Notes, ok = cleanLabel(p.Notes, MaxNotesChars); !ok {
				return p, bad
			}
		}
		if p.CalendarID, ok = cleanText(p.CalendarID, MaxIDChars); !ok { // required since L-1
			return p, bad
		}
		if err := checkEvent(p); err != nil {
			return p, err
		}
	default:
		return p, bad
	}
	if p != in {
		return p, bad
	}
	return p, nil
}

// Revalidate checks, right before executing, that a card still describes what
// the user saw: a complete card the same open reminder (title, list, due), an
// event card with a calendar_id a writable calendar. Add-reminder cards need
// no check. A mismatch is CodeNotFound: nothing is written.
func Revalidate(ctx context.Context, rd Reader, tool string, p Payload) error {
	switch tool {
	case ToolCompleteReminder:
		r, err := findOpenReminder(ctx, rd, p.ID)
		if err != nil {
			return err
		}
		due := ""
		if r.Due != nil {
			t, ok := minuteTimeLoose(*r.Due)
			if !ok {
				return &Error{CodeUnavailable}
			}
			due = kst(t)
		}
		if r.Title != p.Title || r.List != p.List || due != p.Due {
			return &Error{CodeNotFound}
		}
	case ToolAddEvent:
		_, err := writableCalendar(ctx, rd, p.CalendarID)
		return err
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
	case ToolAddEvent:
		return w.AddEvent(ctx, EventWrite{Title: p.Title, Start: p.Start, End: p.End, AllDay: p.AllDay,
			Location: p.Location, Notes: p.Notes, CalendarID: p.CalendarID})
	}
	return &Error{CodeInvalidArgs}
}
