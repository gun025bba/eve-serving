package handlers

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/search"
)

// #137 (BE-2): first-round streaming, tool_call before execution, the day-level
// date, and the source contract (count == len(sources), published YYYY-MM-DD).

// A turn that needs no search: the first token reaches the client while the
// upstream is still generating (auto mode, round 1 is streamed).
func TestBE2_FirstRoundStreamsInAutoMode(t *testing.T) {
	release := make(chan struct{})
	e, _ := streamEnv(t, func(n int, b map[string]any, w http.ResponseWriter, fl http.Flusher) {
		sse(w, fl, delta("안녕"))
		select { // the rest only after the client saw the first token
		case <-release:
		case <-time.After(5 * time.Second):
		}
		sse(w, fl, delta("하세요"))
		sse(w, fl, usageChunk)
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	e.srv.Cfg.Web.AutoSelectOff = false
	resp := streamPost(t, e, `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	got := make(chan bool, 1)
	var acc strings.Builder
	go func() {
		for sc.Scan() {
			acc.WriteString(sc.Text() + "\n")
			if strings.Contains(acc.String(), "안녕") {
				got <- true
				return
			}
		}
		got <- false
	}()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("stream ended without the first token")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first token was not forwarded before the upstream finished")
	}
	close(release)
	for sc.Scan() {
		acc.WriteString(sc.Text() + "\n")
	}
	if got := clientText(acc.String()); got != "안녕하세요" {
		t.Fatalf("client text %q", got)
	}
}

// The accepted tool_call is on the wire before the search runs: the search is
// held until the client has read the tool_call, and no tool_result precedes it.
func TestBE2_ToolCallSentBeforeSearchRuns(t *testing.T) {
	reached, proceed := make(chan struct{}), make(chan struct{})
	var timedOut atomic.Bool
	e, _ := autoEnv(t, callStep{tool: "web_search", args: q("go 1.27 release")}, callStep{text: "ok [1]"})
	e.srv.Web.Search = hookSearch{e.fs, func() {
		close(reached)
		select {
		case <-proceed:
		case <-time.After(3 * time.Second):
			timedOut.Store(true)
		}
	}}
	resp := streamPost(t, e, `{"stream":true,"thread_id":"new","messages":[{"role":"user","content":"go 1.27 release"}]}`)
	evs := []map[string]any{}
	sc := bufio.NewScanner(resp.Body)
	defer resp.Body.Close()
	released := false
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "data: ") || l == "data: [DONE]" {
			continue
		}
		ev := js(t, []byte(l[6:]))
		evs = append(evs, ev)
		if ev["object"] == "chat.completion.tool_call" && !released {
			released = true
			<-reached // the search is running only now (or already) and still blocked
			close(proceed)
		}
	}
	if timedOut.Load() {
		t.Fatal("tool_call was only sent after the search finished")
	}
	var order []string
	for _, ev := range evs {
		if o, _ := ev["object"].(string); o == "chat.completion.tool_call" || o == "chat.completion.tool_result" {
			order = append(order, o+":"+fmt.Sprint(ev["id"]))
		}
	}
	if len(order) != 2 || order[0] != "chat.completion.tool_call:c1" || order[1] != "chat.completion.tool_result:c1" {
		t.Fatalf("order %v", order)
	}
}

// Text before a tool call is streamed and stored with the final answer.
func TestBE2_PreambleStreamedAndStored(t *testing.T) {
	e, _ := streamEnv(t, func(n int, b map[string]any, w http.ResponseWriter, fl http.Flusher) {
		if n == 1 {
			sse(w, fl, delta("검색해 볼게요. "))
			sse(w, fl, toolDelta(0, "c1", "web_search", q("go 1.27 release")))
		} else {
			sse(w, fl, delta("답이에요 [1]"))
		}
		sse(w, fl, usageChunk)
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	e.srv.Cfg.Web.AutoSelectOff = false
	evs := post(t, e, "go 1.27 release")
	var txt strings.Builder
	for _, ev := range evs {
		if cs, _ := ev["choices"].([]any); len(cs) > 0 {
			d, _ := cs[0].(map[string]any)["delta"].(map[string]any)
			if s, ok := d["content"].(string); ok {
				txt.WriteString(s)
			}
		}
	}
	want := "검색해 볼게요. 답이에요 [1]"
	if txt.String() != want {
		t.Fatalf("streamed %q", txt.String())
	}
	p := byObject(evs, "chat.completion.persisted")
	if len(p) != 1 {
		t.Fatal(p)
	}
	m, err := e.st.GetMessage(t.Context(), "alice@example", p[0]["message_id"].(string))
	if err != nil || m.Content != want {
		t.Fatalf("stored %q, %v", m.Content, err)
	}
}

// The date line is day-level (no clock time), comes from the server clock,
// and is the first system text, ahead of the self-description and memory.
func TestBE2_DateInjectedDayLevel(t *testing.T) {
	e, up := autoEnv(t, callStep{text: "네"})
	e.srv.Now = func() time.Time { return time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC) } // 18:30 KST
	post(t, e, "안녕")
	reqs := up.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	ms, _ := reqs[0]["messages"].([]any)
	first, _ := ms[0].(map[string]any)
	c, _ := first["content"].(string)
	if first["role"] != "system" || !strings.Contains(c, "Today is 2026-10-29 (Thu) in Asia/Seoul") {
		t.Fatalf("first message %v", first)
	}
	if strings.Contains(c, "18:30") || strings.Contains(c, "09:30") {
		t.Fatalf("clock time in the prompt: %q", c)
	}
	n := 0
	for _, m := range ms {
		if s, _ := m.(map[string]any)["content"].(string); strings.Contains(s, "Today is ") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d date lines", n)
	}
}

type listSearch []search.Result

func (listSearch) Name() string { return "fake" }
func (l listSearch) Search(context.Context, search.Query) ([]search.Result, error) {
	return append([]search.Result(nil), l...), nil
}

// count == len(sources) of the same event; published is YYYY-MM-DD and only
// inside 2000-01-01 .. today+1.
func TestBE2_SourceContract(t *testing.T) {
	day := func(y int, m time.Month, d int) *time.Time { x := time.Date(y, m, d, 5, 0, 0, 0, time.UTC); return &x }
	now := time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)
	e, _ := autoEnv(t, callStep{tool: "web_search", args: q("go 1.27 release")}, callStep{text: "ok [1]"})
	e.srv.Now = func() time.Time { return now }
	e.srv.Web.Search = listSearch{
		{Title: "ok", URL: "https://a.example/1", Snippet: "s", Published: day(2026, 10, 29)},
		{Title: "dup", URL: "https://a.example/1", Snippet: "s", Published: day(2026, 10, 29)},
		{Title: "old", URL: "https://b.example/2", Snippet: "s", Published: day(1999, 12, 31)},
		{Title: "future", URL: "https://c.example/3", Snippet: "s", Published: day(2026, 11, 5)},
		{Title: "tomorrow", URL: "https://d.example/4", Snippet: "s", Published: day(2026, 10, 30)},
		{Title: "none", URL: "https://e.example/5", Snippet: "s"},
	}
	evs := post(t, e, "go 1.27 release")
	rs := results(evs)
	if len(rs) != 1 {
		t.Fatal(rs)
	}
	ss, _ := rs[0]["sources"].([]any)
	if len(ss) != 5 || rs[0]["count"] != float64(len(ss)) {
		t.Fatalf("count %v sources %d", rs[0]["count"], len(ss))
	}
	want := map[string]string{"ok": "2026-10-29", "old": "", "future": "", "tomorrow": "2026-10-30", "none": ""}
	for _, x := range ss {
		s := x.(map[string]any)
		got, _ := s["published"].(string)
		if w := want[s["title"].(string)]; got != w {
			t.Errorf("%v: published %q, want %q", s["title"], got, w)
		}
	}
	p := byObject(evs, "chat.completion.persisted")
	tl, _ := p[0]["tools"].([]any)
	if len(tl) != 1 || tl[0].(map[string]any)["count"] != float64(5) {
		t.Fatalf("persisted tools %v", tl)
	}
}
