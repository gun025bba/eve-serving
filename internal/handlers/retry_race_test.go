package handlers

import (
	"context"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

// slowList widens the window between "read last turn" and "insert" so an
// unserialized implementation reliably double-inserts.
type slowList struct {
	store.Store
	mu   sync.Mutex
	adds int
}

func (s *slowList) AddMessage(ctx context.Context, user string, m store.Message) (store.Message, error) {
	s.mu.Lock()
	s.adds++
	s.mu.Unlock()
	return s.Store.AddMessage(ctx, user, m)
}

func (s *slowList) ListMessages(ctx context.Context, user, tid string, limit int, before string) ([]store.Message, bool, error) {
	ms, more, err := s.Store.ListMessages(ctx, user, tid, limit, before)
	time.Sleep(30 * time.Millisecond) // stale snapshot held across the window
	return ms, more, err
}

func TestRetryDedup_ConcurrentIdenticalResendsInsertOnce(t *testing.T) {
	ms := memstore.New()
	sl := &slowList{Store: ms}
	s := &Server{Store: sl, Log: logging.New(io.Discard, logging.INFO)}
	ctx := context.Background()
	th, _ := ms.CreateThread(ctx, "alice@example", "t")
	ms.AddMessage(ctx, "alice@example", store.Message{ThreadID: th.ID, Role: "user", Content: "q"})
	ms.AddMessage(ctx, "alice@example", store.Message{ThreadID: th.ID, Role: "assistant", Content: "a", Status: store.StatusComplete})

	// #30: of n identical concurrent sends exactly one proceeds; the others
	// get 409 generation_in_progress instead of a second answer.
	const n = 4
	var mu sync.Mutex
	var ok []*chatPersist
	conflicts := 0
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{User: "alice@example"}))
			p, err := s.beginPersist(r, map[string]any{"thread_id": th.ID, "messages": []any{map[string]any{"role": "user", "content": "same"}}})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if e, isAPI := err.(*apierr.Error); isAPI && e.Status == 409 && e.Code == "generation_in_progress" {
					conflicts++
					return
				}
				t.Errorf("beginPersist: %v", err)
				return
			}
			ok = append(ok, p)
		}()
	}
	wg.Wait()
	if len(ok) != 1 || conflicts != n-1 {
		t.Fatalf("want 1 ok + %d conflicts, got %d ok + %d", n-1, len(ok), conflicts)
	}
	if sl.adds != 1 {
		t.Fatalf("want 1 AddMessage, got %d", sl.adds)
	}
	// Releasing the mark (completion, error or client cancel) lets a resend
	// of the same turn through again and it reuses the saved user message.
	ok[0].releaseInflight()
	ok[0].releaseInflight() // idempotent
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{User: "alice@example"}))
	p, err := s.beginPersist(r, map[string]any{"thread_id": th.ID, "messages": []any{map[string]any{"role": "user", "content": "same"}}})
	if err != nil || p.userMsgID != ok[0].userMsgID {
		t.Fatalf("resend after release: %v %+v", err, p)
	}
	p.releaseInflight()
	if s.inflight.has("alice@example\x00" + th.ID + "\x00same") {
		t.Fatal("in-flight mark leaked")
	}
}

// retryTarget relies on ListMessages(limit=2, before="") returning the two
// NEWEST messages ordered oldest -> newest (store.MessageStore contract).
func TestListMessages_LastNOldestToNewest(t *testing.T) {
	ms := memstore.New()
	ctx := context.Background()
	th, _ := ms.CreateThread(ctx, "alice@example", "t")
	var want []string
	for _, c := range []string{"m1", "m2", "m3", "m4"} {
		m, _ := ms.AddMessage(ctx, "alice@example", store.Message{ThreadID: th.ID, Role: "user", Content: c})
		want = append(want, m.ID)
	}
	got, more, err := ms.ListMessages(ctx, "alice@example", th.ID, 2, "")
	if err != nil || len(got) != 2 || !more {
		t.Fatalf("got %d more=%v err=%v", len(got), more, err)
	}
	if got[0].ID != want[2] || got[1].ID != want[3] {
		t.Fatalf("want [m3 m4], got [%s %s]", got[0].Content, got[1].Content)
	}
}
