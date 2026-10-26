package handlers

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/proxy"
	"github.com/gun025bba/eve-serving/internal/queue"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

// slowUpstream streams "w0 w1 ... w{n-1}" one word per step; it keeps going
// until done unless its own request context is canceled (recorded in canceled).
func slowUpstream(t *testing.T, n int, step time.Duration, canceled chan<- struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < n; i++ {
			select {
			case <-r.Context().Done():
				canceled <- struct{}{}
				return
			case <-time.After(step):
			}
			fmt.Fprintf(w, "data: {\"model\":\"m1\",\"choices\":[{\"delta\":{\"content\":\"w%d \"}}]}\n\n", i)
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n")
		fl.Flush()
	}))
}

func newDetachEnv(t *testing.T, n int, step time.Duration, mod ...func(*Server)) (*env, chan struct{}) {
	canceled := make(chan struct{}, 4)
	up := slowUpstream(t, n, step, canceled)
	t.Cleanup(up.Close)
	cfg := config.Default()
	cfg.AllowedOrigins = []string{testOrigin}
	cfg.Upstream, cfg.Model, cfg.StaticDir = up.URL, "m1", ""
	cfg.AllowedUsers = []string{"alice@example", "bob@example"}
	s := &Server{Cfg: cfg, Key: key, Log: logging.New(io.Discard, logging.INFO), M: metrics.New(10), Mem: &metrics.MemSampler{Needle: "none-xyz"},
		Q: queue.New(1, 4, 5*time.Second), Up: proxy.New(proxy.Options{BaseURL: up.URL, ConnectTimeout: time.Second, ReadTimeout: 5 * time.Second}), Start: time.Now()}
	e := &env{st: memstore.New()}
	s.Store = e.st
	for _, f := range mod {
		f(s)
	}
	e.ts = httptest.NewServer(s.Handler())
	t.Cleanup(e.ts.Close)
	return e, canceled
}

type sseEv struct{ id, event, data string }

func readEvents(t *testing.T, r io.Reader, stopAfter int) []sseEv {
	t.Helper()
	var out []sseEv
	var cur sseEv
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case l == "":
			if cur.data != "" || cur.event != "" {
				out = append(out, cur)
				if stopAfter > 0 && len(out) >= stopAfter {
					return out
				}
			}
			cur = sseEv{}
		case strings.HasPrefix(l, "id: "):
			cur.id = l[4:]
		case strings.HasPrefix(l, "event: "):
			cur.event = l[7:]
		case strings.HasPrefix(l, "data: "):
			cur.data = l[6:]
		}
	}
	return out
}

func TestDisconnectMidStreamStillCompletes(t *testing.T) {
	e, canceled := newDetachEnv(t, 20, 30*time.Millisecond)
	req, _ := http.NewRequest("POST", e.ts.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"count"}],"thread_id":"new"}`))
	req.Header.Set("Tailscale-User-Login", "alice@example")
	browserHeaders(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	evs := readEvents(t, resp.Body, 4) // meta, persist chunk, two content chunks
	resp.Body.Close()                  // client goes away (PWA backgrounded)
	if evs[0].event != "eve.meta" || evs[0].id != "1" || evs[3].id != "4" {
		t.Fatal(evs)
	}
	meta := js(t, []byte(evs[0].data))
	tid, mid := meta["thread_id"].(string), meta["message_id"].(string)
	if mid == "" || resp.Header.Get("X-Message-Id") != mid || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(meta, resp.Header)
	}

	// While running: list shows generating + partial content.
	time.Sleep(100 * time.Millisecond)
	_, b := e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages", "")
	a := js(t, b)["data"].([]any)[1].(map[string]any)
	if a["id"] != mid || a["status"] != "generating" || !strings.HasPrefix(a["content"].(string), "w0 ") {
		t.Fatal(string(b))
	}

	// Resume from seq 4: replays the rest and ends with [DONE].
	rq, _ := http.NewRequest("GET", e.ts.URL+"/v1/threads/"+tid+"/messages/"+mid+"/stream", nil)
	rq.Header.Set("Tailscale-User-Login", "alice@example")
	rq.Header.Set("Last-Event-ID", "4")
	rr, err := http.DefaultClient.Do(rq)
	if err != nil || rr.StatusCode != 200 {
		t.Fatal(err, rr.StatusCode)
	}
	rest := readEvents(t, rr.Body, 0)
	rr.Body.Close()
	if rest[0].id != "5" || rest[len(rest)-1].data != "[DONE]" {
		t.Fatal(rest)
	}
	var content strings.Builder
	for _, ev := range append(evs[2:], rest...) {
		if strings.Contains(ev.data, `"content"`) {
			content.WriteString(js(t, []byte(ev.data))["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"].(string))
		}
	}

	m, err := e.st.GetMessage(t.Context(), "alice@example", mid)
	want := ""
	for i := 0; i < 20; i++ {
		want += fmt.Sprintf("w%d ", i)
	}
	if err != nil || m.Status != "complete" || m.Content != want || content.String() != want {
		t.Fatalf("%+v %v %q", m, err, content.String())
	}
	select {
	case <-canceled:
		t.Fatal("upstream generation was canceled by the client disconnect")
	default:
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages", "")
	if a := js(t, b)["data"].([]any)[1].(map[string]any); a["status"] != "complete" || a["content"] != want {
		t.Fatal(string(b))
	}

	// Ownership: foreign user, wrong thread, user message, unknown id -> 404.
	_, b2 := e.call(t, "alice@example", "POST", "/v1/threads", `{}`)
	other := js(t, b2)["id"].(string)
	umid := js(t, b)["data"].([]any)[0].(map[string]any)["id"].(string)
	for _, c := range []struct{ who, path string }{
		{"bob@example", "/v1/threads/" + tid + "/messages/" + mid + "/stream"},
		{"alice@example", "/v1/threads/" + other + "/messages/" + mid + "/stream"},
		{"alice@example", "/v1/threads/" + tid + "/messages/" + umid + "/stream"},
		{"alice@example", "/v1/threads/" + tid + "/messages/nope/stream"},
	} {
		if r, _ := e.call(t, c.who, "GET", c.path, ""); r.StatusCode != 404 {
			t.Fatal(c, r.StatusCode)
		}
	}
	if r, _ := e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages/"+mid+"/stream?after=-1", ""); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

func TestResumeAfterRunEvictedSendsSnapshot(t *testing.T) {
	e, _ := newDetachEnv(t, 3, time.Millisecond, func(s *Server) { s.Cfg.Generation.RetainS = 0.01 })
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"x"}],"thread_id":"new"}`)
	if r.StatusCode != 200 {
		t.Fatal(string(b))
	}
	meta := js(t, []byte(readEvents(t, strings.NewReader(string(b)), 1)[0].data))
	time.Sleep(50 * time.Millisecond)
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+meta["thread_id"].(string)+"/messages/"+meta["message_id"].(string)+"/stream?after=2", "")
	evs := readEvents(t, strings.NewReader(string(b)), 0)
	if len(evs) != 2 || evs[0].event != "eve.snapshot" || evs[1].data != "[DONE]" {
		t.Fatal(string(b))
	}
	if s := js(t, []byte(evs[0].data)); s["status"] != "complete" || s["content"] != "w0 w1 w2 " {
		t.Fatal(s)
	}
}

func TestStaleGeneratingFailedOnRestart(t *testing.T) {
	e, _ := newDetachEnv(t, 1, time.Millisecond)
	_, b := e.call(t, "alice@example", "POST", "/v1/threads", `{}`)
	tid := js(t, b)["id"].(string)
	m, _ := e.st.AddMessage(t.Context(), "alice@example", storeMsg(tid, "generating"))
	if n, _ := e.st.(store.GenStore).FailStaleGenerating(t.Context()); n != 1 {
		t.Fatal(n)
	}
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages", "")
	a := js(t, b)["data"].([]any)[0].(map[string]any)
	if a["id"] != m.ID || a["status"] != "failed" || a["error_code"] != "gateway_restart" {
		t.Fatal(string(b))
	}
}

func storeMsg(tid, status string) store.Message {
	return store.Message{ThreadID: tid, Role: "assistant", Status: status}
}

func openStream(t *testing.T, e *env, who, body string, n int) (*http.Response, []sseEv) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Tailscale-User-Login", who)
	browserHeaders(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || n == 0 {
		return resp, nil
	}
	return resp, readEvents(t, resp.Body, n)
}

func waitStatus(t *testing.T, e *env, mid, want string) store.Message {
	t.Helper()
	for i := 0; i < 400; i++ {
		m, _ := e.st.GetMessage(t.Context(), "alice@example", mid)
		if m.Status == want {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	m, _ := e.st.GetMessage(t.Context(), "alice@example", mid)
	t.Fatalf("status %q, want %q (%s)", m.Status, want, m.Metadata)
	return m
}

func metaOf(t *testing.T, evs []sseEv) (string, string) {
	m := js(t, []byte(evs[0].data))
	return m["thread_id"].(string), m["message_id"].(string)
}

const longChat = `{"stream":true,"messages":[{"role":"user","content":"x"}],"thread_id":"new"}`

func TestPerUserGenerationCap(t *testing.T) {
	e, _ := newDetachEnv(t, 100, 20*time.Millisecond, func(s *Server) { s.Q = queue.New(4, 4, 5*time.Second) })
	r1, _ := openStream(t, e, "alice@example", longChat, 1)
	defer r1.Body.Close()
	r2, _ := openStream(t, e, "alice@example", longChat, 1)
	defer r2.Body.Close()
	r3, _ := openStream(t, e, "alice@example", longChat, 0)
	b, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if r3.StatusCode != 429 || r3.Header.Get("Retry-After") == "" || errCode(js(t, b)) != "too_many_generations" {
		t.Fatal(r3.StatusCode, string(b))
	}
	r4, _ := openStream(t, e, "bob@example", longChat, 1) // other users unaffected
	defer r4.Body.Close()
	if r4.StatusCode != 200 {
		t.Fatal(r4.StatusCode)
	}
}

func TestCancelGeneration(t *testing.T) {
	e, _ := newDetachEnv(t, 200, 20*time.Millisecond)
	resp, evs := openStream(t, e, "alice@example", longChat, 3)
	defer resp.Body.Close()
	tid, mid := metaOf(t, evs)
	path := "/v1/threads/" + tid + "/messages/" + mid + "/cancel"
	// CSRF: write without the browser headers is rejected
	req, _ := http.NewRequest("POST", e.ts.URL+path, nil)
	req.Header.Set("Tailscale-User-Login", "alice@example")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatal("csrf", r.StatusCode)
	}
	if r, _ := e.call(t, "bob@example", "POST", path, ""); r.StatusCode != 404 {
		t.Fatal("bob", r.StatusCode)
	}
	if r, _ := e.call(t, "alice@example", "POST", "/v1/threads/"+tid+"/messages/nope/cancel", ""); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
	if r, _ := e.call(t, "alice@example", "POST", path, ""); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	rest := readEvents(t, resp.Body, 0)
	last, prev := rest[len(rest)-1], rest[len(rest)-2]
	if last.data != "[DONE]" || js(t, []byte(prev.data))["status"] != "failed" {
		t.Fatal(rest)
	}
	m := waitStatus(t, e, mid, "failed")
	if !strings.Contains(string(m.Metadata), `"error_code":"cancelled"`) {
		t.Fatal(string(m.Metadata))
	}
	if r, _ := e.call(t, "alice@example", "POST", path, ""); r.StatusCode != 204 { // idempotent
		t.Fatal(r.StatusCode)
	}
	_, b := e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages", "")
	if a := js(t, b)["data"].([]any)[1].(map[string]any); a["status"] != "failed" || a["error_code"] != "cancelled" {
		t.Fatal(string(b))
	}
}

func TestIdleAutoCancel(t *testing.T) {
	e, _ := newDetachEnv(t, 300, 20*time.Millisecond, func(s *Server) { s.Cfg.Generation.IdleCancelS = 0.1 })
	resp, evs := openStream(t, e, "alice@example", longChat, 2)
	resp.Body.Close()
	_, mid := metaOf(t, evs)
	m := waitStatus(t, e, mid, "failed")
	if !strings.Contains(string(m.Metadata), `"error_code":"no_subscriber"`) {
		t.Fatal(string(m.Metadata))
	}
}

func TestPartialTooLargeFails(t *testing.T) {
	e, _ := newDetachEnv(t, 100, 2*time.Millisecond, func(s *Server) { s.Cfg.Generation.PartialMaxBytes = 30 })
	resp, evs := openStream(t, e, "alice@example", longChat, 1)
	_, mid := metaOf(t, evs)
	readEvents(t, resp.Body, 0)
	resp.Body.Close()
	m := waitStatus(t, e, mid, "failed")
	if !strings.Contains(string(m.Metadata), `"error_code":"too_large"`) {
		t.Fatal(string(m.Metadata))
	}
}

func TestEventLogOverflowResumeSnapshot(t *testing.T) {
	e, _ := newDetachEnv(t, 30, 2*time.Millisecond, func(s *Server) { s.Cfg.Generation.RunMaxEvents = 5 })
	resp, evs := openStream(t, e, "alice@example", longChat, 1)
	tid, mid := metaOf(t, evs)
	all := readEvents(t, resp.Body, 0) // slow-consumer path is exercised too; the original sees [DONE]
	resp.Body.Close()
	if all[len(all)-1].data != "[DONE]" {
		t.Fatal(all)
	}
	waitStatus(t, e, mid, "complete")
	_, b := e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages/"+mid+"/stream?after=1", "")
	got := readEvents(t, strings.NewReader(string(b)), 0)
	if got[0].event != "eve.snapshot" || got[len(got)-1].data != "[DONE]" {
		t.Fatal(string(b))
	}
	if s := js(t, []byte(got[0].data)); !strings.HasPrefix(s["content"].(string), "w0 w1 ") || s["status"] != "complete" {
		t.Fatal(s)
	}
	// a cursor inside the retained log still replays events
	snap := js(t, []byte(got[0].data))
	last := int(snap["last_event_id"].(float64))
	_, b = e.call(t, "alice@example", "GET", fmt.Sprintf("/v1/threads/%s/messages/%s/stream?after=%d", tid, mid, last-2), "")
	if got := readEvents(t, strings.NewReader(string(b)), 0); got[0].event == "eve.snapshot" || got[0].id != fmt.Sprint(last-1) {
		t.Fatal(string(b))
	}
}

func TestSubscriberCap(t *testing.T) {
	e, _ := newDetachEnv(t, 200, 20*time.Millisecond, func(s *Server) { s.Cfg.Generation.MaxSubscribers = 2 })
	resp, evs := openStream(t, e, "alice@example", longChat, 1)
	defer resp.Body.Close()
	tid, mid := metaOf(t, evs)
	path := e.ts.URL + "/v1/threads/" + tid + "/messages/" + mid + "/stream"
	get := func() *http.Response {
		rq, _ := http.NewRequest("GET", path, nil)
		rq.Header.Set("Tailscale-User-Login", "alice@example")
		r, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r2 := get()
	defer r2.Body.Close()
	if r2.StatusCode != 200 || r2.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(r2.StatusCode)
	}
	r3 := get()
	r3.Body.Close()
	if r3.StatusCode != 429 || r3.Header.Get("Retry-After") == "" {
		t.Fatal(r3.StatusCode)
	}
}

func TestQueueFullBeforeAdmissionKeepsHTTPStatus(t *testing.T) {
	e, _ := newDetachEnv(t, 200, 20*time.Millisecond, func(s *Server) { s.Q = queue.New(1, 0, time.Second) })
	r1, evs := openStream(t, e, "alice@example", longChat, 1)
	defer r1.Body.Close()
	tid, mid := metaOf(t, evs)
	r2, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"y"}],"thread_id":"`+tid+`"}`)
	if r2.StatusCode != 429 || errCode(js(t, b)) != "queue_full" {
		t.Fatal(r2.StatusCode, string(b))
	}
	_, lb := e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages", "")
	n := 0
	for _, x := range js(t, lb)["data"].([]any) {
		if x.(map[string]any)["role"] == "assistant" {
			n++
		}
	}
	if n != 1 { // no generating row for the rejected request
		t.Fatal(string(lb))
	}
	// concurrent regenerate of the running message -> 409
	r3, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"x"}],"thread_id":"`+tid+`","replace_message_id":"`+mid+`"}`)
	if r3.StatusCode != 429 && r3.StatusCode != 409 {
		t.Fatal(r3.StatusCode, string(b))
	}
}

func TestConcurrentRegenerateConflict(t *testing.T) {
	e, _ := newDetachEnv(t, 200, 20*time.Millisecond, func(s *Server) { s.Q = queue.New(4, 4, time.Second) })
	r1, evs := openStream(t, e, "alice@example", longChat, 1)
	defer r1.Body.Close()
	tid, mid := metaOf(t, evs)
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"x"}],"thread_id":"`+tid+`","replace_message_id":"`+mid+`"}`)
	if r.StatusCode != 409 || errCode(js(t, b)) != "generation_in_progress" {
		t.Fatal(r.StatusCode, string(b))
	}
}

func TestHubBytesFullOfRunningRunsRejects(t *testing.T) {
	e, _ := newDetachEnv(t, 300, 10*time.Millisecond, func(s *Server) { s.Cfg.Generation.HubMaxBytes = 300 })
	r1, _ := openStream(t, e, "alice@example", longChat, 6) // running run now holds > 300 bytes
	defer r1.Body.Close()
	r2, b := e.call(t, "bob@example", "POST", "/v1/chat/completions", longChat)
	if r2.StatusCode != 429 || r2.Header.Get("Retry-After") == "" || errCode(js(t, b)) != "server_busy" {
		t.Fatal(r2.StatusCode, string(b))
	}
}
