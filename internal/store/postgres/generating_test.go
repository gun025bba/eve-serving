package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/store"
)

func TestPostgresGeneratingLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a, b = "alice@example", "bob@example"
	th, err := s.CreateThread(ctx, a, "t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(ctx, b, "t"); err != nil {
		t.Fatal(err)
	}
	m, err := s.AddMessage(ctx, a, store.Message{ThreadID: th.ID, Role: "assistant", Status: store.StatusGenerating})
	if err != nil {
		t.Fatal("generating insert (00006 CHECK)", err)
	}
	if err := s.MarkGenerating(ctx, b, m.ID); err != store.ErrNotFound {
		t.Fatal("foreign MarkGenerating", err)
	}
	if err := s.MarkGenerating(ctx, a, m.ID); err != store.ErrGenerating {
		t.Fatal("already generating must conflict", err)
	}
	m.Content, m.Status = "done", store.StatusComplete
	if _, err := s.ReplaceMessage(ctx, a, m); err != nil {
		t.Fatal(err)
	}
	// concurrent regenerate: exactly one winner
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { errs <- s.MarkGenerating(ctx, a, m.ID) }()
	}
	wins := 0
	for i := 0; i < 8; i++ {
		switch err := <-errs; err {
		case nil:
			wins++
		case store.ErrGenerating:
		default:
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("winners", wins)
	}
	if _, err := s.AddMessage(ctx, a, store.Message{ThreadID: th.ID, Role: "assistant", Status: "bogus"}); err == nil {
		t.Fatal("CHECK must reject unknown status")
	}
	n, err := s.FailStaleGenerating(ctx)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	g, err := s.GetMessage(ctx, a, m.ID)
	if err != nil || g.Status != store.StatusFailed || g.Content != "done" || !strings.Contains(string(g.Metadata), `"error_code": "gateway_restart"`) {
		t.Fatalf("%+v %v %s", g, err, g.Metadata)
	}
}
