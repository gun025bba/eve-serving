package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/pim"
	"github.com/gun025bba/eve-serving/internal/proxy"
	"github.com/gun025bba/eve-serving/internal/store"
)

// fakeWriter is a pim helper stand-in that can read and write.
type fakeWriter struct {
	mu        sync.Mutex
	reminders []pim.Reminder
	added     []pim.Payload
	completed []string
	events    []pim.EventWrite
	calendars []pim.Calendar
	addErr    error
}

func (f *fakeWriter) ListCalendars(context.Context) (pim.Calendars, error) {
	return pim.Calendars{Calendars: append([]pim.Calendar{}, f.calendars...)}, nil
}

func (f *fakeWriter) AddEvent(_ context.Context, e pim.EventWrite) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return f.addErr
}

func (f *fakeWriter) ListEvents(context.Context, time.Time, time.Time) (pim.Events, error) {
	return pim.Events{Events: []pim.Event{}}, nil
}

func (f *fakeWriter) ListReminders(context.Context, *time.Time, bool) (pim.Reminders, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return pim.Reminders{Reminders: append([]pim.Reminder{}, f.reminders...)}, nil
}

func (f *fakeWriter) AddReminder(_ context.Context, title string, due *time.Time, list string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := pim.Payload{Title: title, List: list}
	if due != nil {
		p.Due = due.Format(time.RFC3339)
	}
	f.added = append(f.added, p)
	return f.addErr
}

func (f *fakeWriter) CompleteReminder(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, id)
	return nil
}

func (f *fakeWriter) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.added) + len(f.completed) + len(f.events)
}

// pimWriteEnv: web env whose upstream emits the given tool calls one per
// round (all as alice), then answers. Routes depend on config, so the
// handler is rebuilt after the flags are set.
func pimWriteEnv(t *testing.T, writesOn bool, calls ...[2]string) (*webEnv, *fakeWriter) {
	e := newWebEnv(t, true)
	seen, mu := e.seen, e.mu
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		*seen = append(*seen, b)
		mu.Unlock()
		n := 0
		for _, m := range b["messages"].([]any) {
			if m.(map[string]any)["role"] == "tool" {
				n++
			}
		}
		if n < len(calls) {
			ab, _ := json.Marshal(calls[n][1])
			fmt.Fprintf(w, `{"id":"r","model":"m1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c%d","type":"function","function":{"name":%q,"arguments":%s}}]}}]}`, n, calls[n][0], ab)
			return
		}
		fmt.Fprint(w, `{"id":"r","model":"m1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(up.Close)
	e.srv.Cfg.Upstream = up.URL
	e.srv.Up = proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: 2 * time.Second})
	fw := &fakeWriter{reminders: []pim.Reminder{{ID: "r1", Title: "치과 예약 전화", List: "집"}}}
	e.srv.Pim = fw
	e.srv.Cfg.Pim.Enabled, e.srv.Cfg.Pim.WritesEnabled = true, writesOn
	owner := uidOf(t, &e.env, "alice@example")
	uidOf(t, &e.env, "bob@example")
	e.srv.Cfg.Pim.OwnerUserID = owner
	for _, who := range []string{"alice@example", "bob@example"} {
		if _, err := e.st.(store.UserSettingsStore).MergeUserSettings(t.Context(), who, "pim", []byte(`{"enabled":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	e.ts.Close()
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	return e, fw
}

func pimWriteChat(t *testing.T, e *webEnv, who string) string {
	t.Helper()
	r, b := e.call(t, who, "POST", "/v1/chat/completions", `{"stream":true,"web_search":"auto","messages":[{"role":"user","content":"리마인더 추가해줘"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	return string(b)
}

func pendingCard(t *testing.T, e *webEnv, who string) store.Action {
	t.Helper()
	acts, err := e.st.ListPendingActions(t.Context(), who)
	if err != nil || len(acts) != 1 {
		t.Fatal(err, acts)
	}
	return acts[0]
}

func pimToken(t *testing.T, e *webEnv, id string) string {
	t.Helper()
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/reissue", "")
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	tok, _ := js(t, b)["confirm_token"].(string)
	if tok == "" {
		t.Fatal(string(b))
	}
	return tok
}

func approveBody(tok, ph string) string {
	return `{"confirm_token":"` + tok + `","payload_hash":"` + ph + `"}`
}

const addArgs = `{"title":"우유 사기","due":"2026-10-29T09:00:00+09:00","list":"장보기"}`

func TestPimWrite_OffByDefault(t *testing.T) {
	e, fw := pimWriteEnv(t, false, [2]string{pim.ToolAddReminder, addArgs})
	pimWriteChat(t, e, "alice@example")
	for _, x := range e.requests() {
		if offered(x, pim.ToolAddReminder) || offered(x, pim.ToolCompleteReminder) {
			t.Fatal("write verbs offered while pim.writes_enabled=false")
		}
		if !offered(x, pim.ToolListReminders) {
			t.Fatal("read tools should still be offered")
		}
	}
	if res := lastToolResult(e); res != pim.Denied() {
		t.Fatal(res)
	}
	if acts, _ := e.st.ListPendingActions(t.Context(), "alice@example"); len(acts) != 0 || fw.writes() != 0 {
		t.Fatal("card or write without the flag", acts)
	}
	// no approve route either (memory is off too)
	if r, _ := e.call(t, "alice@example", "POST", "/v1/actions/x/approve", `{}`); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
	// writes flag without pim.enabled is still off
	e.srv.Cfg.Pim.Enabled, e.srv.Cfg.Pim.WritesEnabled = false, true
	if e.srv.pimWriteToolsFor(t.Context(), "alice@example") != nil {
		t.Fatal("writes_enabled must not outlive pim.enabled=false")
	}
}

func TestPimWrite_ProposeThenApproveExecutesOnce(t *testing.T) {
	e, fw := pimWriteEnv(t, true, [2]string{pim.ToolAddReminder, addArgs})
	var logs strings.Builder
	e.srv.Log = logging.New(&logs, logging.INFO)
	sse := pimWriteChat(t, e, "alice@example")
	for _, x := range e.requests() {
		if !offered(x, pim.ToolAddReminder) || !offered(x, pim.ToolCompleteReminder) {
			t.Fatal("owner: write verbs not offered")
		}
	}
	res := lastToolResult(e)
	if !strings.Contains(res, "pending_confirmation") || strings.Contains(res, "우유") {
		t.Fatal(res)
	}
	if fw.writes() != 0 {
		t.Fatal("written before approval")
	}
	card := pendingCard(t, e, "alice@example")
	if card.Tool != pim.ToolAddReminder || card.Tainted || len(card.Scopes) != 1 || card.Scopes[0] != pim.ScopeWrite {
		t.Fatalf("%+v", card)
	}
	// the card shows the exact values that will be written
	var v map[string]any
	json.Unmarshal(card.PayloadView, &v)
	if v["title"] != "우유 사기" || v["due"] != "2026-10-29T09:00:00+09:00" || v["list"] != "장보기" || v["verb"] != "add" {
		t.Fatal(string(card.PayloadView))
	}
	if ev := confirmEvent(t, sse); ev == nil || ev["tool"] != pim.ToolAddReminder {
		t.Fatal("no confirmation event", sse)
	}
	tok := pimToken(t, e, card.ID)
	// API key: interactive scope missing -> 403, nothing written
	if r, _ := e.call(t, "", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 403 {
		t.Fatal("bearer", r.StatusCode)
	}
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, "deadbeef")); r.StatusCode != 422 {
		t.Fatal(r.StatusCode, string(b))
	}
	if r, _ := e.call(t, "bob@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 404 {
		t.Fatal("foreign", r.StatusCode)
	}
	if fw.writes() != 0 {
		t.Fatal("written without a valid approval")
	}
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash))
	if r.StatusCode != 200 || !strings.Contains(string(b), `"executed"`) || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(r.StatusCode, string(b))
	}
	if len(fw.added) != 1 || fw.added[0].Title != "우유 사기" || fw.added[0].Due != "2026-10-29T09:00:00+09:00" || fw.added[0].List != "장보기" {
		t.Fatal(fw.added)
	}
	if strings.Contains(string(b), "우유") {
		t.Fatal("response echoes content", string(b))
	}
	// replay: the card is consumed
	if r, _ := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 409 {
		t.Fatal("replay", r.StatusCode)
	}
	if fw.writes() != 1 {
		t.Fatal("double write")
	}
	// logs: method, count, error code only
	for _, leak := range []string{"우유", "장보기", "2026-10-29"} {
		if strings.Contains(logs.String(), leak) {
			t.Fatalf("log leaks content %q: %s", leak, logs.String())
		}
	}
	if !strings.Contains(logs.String(), `"method":"add_reminder"`) || !strings.Contains(logs.String(), `"count":1`) {
		t.Fatal(logs.String())
	}
	// audit: no content either
	for _, ev := range e.audit.Rows() {
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), "우유") || strings.Contains(string(b), "장보기") {
			t.Fatal("audit leaks content", string(b))
		}
	}
}

func TestPimWrite_NonOwnerHiddenAndDenied(t *testing.T) {
	e, fw := pimWriteEnv(t, true, [2]string{pim.ToolAddReminder, addArgs})
	pimWriteChat(t, e, "bob@example")
	for _, x := range e.requests() {
		if offered(x, pim.ToolAddReminder) || offered(x, pim.ToolCompleteReminder) {
			t.Fatal("non-owner sees write verbs")
		}
	}
	if res := lastToolResult(e); res != pim.Denied() {
		t.Fatal(res)
	}
	if acts, _ := e.st.ListPendingActions(t.Context(), "bob@example"); len(acts) != 0 || fw.writes() != 0 {
		t.Fatal("non-owner got a card")
	}
}

func TestPimWrite_InvalidArgsNoCard(t *testing.T) {
	for name, args := range map[string]string{
		"empty title": `{"title":"   "}`,
		"newline":     "{\"title\":\"a\\nb\"}",
		"unknown key": `{"title":"x","notes":"y"}`,
		"bad due":     `{"title":"x","due":"tomorrow"}`,
		"long title":  `{"title":"` + strings.Repeat("가", 201) + `"}`,
		"secret-like": `{"title":"AKIAIOSFODNN7EXAMPLE 보관"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e, fw := pimWriteEnv(t, true, [2]string{pim.ToolAddReminder, args})
			pimWriteChat(t, e, "alice@example")
			if res := lastToolResult(e); !strings.Contains(res, `"error_code":"invalid_arguments"`) {
				t.Fatal(res)
			}
			if acts, _ := e.st.ListPendingActions(t.Context(), "alice@example"); len(acts) != 0 || fw.writes() != 0 {
				t.Fatal("card or write for invalid args")
			}
		})
	}
}

func TestPimWrite_CompleteCardShowsTitleAndRevalidates(t *testing.T) {
	e, fw := pimWriteEnv(t, true, [2]string{pim.ToolCompleteReminder, `{"id":"r1"}`})
	pimWriteChat(t, e, "alice@example")
	card := pendingCard(t, e, "alice@example")
	var v map[string]any
	json.Unmarshal(card.PayloadView, &v)
	if v["title"] != "치과 예약 전화" || v["list"] != "집" || v["verb"] != "update" {
		t.Fatal(string(card.PayloadView))
	}
	tok := pimToken(t, e, card.ID)
	// the reminder changed since the card was shown: nothing is written
	fw.mu.Lock()
	fw.reminders[0].Title = "다른 제목"
	fw.mu.Unlock()
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 409 || !strings.Contains(string(b), "item_gone") {
		t.Fatal(r.StatusCode, string(b))
	}
	if fw.writes() != 0 {
		t.Fatal("completed a different reminder")
	}
	// back to the shown state: approve completes exactly r1
	fw.mu.Lock()
	fw.reminders[0].Title = "치과 예약 전화"
	fw.mu.Unlock()
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	if len(fw.completed) != 1 || fw.completed[0] != "r1" {
		t.Fatal(fw.completed)
	}
}

func TestPimWrite_CompleteUnknownIDNoCard(t *testing.T) {
	e, fw := pimWriteEnv(t, true, [2]string{pim.ToolCompleteReminder, `{"id":"nope"}`})
	pimWriteChat(t, e, "alice@example")
	if res := lastToolResult(e); !strings.Contains(res, `"error_code":"not_found"`) {
		t.Fatal(res)
	}
	if acts, _ := e.st.ListPendingActions(t.Context(), "alice@example"); len(acts) != 0 || fw.writes() != 0 {
		t.Fatal("card for unknown id")
	}
}

func TestPimWrite_FlagOffAfterProposalBlocksApprove(t *testing.T) {
	e, fw := pimWriteEnv(t, true, [2]string{pim.ToolAddReminder, addArgs})
	pimWriteChat(t, e, "alice@example")
	card := pendingCard(t, e, "alice@example")
	tok := pimToken(t, e, card.ID)
	e.srv.Cfg.Pim.WritesEnabled = false
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 403 || !strings.Contains(string(b), "kill_switch") {
		t.Fatal(r.StatusCode, string(b))
	}
	if fw.writes() != 0 {
		t.Fatal("wrote with the flag off")
	}
	// the user's own pim.enabled=false also stops it
	e.srv.Cfg.Pim.WritesEnabled = true
	if _, err := e.st.(store.UserSettingsStore).MergeUserSettings(t.Context(), "alice@example", "pim", []byte(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 403 {
		t.Fatal(r.StatusCode)
	}
	if fw.writes() != 0 {
		t.Fatal("wrote with pim.enabled=false for the user")
	}
}

func TestPimWrite_HelperRefusalReleasesCard(t *testing.T) {
	e, fw := pimWriteEnv(t, true, [2]string{pim.ToolAddReminder, addArgs})
	pimWriteChat(t, e, "alice@example")
	card := pendingCard(t, e, "alice@example")
	tok := pimToken(t, e, card.ID)
	fw.addErr = &pim.Error{Code: pim.CodeWritesDisabled}
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 403 || !strings.Contains(string(b), "kill_switch") {
		t.Fatal(r.StatusCode, string(b))
	}
	// definite refusal: the card is pending again (needs a fresh token)
	st, err := e.st.(store.ActionApprover).ActionStatus(t.Context(), "alice@example", card.ID)
	if err != nil || st != "pending" {
		t.Fatal(st, err)
	}
}

// A helper that takes the request and then drops the connection: the write
// may or may not have happened, so the card stays consumed (no retry, no
// duplicate reminder).
func TestPimWrite_UnknownOutcomeKeepsCardConsumed(t *testing.T) {
	e, fw := pimWriteEnv(t, true, [2]string{pim.ToolAddReminder, addArgs})
	var dials int
	var mu sync.Mutex
	e.srv.Pim = &pimDropWriter{fakeWriter: fw, c: &pim.Client{Timeout: time.Second, Dial: func(context.Context) (net.Conn, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		a, b := net.Pipe()
		go func() {
			buf := make([]byte, 4096)
			b.Read(buf)
			b.Close()
		}()
		return a, nil
	}}}
	pimWriteChat(t, e, "alice@example")
	card := pendingCard(t, e, "alice@example")
	tok := pimToken(t, e, card.ID)
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash))
	m := js(t, b)
	if r.StatusCode != 502 || m["outcome_unknown"] != true || m["error_code"] == "" {
		t.Fatal(r.StatusCode, string(b))
	}
	st, _ := e.st.(store.ActionApprover).ActionStatus(t.Context(), "alice@example", card.ID)
	if st == "pending" {
		t.Fatal("card released after an unknown outcome")
	}
	if r, _ := e.call(t, "alice@example", "POST", "/v1/actions/"+card.ID+"/approve", approveBody(tok, card.PayloadHash)); r.StatusCode != 409 {
		t.Fatal("retry allowed", r.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if dials != 1 {
		t.Fatal("helper dialed", dials)
	}
}

// pimDropWriter reads like fakeWriter but writes through a real Client.
type pimDropWriter struct {
	*fakeWriter
	c *pim.Client
}

func (p *pimDropWriter) AddReminder(ctx context.Context, title string, due *time.Time, list string) error {
	return p.c.AddReminder(ctx, title, due, list)
}

// A proposal that follows a read of third-party-writable text is allowed, but
// the card is marked tainted and still shows the exact values; it executes
// only after approval.
func TestPimWrite_ProposalAfterReadIsTaintedCard(t *testing.T) {
	e, fw := pimWriteEnv(t, true, [2]string{pim.ToolListReminders, `{}`}, [2]string{pim.ToolAddReminder, addArgs})
	pimWriteChat(t, e, "alice@example")
	card := pendingCard(t, e, "alice@example")
	if !card.Tainted {
		t.Fatal("card after a pim read must be tainted")
	}
	if fw.writes() != 0 {
		t.Fatal("written without approval")
	}
}
