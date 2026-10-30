package handlers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// useDenylist installs tools.denylist for one test.
func useDenylist(t *testing.T, names ...string) {
	t.Helper()
	if _, err := tools.SetDenylist(names); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tools.SetDenylist(nil) })
}

// #159: the default is the empty list - every tool is offered.
func TestDenylist159_DefaultOffersEveryTool(t *testing.T) {
	pim := &mockPIM{names: []string{"calendar_list_events"}, fields: map[string]any{"title": "x"}}
	e, up := autoEnv(t, callStep{text: "ok"})
	e.srv.Personal = pim
	post(t, e, "hi")
	n := toolNames(up.requests()[0])
	for _, want := range []string{"web_search", "web_fetch", "calendar_list_events"} {
		if !has(n, want) {
			t.Errorf("%s missing from %v", want, n)
		}
	}
}

// #159 exposure: a denied tool is not offered to the model - web, personal and
// the memory tool alike - and the others stay.
func TestDenylist159_DeniedToolsAreNotOffered(t *testing.T) {
	useDenylist(t, "web_search", "calendar_list_events", "memory.propose") // alias accepted
	pim := &mockPIM{names: []string{"calendar_list_events", "reminder_list"}, fields: map[string]any{"title": "x"}}
	e, up := autoEnv(t, callStep{text: "ok"})
	e.srv.Personal = pim
	post(t, e, "hi")
	n := toolNames(up.requests()[0])
	for _, bad := range []string{"web_search", "calendar_list_events", memory.ToolName} {
		if has(n, bad) {
			t.Errorf("denied tool %s offered: %v", bad, n)
		}
	}
	for _, want := range []string{"web_fetch", "reminder_list"} {
		if !has(n, want) {
			t.Errorf("%s should still be offered: %v", want, n)
		}
	}
}

// #159 refusal: a call to a denied tool that arrives anyway is refused and
// never executed (web adapter, personal adapter, memory tool).
func TestDenylist159_DeniedCallsAreRefused(t *testing.T) {
	useDenylist(t, "web_search", "calendar_list_events", "memory_propose")
	pim := &mockPIM{names: []string{"calendar_list_events"}, fields: map[string]any{"title": "x"}}
	e, _ := autoEnv(t,
		callStep{tool: "web_search", args: q("go 1.27 release")},
		callStep{tool: "calendar_list_events", args: `{}`},
		callStep{tool: memory.ToolName, args: `{"op":"add","text":"고양이를 키운다"}`},
		callStep{text: "ok"})
	e.srv.Personal = pim
	evs := post(t, e, "go 1.27 release")
	if e.fs.n != 0 {
		t.Fatal("a denied web_search was executed")
	}
	if pim.calls != 0 {
		t.Fatal("a denied personal tool was executed")
	}
	got := map[string]string{}
	for _, r := range results(evs) {
		got[fmt.Sprint(r["tool"])] = fmt.Sprint(r["status"]) + "/" + fmt.Sprint(r["error_code"])
	}
	for _, name := range []string{"web_search", "calendar_list_events"} {
		if got[name] != "denied/kill_switch" && got[name] != "blocked/kill_switch" && got[name] != "denied/permission_denied" {
			t.Errorf("%s: status/code %q (all: %v)", name, got[name], got)
		}
	}
}

// A memory_propose call that arrives although the tool is denied is refused
// with tool_unavailable and saves nothing (no card, no write).
func TestDenylist159_DeniedMemoryCallIsRefused(t *testing.T) {
	useDenylist(t, "memory_propose")
	e, up := autoEnv(t, callStep{tool: memory.ToolName, args: `{"op":"add","text":"고양이를 키운다"}`}, callStep{text: "ok"})
	post(t, e, "hi")
	if has(toolNames(up.requests()[0]), memory.ToolName) {
		t.Fatal("denied memory tool offered")
	}
	reqs := up.requests()
	if len(reqs) < 2 || !strings.Contains(fmt.Sprint(reqs[len(reqs)-1]["messages"]), "tool_unavailable") {
		t.Fatalf("memory call not refused: %d requests", len(reqs))
	}
	if d, _ := e.srv.Memory.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 0 {
		t.Fatalf("write executed: %v", d.Items)
	}
}

// #159: names are validated (a typo must not silently expose a tool) and the
// alias is canonicalized.
func TestDenylist159_Normalize(t *testing.T) {
	got, err := tools.NormalizeDenylist([]string{"web_search", "memory.propose", "web_search", "calendar_list_events"})
	if err != nil || fmt.Sprint(got) != "[calendar_list_events memory_propose web_search]" {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"", "Web_Search", "web search", "web-search", "../x", "a,b"} {
		if _, err := tools.SetDenylist([]string{"web_search", bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if tools.Denied("web_search") {
			t.Errorf("a rejected list changed the active list (%q)", bad)
		}
	}
}
