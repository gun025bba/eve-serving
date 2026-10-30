package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
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

// Migration 00009 (additive): the new nullable columns round-trip, 'denied'
// is stored as 'denied', and ClearExpiredArgs empties only expired args.
func TestPostgresToolCallsAutoSelect(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a = "alice@example"
	th, _ := s.CreateThread(ctx, a, "auto")
	m, err := s.AddMessage(ctx, a, store.Message{ThreadID: th.ID, Role: "assistant", Content: "x", Status: "complete"})
	if err != nil {
		t.Fatal(err)
	}
	str := func(v string) *string { return &v }
	n3 := 3
	past, future := time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
	calls := []store.ToolCall{
		{Round: 1, Seq: 0, Tool: "web_search", Args: json.RawMessage(`{"query":"q"}`), Status: "ok", SelectedBy: str("model"), ResultCount: &n3, ArgsExpiresAt: &past},
		{Round: 1, Seq: 1, Tool: "web_fetch", Args: json.RawMessage(`{}`), Status: "denied", SelectedBy: str("model"), ErrorCode: str("permission_denied"), ArgsExpiresAt: &future},
		{Round: 2, Seq: 0, Tool: "web_search", Args: json.RawMessage(`{"query":"keep"}`), Status: "ok", ArgsExpiresAt: &future},
		{Round: 2, Seq: 1, Tool: "web_search", Args: json.RawMessage(`{"query":"old row"}`), Status: "ok"}, // pre-6.0.0 style: NULL expiry
	}
	if err := s.SaveWebRun(ctx, a, m.ID, calls, nil); err != nil {
		t.Fatal(err)
	}
	var st, by, code string
	var cnt int
	if err := s.pool.QueryRow(ctx, `SELECT status, selected_by, coalesce(error_code,''), 0 FROM tool_calls WHERE message_id=$1 AND round=1 AND seq=1`, m.ID).Scan(&st, &by, &code, &cnt); err != nil {
		t.Fatal(err)
	}
	if st != "denied" || by != "model" || code != "permission_denied" {
		t.Fatal(st, by, code)
	}
	if err := s.pool.QueryRow(ctx, `SELECT result_count FROM tool_calls WHERE message_id=$1 AND round=1 AND seq=0`, m.ID).Scan(&cnt); err != nil || cnt != 3 {
		t.Fatal(cnt, err)
	}
	if n, err := s.ClearExpiredArgs(ctx, time.Now()); err != nil || n != 1 {
		t.Fatal("cleared", n, err)
	}
	args := func(round, seq int) string {
		var b []byte
		if err := s.pool.QueryRow(ctx, `SELECT args FROM tool_calls WHERE message_id=$1 AND round=$2 AND seq=$3`, m.ID, round, seq).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := args(1, 0); got != `{}` {
		t.Fatal("expired args kept:", got)
	}
	if !strings.Contains(args(2, 0), "keep") || !strings.Contains(args(2, 1), "old row") {
		t.Fatal("unexpired / NULL-expiry args were cleared")
	}
	if n, _ := s.ClearExpiredArgs(ctx, time.Now()); n != 0 {
		t.Fatal("second run must be a no-op", n)
	}
	// the status CHECK still rejects unknown values
	if _, err := s.pool.Exec(ctx, `UPDATE tool_calls SET status='weird' WHERE message_id=$1`, m.ID); err == nil {
		t.Fatal("status CHECK accepts unknown values")
	}
}
