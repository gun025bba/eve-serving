package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/pim"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

// pimStream runs one streamed chat as who (persisted thread) and returns the
// SSE events in order.
func pimStream(t *testing.T, e *webEnv, who string) []map[string]any {
	t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true,"web_search":"auto","thread_id":"new","messages":[{"role":"user","content":"오늘 일정?"}]}`))
	req.Header.Set(testUserHeader, who)
	browserHeaders(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	evs, done := readSSE(t, resp.Body)
	if !done {
		t.Fatal("no [DONE]")
	}
	return evs
}

func pimToolEventsOf(evs []map[string]any) (call, result map[string]any) {
	for _, ev := range evs {
		switch ev["object"] {
		case "chat.completion.tool_call":
			call = ev
		case "chat.completion.tool_result":
			result = ev
		}
	}
	return
}

func TestToolEvent_PimReadShape(t *testing.T) {
	e, _ := pimEnv(t, true)
	evs := pimStream(t, e, "alice@example")
	var order []string
	for _, ev := range evs {
		switch o, _ := ev["object"].(string); o {
		case "chat.completion.tool_call", "chat.completion.tool_result", "chat.completion.persisted":
			order = append(order, strings.TrimPrefix(o, "chat.completion."))
		case "chat.completion.chunk":
			if cs, _ := ev["choices"].([]any); len(cs) > 0 {
				order = append(order, "delta")
			}
		case "chat.completion.sources":
			order = append(order, "sources")
		}
	}
	if got := strings.Join(order, ","); got != "tool_call,tool_result,delta,sources,persisted" {
		t.Fatal("order", got)
	}
	call, res := pimToolEventsOf(evs)
	if call["tool"] != "calendar_list_events" || call["id"] != "c0" {
		t.Fatal(call)
	}
	args := call["args"].(map[string]any)
	if len(args) != 2 || args["from"] != "2026-10-28T00:00:00+09:00" || args["to"] != "2026-10-29T00:00:00+09:00" {
		t.Fatal(args)
	}
	if res["status"] != "ok" || res["count"] != float64(1) || res["truncated"] != false {
		t.Fatal(res)
	}
	if _, has := res["sources"]; has {
		t.Fatal("pim tool_result must not carry sources")
	}
	if _, has := res["error_code"]; has {
		t.Fatal(res)
	}
	card := res["card"].(map[string]any)
	if card["kind"] != "calendar_events" || card["v"] != float64(1) {
		t.Fatal(card)
	}
	rg := card["range"].(map[string]any)
	if rg["from"] != "2026-10-28T00:00:00+09:00" || rg["to"] != "2026-10-29T00:00:00+09:00" {
		t.Fatal(rg)
	}
	it := card["items"].([]any)[0].(map[string]any)
	if len(it) != 7 || it["id"] != "e1" || it["title"] != "치과 예약" || it["start"] != "2026-10-28T18:30:00+09:00" ||
		it["end"] != "2026-10-28T19:00:00+09:00" || it["all_day"] != false || it["calendar"] != "c" || it["calendar_id"] != "cal1" {
		t.Fatal(it)
	}
}

func TestToolResult_ErrorCodeMirroredInCard(t *testing.T) {
	for _, tc := range []struct {
		err          error
		code, status string
	}{
		{&pim.Error{Code: pim.CodeUnavailable}, "pim_unavailable", "error"},
		{&pim.Error{Code: pim.CodeTimeout}, "timeout", "timeout"},
		{&pim.Error{Code: pim.CodePermissionDenied}, "permission_denied", "denied"},
	} {
		e, fp := pimEnv(t, true)
		fp.err = tc.err
		_, res := pimToolEventsOf(pimStream(t, e, "alice@example"))
		card, _ := res["card"].(map[string]any)
		if res["status"] != tc.status || res["error_code"] != tc.code || card["kind"] != "error" || card["v"] != float64(1) || card["error_code"] != tc.code {
			t.Fatal(tc.code, res)
		}
		if _, has := res["count"]; has {
			t.Fatal("error result carries count", res)
		}
	}
	// not exposed to this principal: denied with the same shape
	e, fp := pimEnv(t, true)
	call, res := pimToolEventsOf(pimStream(t, e, "bob@example"))
	card, _ := res["card"].(map[string]any)
	if call == nil || res["status"] != "denied" || res["error_code"] != "kill_switch" || card["error_code"] != "kill_switch" || fp.calls != 0 {
		t.Fatal(res, fp.calls)
	}
}

func TestToolResult_ErrorNoInternalDetail(t *testing.T) {
	e, fp := pimEnv(t, true)
	fp.err = errors.New("dial unix /Users/owner/eve-pim/pim.sock: connect: operation not permitted (errno 1) at helper.swift:42 host=mac-studio.local user=owner TCC /Library")
	evs := pimStream(t, e, "alice@example")
	b, _ := json.Marshal(evs)
	all := string(b)
	for _, bad := range []string{"sock", "errno", "helper", "swift", "mac-studio", "owner", "TCC", "/Users", "operation not permitted"} {
		if strings.Contains(all, bad) {
			t.Fatalf("event stream leaks %q: %s", bad, all)
		}
	}
	_, res := pimToolEventsOf(evs)
	if res["error_code"] != "pim_unavailable" || res["card"].(map[string]any)["error_code"] != "pim_unavailable" {
		t.Fatal(res)
	}
}

func TestToolResult_CardNotPersisted(t *testing.T) {
	e, _ := pimEnv(t, true)
	evs := pimStream(t, e, "alice@example")
	last := evs[len(evs)-1]
	mid, _ := last["message_id"].(string)
	if mid == "" {
		t.Fatal(last)
	}
	m, err := e.st.GetMessage(t.Context(), "alice@example", mid)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	calls := e.st.(*memstore.Store).ToolCalls(mid)
	cb, _ := json.Marshal(calls)
	for _, s := range []string{string(b), string(cb)} {
		for _, bad := range []string{"calendar_events", "치과", `"kind"`, "calendar_id", "cal1"} {
			if strings.Contains(s, bad) {
				t.Fatalf("stored data contains %q: %s", bad, s)
			}
		}
	}
	if len(calls) != 0 {
		t.Fatal("pim calls are not tool_calls rows", calls)
	}
}
