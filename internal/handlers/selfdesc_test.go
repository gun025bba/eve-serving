package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/tools"
)

// firstSystem returns the content of the first system message of the first
// upstream request.
func firstSystem(t *testing.T, reqs []map[string]any) string {
	t.Helper()
	if len(reqs) == 0 {
		t.Fatal("no upstream request")
	}
	for _, m := range reqs[0]["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] == "system" {
			c, _ := mm["content"].(string)
			if strings.HasPrefix(c, selfDescPrefix) {
				return c
			}
		}
	}
	t.Fatalf("no self-description in %v", reqs[0]["messages"])
	return ""
}

func selfDescSection(t *testing.T, sys, label string) string {
	t.Helper()
	for _, l := range strings.Split(sys, "\n") {
		if strings.HasPrefix(l, "- "+label+":") {
			return l
		}
	}
	return ""
}

// memWebEnv: web env (GW_WEB_ENABLED=web) with the memory store wired.
func memWebEnv(t *testing.T, web, memSrv bool) *webEnv {
	t.Helper()
	e := newWebEnv(t, web)
	fs, err := memory.Open(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Memory, e.srv.MemoryAuditKey = fs, []byte(strings.Repeat("k", 32))
	e.srv.Cfg.Memory.Enabled = memSrv
	e.srv.Version = "5.0.2+test123"
	e.ts.Close()
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

func setUser(t *testing.T, st store.Store, login, section, raw string) {
	t.Helper()
	if _, err := st.(store.UserSettingsStore).MergeUserSettings(context.Background(), login, section, []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

// The self-description is generated from runtime state: web flag x request
// web mode x server memory flag x the user's memory settings.
func TestSelfDescription_StateMatrix(t *testing.T) {
	type tc struct {
		name       string
		web, mem   bool
		user       string // "" = API key principal
		settings   func(t *testing.T, st store.Store)
		body       string
		wantMemory string
		wantTools  []string
		notTools   []string
	}
	on := func(t *testing.T, st store.Store) { enableUserMemory(t, st, "alice@example") }
	chat := `{"messages":[{"role":"user","content":"hi"}]}`
	cases := []tc{
		{"web on/off-mode, memory on", true, true, "alice@example", on, `{"web_search":"off","messages":[{"role":"user","content":"hi"}]}`,
			"켜짐", []string{"웹 검색(꺼짐", "기억 제안(memory_propose)"}, nil},
		{"web default (off), memory on", true, true, "alice@example", on, chat, "켜짐", []string{"웹 검색(꺼짐", "기억 제안(memory_propose)"}, nil},
		{"web auto, memory on", true, true, "alice@example", on, `{"web_search":"auto","messages":[{"role":"user","content":"hi"}]}`,
			"켜짐", []string{"웹 검색(켜짐)", "기억 제안(memory_propose)"}, []string{"꺼짐"}},
		{"web force, memory server-off", true, false, "alice@example", nil, `{"web_search":"force","messages":[{"role":"user","content":"hi"}]}`,
			"이 연결에서는 사용할 수 없어요", []string{"웹 검색(켜짐)"}, []string{"기억 제안", "memory_propose"}},
		{"web disabled, memory on", false, true, "alice@example", on, chat, "켜짐", []string{"기억 제안(memory_propose)"}, []string{"웹 검색"}},
		{"web disabled, memory server-off", false, false, "alice@example", nil, chat, "이 연결에서는 사용할 수 없어요", []string{"없음"}, []string{"웹 검색", "기억 제안"}},
		{"user turned memory off", true, true, "alice@example", func(t *testing.T, st store.Store) {
			setUser(t, st, "alice@example", "memory", `{"enabled":false}`)
		}, chat, "꺼짐", []string{"웹 검색(꺼짐"}, []string{"기억 제안"}},
		{"intro not seen", true, true, "alice@example", func(t *testing.T, st store.Store) {
			setUser(t, st, "alice@example", "memory", `{"enabled":true}`)
		}, chat, "첫 사용 안내", []string{"웹 검색(꺼짐"}, []string{"기억 제안"}},
		{"api key principal", true, true, "", on, chat, "이 연결에서는 사용할 수 없어요", []string{"웹 검색(꺼짐"}, []string{"기억 제안"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := memWebEnv(t, c.web, c.mem)
			if c.settings != nil {
				c.settings(t, e.st)
			}
			r, b := e.call(t, c.user, "POST", "/v1/chat/completions", c.body)
			if r.StatusCode != 200 {
				t.Fatal(r.StatusCode, string(b))
			}
			sys := firstSystem(t, e.requests())
			if v := selfDescSection(t, sys, "버전"); v != "- 버전: 5.0.2+test123" {
				t.Fatalf("version line %q", v)
			}
			if m := selfDescSection(t, sys, "메모리"); !strings.Contains(m, c.wantMemory) {
				t.Fatalf("memory line %q, want %q", m, c.wantMemory)
			}
			tl := selfDescSection(t, sys, "도구")
			for _, w := range c.wantTools {
				if !strings.Contains(tl, w) {
					t.Fatalf("tools line %q missing %q", tl, w)
				}
			}
			for _, w := range c.notTools {
				if strings.Contains(tl, w) {
					t.Fatalf("unexpected %q in tools line %q", w, tl)
				}
			}
			// features that are off (calendar/PIM, briefing) are never named
			for _, w := range []string{"캘린더", "브리핑", "미리 알림", "PIM"} {
				if strings.Contains(sys, w) {
					t.Fatalf("disabled feature %q named in\n%s", w, sys)
				}
			}
			if n := memory.EstimateTokens(sys); n >= 200 {
				t.Fatalf("%d tokens", n)
			}
		})
	}
}

// Order: self-description, client system text, memory block last. It is
// per request and never persisted into the stored thread.
func TestSelfDescription_OrderAndNotPersisted(t *testing.T) {
	e := memWebEnv(t, true, true)
	enableUserMemory(t, e.st, "alice@example")
	memory.Add(e.srv.Memory, uidOf(t, &e.env, "alice@example"), memory.NewItem{Kind: "fact", Text: "고양이를 키운다", Confidence: "user"}, time.Now())
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"thread_id":"new","messages":[{"role":"system","content":"CLIENT"},{"role":"user","content":"hi"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	sys := firstSystem(t, e.requests())
	a, c, m := strings.Index(sys, selfDescPrefix), strings.Index(sys, "CLIENT"), strings.Index(sys, "<user_memory")
	if !(a == 0 && a < c && c < m) || !strings.HasSuffix(sys, "\">") {
		t.Fatalf("order wrong:\n%s", sys)
	}
	for _, q := range e.requests() {
		assertBlockLast(t, q, "selfdesc-order")
	}
	// the client sees no gateway text in the stored messages
	if strings.Contains(string(b), "자기 설명") {
		t.Fatal("self-description leaked into the response")
	}
}

// With web off and memory active, memory_propose is the only tool: the model
// can create a card, web tools are never offered or executed.
func TestMemoryPropose_WebModeOff_CardThenInjected(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	if !e.srv.webEnabled() {
		t.Fatal("test needs GW_WEB_ENABLED=true")
	}
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"stream":true,"web_search":"off","messages":[{"role":"user","content":"고양이 키운다고 기억해줘"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	reqs := e.requests()
	if len(reqs) != 2 {
		t.Fatalf("want propose round + answer, got %d requests", len(reqs))
	}
	// only memory_propose is offered; no web system prompt, no web tool
	defs, _ := json.Marshal(reqs[0]["tools"])
	if !strings.Contains(string(defs), memory.ToolName) || strings.Contains(string(defs), "web_search") || strings.Contains(string(defs), "web_fetch") {
		t.Fatal(string(defs))
	}
	for _, m := range reqs[0]["messages"].([]any) {
		if c, _ := m.(map[string]any)["content"].(string); c == tools.SystemPrompt {
			t.Fatal("web system prompt sent in web-off mode")
		}
	}
	if !strings.Contains(string(b), "pending_confirmation") && confirmEvent(t, string(b)) == nil {
		t.Fatalf("no confirmation event in %s", b)
	}
	if strings.Contains(string(b), "chat.completion.sources") {
		t.Fatal("sources event in web-off mode")
	}
	if res := lastToolResult(e); !strings.Contains(res, "pending_confirmation") {
		t.Fatal(res)
	}
	acts, _ := e.st.ListPendingActions(t.Context(), "alice@example")
	if len(acts) != 1 || acts[0].Tool != memory.ToolPropose {
		t.Fatal(acts)
	}
	if d, _ := fs.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 0 {
		t.Fatal("written before approval")
	}
	// approve, then the next request carries the user_memory block
	r, b = e.call(t, "alice@example", "POST", "/v1/actions/"+acts[0].ID+"/reissue", "")
	tok, _ := js(t, b)["confirm_token"].(string)
	if r.StatusCode != 200 || tok == "" {
		t.Fatal(r.StatusCode, string(b))
	}
	r, b = e.call(t, "alice@example", "POST", "/v1/actions/"+acts[0].ID+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+acts[0].PayloadHash+`"}`)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"executed"`) {
		t.Fatal(r.StatusCode, string(b))
	}
	before := len(e.requests())
	if r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"web_search":"off","messages":[{"role":"user","content":"내가 뭘 키우지?"}]}`); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	next := e.requests()[before]
	sys := firstSystem(t, []map[string]any{next})
	if !strings.Contains(sys, "<user_memory") || !strings.Contains(sys, "고양이를 키운다") || !strings.Contains(sys, "기억 제안(memory_propose)") {
		t.Fatal(sys)
	}
	assertBlockLast(t, next, "after-approval")
	if e.fs.n != 0 {
		t.Fatal("web search ran in web-off mode")
	}
}

// A web tool call that arrives while only memory_propose is offered is refused.
func TestMemoryPropose_WebModeOff_RefusesWebToolCall(t *testing.T) {
	e := memWebEnv(t, true, true) // webUpstream answers web_search whenever tools are present
	enableUserMemory(t, e.st, "alice@example")
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"web_search":"off","messages":[{"role":"user","content":"hi"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	if e.fs.n != 0 || len(e.ff.urls) != 0 {
		t.Fatal("web tool executed while not offered")
	}
	if res := lastToolResult(e); !strings.Contains(res, "tool_unavailable") {
		t.Fatal(res)
	}
	if strings.Contains(string(b), "chat.completion.tool_call") {
		t.Fatal("web tool events in web-off mode")
	}
}

// Web server flag off + memory active still gets the propose tool (no Web deps).
func TestMemoryPropose_WebDisabledServer(t *testing.T) {
	e := memWebEnv(t, false, true)
	enableUserMemory(t, e.st, "alice@example")
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	defs, _ := json.Marshal(e.requests()[0]["tools"])
	if !strings.Contains(string(defs), memory.ToolName) || strings.Contains(string(defs), "web_search") {
		t.Fatal(string(defs))
	}
}
