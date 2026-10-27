package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/memory"
	"github.com/gun025bba/eve-serving/internal/proxy"
)

// memToolEnv: web env whose upstream first calls memory_propose with args
// (optionally after a web_search round), then answers.
func memToolEnv(t *testing.T, memOn bool, args string, searchFirst bool) (*webEnv, *memory.FileStore) {
	e := newWebEnv(t, true)
	seen, mu := e.seen, e.mu
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		*seen = append(*seen, b)
		mu.Unlock()
		nTool := 0
		for _, m := range b["messages"].([]any) {
			if m.(map[string]any)["role"] == "tool" {
				nTool++
			}
		}
		call := func(name, a string) {
			ab, _ := json.Marshal(a)
			fmt.Fprintf(w, `{"id":"r","model":"m1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c%d","type":"function","function":{"name":%q,"arguments":%s}}]}}]}`, nTool, name, ab)
		}
		switch {
		case searchFirst && nTool == 0:
			call("web_search", `{"query":"go 1.27"}`)
		case (searchFirst && nTool == 1) || (!searchFirst && nTool == 0):
			call(memory.ToolName, args)
		default:
			fmt.Fprint(w, `{"id":"r","model":"m1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		}
	}))
	t.Cleanup(up.Close)
	e.srv.Cfg.Upstream = up.URL
	e.srv.Up = proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: 2 * time.Second})
	fs, err := memory.Open(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Memory, e.srv.MemoryAuditKey = fs, []byte(strings.Repeat("k", 32))
	e.srv.Cfg.Memory.Enabled = memOn
	// routes depend on memory state: rebuild the handler
	e.ts.Close()
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	return e, fs
}

func chatAuto(t *testing.T, e *webEnv) string {
	t.Helper()
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"stream":true,"web_search":"auto","messages":[{"role":"user","content":"기억해줘"}]}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	return string(b)
}

func confirmEvent(t *testing.T, sse string) map[string]any {
	t.Helper()
	for _, l := range strings.Split(sse, "\n") {
		if strings.HasPrefix(l, "data: ") && strings.Contains(l, "chat.completion.confirmation") || strings.Contains(l, `"proposal"`) && strings.HasPrefix(l, "data: ") {
			var m map[string]any
			if json.Unmarshal([]byte(l[6:]), &m) == nil {
				return m
			}
		}
	}
	return nil
}

func lastToolResult(e *webEnv) string {
	q := e.requests()
	ms := q[len(q)-1]["messages"].([]any)
	for i := len(ms) - 1; i >= 0; i-- {
		if m := ms[i].(map[string]any); m["role"] == "tool" {
			return m["content"].(string)
		}
	}
	return ""
}

func TestMemoryTool_OffNoToolDefNoRoute(t *testing.T) {
	e, _ := memToolEnv(t, false, `{"op":"add","text":"x"}`, false)
	chatAuto(t, e)
	for _, q := range e.requests() {
		b, _ := json.Marshal(q["tools"])
		if strings.Contains(string(b), memory.ToolName) {
			t.Fatal("memory tool exposed while off")
		}
	}
	if r, _ := e.call(t, "alice@example", "POST", "/v1/actions/x/approve", `{"confirm_token":"a","payload_hash":"b"}`); r.StatusCode != 404 && r.StatusCode != 405 {
		t.Fatal(r.StatusCode)
	}
}

func TestMemoryTool_TaintedAfterWebDenied(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, true)
	chatAuto(t, e)
	if res := lastToolResult(e); !strings.Contains(res, `"tainted"`) {
		t.Fatal(res)
	}
	acts, _ := e.st.ListPendingActions(t.Context(), "alice@example")
	if len(acts) != 0 {
		t.Fatal("card created while tainted")
	}
	d, _ := fs.Load(uidOf(t, &e.env, "alice@example"))
	if len(d.Items) != 0 {
		t.Fatal("written")
	}
}

func proposeAndToken(t *testing.T, e *webEnv, args string) (id, tok, ph string) {
	t.Helper()
	sse := chatAuto(t, e)
	if res := lastToolResult(e); !strings.Contains(res, "pending_confirmation") {
		t.Fatal(res, sse)
	}
	acts, _ := e.st.ListPendingActions(t.Context(), "alice@example")
	if len(acts) != 1 || acts[0].Tool != memory.ToolPropose {
		t.Fatal(acts)
	}
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+acts[0].ID+"/reissue", "")
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	m := js(t, b)
	tok, _ = m["confirm_token"].(string)
	if tok == "" {
		t.Fatal(string(b))
	}
	return acts[0].ID, tok, acts[0].PayloadHash
}

func TestMemoryTool_AddApproveExecutesOnce(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	id, tok, ph := proposeAndToken(t, e, "")
	d, _ := fs.Load(uidOf(t, &e.env, "alice@example"))
	if len(d.Items) != 0 {
		t.Fatal("written before approval")
	}
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"deadbeef"}`); r.StatusCode != 422 || !strings.Contains(string(b), "payload_mismatch") {
		t.Fatal(r.StatusCode, string(b))
	}
	if r, b := e.call(t, "bob@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+ph+`"}`); r.StatusCode != 404 {
		t.Fatal("foreign", r.StatusCode, string(b))
	}
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+ph+`"}`)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"executed"`) || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(r.StatusCode, string(b))
	}
	d, _ = fs.Load(uidOf(t, &e.env, "alice@example"))
	if len(d.Items) != 1 || d.Items[0].Text != "고양이를 키운다" {
		t.Fatal(d.Items)
	}
	// replay: no longer pending
	if r, _ := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+ph+`"}`); r.StatusCode != 409 {
		t.Fatal("replay", r.StatusCode)
	}
	d, _ = fs.Load(uidOf(t, &e.env, "alice@example"))
	if len(d.Items) != 1 {
		t.Fatal("double write")
	}
}

func TestMemoryTool_WrongTokenAndStrictBody(t *testing.T) {
	e, _ := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	id, _, ph := proposeAndToken(t, e, "")
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"nope","payload_hash":"`+ph+`"}`); r.StatusCode != 410 {
		t.Fatal(r.StatusCode, string(b))
	}
	if r, _ := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"x","payload_hash":"y","extra":1}`); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

func TestMemoryTool_ForgetCardItemGone(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"forget","query":"고양이"}`, false)
	it, _ := memory.Add(fs, uidOf(t, &e.env, "alice@example"), memory.NewItem{Kind: "fact", Text: "고양이를 키운다", Confidence: "user"}, time.Now())
	id, tok, ph := proposeAndToken(t, e, "")
	memory.Delete(fs, uidOf(t, &e.env, "alice@example"), []string{it.ID}, time.Now())
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+ph+`"}`)
	if r.StatusCode != 409 || !strings.Contains(string(b), "item_gone") {
		t.Fatal(r.StatusCode, string(b))
	}
}

func TestMemoryTool_ForgetApproveDeletes(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"forget","query":"고양이"}`, false)
	memory.Add(fs, uidOf(t, &e.env, "alice@example"), memory.NewItem{Kind: "fact", Text: "고양이를 키운다", Confidence: "user"}, time.Now())
	id, tok, ph := proposeAndToken(t, e, "")
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+ph+`"}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	d, _ := fs.Load(uidOf(t, &e.env, "alice@example"))
	if len(d.Items) != 0 {
		t.Fatal(d.Items)
	}
}

func TestMemoryTool_ForgetNoMatchNoCard(t *testing.T) {
	e, _ := memToolEnv(t, true, `{"op":"forget","query":"강아지"}`, false)
	chatAuto(t, e)
	if res := lastToolResult(e); !strings.Contains(res, "no_match") {
		t.Fatal(res)
	}
}

func TestMemoryTool_SecretDenied(t *testing.T) {
	e, _ := memToolEnv(t, true, `{"op":"add","text":"내 비밀번호: hunter22"}`, false)
	chatAuto(t, e)
	if res := lastToolResult(e); !strings.Contains(res, "secret_detected") {
		t.Fatal(res)
	}
}

func TestMemoryTool_ApproveBearer403(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	id, tok, ph := proposeAndToken(t, e, "")
	r, m := do(t, "POST", e.ts.URL+"/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+ph+`"}`, true)
	if r.StatusCode != 403 {
		t.Fatal(r.StatusCode, m)
	}
	if d, _ := fs.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 0 {
		t.Fatal("api key approved")
	}
}

func TestMemoryApprove_TokenExpiry410(t *testing.T) {
	e, fs := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	id, tok, ph := proposeAndToken(t, e, "")
	base := time.Now()
	e.srv.Now = func() time.Time { return base.Add(121 * time.Second) }
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", `{"confirm_token":"`+tok+`","payload_hash":"`+ph+`"}`)
	if r.StatusCode != 410 {
		t.Fatalf("expired token: %d %s", r.StatusCode, b)
	}
	if d, _ := fs.Load(uidOf(t, &e.env, "alice@example")); len(d.Items) != 0 {
		t.Fatal("written with expired token")
	}
}

func TestHasBlockMarker(t *testing.T) {
	for _, s := range []string{"<user_memory>", "</user_memory>", "< / USER_MEMORY >", "&lt;user_memory&gt;",
		`\u003c/user_memory`, `\x3cuser_memory`, "%3Cuser-memory", "user memory nonce=abc", "＜user_memory"} {
		if !memory.HasBlockMarker(s) {
			t.Errorf("not caught: %q", s)
		}
	}
	for _, s := range []string{"고양이를 키운다", "user 메모리 앱을 쓴다", "a < b"} {
		if memory.HasBlockMarker(s) {
			t.Errorf("false positive: %q", s)
		}
	}
}

func TestStripServerKeys(t *testing.T) {
	got := string(stripServerKeys([]byte(`{"web_search":"auto","memory_tainted":true}`)))
	if got != `{"web_search":"auto"}` {
		t.Fatal(got)
	}
}

func TestMemoryApprove_NonPending409(t *testing.T) {
	e, _ := memToolEnv(t, true, `{"op":"add","text":"고양이를 키운다"}`, false)
	id, tok, ph := proposeAndToken(t, e, "")
	body := `{"confirm_token":"` + tok + `","payload_hash":"` + ph + `"}`
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", body); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	r, b := e.call(t, "alice@example", "POST", "/v1/actions/"+id+"/approve", body)
	if r.StatusCode != 409 || !strings.Contains(string(b), `"conflict"`) {
		t.Fatalf("re-approve: %d %s", r.StatusCode, b)
	}
	// unknown id stays 404 not_found
	if r, b := e.call(t, "alice@example", "POST", "/v1/actions/nope/approve", body); r.StatusCode != 404 {
		t.Fatalf("unknown: %d %s", r.StatusCode, b)
	}
	// another user's (decided) card is 404, not 409
	if r, b := e.call(t, "bob@example", "POST", "/v1/actions/"+id+"/approve", body); r.StatusCode != 404 {
		t.Fatalf("foreign: %d %s", r.StatusCode, b)
	}
}
