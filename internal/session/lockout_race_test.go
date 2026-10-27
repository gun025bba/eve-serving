package session

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slowStore makes code verification slow so concurrent guesses overlap.
type slowStore struct {
	Store
	takes atomic.Int32
}

func (s *slowStore) TakePairCode(ctx context.Context, h string, now time.Time) (int64, bool, error) {
	s.takes.Add(1)
	time.Sleep(20 * time.Millisecond)
	return s.Store.TakePairCode(ctx, h, now)
}

func TestPairLockoutConcurrent(t *testing.T) {
	st := &slowStore{Store: NewMemory()}
	m, err := New(st, []byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	good, err := m.IssueCode(context.Background(), "alice@example")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, err := m.Pair(context.Background(), "ZZZZ-ZZZZ"); err != ErrPairFailed {
				t.Errorf("err = %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := st.takes.Load(); n > MaxFailures {
		t.Fatalf("%d guesses evaluated, want <= %d", n, MaxFailures)
	}
	if !m.Locked() {
		t.Fatal("not locked after concurrent failures")
	}
	if _, _, err := m.Pair(context.Background(), good); err != ErrPairFailed {
		t.Fatal("valid code accepted while locked")
	}
	if n := st.takes.Load(); n > MaxFailures {
		t.Fatalf("locked attempt reached the store (%d)", n)
	}
}
