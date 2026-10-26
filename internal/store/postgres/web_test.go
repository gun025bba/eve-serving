package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gun025bba/llm-serving/internal/store"
)

func TestPostgresWebHistory(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a, b = "alice@example", "bob@example"
	th, _ := s.CreateThread(ctx, a, "web")
	m, err := s.AddMessage(ctx, a, store.Message{ThreadID: th.ID, Role: "assistant", Content: "x [1]", Status: "complete"})
	if err != nil {
		t.Fatal(err)
	}
	p := "searxng"
	seq := 0
	calls := []store.ToolCall{{Round: 1, Seq: 0, Tool: "web_search", Provider: &p, Args: json.RawMessage(`{"query":"q"}`), Status: "ok"},
		{Round: 1, Seq: 1, Tool: "web_fetch", Args: json.RawMessage(`{"url_sha256":"ab"}`), Status: "blocked"}}
	srcs := []store.WebSource{{N: 1, URL: "https://a.example/", Title: "A", Domain: "a.example", Cited: true, ToolCallSeq: &seq, ContentSHA256: "ff", FinalURL: "https://www.a.example/moved"}}
	if err := s.SaveWebRun(ctx, a, m.ID, calls, srcs); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWebRun(ctx, a, m.ID, calls, srcs); err != nil { // replace, no dup
		t.Fatal(err)
	}
	if err := s.SaveWebRun(ctx, b, m.ID, calls, srcs); err == nil {
		t.Fatal("bob wrote alice's run")
	}
	got, err := s.Sources(ctx, a, []string{m.ID})
	if err != nil || len(got[m.ID]) != 1 || !got[m.ID][0].Cited || got[m.ID][0].FinalURL != "https://www.a.example/moved" {
		t.Fatal(got, err)
	}
	if got, _ := s.Sources(ctx, b, []string{m.ID}); len(got[m.ID]) != 0 {
		t.Fatal("isolation")
	}
	if n, err := s.PurgeWebHistory(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if n, err := s.PurgeWebHistory(ctx, time.Now().Add(time.Hour)); err != nil || n != 3 {
		t.Fatal("purge", n, err)
	}
	s.SaveWebRun(ctx, a, m.ID, calls, srcs)
	m.Content = "regen"
	if _, err := s.ReplaceMessage(ctx, a, m); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Sources(ctx, a, []string{m.ID}); len(got[m.ID]) != 0 {
		t.Fatal("sources survived regenerate")
	}
	if err := s.SetThreadSettings(ctx, a, th.ID, json.RawMessage(`{"web_search":"auto"}`)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ThreadSettings(ctx, a, th.ID); string(v) != `{"web_search": "auto"}` && string(v) != `{"web_search":"auto"}` {
		t.Fatal(string(v))
	}
	if err := s.SetThreadSettings(ctx, b, th.ID, json.RawMessage(`{"web_search":"force"}`)); err == nil {
		t.Fatal("bob changed alice's thread")
	}
}
