package handlers

import (
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
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"x"}],"thread_id":"`+tid+`"}`)
	if r.StatusCode != 409 || errCode(js(t, b)) != "generation_in_progress" {
		t.Fatal(r.StatusCode, string(b))
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
