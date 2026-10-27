package handlers

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/queue"
	"github.com/gun025bba/eve-serving/internal/store"
)

// #30: resending the same turn while its reply is still generating returns
// 409 generation_in_progress and does not start a second answer.
func TestRetryWhileGenerating409(t *testing.T) {
	e, _ := newDetachEnv(t, 200, 20*time.Millisecond, func(s *Server) { s.Q = queue.New(4, 4, 5*time.Second) })
	r1, evs := openStream(t, e, "alice@example", longChat, 1)
	defer r1.Body.Close()
	tid, _ := metaOf(t, evs)
	_, mid := metaOf(t, evs)
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"x"}],"thread_id":"`+tid+`"}`)
	if r.StatusCode != 409 || errCode(js(t, b)) != "generation_in_progress" {
		t.Fatal(r.StatusCode, string(b))
	}
	// The 409 carries the reply being generated so the client can reattach.
	eo, _ := js(t, b)["error"].(map[string]any)
	if eo["message_id"] != mid || eo["thread_id"] != tid {
		t.Fatalf("409 must carry message_id %s / thread_id %s: %s", mid, tid, b)
	}
	ms := threadMsgs(t, e, tid)
	if uid, _ := eo["user_message_id"].(string); uid == "" || uid != ms[0].ID {
		t.Fatalf("409 user_message_id: %s", b)
	}
	if r.Header.Get("Idempotency-Key") != "" {
		t.Fatal("no Idempotency-Key expected")
	}
	if n := len(threadMsgs(t, e, tid)); n != 2 {
		t.Fatalf("want 2 messages (1 user + 1 generating), got %d", n)
	}
	// A different message is a new turn and is not blocked.
	r2, b2 := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"other"}],"thread_id":"`+tid+`"}`)
	if r2.StatusCode == 409 {
		t.Fatal(string(b2))
	}
}

// Cancelling the running reply releases the turn: the same resend then
// proceeds and overwrites the cancelled reply instead of adding one.
func TestRetryAfterCancelProceeds(t *testing.T) {
	e, _ := newDetachEnv(t, 200, 20*time.Millisecond, func(s *Server) { s.Q = queue.New(4, 4, 5*time.Second) })
	r1, evs := openStream(t, e, "alice@example", longChat, 1)
	tid, mid := metaOf(t, evs)
	r1.Body.Close()
	if r, b := e.call(t, "alice@example", "POST", "/v1/threads/"+tid+"/messages/"+mid+"/cancel", ""); r.StatusCode/100 != 2 {
		t.Fatal(r.StatusCode, string(b))
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ms := threadMsgs(t, e, tid)
		if ms[len(ms)-1].Status != store.StatusGenerating {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still generating after cancel")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r2, evs2 := openStream(t, e, "alice@example", `{"stream":true,"messages":[{"role":"user","content":"x"}],"thread_id":"`+tid+`"}`, 1)
	defer r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Fatal(r2.StatusCode)
	}
	if _, mid2 := metaOf(t, evs2); mid2 != mid {
		t.Fatalf("retry should overwrite %s, got %s", mid, mid2)
	}
	if n := len(threadMsgs(t, e, tid)); n != 2 {
		t.Fatalf("want 2 messages after retry, got %d", n)
	}
}

// #30 (security Med 1): a resend arriving after the generating row exists
// but before the run is registered must still get 409, not a second
// user message and a second answer.
func TestRetryInPlaceholderToStartWindow409(t *testing.T) {
	var e *env
	var armed atomic.Bool
	var tid string
	var code int
	var body []byte
	e, _ = newDetachEnv(t, 3, 5*time.Millisecond, func(s *Server) {
		s.Q = queue.New(4, 4, 5*time.Second)
		s.testHookBeforeStart = func() {
			if !armed.CompareAndSwap(true, false) {
				return
			}
			r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"x"}],"thread_id":"`+tid+`"}`)
			code, body = r.StatusCode, b
		}
	})
	r0, evs0 := openStream(t, e, "alice@example", longChat, 1)
	tid, _ = metaOf(t, evs0)
	io.Copy(io.Discard, r0.Body)
	r0.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for ms := threadMsgs(t, e, tid); ms[len(ms)-1].Status == store.StatusGenerating; ms = threadMsgs(t, e, tid) {
		if time.Now().After(deadline) {
			t.Fatal("seed turn still generating")
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := len(threadMsgs(t, e, tid))
	armed.Store(true)
	r1, _ := openStream(t, e, "alice@example", `{"stream":true,"messages":[{"role":"user","content":"x"}],"thread_id":"`+tid+`"}`, 1)
	defer r1.Body.Close()
	if armed.Load() {
		t.Fatal("hook did not fire")
	}
	if code != 409 || errCode(js(t, body)) != "generation_in_progress" {
		t.Fatalf("resend in window: %d %s", code, body)
	}
	users := 0
	for _, m := range threadMsgs(t, e, tid) {
		if m.Role == "user" {
			users++
		}
	}
	// seed turn + the in-window turn only; the 409'd resend added nothing.
	if users != 2 || len(threadMsgs(t, e, tid)) != before+2 {
		t.Fatalf("resend in window stored a second turn: %d users", users)
	}
}
