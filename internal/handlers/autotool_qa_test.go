package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/search"
)

// QA findings on #158 (freshness hint, kill-switch text calls, selected_by,
// persisted tools[], sources[].published).

const killTextCall = `<tool_call>{"name":"web_search","arguments":{"query":"go 1.27 release"}}</tool_call>`

// (2) The kill switch removes the tools from the request; a model that still
// writes a textual call must not have that text shown, and no tool runs. (The
// non-streamed path shares the same strip, covered by
// tools.TestStripDisabledToolCalls.)
func TestQA_KillSwitchStripsRawToolCallText(t *testing.T) {
	for _, y := range []string{"all_off: true\n", "disabled: [web_search, web_fetch]\n", "disabled: [web_search]\n"} {
		write, _ := useKillswitch(t)
		e, _ := autoEnv(t, callStep{text: "확인해 볼게요. " + killTextCall})
		write(y)
		out := readAll(t, streamPost(t, e, `{"stream":true,"thread_id":"new","messages":[{"role":"user","content":"go 1.27 release"}]}`))
		if strings.Contains(out, "tool_call>") || strings.Contains(out, `go 1.27 release"}`) {
			t.Fatalf("%q: raw tool call text reached the client:\n%s", y, out)
		}
		if got := clientText(out); got != "확인해 볼게요." {
			t.Errorf("%q: answer %q", y, got)
		}
		if e.fs.n != 0 {
			t.Errorf("%q: searched", y)
		}
	}
}

// Only calls to tools the kill switch turned off are stripped; text that is
// not a complete call of such a tool is left alone.
func TestQA_KillSwitchKeepsOtherText(t *testing.T) {
	write, _ := useKillswitch(t)
	e, _ := autoEnv(t, callStep{text: `예: <tool_call>{"name":"not_a_tool","arguments":{}}</tool_call>`})
	write("all_off: true\n")
	out := readAll(t, streamPost(t, e, `{"stream":true,"thread_id":"new","messages":[{"role":"user","content":"hi"}]}`))
	if got := clientText(out); got != `예: <tool_call>{"name":"not_a_tool","arguments":{}}</tool_call>` {
		t.Fatalf("%q", got)
	}
}

// (3) tool_result carries selected_by like tool_call does.
func TestQA_ToolResultSelectedBy(t *testing.T) {
	e, _ := autoEnv(t, callStep{tool: "web_search", args: q("go 1.27 release")}, callStep{text: "ok"})
	evs := post(t, e, "go 1.27 release")
	rs := results(evs)
	if len(rs) != 1 || rs[0]["selected_by"] != "model" {
		t.Fatalf("tool_result %v", rs)
	}
	tc := byObject(evs, "chat.completion.tool_call")
	if len(tc) != 1 || tc[0]["selected_by"] != rs[0]["selected_by"] {
		t.Fatalf("tool_call %v", tc)
	}
	// a rejected call still reports who selected it
	e, _ = autoEnv(t, callStep{tool: "web_search", args: `{}`}, callStep{text: "ok"})
	for _, r := range results(post(t, e, "go 1.27 release")) {
		if r["selected_by"] != "model" {
			t.Fatalf("rejected call %v", r)
		}
	}
}

// (4) the persisted event always carries tools[]; [] when no tool was used.
func TestQA_PersistedToolsEmptyArray(t *testing.T) {
	for _, off := range []bool{false, true} {
		e, _ := autoEnv(t, callStep{text: "네"})
		e.srv.Cfg.Web.AutoSelectOff = off // off: memory-only loop or the plain chat path
		out := readAll(t, streamPost(t, e, `{"stream":true,"thread_id":"new","messages":[{"role":"user","content":"한 단어로 대답해: 네"}]}`))
		var found bool
		for _, l := range strings.Split(out, "\n") {
			if !strings.HasPrefix(l, "data: {") || !strings.Contains(l, `"chat.completion.persisted"`) {
				continue
			}
			found = true
			var v map[string]json.RawMessage
			if err := json.Unmarshal([]byte(l[6:]), &v); err != nil {
				t.Fatal(err)
			}
			if string(v["tools"]) != "[]" {
				t.Errorf("auto_select_off=%v: tools = %s in %s", off, v["tools"], l)
			}
		}
		if !found {
			t.Fatalf("auto_select_off=%v: no persisted event:\n%s", off, out)
		}
	}
}

// (1) "지금" as filler does not make the answer fresh:"unverified"; a
// time-sensitive "지금 ..." still does.
func TestQA_NowFillerNotUnverified(t *testing.T) {
	cases := map[string]string{
		"지금 한 단어로만 대답해: 네": "",
		"지금 몇 시야?":         "unverified",
		"오늘 서울 날씨":         "unverified",
	}
	for text, want := range cases {
		e, _ := autoEnv(t, callStep{text: "네"})
		b, _ := json.Marshal(map[string]any{"stream": true, "thread_id": "new", "messages": []any{map[string]any{"role": "user", "content": text}}})
		p := byObject(sseEvents(t, e, string(b)), "chat.completion.persisted")
		if len(p) != 1 {
			t.Fatalf("%q: %v", text, p)
		}
		got, _ := p[0]["fresh"].(string)
		if got != want {
			t.Errorf("%q: fresh=%q, want %q", text, got, want)
		}
	}
}

type pubSearch struct{ pub *time.Time }

func (pubSearch) Name() string { return "fake" }
func (p pubSearch) Search(context.Context, search.Query) ([]search.Result, error) {
	return []search.Result{
		{Title: "Dated", URL: "https://dated.example/a", Snippet: "s1", Published: p.pub},
		{Title: "Undated", URL: "https://undated.example/b", Snippet: "s2"},
	}, nil
}

// (5) sources[].published appears when the result had a date, and only then.
func TestQA_SourcesPublished(t *testing.T) {
	pub := time.Date(2026, 10, 29, 3, 4, 5, 0, time.UTC)
	e, _ := autoEnv(t, callStep{tool: "web_search", args: q("go 1.27 release")}, callStep{text: "ok [1]"})
	e.srv.Web.Search = pubSearch{&pub}
	evs := post(t, e, "go 1.27 release")
	check := func(name string, ss any) {
		list, _ := ss.([]any)
		if len(list) != 2 {
			t.Fatalf("%s: sources %v", name, ss)
		}
		a, b := list[0].(map[string]any), list[1].(map[string]any)
		if a["published"] != "2026-10-29T03:04:05Z" {
			t.Errorf("%s: dated source %v", name, a)
		}
		if _, has := b["published"]; has {
			t.Errorf("%s: undated source has published: %v", name, b)
		}
	}
	rs := results(evs)
	if len(rs) != 1 {
		t.Fatal(rs)
	}
	check("tool_result", rs[0]["sources"])
	src := byObject(evs, "chat.completion.sources")
	if len(src) != 1 {
		t.Fatal(src)
	}
	check("sources", src[0]["sources"])
}
