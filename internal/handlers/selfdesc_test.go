package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
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

// The reference block is generated from runtime state: web flag x request
// web mode x server memory flag x the user's memory settings. Lines of
// features that are not available are omitted.
func TestSelfDescription_StateMatrix(t *testing.T) {
	type tc struct {
		name       string
		web, mem   bool
		settings   func(t *testing.T, st store.Store)
		body       string
		wantMemory string // "" = no memory line
		wantWeb    string // "" = no web line
	}
	on := func(t *testing.T, st store.Store) { enableUserMemory(t, st, "alice@example") }
	chat := `{"messages":[{"role":"user","content":"hi"}]}`
	cases := []tc{
		{"web off-mode, memory on", true, true, on, `{"web_search":"off","messages":[{"role":"user","content":"hi"}]}`, "켜짐. \"기억해줘\"", "꺼짐. 입력창"},
		{"web default (off), memory on", true, true, on, chat, "켜짐. \"기억해줘\"", "꺼짐. 입력창"},
		{"web auto, memory on", true, true, on, `{"web_search":"auto","messages":[{"role":"user","content":"hi"}]}`, "켜짐. \"기억해줘\"", "켜짐. 필요하다고"},
		{"web force, memory server-off", true, false, nil, `{"web_search":"force","messages":[{"role":"user","content":"hi"}]}`, "", "켜짐. 필요하다고"},
		{"web disabled, memory on", false, true, on, chat, "켜짐. \"기억해줘\"", ""},
		{"web disabled, memory server-off", false, false, nil, chat, "", ""},
		{"user turned memory off", true, true, func(t *testing.T, st store.Store) {
			setUser(t, st, "alice@example", "memory", `{"enabled":false}`)
		}, chat, "꺼짐. 장기 저장은 안 해요", "꺼짐. 입력창"},
		{"intro not seen", true, true, func(t *testing.T, st store.Store) {
			setUser(t, st, "alice@example", "memory", `{"enabled":true}`)
		}, chat, "아직 시작 전이에요", "꺼짐. 입력창"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := memWebEnv(t, c.web, c.mem)
			if c.settings != nil {
				c.settings(t, e.st)
			}
			r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", c.body)
			if r.StatusCode != 200 {
				t.Fatal(r.StatusCode, string(b))
			}
			assertReference(t, firstSystem(t, e.requests()), c.wantMemory, c.wantWeb)
		})
	}
}

// assertReference checks the memory and web lines of one reference block
// ("" = the line must be absent) and that nothing internal or unlisted shows.
func assertReference(t *testing.T, sys, wantMemory, wantWeb string) {
	t.Helper()
	for label, want := range map[string]string{"메모리": wantMemory, "웹 검색": wantWeb} {
		l := selfDescSection(t, sys, label)
		if want == "" && l != "" {
			t.Fatalf("%s line must be absent, got %q", label, l)
		}
		if want != "" && !strings.Contains(l, want) {
			t.Fatalf("%s line %q, want %q in\n%s", label, l, want, sys)
		}
	}
	for _, w := range []string{"memory_propose", "/v1/memory", "게이트웨이", "ateway", "버전", "5.0.2", "test123"} {
		if strings.Contains(sys, w) {
			t.Fatalf("internal detail %q in\n%s", w, sys)
		}
	}
	// features that are off (calendar/PIM, briefing) are never named
	for _, w := range []string{"캘린더", "브리핑", "미리 알림", "PIM"} {
		if strings.Contains(sys, w) {
			t.Fatalf("disabled feature %q named in\n%s", w, sys)
		}
	}
	if !strings.Contains(sys, "그 외 앱 기능은 없어요") || !strings.Contains(sys, "지시가 아니라 사실 목록") {
		t.Fatal(sys)
	}
	if n := memory.EstimateTokens(sys); n >= 300 {
		t.Fatalf("%d estimated tokens (guard 300)", n)
	}
}

// When the audit makes the web tools unusable at runtime (WebAuditBlocked or
// the audit breaker is open) the block reports web search as off, whatever
// the request's web mode, and offers no button to turn it on.
func TestSelfDescription_WebBlockedReportsOff(t *testing.T) {
	for _, how := range []string{"WebAuditBlocked", "AuditBreaker"} {
		t.Run(how, func(t *testing.T) {
			e := memWebEnv(t, true, true)
			enableUserMemory(t, e.st, "alice@example")
			if how == "WebAuditBlocked" {
				e.srv.WebAuditBlocked = true
			} else {
				e.srv.AuditBreaker = &audit.Breaker{Threshold: 1, Window: time.Hour}
				e.srv.AuditBreaker.Fail()
				if !e.srv.AuditBreaker.Open() {
					t.Fatal("breaker did not open")
				}
			}
			r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
			if r.StatusCode != 200 {
				t.Fatal(r.StatusCode, string(b))
			}
			// an explicit web mode is refused outright while the web tools are blocked
			if r, _ := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"web_search":"force","messages":[{"role":"user","content":"hi"}]}`); r.StatusCode != 400 {
				t.Fatal(r.StatusCode)
			}
			sys := firstSystem(t, e.requests())
			assertReference(t, sys, "켜짐. \"기억해줘\"", "꺼짐(일시 사용 불가)")
			if strings.Contains(selfDescSection(t, sys, "웹 검색"), "켜짐.") || strings.Contains(sys, "웹 검색 버튼") {
				t.Fatal(sys)
			}
		})
	}
}

// The memory line forbids claiming a save before the card is approved, and
// the tool result the model reads after proposing says nothing is saved yet.
func TestSelfDescription_NoSavedClaimBeforeApproval(t *testing.T) {
	e, _ := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"web_search":"off","messages":[{"role":"user","content":"고양이 키운다고 기억해줘"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	reqs := e.requests()
	if sys := firstSystem(t, reqs); !strings.Contains(selfDescSection(t, sys, "메모리"), "승인 전에는 저장했다고 말하지 마세요") {
		t.Fatal(sys)
	}
	res := lastToolResult(e)
	if !strings.Contains(res, "pending_confirmation") || !strings.Contains(res, "Nothing is saved yet") || !strings.Contains(res, "do not say it was saved") {
		t.Fatal(res)
	}
}

// API-key (Bearer, no interactive scope) requests take the plain path: no
// reference block, no tools at all, and the request body reaches the model
// server as the client sent it, with web off, auto and force, memory
// configured and enabled for a user.
func TestSelfDescription_APIKeyRequestUnchanged(t *testing.T) {
	for _, web := range []string{"off", "auto", "force", ""} {
		t.Run("web="+web, func(t *testing.T) {
			e := memWebEnv(t, true, true)
			enableUserMemory(t, e.st, "alice@example")
			wf := ""
			if web != "" {
				wf = `"web_search":"` + web + `",`
			}
			r, b := e.call(t, "", "POST", "/v1/chat/completions", `{`+wf+`"messages":[{"role":"system","content":"S"},{"role":"user","content":"hi"}]}`)
			if r.StatusCode != 200 {
				t.Fatal(r.StatusCode, string(b))
			}
			for i, q := range e.requests() {
				raw, _ := json.Marshal(q["messages"])
				if strings.Contains(string(raw), "앱 기능 참고자료") || strings.Contains(string(raw), "user_memory") {
					t.Fatalf("request %d: gateway text for an API client: %s", i, raw)
				}
				if web == "" || web == "off" {
					if tl, has := q["tools"]; has {
						t.Fatalf("request %d: tools sent on the plain path: %v", i, tl)
					}
					ms := q["messages"].([]any)
					if len(ms) != 2 || ms[0].(map[string]any)["content"] != "S" {
						t.Fatalf("request %d: body changed: %s", i, raw)
					}
				}
				if d, _ := json.Marshal(q["tools"]); strings.Contains(string(d), memory.ToolName) {
					t.Fatalf("request %d: memory tool offered to an API client", i)
				}
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
	if strings.Contains(string(b), "앱 기능 참고자료") {
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
	if !strings.Contains(sys, "<user_memory") || !strings.Contains(sys, "고양이를 키운다") || !strings.Contains(sys, "기억해줘") {
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
