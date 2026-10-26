package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/jackc/pgx/v5"
)

const actionCols = `id, root_id, thread_id, message_id, tool, tool_version, scopes, summary, payload, payload_view, payload_hash,
	actor, job_id, job_name, tainted, status, issue_count, token_sha256, token_expires_at, expires_at, created_at`

func scanAction(r pgx.Row) (store.Action, error) {
	var a store.Action
	var payload, view []byte
	err := r.Scan(&a.ID, &a.RootID, &a.ThreadID, &a.MessageID, &a.Tool, &a.ToolVersion, &a.Scopes, &a.Summary, &payload, &view, &a.PayloadHash,
		&a.Provenance.Actor, &a.Provenance.JobID, &a.Provenance.JobName, &a.Tainted, &a.Status, &a.IssueCount, &a.TokenSHA256, &a.TokenExpiresAt,
		&a.ExpiresAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, store.ErrNotFound
	}
	a.Payload, a.PayloadView = payload, view
	a.ExpiresAt, a.CreatedAt = a.ExpiresAt.UTC(), a.CreatedAt.UTC()
	if a.TokenExpiresAt != nil {
		t := a.TokenExpiresAt.UTC()
		a.TokenExpiresAt = &t
	}
	return a, err
}

func (s *Store) CreateAction(ctx context.Context, user string, a store.Action) (store.Action, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return a, err
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.ID == "" {
		a.ID = store.NewID(a.CreatedAt)
	}
	a.RootID = a.ID
	if a.ExpiresAt.IsZero() || a.ExpiresAt.After(a.CreatedAt.Add(store.ProposalMaxAge)) {
		a.ExpiresAt = a.CreatedAt.Add(store.ProposalMaxAge)
	}
	if len(a.PayloadView) == 0 {
		a.PayloadView = json.RawMessage(`{}`)
	}
	a.PayloadHash = store.PayloadHash(a.Payload)
	a.Status = "pending"
	if a.Scopes == nil {
		a.Scopes = []string{}
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO pending_actions (id, root_id, user_id, thread_id, message_id, tool, tool_version, scopes, summary, payload, payload_view,
		payload_hash, actor, job_id, job_name, tainted, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		a.ID, a.RootID, u, a.ThreadID, a.MessageID, a.Tool, a.ToolVersion, a.Scopes, a.Summary, []byte(a.Payload), []byte(a.PayloadView),
		a.PayloadHash, a.Provenance.Actor, a.Provenance.JobID, a.Provenance.JobName, a.Tainted, a.ExpiresAt, a.CreatedAt)
	return a, err
}

func (s *Store) ListPendingActions(ctx context.Context, user string) ([]store.Action, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+actionCols+` FROM pending_actions WHERE user_id=$1 AND status='pending' ORDER BY created_at, id`, u)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Action{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// lockOwned starts the shared prologue of reissue/revise: lock the user row
// (cap counting is serialized per user), then load the proposal through the
// single owner-scoped query path so missing and foreign ids are identical.
func lockOwned(ctx context.Context, tx pgx.Tx, u int64, id string) (store.Action, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id=$1 FOR UPDATE`, u); err != nil {
		return store.Action{}, err
	}
	return scanAction(tx.QueryRow(ctx, `SELECT `+actionCols+` FROM pending_actions WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, u))
}

func chainIssued(ctx context.Context, tx pgx.Tx, u int64, root string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT COALESCE(sum(issue_count),0) FROM pending_actions WHERE root_id=$1 AND user_id=$2`, root, u).Scan(&n)
	return n, err
}

func rootCreated(ctx context.Context, tx pgx.Tx, u int64, a store.Action) (time.Time, error) {
	if a.RootID == a.ID {
		return a.CreatedAt, nil
	}
	var t time.Time
	err := tx.QueryRow(ctx, `SELECT created_at FROM pending_actions WHERE id=$1 AND user_id=$2`, a.RootID, u).Scan(&t)
	return t.UTC(), err
}

func (s *Store) ReissueAction(ctx context.Context, user, id string, now time.Time) (store.Action, string, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return store.Action{}, "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.Action{}, "", err
	}
	defer tx.Rollback(ctx)
	a, err := lockOwned(ctx, tx, u, id)
	if err != nil {
		return a, "", err
	}
	if a.Status != "pending" {
		return a, "", store.ErrConflict
	}
	if !now.Before(a.ExpiresAt) {
		return a, "", store.ErrExpired
	}
	n, err := chainIssued(ctx, tx, u, a.RootID)
	if err != nil {
		return a, "", err
	}
	if n >= store.ReissueCap {
		return a, "", store.ErrCapExceeded
	}
	tok, h := store.NewToken()
	texp := store.TokenExpiry(now, a.ExpiresAt)
	// Counter increment and replacing (= invalidating) the previous token
	// are one conditional single-row UPDATE.
	tag, err := tx.Exec(ctx, `UPDATE pending_actions SET issue_count=issue_count+1, token_sha256=$3, token_expires_at=$4, updated_at=now()
		WHERE id=$1 AND user_id=$2 AND status='pending'`, id, u, h, texp)
	if err != nil {
		return a, "", err
	}
	if tag.RowsAffected() != 1 {
		return a, "", store.ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return a, "", err
	}
	a.IssueCount++
	a.TokenSHA256, a.TokenExpiresAt = &h, &texp
	return a, tok, nil
}

func (s *Store) ReviseAction(ctx context.Context, user, id string, itemIDs []string, now time.Time) (store.Action, string, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return store.Action{}, "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.Action{}, "", err
	}
	defer tx.Rollback(ctx)
	orig, err := lockOwned(ctx, tx, u, id)
	if err != nil {
		return orig, "", err
	}
	rc, err := rootCreated(ctx, tx, u, orig)
	if err != nil {
		return orig, "", err
	}
	if !now.Before(rc.Add(store.ProposalMaxAge)) {
		return orig, "", store.ErrExpired
	}
	if orig.Status != "pending" {
		return orig, "", store.ErrConflict
	}
	payload, err := store.FilterItems(orig.Payload, itemIDs)
	if err != nil {
		return orig, "", err
	}
	view, err := store.FilterItems(orig.PayloadView, itemIDs)
	if err != nil {
		return orig, "", err
	}
	n, err := chainIssued(ctx, tx, u, orig.RootID)
	if err != nil {
		return orig, "", err
	}
	if n >= store.ReissueCap {
		return orig, "", store.ErrCapExceeded
	}
	tag, err := tx.Exec(ctx, `UPDATE pending_actions SET status='voided', token_sha256=NULL, token_expires_at=NULL, updated_at=now()
		WHERE id=$1 AND user_id=$2 AND status='pending'`, id, u)
	if err != nil {
		return orig, "", err
	}
	if tag.RowsAffected() != 1 {
		return orig, "", store.ErrConflict
	}
	na := orig
	na.ID = store.NewID(now)
	na.Payload, na.PayloadView = payload, view
	na.PayloadHash = store.PayloadHash(payload)
	na.Status = "pending"
	na.CreatedAt = now.UTC()
	na.ExpiresAt = store.ReviseExpiry(now, rc, orig).UTC()
	tok, h := store.NewToken()
	texp := store.TokenExpiry(now, na.ExpiresAt).UTC()
	na.IssueCount, na.TokenSHA256, na.TokenExpiresAt = 1, &h, &texp
	// Inherits root_id, actor, job, taint and scopes; none come from the request.
	_, err = tx.Exec(ctx, `INSERT INTO pending_actions (id, root_id, user_id, thread_id, message_id, tool, tool_version, scopes, summary, payload, payload_view,
		payload_hash, actor, job_id, job_name, tainted, issue_count, token_sha256, token_expires_at, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,1,$17,$18,$19,$20)`,
		na.ID, na.RootID, u, na.ThreadID, na.MessageID, na.Tool, na.ToolVersion, na.Scopes, na.Summary, []byte(payload), []byte(view),
		na.PayloadHash, na.Provenance.Actor, na.Provenance.JobID, na.Provenance.JobName, na.Tainted, h, texp, na.ExpiresAt, na.CreatedAt)
	if err != nil {
		return orig, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return orig, "", err
	}
	return na, tok, nil
}

func (s *Store) AddPushSubscription(ctx context.Context, user string, sub store.PushSubscription) (store.PushAddResult, error) {
	u, err := s.uid(ctx, user)
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id=$1 FOR UPDATE`, u); err != nil {
		return 0, err
	}
	// Cap first, before looking at who owns the endpoint (§3.5.3 decision 1).
	// Re-registering one of my own endpoints is an update, not a new row.
	var n int
	var mine bool
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(bool_or(endpoint=$2), false) FROM push_subscriptions WHERE user_id=$1`, u, sub.Endpoint).Scan(&n, &mine); err != nil {
		return 0, err
	}
	if !mine && n >= store.PushSubscriptionCap {
		return 0, store.ErrCapExceeded
	}
	tag, err := tx.Exec(ctx, `INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth, vapid_key_id) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (endpoint) DO UPDATE SET p256dh=EXCLUDED.p256dh, auth=EXCLUDED.auth, vapid_key_id=EXCLUDED.vapid_key_id, disabled_at=NULL
		WHERE push_subscriptions.user_id = EXCLUDED.user_id`,
		store.NewID(time.Now()), u, sub.Endpoint, sub.P256dh, sub.Auth, sub.VAPIDKeyID)
	if err != nil {
		return 0, err
	}
	res := store.PushAdded
	if tag.RowsAffected() == 0 { // conflict with another user's row: kept as is
		res = store.PushOwnedByOther
	}
	return res, tx.Commit(ctx)
}

func (s *Store) DeletePushSubscription(ctx context.Context, user, endpoint string) error {
	u, err := s.uid(ctx, user)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint=$1 AND user_id=$2`, endpoint, u)
	return err
}
