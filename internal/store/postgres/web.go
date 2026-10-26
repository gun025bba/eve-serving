package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gun025bba/llm-serving/internal/store"
	"github.com/jackc/pgx/v5"
)

var _ store.WebStore = (*Store)(nil)
var _ store.ThreadSettingsStore = (*Store)(nil)

// SaveWebRun replaces the message's tool_calls/web_sources in one transaction.
func (s *Store) SaveWebRun(ctx context.Context, user, messageID string, calls []store.ToolCall, srcs []store.WebSource) error {
	u, err := s.uid(ctx, user)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM messages WHERE id=$1 AND user_id=$2`, messageID, u).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM web_sources WHERE message_id=$1`, messageID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tool_calls WHERE message_id=$1`, messageID); err != nil {
		return err
	}
	now := time.Now()
	ids := make([]string, len(calls))
	for i, c := range calls {
		ids[i] = store.NewID(now)
		args := c.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tool_calls (id, message_id, user_id, round, seq, tool, provider, args, status, error, latency_ms, result_chars)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			ids[i], messageID, u, c.Round, c.Seq, c.Tool, c.Provider, []byte(args), c.Status, c.Error, c.LatencyMs, c.ResultChars); err != nil {
			return err
		}
	}
	for _, w := range srcs {
		var tc *string
		if w.ToolCallSeq != nil && *w.ToolCallSeq >= 0 && *w.ToolCallSeq < len(ids) {
			tc = &ids[*w.ToolCallSeq]
		}
		var sha *string
		if w.ContentSHA256 != "" {
			sha = &w.ContentSHA256
		}
		if _, err := tx.Exec(ctx, `INSERT INTO web_sources (message_id, user_id, tool_call_id, n, url, title, domain, snippet, fetched, content_sha256, cited)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			messageID, u, tc, w.N, w.URL, w.Title, w.Domain, w.Snippet, w.Fetched, sha, w.Cited); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) Sources(ctx context.Context, user string, ids []string) (map[string][]store.WebSource, error) {
	out := map[string][]store.WebSource{}
	if len(ids) == 0 {
		return out, nil
	}
	u, err := s.uid(ctx, user)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT message_id, n, url, coalesce(title,''), coalesce(domain,''), coalesce(snippet,''), fetched, cited
		FROM web_sources WHERE user_id=$1 AND message_id = ANY($2) ORDER BY message_id, n`, u, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w store.WebSource
		if err := rows.Scan(&w.MessageID, &w.N, &w.URL, &w.Title, &w.Domain, &w.Snippet, &w.Fetched, &w.Cited); err != nil {
			return nil, err
		}
		out[w.MessageID] = append(out[w.MessageID], w)
	}
	return out, rows.Err()
}

func (s *Store) PurgeWebHistory(ctx context.Context, cutoff time.Time) (int64, error) {
	a, err := s.pool.Exec(ctx, `DELETE FROM web_sources WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	b, err := s.pool.Exec(ctx, `DELETE FROM tool_calls WHERE created_at < $1`, cutoff)
	if err != nil {
		return a.RowsAffected(), err
	}
	return a.RowsAffected() + b.RowsAffected(), nil
}

func (s *Store) ThreadSettings(ctx context.Context, user, id string) (json.RawMessage, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return nil, err
	}
	var b []byte
	err = s.pool.QueryRow(ctx, `SELECT settings FROM threads WHERE id=$1 AND user_id=$2`, id, u).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return b, err
}

func (s *Store) SetThreadSettings(ctx context.Context, user, id string, v json.RawMessage) error {
	u, err := s.uid(ctx, user)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE threads SET settings = settings || $3::jsonb WHERE id=$1 AND user_id=$2`, id, u, []byte(v))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
