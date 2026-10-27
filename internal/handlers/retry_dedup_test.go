package handlers

import (
	"testing"

	"github.com/gun025bba/eve-serving/internal/store"
)

func threadMsgs(t *testing.T, e *env, tid string) []store.Message {
	t.Helper()
	ms, _, err := e.st.ListMessages(t.Context(), "alice@example", tid, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestRetryWithoutReplaceDedupsUserMessage(t *testing.T) {
	e := newEnv(t, true)
	ctx := t.Context()
	post := func(tid, content string) map[string]any {
		r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"`+content+`"}],"thread_id":"`+tid+`"}`)
		if r.StatusCode != 200 {
			t.Fatal(r.StatusCode, string(b))
		}
		return js(t, b)
	}
	tid := post("new", "hi")["thread_id"].(string)

	// Same content after a complete reply is a genuinely new turn.
	post(tid, "hi")
	if n := len(threadMsgs(t, e, tid)); n != 4 {
		t.Fatalf("complete reply: want 4 messages, got %d", n)
	}

	// User message with no reply (e.g. lost before placeholder): reused.
	um, _ := e.st.AddMessage(ctx, "alice@example", store.Message{ThreadID: tid, Role: "user", Content: "again"})
	m := post(tid, "again")
	if m["user_message_id"] != um.ID {
		t.Fatalf("want reused %s, got %v", um.ID, m)
	}
	ms := threadMsgs(t, e, tid)
	if len(ms) != 6 || ms[5].Status != store.StatusComplete {
		t.Fatalf("no reply: %+v", ms)
	}

	// Failed (or cancelled) reply: user message reused, failed reply overwritten.
	um2, _ := e.st.AddMessage(ctx, "alice@example", store.Message{ThreadID: tid, Role: "user", Content: "third"})
	fa, _ := e.st.AddMessage(ctx, "alice@example", store.Message{ThreadID: tid, Role: "assistant", Status: store.StatusFailed})
	m = post(tid, "third")
	if m["user_message_id"] != um2.ID || m["message_id"] != fa.ID || m["replaced_message_id"] != fa.ID {
		t.Fatalf("failed reply: %v", m)
	}
	ms = threadMsgs(t, e, tid)
	if len(ms) != 8 || ms[7].ID != fa.ID || ms[7].Status != store.StatusComplete || ms[7].Content != "답변" {
		t.Fatalf("failed reply rows: %+v", ms)
	}

	// Different content is never deduped.
	e.st.AddMessage(ctx, "alice@example", store.Message{ThreadID: tid, Role: "user", Content: "x"})
	post(tid, "y")
	if n := len(threadMsgs(t, e, tid)); n != 11 {
		t.Fatalf("different content: want 11, got %d", n)
	}

	// Still generating: not a retry target.
	e.st.AddMessage(ctx, "alice@example", store.Message{ThreadID: tid, Role: "user", Content: "z"})
	e.st.AddMessage(ctx, "alice@example", store.Message{ThreadID: tid, Role: "assistant", Status: store.StatusGenerating})
	post(tid, "z")
	if n := len(threadMsgs(t, e, tid)); n != 15 {
		t.Fatalf("generating: want 15, got %d", n)
	}
}
