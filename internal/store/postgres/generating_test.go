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
	if err := s.SetMessageStatus(ctx, b, m.ID, store.StatusFailed); err != store.ErrNotFound {
		t.Fatal("foreign SetMessageStatus", err)
	}
	m.Content, m.Status = "done", store.StatusComplete
	if _, err := s.ReplaceMessage(ctx, a, m); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageStatus(ctx, a, m.ID, store.StatusGenerating); err != nil {
		t.Fatal(err)
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
