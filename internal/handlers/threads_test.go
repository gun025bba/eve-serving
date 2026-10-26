package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/llm-serving/internal/config"
	"github.com/gun025bba/llm-serving/internal/logging"
	"github.com/gun025bba/llm-serving/internal/metrics"
	"github.com/gun025bba/llm-serving/internal/proxy"
	"github.com/gun025bba/llm-serving/internal/queue"
	"github.com/gun025bba/llm-serving/internal/store"
	"github.com/gun025bba/llm-serving/internal/store/memstore"
)

// reasoningUpstream streams reasoning + content; a last user message "abort"
// makes it drop the connection mid-stream.
func reasoningUpstream(t *testing.T, seen *[]map[string]any, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		*seen = append(*seen, b)
		mu.Unlock()
		msgs := b["messages"].([]any)
		last := msgs[len(msgs)-1].(map[string]any)["content"]
		if b["stream"] != true {
			fmt.Fprint(w, `{"id":"x","model":"m1","choices":[{"message":{"role":"assistant","content":"답변","reasoning":"생각"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"model\":\"m1\",\"choices\":[{\"delta\":{\"reasoning\":\"think\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n")
		w.(http.Flusher).Flush()
		if last == "abort" {
			time.Sleep(50 * time.Millisecond)
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
}

type env struct {
	ts   *httptest.Server
	st   store.Store
	seen *[]map[string]any
	mu   *sync.Mutex
}

func newEnv(t *testing.T, withStore bool) *env {
	seen, mu := &[]map[string]any{}, &sync.Mutex{}
	up := reasoningUpstream(t, seen, mu)
	t.Cleanup(up.Close)
	cfg := config.Default()
	cfg.Upstream, cfg.Model, cfg.StaticDir = up.URL, "m1", ""
	cfg.AllowedUsers = []string{"alice@example", "bob@example"}
	s := &Server{Cfg: cfg, Key: key, Log: logging.New(io.Discard, logging.INFO), M: metrics.New(10), Mem: &metrics.MemSampler{Needle: "none-xyz"},
		Q: queue.New(2, 4, time.Second), Up: proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: 2 * time.Second}), Start: time.Now()}
	e := &env{seen: seen, mu: mu}
	if withStore {
		e.st = memstore.New()
		s.Store = e.st
	}
	e.ts = httptest.NewServer(s.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

// call sends a request as a tailscale identity ("" = API key).
func (e *env) call(t *testing.T, who, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if who == "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("Tailscale-User-Login", who)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func js(t *testing.T, b []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json: %v: %s", err, b)
	}
	return m
}

func TestPersistenceDisabled(t *testing.T) {
	e := newEnv(t, false)
	for _, p := range []string{"/v1/threads", "/v1/search?q=x"} {
		r, b := e.call(t, "alice@example", "GET", p, "")
		if r.StatusCode != 503 || errCode(js(t, b)) != "persistence_disabled" {
			t.Fatal(p, r.StatusCode, string(b))
		}
	}
	r, b := e.call(t, "", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}],"thread_id":"new"}`)
	if r.StatusCode != 503 {
		t.Fatal(r.StatusCode, string(b))
	}
	r, _ = e.call(t, "", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if r.StatusCode != 200 {
		t.Fatal("plain chat must still work", r.StatusCode)
	}
}

func TestThreadCRUDAndIsolation(t *testing.T) {
	e := newEnv(t, true)
	r, b := e.call(t, "alice@example", "POST", "/v1/threads", `{"title":"첫 스레드"}`)
	if r.StatusCode != 201 {
		t.Fatal(r.StatusCode, string(b))
	}
	th := js(t, b)
	id := th["id"].(string)
	if th["title"] != "첫 스레드" || th["message_count"].(float64) != 0 || th["archived"] != false {
		t.Fatal(th)
	}
	// empty body create
	if r, _ := e.call(t, "alice@example", "POST", "/v1/threads", ""); r.StatusCode != 201 {
		t.Fatal(r.StatusCode)
	}
	// bob can't see / modify alice's thread
	for _, c := range [][2]string{{"GET", "/v1/threads/" + id}, {"PATCH", "/v1/threads/" + id}, {"DELETE", "/v1/threads/" + id}, {"GET", "/v1/threads/" + id + "/messages"}} {
		r, b := e.call(t, "bob@example", c[0], c[1], `{"title":"x"}`)
		if r.StatusCode != 404 || errCode(js(t, b)) != "not_found" {
			t.Fatal(c, r.StatusCode, string(b))
		}
	}
	_, b = e.call(t, "bob@example", "GET", "/v1/threads", "")
	if n := len(js(t, b)["data"].([]any)); n != 0 {
		t.Fatal("bob sees", n)
	}
	r, b = e.call(t, "alice@example", "PATCH", "/v1/threads/"+id, `{"title":"renamed"}`)
	if r.StatusCode != 200 || js(t, b)["title"] != "renamed" {
		t.Fatal(string(b))
	}
	if r, _ := e.call(t, "alice@example", "DELETE", "/v1/threads/"+id, ""); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/threads?archived=true", "")
	if d := js(t, b)["data"].([]any); len(d) != 1 || d[0].(map[string]any)["archived"] != true {
		t.Fatal(string(b))
	}
	if r, _ := e.call(t, "alice@example", "PATCH", "/v1/threads/"+id, `{"bogus":1}`); r.StatusCode != 400 {
		t.Fatal("unknown field", r.StatusCode)
	}
}

func TestThreadPagination(t *testing.T) {
	e := newEnv(t, true)
	for i := 0; i < 5; i++ {
		e.call(t, "alice@example", "POST", "/v1/threads", fmt.Sprintf(`{"title":"t%d"}`, i))
		time.Sleep(2 * time.Millisecond)
	}
	var titles []string
	cur := ""
	for pages := 0; ; pages++ {
		_, b := e.call(t, "alice@example", "GET", "/v1/threads?limit=2&cursor="+cur, "")
		m := js(t, b)
		for _, x := range m["data"].([]any) {
			titles = append(titles, x.(map[string]any)["title"].(string))
		}
		if m["next_cursor"] == nil {
			break
		}
		cur = m["next_cursor"].(string)
		if pages > 5 {
			t.Fatal("loop")
		}
	}
	if strings.Join(titles, ",") != "t4,t3,t2,t1,t0" {
		t.Fatal(titles)
	}
	if r, _ := e.call(t, "alice@example", "GET", "/v1/threads?cursor=@@@", ""); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

func TestChatNonStreamPersists(t *testing.T) {
	e := newEnv(t, true)
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions",
		`{"messages":[{"role":"user","content":"안녕하세요 반가워요, 오늘 날씨가 정말 좋네요. 산책하기 딱 좋은 날입니다 그렇죠?"}],"thread_id":"new","temperature":0.3}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	m := js(t, b)
	tid, mid, umid := m["thread_id"].(string), m["message_id"].(string), m["user_message_id"].(string)
	if r.Header.Get("X-Thread-Id") != tid || r.Header.Get("X-Message-Id") != mid || r.Header.Get("X-User-Message-Id") != umid {
		t.Fatal(r.Header)
	}
	e.mu.Lock()
	up := (*e.seen)[0]
	e.mu.Unlock()
	if _, ok := up["thread_id"]; ok {
		t.Fatal("thread_id forwarded upstream")
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+tid, "")
	th := js(t, b)
	if th["message_count"].(float64) != 2 || !strings.HasSuffix(th["title"].(string), "…") || len([]rune(th["title"].(string))) != 41 {
		t.Fatal(th)
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages", "")
	ms := js(t, b)["data"].([]any)
	a := ms[1].(map[string]any)
	if ms[0].(map[string]any)["role"] != "user" || a["content"] != "답변" || a["reasoning"] != "생각" || a["status"] != "complete" ||
		a["model"] != "m1" || a["usage"].(map[string]any)["completion_tokens"].(float64) != 1 || a["params"].(map[string]any)["temperature"].(float64) != 0.3 {
		t.Fatal(a)
	}
	// bob cannot append to alice's thread
	r, _ = e.call(t, "bob@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"x"}],"thread_id":"`+tid+`"}`)
	if r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
}

func readSSE(t *testing.T, body io.Reader) (events []map[string]any, done bool) {
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		l := sc.Text()
		if l == "data: [DONE]" {
			done = true
			continue
		}
		if strings.HasPrefix(l, "data: ") {
			events = append(events, js(t, []byte(l[6:])))
		}
	}
	return
}

func TestChatStreamPersistsAndRegenerate(t *testing.T) {
	e := newEnv(t, true)
	req, _ := http.NewRequest("POST", e.ts.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hi"}],"thread_id":"new"}`))
	req.Header.Set("Tailscale-User-Login", "alice@example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	evs, done := readSSE(t, resp.Body)
	resp.Body.Close()
	first, last := evs[0], evs[len(evs)-1]
	if !done || first["thread_id"] == nil || first["user_message_id"] == nil || resp.Header.Get("X-Thread-Id") != first["thread_id"] {
		t.Fatal(first, done)
	}
	if last["object"] != "chat.completion.persisted" || last["message_id"] == nil || last["thread_id"] != first["thread_id"] || last["status"] != "complete" {
		t.Fatal(last)
	}
	tid, mid := last["thread_id"].(string), last["message_id"].(string)
	m, err := e.st.GetMessage(t.Context(), "alice@example", mid)
	if err != nil || m.Content != "hello" || *m.Reasoning != "think" || m.Status != "complete" || *m.Usage.CompletionTokens != 2 {
		t.Fatal(m, err)
	}
	// feedback then regenerate clears it and keeps id
	r, b := e.call(t, "alice@example", "PUT", "/v1/messages/"+mid+"/feedback", `{"rating":"up","comment":"좋아요"}`)
	if r.StatusCode != 200 || js(t, b)["rating"] != "up" {
		t.Fatal(string(b))
	}
	r, b = e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}],"thread_id":"`+tid+`","replace_message_id":"`+mid+`"}`)
	if r.StatusCode != 200 || js(t, b)["message_id"] != mid || js(t, b)["user_message_id"] != nil {
		t.Fatal(string(b))
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages", "")
	ms := js(t, b)["data"].([]any)
	a := ms[1].(map[string]any)
	if len(ms) != 2 || a["id"] != mid || a["content"] != "답변" || a["feedback"] != nil || a["updated_at"] == nil {
		t.Fatal(string(b))
	}
	e.mu.Lock()
	_, leaked := (*e.seen)[1]["replace_message_id"]
	e.mu.Unlock()
	if leaked {
		t.Fatal("replace_message_id forwarded")
	}
	// replace target must be an assistant message
	umid := ms[0].(map[string]any)["id"].(string)
	if r, _ := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}],"thread_id":"`+tid+`","replace_message_id":"`+umid+`"}`); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	if r, _ := e.call(t, "bob@example", "PUT", "/v1/messages/"+mid+"/feedback", `{"rating":"down"}`); r.StatusCode != 404 {
		t.Fatal("bob feedback", r.StatusCode)
	}
	if r, _ := e.call(t, "alice@example", "PUT", "/v1/messages/"+mid+"/feedback", `{"rating":"meh"}`); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	e.call(t, "alice@example", "PUT", "/v1/messages/"+mid+"/feedback", `{"rating":"down"}`)
	if r, _ := e.call(t, "alice@example", "DELETE", "/v1/messages/"+mid+"/feedback", ""); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	if m, _ := e.st.GetMessage(t.Context(), "alice@example", mid); m.Feedback != nil {
		t.Fatal("feedback not cleared")
	}
}

func TestStreamAbortPersistsIncomplete(t *testing.T) {
	e := newEnv(t, true)
	req, _ := http.NewRequest("POST", e.ts.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"abort"}],"thread_id":"new"}`))
	req.Header.Set("Tailscale-User-Login", "alice@example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := readSSE(t, resp.Body)
	resp.Body.Close()
	last := evs[len(evs)-1]
	if last["object"] != "chat.completion.persisted" || last["status"] != "incomplete" {
		t.Fatal(evs)
	}
	m, _ := e.st.GetMessage(t.Context(), "alice@example", last["message_id"].(string))
	if m.Status != "incomplete" || m.Content != "he" || !strings.Contains(string(m.Metadata), `"incomplete":true`) {
		t.Fatal(m)
	}
}

func TestMessagesPaginationBefore(t *testing.T) {
	e := newEnv(t, true)
	_, b := e.call(t, "alice@example", "POST", "/v1/threads", `{}`)
	tid := js(t, b)["id"].(string)
	for i := 0; i < 5; i++ {
		e.st.AddMessage(t.Context(), "alice@example", store.Message{ThreadID: tid, Role: "user", Content: fmt.Sprint(i), CreatedAt: time.Now().Add(time.Duration(i) * time.Second)})
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages?limit=2", "")
	m := js(t, b)
	d := m["data"].([]any)
	if m["has_more"] != true || d[0].(map[string]any)["content"] != "3" || d[1].(map[string]any)["content"] != "4" {
		t.Fatal(string(b))
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages?limit=10&before="+d[0].(map[string]any)["id"].(string), "")
	m = js(t, b)
	if m["has_more"] != false || len(m["data"].([]any)) != 3 {
		t.Fatal(string(b))
	}
}

func TestSearchAndImport(t *testing.T) {
	e := newEnv(t, true)
	body := `{"threads":[{"client_id":"c1","title":"<script>제주도 여행</script>","created_at":"2026-01-02T03:04:05.123Z",
	  "messages":[{"role":"user","content":"제주도 맛집 추천해줘 & 고마워","created_at":"2026-01-02T03:04:05.123Z","client_id":"m1"},
	              {"role":"assistant","content":"흑돼지 거리","reasoning":"r","created_at":"2026-01-02T03:05:00Z"}]}]}`
	r, b := e.call(t, "alice@example", "POST", "/v1/threads/import", body)
	res := js(t, b)["results"].([]any)[0].(map[string]any)
	if r.StatusCode != 200 || res["status"] != "created" || res["client_id"] != "c1" {
		t.Fatal(string(b))
	}
	_, b = e.call(t, "alice@example", "POST", "/v1/threads/import", body)
	res2 := js(t, b)["results"].([]any)[0].(map[string]any)
	if res2["status"] != "exists" || res2["thread_id"] != res["thread_id"] {
		t.Fatal(string(b))
	}
	tid := res["thread_id"].(string)
	th, _ := e.st.GetThread(t.Context(), "alice@example", tid)
	if th.MessageCount != 2 || !th.CreatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 123e6, time.UTC)) {
		t.Fatal(th)
	}
	// bob importing same client_id gets his own thread
	_, b = e.call(t, "bob@example", "POST", "/v1/threads/import", body)
	if js(t, b)["results"].([]any)[0].(map[string]any)["status"] != "created" {
		t.Fatal(string(b))
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/search?q=제주도", "")
	hits := js(t, b)["data"].([]any)
	if len(hits) != 2 {
		t.Fatal(string(b))
	}
	h0, h1 := hits[0].(map[string]any), hits[1].(map[string]any)
	if h0["match"] != "title" || h0["snippet"] != "&lt;script&gt;<mark>제주도</mark> 여행&lt;/script&gt;" || h0["message_id"] != nil {
		t.Fatal(h0)
	}
	if h1["match"] != "content" || h1["role"] != "user" || h1["snippet"] != "<mark>제주도</mark> 맛집 추천해줘 &amp; 고마워" {
		t.Fatal(h1)
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/search?q=제주도&limit=1", "")
	m := js(t, b)
	if len(m["data"].([]any)) != 1 || m["next_cursor"] == nil {
		t.Fatal(string(b))
	}
	if r, _ := e.call(t, "alice@example", "GET", "/v1/search", ""); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	if r, _ := e.call(t, "alice@example", "POST", "/v1/threads/import", `{"threads":[{"title":"x"}]}`); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	big := `{"threads":[{"client_id":"b","title":"` + strings.Repeat("a", 6<<20) + `","created_at":"2026-01-01T00:00:00Z"}]}`
	if r, b := e.call(t, "alice@example", "POST", "/v1/threads/import", big); r.StatusCode != 413 {
		t.Fatal(r.StatusCode, string(b))
	}
}
