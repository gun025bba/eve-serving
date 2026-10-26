package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

// Integration tests: set GW_TEST_DATABASE_URL to a throwaway database
// (e.g. llmchat_test). All tables are TRUNCATEd.
func open(t *testing.T) *Store {
	dsn := os.Getenv("GW_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("GW_TEST_DATABASE_URL must be set in CI (Postgres tests may not be skipped)")
		}
		t.Skip("GW_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, dsn); err != nil { // idempotent
		t.Fatal(err)
	}
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE web_sources, tool_calls, feedback, messages, threads, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestPostgresStore(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const a, b = "alice@example", "bob@example"
	th, err := s.CreateThread(ctx, a, "한국어 테스트")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetThread(ctx, b, th.ID); err != store.ErrNotFound {
		t.Fatal("isolation", err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	var ids []string
	for i, c := range []string{"서울의 맛집을 알려줘", "명동 칼국수가 유명해요", "the quick brown fox"} {
		role := "user"
		if i == 1 {
			role = "assistant"
		}
		m, err := s.AddMessage(ctx, a, store.Message{ThreadID: th.ID, Role: role, Content: c, CreatedAt: base.Add(time.Duration(i) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	if _, err := s.AddMessage(ctx, b, store.Message{ThreadID: th.ID, Role: "user", Content: "x"}); err != store.ErrNotFound {
		t.Fatal("bob wrote to alice thread", err)
	}
	if _, err := s.AddMessage(ctx, a, store.Message{ThreadID: th.ID, Role: "bogus", Content: "x"}); err != store.ErrInvalid {
		t.Fatal(err)
	}
	ms, more, err := s.ListMessages(ctx, a, th.ID, 2, "")
	if err != nil || !more || ms[0].ID != ids[1] || ms[1].ID != ids[2] {
		t.Fatal(ms, more, err)
	}
	ms, more, _ = s.ListMessages(ctx, a, th.ID, 10, ids[1])
	if more || len(ms) != 1 || ms[0].ID != ids[0] {
		t.Fatal(ms)
	}
	got, _ := s.GetThread(ctx, a, th.ID)
	if got.MessageCount != 3 || *got.LastMessagePreview != "the quick brown fox" {
		t.Fatal(got)
	}

	// feedback + replace clears it
	c := "good"
	if _, err := s.SetFeedback(ctx, a, ids[1], "up", nil, &c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFeedback(ctx, b, ids[1], "up", nil, nil); err != store.ErrNotFound {
		t.Fatal(err)
	}
	m, _ := s.GetMessage(ctx, a, ids[1])
	if m.Feedback == nil || m.Feedback.Rating != "up" {
		t.Fatal(m.Feedback)
	}
	if len(m.Feedback.Reasons) != 0 || *m.Feedback.Comment != "good" {
		t.Fatal(m.Feedback)
	}
	if f, err := s.SetFeedback(ctx, a, ids[1], "down", []string{"wrong_info", "bad_source"}, nil); err != nil || len(f.Reasons) != 2 || f.Comment != nil {
		t.Fatal(f, err)
	}
	// DB CHECKs back up the API validation
	if _, err := s.SetFeedback(ctx, a, ids[1], "up", []string{"wrong_info"}, nil); err == nil {
		t.Fatal("polarity check missing")
	}
	long := strings.Repeat("가", 141)
	if _, err := s.SetFeedback(ctx, a, ids[1], "up", nil, &long); err == nil {
		t.Fatal("comment length check missing")
	}
	var ex []store.FeedbackExport
	if err := s.ExportFeedback(ctx, time.Time{}, func(e store.FeedbackExport) error { ex = append(ex, e); return nil }); err != nil || len(ex) != 1 ||
		ex[0].MessageID != ids[1] || ex[0].Message == "" || ex[0].Rating != "down" || len(ex[0].Reasons) != 2 {
		t.Fatal(ex, err)
	}
	m, _ = s.GetMessage(ctx, a, ids[1])
	if m.Feedback.Rating != "down" || len(m.Feedback.Reasons) != 2 {
		t.Fatal(m.Feedback)
	}
	pt := int64(5)
	m, err = s.ReplaceMessage(ctx, a, store.Message{ID: ids[1], Content: "새 답변", Usage: &store.Usage{PromptTokens: &pt}, Params: []byte(`{"temperature":0.2}`), Status: "incomplete", Metadata: []byte(`{"incomplete":true}`)})
	if err != nil || m.Feedback != nil || m.Content != "새 답변" || m.UpdatedAt == nil || !m.CreatedAt.Equal(base.Add(time.Second)) || *m.Usage.PromptTokens != 5 || m.Status != "incomplete" {
		t.Fatal(m, err)
	}
	if err := s.DeleteFeedback(ctx, b, ids[1]); err != store.ErrNotFound {
		t.Fatal(err)
	}

	// Korean substring search (trigram/ILIKE) and FTS on English words
	hits, err := s.Search(ctx, a, "맛집", 10, 0)
	if err != nil || len(hits) != 1 || hits[0].Snippet != "서울의 <mark>맛집</mark>을 알려줘" || *hits[0].MessageID != ids[0] {
		t.Fatal(hits, err)
	}
	hits, _ = s.Search(ctx, a, "한국어", 10, 0)
	if len(hits) != 1 || hits[0].Match != "title" || hits[0].MessageID != nil {
		t.Fatal(hits)
	}
	hits, _ = s.Search(ctx, a, "quick fox", 10, 0)
	if len(hits) != 1 || !strings.Contains(hits[0].Snippet, "<mark>quick</mark>") {
		t.Fatal(hits)
	}
	if hits, _ = s.Search(ctx, b, "맛집", 10, 0); len(hits) != 0 {
		t.Fatal("bob found alice's data")
	}
	if hits, _ = s.Search(ctx, a, "100%_", 10, 0); len(hits) != 0 {
		t.Fatal("wildcards not escaped")
	}

	// keyset pagination
	for i := 0; i < 4; i++ {
		s.CreateThread(ctx, a, "p")
	}
	var seen []string
	var cur *store.ThreadCursor
	for {
		ts, err := s.ListThreads(ctx, a, false, 2, cur)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range ts {
			seen = append(seen, x.ID)
		}
		if len(ts) < 2 {
			break
		}
		cur = &store.ThreadCursor{UpdatedAt: ts[1].UpdatedAt, ID: ts[1].ID}
	}
	if len(seen) != 5 {
		t.Fatal(len(seen))
	}
	yes := true
	if _, err := s.UpdateThread(ctx, a, th.ID, nil, &yes); err != nil {
		t.Fatal(err)
	}
	if ts, _ := s.ListThreads(ctx, a, true, 10, nil); len(ts) != 1 {
		t.Fatal(ts)
	}

	// import idempotency + created_at preservation
	ct := time.Date(2025, 5, 6, 7, 8, 9, 123456000, time.UTC)
	mc := "m-1"
	it := store.ImportThread{ClientID: "c-1", Title: "imported", CreatedAt: ct, Messages: []store.ImportMessage{
		{ClientID: &mc, Role: "user", Content: "가져온 메시지", CreatedAt: ct},
		{Role: "assistant", Content: "응답", CreatedAt: ct.Add(time.Minute)}}}
	id1, created, err := s.ImportThread(ctx, a, it)
	if err != nil || !created {
		t.Fatal(err)
	}
	id2, created, _ := s.ImportThread(ctx, a, it)
	if created || id2 != id1 {
		t.Fatal("not idempotent")
	}
	if _, created, _ := s.ImportThread(ctx, b, it); !created {
		t.Fatal("client_id must be per user")
	}
	g, _ := s.GetThread(ctx, a, id1)
	if g.CreatedAt != ct || g.CreatedAt.Location() != time.UTC || !g.UpdatedAt.Equal(ct.Add(time.Minute)) || g.MessageCount != 2 {
		t.Fatal(g)
	}
	ms, _, _ = s.ListMessages(ctx, a, id1, 10, "")
	if !ms[0].CreatedAt.Equal(ct) || *ms[0].ClientID != "m-1" {
		t.Fatal(ms[0])
	}
}
