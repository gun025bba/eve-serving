package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/pim"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// Calendar write cards and write tool events (design §11.1, §11.4).

var evNow = time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC) // 12:00 KST

const addEventArgs = `{"title":"치과","start":"2026-10-28T06:00:00Z","end":"2026-10-28T07:00:00Z","location":"역삼동 치과","notes":"보험증","calendar_id":"cal_1"}`

func pimEventEnv(t *testing.T, calls ...[2]string) (*webEnv, *fakeWriter) {
	t.Helper()
	e, fw := pimWriteEnv(t, true, calls...)
	e.srv.Now = func() time.Time { return evNow }
	fw.calendars = []pim.Calendar{{ID: "cal_1", Name: "개인", Writable: true}, {ID: "cal_ro", Name: "공휴일", Writable: false}}
	return e, fw
}

func writeStream(t *testing.T, e *webEnv, who string) []map[string]any {
	t.Helper()
	return pimStream(t, e, who)
}

func objects(evs []map[string]any, kinds ...string) []map[string]any {
	var out []map[string]any
	for _, ev := range evs {
		o, _ := ev["object"].(string)
		for _, k := range kinds {
			if o == "chat.completion."+k {
				out = append(out, ev)
			}
		}
	}
	return out
}

func TestToolEvent_PimWriteArgsEmptyCardOnlyOnError(t *testing.T) {
	for _, tc := range [][2]string{{pim.ToolAddReminder, addArgs}, {pim.ToolAddEvent, addEventArgs}, {pim.ToolCompleteReminder, `{"id":"r1"}`}} {
		e, _ := pimEventEnv(t, tc)
		evs := objects(writeStream(t, e, "alice@example"), "tool_call", "tool_result", "confirmation")
		if len(evs) != 3 || evs[0]["object"] != "chat.completion.tool_call" || evs[1]["object"] != "chat.completion.tool_result" || evs[2]["object"] != "chat.completion.confirmation" {
			t.Fatal(tc[0], "order", evs)
		}
		if a, ok := evs[0]["args"].(map[string]any); !ok || len(a) != 0 {
			t.Fatal(tc[0], "tool_call.args must be {}", evs[0])
		}
		res := evs[1]
		if res["status"] != "pending_confirmation" || res["action_id"] != evs[2]["action_id"] || res["action_id"] == "" {
			t.Fatal(res, evs[2])
		}
		for _, k := range []string{"card", "count", "truncated", "error_code", "sources"} {
			if _, has := res[k]; has {
				t.Fatal(tc[0], "pending result must not carry", k, res)
			}
		}
		b, _ := json.Marshal(evs[0])
		if strings.Contains(string(b), "치과") || strings.Contains(string(b), "우유") {
			t.Fatal("content before the card", string(b))
		}
	}
	// failure: the read-error path: error card, no confirmation, one card only
	for name, tc := range map[string][2]string{
		"bad args":   {pim.ToolAddEvent, `{"title":"x","start":"2026-10-28T15:00:00"}`},
		"bad cal":    {pim.ToolAddEvent, `{"title":"x","start":"2026-10-28T06:00:00Z","calendar_id":"cal_ro"}`},
		"gone":       {pim.ToolCompleteReminder, `{"id":"nope"}`},
		"add secret": {pim.ToolAddReminder, `{"title":"AKIAIOSFODNN7EXAMPLE 보관"}`},
	} {
		e, fw := pimEventEnv(t, tc)
		evs := writeStream(t, e, "alice@example")
		if n := len(objects(evs, "confirmation")); n != 0 {
			t.Fatal(name, "confirmation on failure")
		}
		res := objects(evs, "tool_result")[0]
		card, _ := res["card"].(map[string]any)
		if res["status"] != "error" && res["status"] != "denied" || res["error_code"] == nil || card["kind"] != "error" || card["v"] != float64(1) || card["error_code"] != res["error_code"] {
			t.Fatal(name, res)
		}
		if _, has := res["action_id"]; has || fw.writes() != 0 {
			t.Fatal(name, res)
		}
		if acts, _ := e.st.ListPendingActions(t.Context(), "alice@example"); len(acts) != 0 {
			t.Fatal(name, "card stored for a failed write")
		}
	}
	// a write call while the verb is not offered: denied + kill_switch error card, nothing stored
	e, fw := pimEventEnv(t, [2]string{pim.ToolAddEvent, addEventArgs})
	e.srv.Cfg.Pim.WritesEnabled = false
	evs := writeStream(t, e, "alice@example")
	res := objects(evs, "tool_result")[0]
	if card, _ := res["card"].(map[string]any); res["status"] != "denied" || res["error_code"] != "kill_switch" || card["error_code"] != "kill_switch" || fw.writes() != 0 || len(objects(evs, "confirmation")) != 0 {
		t.Fatal(res)
	}
}

func TestAddEvent_CardMatchesExecutedPayload(t *testing.T) {
	e, fw := pimEventEnv(t, [2]string{pim.ToolAddEvent, addEventArgs})
	evs := writeStream(t, e, "alice@example")
	conf := objects(evs, "confirmation")[0]
	card := pendingCard(t, e, "alice@example")
	if card.Tool != pim.ToolAddEvent || len(card.Scopes) != 1 || card.Scopes[0] != "calendar.write" || card.Summary != "일정 추가" || conf["summary"] != "일정 추가" {
		t.Fatalf("%+v", card)
	}
	var v map[string]any
	json.Unmarshal(card.PayloadView, &v)
	want := map[string]any{"verb": "add", "title": "치과", "start": "2026-10-28T15:00:00+09:00", "end": "2026-10-28T16:00:00+09:00", "all_day": false,
		"display": "10/28 (수) 15:00–16:00", "location": "역삼동 치과", "notes": "보험증", "calendar_id": "cal_1", "calendar": "개인"}
	if len(v) != len(want) {
		t.Fatal(string(card.PayloadView))
	}
	for k, w := range want {
		if v[k] != w {
			t.Fatal(k, v[k], w, string(card.PayloadView))
		}
	}
	if _, has := v["method"]; has {
		t.Fatal("method in view")
	}
	// the confirmation event carries the same view
	cv, _ := conf["payload_view"].(map[string]any)
	if cv["display"] != v["display"] || cv["start"] != v["start"] {
		t.Fatal(conf)
	}
	tok := pimToken(t, e, card.ID)
	if fw.writes() != 0 {
		t.Fatal("written before approval")
	}
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash))
	if r.StatusCode != 200 || strings.Contains(string(b), "치과") {
		t.Fatal(r.StatusCode, string(b))
	}
	// golden: the helper request has exactly the view's argument fields
	if len(fw.events) != 1 {
		t.Fatal(fw.events)
	}
	got := fw.events[0]
	exp := pim.EventWrite{Title: v["title"].(string), Start: v["start"].(string), End: v["end"].(string), AllDay: v["all_day"].(bool),
		Location: v["location"].(string), Notes: v["notes"].(string), CalendarID: v["calendar_id"].(string)}
	if got != exp {
		t.Fatalf("executed %+v != card %+v", got, exp)
	}
}

func TestAddEvent_ApproveRevalidatesCalendar(t *testing.T) {
	e, fw := pimEventEnv(t, [2]string{pim.ToolAddEvent, addEventArgs})
	writeStream(t, e, "alice@example")
	card := pendingCard(t, e, "alice@example")
	tok := pimToken(t, e, card.ID)
	fw.calendars = nil // the calendar went away after the card was made
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash))
	if r.StatusCode == 200 || fw.writes() != 0 {
		t.Fatal(r.StatusCode, string(b))
	}
}

func TestAddEvent_RedactorMismatchRefuses(t *testing.T) {
	// a secret-like value that slips the arg rules but that the display
	// redactor would mask must never become a card showing something else
	const secret = "sk-canary-evtest-9876543"
	tools.Default().Redact.Add(secret) // a registered secret value: not caught by the argument rules
	e, fw := pimEventEnv(t, [2]string{pim.ToolAddEvent, `{"title":"회의 ` + secret + `","start":"2026-10-28T06:00:00Z"}`})
	evs := writeStream(t, e, "alice@example")
	res := objects(evs, "tool_result")[0]
	if res["error_code"] != "invalid_arguments" || len(objects(evs, "confirmation")) != 0 || fw.writes() != 0 {
		t.Fatal(res)
	}
	// a calendar whose name the redactor would mask: refuse, don't show a different name
	e, fw = pimEventEnv(t, [2]string{pim.ToolAddEvent, `{"title":"x","start":"2026-10-28T06:00:00Z","calendar_id":"cal_s"}`})
	fw.calendars = []pim.Calendar{{ID: "cal_s", Name: "팀 " + secret, Writable: true}}
	evs = writeStream(t, e, "alice@example")
	if res := objects(evs, "tool_result")[0]; res["error_code"] != "invalid_arguments" || len(objects(evs, "confirmation")) != 0 {
		t.Fatal(res)
	}
}

func TestPimCard_NoMethodKey(t *testing.T) {
	for _, tc := range [][2]string{{pim.ToolAddReminder, addArgs}, {pim.ToolCompleteReminder, `{"id":"r1"}`}, {pim.ToolAddEvent, addEventArgs}} {
		e, _ := pimEventEnv(t, tc)
		evs := writeStream(t, e, "alice@example")
		conf := objects(evs, "confirmation")[0]
		pv, _ := conf["payload_view"].(map[string]any)
		if _, has := pv["method"]; has || pv["verb"] == nil || pv["display"] == nil && tc[0] == pim.ToolAddEvent {
			t.Fatal(tc[0], pv)
		}
		card := pendingCard(t, e, "alice@example")
		if strings.Contains(string(card.PayloadView), `"method"`) {
			t.Fatal(tc[0], string(card.PayloadView))
		}
		// the stored payload (what is executed) keeps the method
		if !strings.Contains(string(card.Payload), `"method"`) {
			t.Fatal(tc[0], string(card.Payload))
		}
	}
	// reminders views per §11.4
	e, _ := pimEventEnv(t, [2]string{pim.ToolAddReminder, `{"title":"우유 사기","due":"2026-10-28T09:00:00Z"}`})
	pv := objects(writeStream(t, e, "alice@example"), "confirmation")[0]["payload_view"].(map[string]any)
	if pv["verb"] != "add" || pv["due"] != "2026-10-28T18:00:00+09:00" || pv["display"] != "10/28 (수) 18:00" || pv["list"] != nil || len(pv) != 5 {
		t.Fatal(pv)
	}
	e, _ = pimEventEnv(t, [2]string{pim.ToolCompleteReminder, `{"id":"r1"}`})
	pv = objects(writeStream(t, e, "alice@example"), "confirmation")[0]["payload_view"].(map[string]any)
	if pv["verb"] != "update" || pv["id"] != "r1" || pv["title"] != "치과 예약 전화" || pv["list"] != "집" || pv["display"] != nil || len(pv) != 5 {
		t.Fatal(pv)
	}
}

// I-2: ScrubPimCards blanks the content of an ended event card (never a live one)
// and goes through the optional store interface.
func TestScrubPimCards(t *testing.T) {
	e, _ := pimEventEnv(t, [2]string{pim.ToolAddEvent, addEventArgs})
	evs := objects(writeStream(t, e, "alice@example"), "confirmation")
	if len(evs) != 1 {
		t.Fatal(evs)
	}
	// the card is live: even far in the future of its creation, "now" = just after it was made
	if n, err := e.srv.ScrubPimCards(t.Context(), time.Now()); err != nil || n != 0 {
		t.Fatalf("live card scrubbed: %d %v", n, err)
	}
	// 3 days later it has expired and is past the retention
	if n, err := e.srv.ScrubPimCards(t.Context(), time.Now().Add(72*time.Hour)); err != nil || n != 1 {
		t.Fatalf("expired card: scrubbed %d %v, want 1", n, err)
	}
	if n, _ := e.srv.ScrubPimCards(t.Context(), time.Now().Add(72*time.Hour)); n != 0 {
		t.Fatal("not idempotent")
	}
}
