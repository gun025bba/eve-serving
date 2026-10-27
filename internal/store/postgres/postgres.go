// Package postgres implements store.Store on PostgreSQL via pgx/pgxpool.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool  *pgxpool.Pool
	users sync.Map // login -> int64
}

var _ store.Store = (*Store)(nil)

// Open connects (and pings) using a libpq/URL DSN.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid database url") // never echo the DSN
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// UserID implements store.UserIDResolver.
func (s *Store) UserID(ctx context.Context, login string) (int64, error) { return s.uid(ctx, login) }

func (s *Store) uid(ctx context.Context, login string) (int64, error) {
	if v, ok := s.users.Load(login); ok {
		return v.(int64), nil
	}
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO users (login) VALUES ($1)
		ON CONFLICT (login) DO UPDATE SET login = EXCLUDED.login RETURNING id`, login).Scan(&id)
	if err != nil {
		return 0, err
	}
	s.users.Store(login, id)
	return id, nil
}

const threadCols = `t.id, t.title, t.created_at, t.updated_at, t.archived, t.client_id,
	(SELECT count(*) FROM messages m WHERE m.thread_id = t.id),
	(SELECT left(m.content, 200) FROM messages m WHERE m.thread_id = t.id ORDER BY m.created_at DESC, m.id DESC LIMIT 1),
	(SELECT count(*) FROM messages m WHERE m.thread_id = t.id AND m.user_id = t.user_id AND m.origin = 'proactive' AND m.read_at IS NULL)`

func scanThread(r pgx.Row) (store.Thread, error) {
	var t store.Thread
	var prev *string
	err := r.Scan(&t.ID, &t.Title, &t.CreatedAt, &t.UpdatedAt, &t.Archived, &t.ClientID, &t.MessageCount, &prev, &t.UnreadCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, store.ErrNotFound
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	if prev != nil {
		p := store.Preview(*prev, 120)
		t.LastMessagePreview = &p
	}
	return t, err
}

func (s *Store) CreateThread(ctx context.Context, user, title string) (store.Thread, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return store.Thread{}, err
	}
	id := store.NewID(time.Now())
	if _, err := s.pool.Exec(ctx, `INSERT INTO threads (id, user_id, title) VALUES ($1,$2,$3)`, id, u, title); err != nil {
		return store.Thread{}, err
	}
	return s.GetThread(ctx, user, id)
}

func (s *Store) GetThread(ctx context.Context, user, id string) (store.Thread, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return store.Thread{}, err
	}
	return scanThread(s.pool.QueryRow(ctx, `SELECT `+threadCols+` FROM threads t WHERE t.id=$1 AND t.user_id=$2`, id, u))
}

func (s *Store) ListThreads(ctx context.Context, user string, archived bool, limit int, after *store.ThreadCursor) ([]store.Thread, error) {
	return s.listThreads(ctx, user, archived, false, limit, after)
}

// ListUnreadThreads is ListThreads restricted to threads with unread
// proactive messages (GET /v1/threads?unread=true).
func (s *Store) ListUnreadThreads(ctx context.Context, user string, archived bool, limit int, after *store.ThreadCursor) ([]store.Thread, error) {
	return s.listThreads(ctx, user, archived, true, limit, after)
}

func (s *Store) listThreads(ctx context.Context, user string, archived, unread bool, limit int, after *store.ThreadCursor) ([]store.Thread, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + threadCols + ` FROM threads t WHERE t.user_id=$1 AND t.archived=$2`
	if unread {
		q += ` AND EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = t.id AND m.user_id = t.user_id AND m.origin = 'proactive' AND m.read_at IS NULL)`
	}
	args := []any{u, archived, limit}
	if after != nil {
		q += ` AND (t.updated_at, t.id) < ($4, $5)`
		args = append(args, after.UpdatedAt, after.ID)
	}
	q += ` ORDER BY t.updated_at DESC, t.id DESC LIMIT $3`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Thread{}
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateThread(ctx context.Context, user, id string, title *string, archived *bool) (store.Thread, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return store.Thread{}, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE threads SET title=COALESCE($3,title), archived=COALESCE($4,archived), updated_at=now()
		WHERE id=$1 AND user_id=$2`, id, u, title, archived)
	if err != nil {
		return store.Thread{}, err
	}
	if tag.RowsAffected() == 0 {
		return store.Thread{}, store.ErrNotFound
	}
	return s.GetThread(ctx, user, id)
}

func jsonb(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return []byte(r)
}

func usageJSON(u *store.Usage) any {
	if u == nil {
		return nil
	}
	b, _ := json.Marshal(u)
	return b
}

func (s *Store) AddMessage(ctx context.Context, user string, m store.Message) (store.Message, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return m, err
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.ID == "" {
		m.ID = store.NewID(m.CreatedAt)
	}
	if m.Status == "" {
		m.Status = store.StatusComplete
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return m, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE threads SET updated_at=GREATEST(updated_at,$3) WHERE id=$1 AND user_id=$2`, m.ThreadID, u, m.CreatedAt)
	if err != nil {
		return m, err
	}
	if tag.RowsAffected() == 0 {
		return m, store.ErrNotFound
	}
	origin := store.OriginUserTurn
	var jobName *string
	if m.Origin == store.OriginProactive {
		origin = store.OriginProactive
		if m.Provenance != nil {
			jobName = m.Provenance.JobName
		}
	}
	if m.JobID != nil {
		// FK alone would accept another user's job; require the same owner.
		// Soft-deleted jobs remain valid references.
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM scheduled_jobs WHERE id=$1 AND user_id=$2)`, *m.JobID, u).Scan(&ok); err != nil {
			return m, err
		}
		if !ok {
			return m, store.ErrInvalid
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO messages (id, thread_id, user_id, role, content, reasoning, model, params, usage, latency_ms, metadata, status, client_id, created_at,
		origin, job_id, job_name, template_kind)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		m.ID, m.ThreadID, u, m.Role, m.Content, m.Reasoning, m.Model, jsonb(m.Params), usageJSON(m.Usage), m.LatencyMs, jsonb(m.Metadata), m.Status, m.ClientID, m.CreatedAt,
		origin, m.JobID, jobName, m.TemplateKind)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "23514" || pe.Code == "23503" && pe.ConstraintName == "messages_job_id_fkey") {
			return m, store.ErrInvalid
		}
		return m, err
	}
	m.Origin = store.APIOrigin(origin, m.Role)
	if origin != store.OriginProactive {
		m.JobID, m.TemplateKind, m.Provenance = nil, nil, nil
	}
	return m, tx.Commit(ctx)
}

func (s *Store) ReplaceMessage(ctx context.Context, user string, m store.Message) (store.Message, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return m, err
	}
	if m.Status == "" {
		m.Status = store.StatusComplete
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return m, err
	}
	defer tx.Rollback(ctx)
	var tid string
	err = tx.QueryRow(ctx, `UPDATE messages SET content=$3, reasoning=$4, model=$5, params=$6, usage=$7, latency_ms=$8, metadata=$9, status=$10, updated_at=now()
		WHERE id=$1 AND user_id=$2 RETURNING thread_id`,
		m.ID, u, m.Content, m.Reasoning, m.Model, jsonb(m.Params), usageJSON(m.Usage), m.LatencyMs, jsonb(m.Metadata), m.Status).Scan(&tid)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, store.ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM feedback WHERE message_id=$1`, m.ID); err != nil {
		return m, err
	}
	// Regenerate replaces the message's web history too (SaveWebRun re-adds it).
	if _, err := tx.Exec(ctx, `DELETE FROM web_sources WHERE message_id=$1`, m.ID); err != nil {
		return m, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tool_calls WHERE message_id=$1`, m.ID); err != nil {
		return m, err
	}
	if _, err := tx.Exec(ctx, `UPDATE threads SET updated_at=now() WHERE id=$1`, tid); err != nil {
		return m, err
	}
	if err := tx.Commit(ctx); err != nil {
		return m, err
	}
	return s.GetMessage(ctx, user, m.ID)
}

const msgCols = `m.id, m.thread_id, m.role, m.content, m.reasoning, m.model, m.params, m.usage, m.latency_ms, m.metadata, m.status, m.client_id, m.created_at, m.updated_at,
	m.origin, m.job_id, m.job_name, m.template_kind, m.read_at,
	f.rating, f.reasons, f.comment, f.created_at, f.updated_at`

func scanMsg(r pgx.Row) (store.Message, error) {
	var m store.Message
	var params, usage, meta []byte
	var rating, comment *string
	var reasons []string
	var fc, fu *time.Time
	var origin string
	var jobName *string
	err := r.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.Reasoning, &m.Model, &params, &usage, &m.LatencyMs, &meta, &m.Status, &m.ClientID, &m.CreatedAt, &m.UpdatedAt,
		&origin, &m.JobID, &jobName, &m.TemplateKind, &m.ReadAt,
		&rating, &reasons, &comment, &fc, &fu)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, store.ErrNotFound
	}
	if err != nil {
		return m, err
	}
	m.Params, m.Metadata = params, meta
	m.CreatedAt = m.CreatedAt.UTC()
	m.Origin = store.APIOrigin(origin, m.Role)
	if origin == store.OriginProactive {
		m.Provenance = &store.Provenance{Actor: "scheduler", JobID: m.JobID, JobName: jobName}
		if m.ReadAt != nil {
			ra := m.ReadAt.UTC()
			m.ReadAt = &ra
		}
	} else {
		m.JobID, m.TemplateKind, m.ReadAt = nil, nil, nil
	}
	if m.UpdatedAt != nil {
		u := m.UpdatedAt.UTC()
		m.UpdatedAt = &u
	}
	if usage != nil {
		m.Usage = &store.Usage{}
		json.Unmarshal(usage, m.Usage)
	}
	if rating != nil {
		m.Feedback = &store.Feedback{MessageID: m.ID, Rating: *rating, Reasons: nonNil(reasons), Comment: comment, CreatedAt: fc.UTC(), UpdatedAt: fu.UTC()}
	}
	return m, nil
}

func (s *Store) GetMessage(ctx context.Context, user, id string) (store.Message, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return store.Message{}, err
	}
	return scanMsg(s.pool.QueryRow(ctx, `SELECT `+msgCols+` FROM messages m LEFT JOIN feedback f ON f.message_id=m.id WHERE m.id=$1 AND m.user_id=$2`, id, u))
}

func (s *Store) ListMessages(ctx context.Context, user, threadID string, limit int, before string) ([]store.Message, bool, error) {
	if _, err := s.GetThread(ctx, user, threadID); err != nil {
		return nil, false, err
	}
	u, _ := s.uid(ctx, user)
	q := `SELECT ` + msgCols + ` FROM messages m LEFT JOIN feedback f ON f.message_id=m.id WHERE m.thread_id=$1 AND m.user_id=$2`
	args := []any{threadID, u, limit + 1}
	if before != "" {
		var bc time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at FROM messages WHERE id=$1 AND thread_id=$2 AND user_id=$3`, before, threadID, u).Scan(&bc)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, store.ErrNotFound
		} else if err != nil {
			return nil, false, err
		}
		q += ` AND (m.created_at, m.id) < ($4, $5)`
		args = append(args, bc, before)
	}
	q += ` ORDER BY m.created_at DESC, m.id DESC LIMIT $3`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []store.Message{}
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, more, nil
}

func nonNil(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

func (s *Store) SetFeedback(ctx context.Context, user, messageID, rating string, reasons []string, comment *string) (store.Feedback, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return store.Feedback{}, err
	}
	f := store.Feedback{MessageID: messageID}
	err = s.pool.QueryRow(ctx, `INSERT INTO feedback (message_id, user_id, rating, reasons, comment)
		SELECT id, user_id, $3, $4, $5 FROM messages WHERE id=$1 AND user_id=$2
		ON CONFLICT (message_id) DO UPDATE SET rating=EXCLUDED.rating, reasons=EXCLUDED.reasons, comment=EXCLUDED.comment, updated_at=now()
		RETURNING rating, reasons, comment, created_at, updated_at`, messageID, u, rating, nonNil(reasons), comment).Scan(&f.Rating, &f.Reasons, &f.Comment, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, store.ErrNotFound
	}
	f.CreatedAt, f.UpdatedAt = f.CreatedAt.UTC(), f.UpdatedAt.UTC()
	return f, err
}

func (s *Store) DeleteFeedback(ctx context.Context, user, messageID string) error {
	if _, err := s.GetMessage(ctx, user, messageID); err != nil {
		return err
	}
	u, _ := s.uid(ctx, user)
	_, err := s.pool.Exec(ctx, `DELETE FROM feedback WHERE message_id=$1 AND user_id=$2`, messageID, u)
	return err
}

// Search matches thread titles and message contents with full-text search
// ('simple' config) OR substring ILIKE (pg_trgm GIN indexes make that fast and
// cover Korean, which has no stemmer). Ranked by ts_rank + trigram similarity.
func (s *Store) Search(ctx context.Context, user, q string, limit, offset int) ([]store.SearchHit, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return nil, err
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
	rows, err := s.pool.Query(ctx, `
WITH q AS (SELECT plainto_tsquery('simple', $2) AS tsq)
SELECT * FROM (
  SELECT t.id, t.title, NULL::text AS mid, NULL::text AS role, t.title AS body, t.updated_at AS ts, 'title' AS match,
         ts_rank(to_tsvector('simple', t.title), q.tsq) + similarity(t.title, $2) + 1.0 AS rank
  FROM threads t, q
  WHERE t.user_id=$1 AND NOT t.archived AND (to_tsvector('simple', t.title) @@ q.tsq OR t.title ILIKE $3)
  UNION ALL
  SELECT t.id, t.title, m.id, m.role, m.content, m.created_at, 'content',
         ts_rank(to_tsvector('simple', m.content), q.tsq) + similarity(m.content, $2)
  FROM messages m JOIN threads t ON t.id=m.thread_id, q
  WHERE m.user_id=$1 AND NOT t.archived AND (to_tsvector('simple', m.content) @@ q.tsq OR m.content ILIKE $3)
) h ORDER BY rank DESC, ts DESC, coalesce(mid, id) DESC LIMIT $4 OFFSET $5`, u, q, like, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.SearchHit{}
	for rows.Next() {
		var h store.SearchHit
		var body string
		var rank float64
		if err := rows.Scan(&h.ThreadID, &h.ThreadTitle, &h.MessageID, &h.Role, &body, &h.CreatedAt, &h.Match, &rank); err != nil {
			return nil, err
		}
		h.Snippet = store.Snippet(body, q, 160)
		h.CreatedAt = h.CreatedAt.UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) ImportThread(ctx context.Context, user string, it store.ImportThread) (string, bool, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return "", false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	var existing string
	err = tx.QueryRow(ctx, `SELECT id FROM threads WHERE user_id=$1 AND client_id=$2`, u, it.ClientID).Scan(&existing)
	if err == nil {
		return existing, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	updated := it.CreatedAt
	for _, m := range it.Messages {
		if m.CreatedAt.After(updated) {
			updated = m.CreatedAt
		}
	}
	id := store.NewID(it.CreatedAt)
	tag, err := tx.Exec(ctx, `INSERT INTO threads (id, user_id, title, created_at, updated_at, archived, client_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (user_id, client_id) DO NOTHING`, id, u, it.Title, it.CreatedAt, updated, it.Archived, it.ClientID)
	if err != nil {
		return "", false, err
	}
	if tag.RowsAffected() == 0 { // raced with a concurrent import
		tx.Rollback(ctx)
		err = s.pool.QueryRow(ctx, `SELECT id FROM threads WHERE user_id=$1 AND client_id=$2`, u, it.ClientID).Scan(&existing)
		return existing, false, err
	}
	for _, m := range it.Messages {
		if _, err := tx.Exec(ctx, `INSERT INTO messages (id, thread_id, user_id, role, content, reasoning, model, status, client_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'complete',$8,$9) ON CONFLICT (user_id, client_id) DO NOTHING`,
			store.NewID(m.CreatedAt), id, u, m.Role, m.Content, m.Reasoning, m.Model, m.ClientID, m.CreatedAt); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23514" {
				return "", false, store.ErrInvalid
			}
			return "", false, err
		}
	}
	return id, true, tx.Commit(ctx)
}

// ExportFeedback streams feedback rows (all users); see scripts/feedback-export.sql.
func (s *Store) ExportFeedback(ctx context.Context, since time.Time, fn func(store.FeedbackExport) error) error {
	rows, err := s.pool.Query(ctx, `SELECT coalesce(up.content, ''), m.content, f.rating, f.reasons, f.comment, f.created_at, m.thread_id, m.id
		FROM feedback f
		JOIN messages m ON m.id = f.message_id
		LEFT JOIN LATERAL (
			SELECT u.content FROM messages u
			WHERE u.thread_id = m.thread_id AND u.role = 'user' AND (u.created_at, u.id) < (m.created_at, m.id)
			ORDER BY u.created_at DESC, u.id DESC LIMIT 1) up ON true
		WHERE f.updated_at >= $1
		ORDER BY f.updated_at, f.message_id`, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e store.FeedbackExport
		if err := rows.Scan(&e.Message, &e.Response, &e.Rating, &e.Reasons, &e.Comment, &e.CreatedAt, &e.ThreadID, &e.MessageID); err != nil {
			return err
		}
		e.Reasons, e.CreatedAt = nonNil(e.Reasons), e.CreatedAt.UTC()
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) MarkGenerating(ctx context.Context, user, id string) error {
	u, err := s.uid(ctx, user)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE messages SET status='generating', updated_at=now()
		WHERE id=$1 AND user_id=$2 AND role='assistant' AND status<>'generating'`, id, u)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE id=$1 AND user_id=$2 AND role='assistant')`, id, u).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return store.ErrGenerating
	}
	return store.ErrNotFound
}

func (s *Store) FailStaleGenerating(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE messages SET status='failed',
		metadata = COALESCE(metadata, '{}'::jsonb) || '{"incomplete":true,"reason":"gateway_restart","error_code":"gateway_restart"}'::jsonb,
		updated_at=now() WHERE status='generating'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
