package handlers

import (
	"context"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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

	const n = 4
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{User: "alice@example"}))
			p, err := s.beginPersist(r, map[string]any{"thread_id": th.ID, "messages": []any{map[string]any{"role": "user", "content": "same"}}})
			if err != nil || p == nil {
				t.Errorf("beginPersist: %v", err)
				return
			}
			ids[i] = p.userMsgID
		}()
	}
	wg.Wait()
	got, _, _ := ms.ListMessages(ctx, "alice@example", th.ID, 100, "")
	users := 0
	for _, m := range got {
		if m.Role == "user" && m.Content == "same" {
			users++
		}
	}
	if sl.adds != 1 {
		t.Fatalf("want 1 AddMessage, got %d", sl.adds)
	}
	if users != 1 {
		t.Fatalf("want 1 stored user message, got %d", users)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("requests got different user message ids: %v", ids)
		}
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
