package handlers

import (
	"context"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
)

type userLedger struct {
	mu   sync.Mutex
	rows []audit.Event
	user []string
}

func (l *userLedger) Append(_ context.Context, user string, e audit.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows, l.user = append(l.rows, e), append(l.user, user)
	return nil
}

func TestBriefingRetention_73(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st := memstore.New()
	led := &userLedger{}
	srv := &Server{Store: st, Audit: led, Log: logging.New(io.Discard, logging.INFO), M: metrics.New(10)}
	job, other := "J1", "J2"
	kind, otherKind := store.BriefingKind, "reminder"
	add := func(user, thread string, m store.Message, age int) string {
		m.ThreadID, m.CreatedAt = thread, now.Add(-time.Duration(age)*24*time.Hour)
		out, err := st.AddMessage(ctx, user, m)
		if err != nil {
			t.Fatal(err)
		}
		return out.ID
	}
	brief := func(user, thread string, age int) string {
		return add(user, thread, store.Message{Role: "assistant", Content: "SECRET-BODY", Origin: store.OriginProactive, JobID: &job, TemplateKind: &kind}, age)
	}
	setDays := func(user string, d int) {
		if _, err := st.MergeUserSettings(ctx, user, "briefing", []byte(`{"retention_days":`+strconv.Itoa(d)+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	ids := func() map[string]bool {
		m := map[string]bool{}
		for _, u := range []string{"alice@example", "bob@example", "carol@example"} {
			ths, _ := st.ListThreads(ctx, u, false, 50, nil)
			for _, th := range ths {
				ms, _, _ := st.ListMessages(ctx, u, th.ID, 100, "")
				for _, x := range ms {
					m[x.ID] = true
				}
			}
		}
		return m
	}

	ta, _ := st.CreateThread(ctx, "alice@example", "a")
	tb, _ := st.CreateThread(ctx, "bob@example", "b")
	tc, _ := st.CreateThread(ctx, "carol@example", "c")
	aOld, aNew, aEdge := brief("alice@example", ta.ID, 31), brief("alice@example", ta.ID, 5), brief("alice@example", ta.ID, 29) // default 30
	aOtherTpl := add("alice@example", ta.ID, store.Message{Role: "assistant", Content: "x", Origin: store.OriginProactive, JobID: &other, TemplateKind: &otherKind}, 90)
	aUser := add("alice@example", ta.ID, store.Message{Role: "user", Content: "hi"}, 90)
	aAsst := add("alice@example", ta.ID, store.Message{Role: "assistant", Content: "yo"}, 90)
	setDays("bob@example", 0) // off
	bOld := brief("bob@example", tb.ID, 400)
	setDays("carol@example", 7)
	cOld, cNew := brief("carol@example", tc.ID, 8), brief("carol@example", tc.ID, 6)

	n, err := srv.PurgeBriefings(ctx, now)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	got := ids()
	for id, want := range map[string]bool{aOld: false, cOld: false, aNew: true, aEdge: true, aOtherTpl: true, aUser: true, aAsst: true, bOld: true, cNew: true} {
		if got[id] != want {
			t.Errorf("message %s present=%v want %v", id, got[id], want)
		}
	}
	// audit: counts only, one row per affected user, actor system
	if len(led.rows) != 2 {
		t.Fatalf("audit rows %+v", led.rows)
	}
	for i, e := range led.rows {
		if e.Event != audit.EvBriefingRetention || e.Actor != audit.ActorSystem || e.Validate() != nil || e.Detail["deleted"] != "1" || len(e.Detail) != 2 ||
			e.ThreadID != "" || e.MessageID != "" || e.Tool != "" || e.PayloadSHA256 != "" {
			t.Errorf("audit row %d %+v", i, e)
		}
	}
	if led.user[0] != "alice@example" || led.rows[0].Detail["retention_days"] != "30" || led.user[1] != "carol@example" || led.rows[1].Detail["retention_days"] != "7" {
		t.Errorf("audit users %v %+v", led.user, led.rows)
	}
	if n, err := srv.PurgeBriefings(ctx, now); err != nil || n != 0 || len(led.rows) != 2 { // idempotent, no empty audit rows
		t.Fatal(n, err, len(led.rows))
	}
}
