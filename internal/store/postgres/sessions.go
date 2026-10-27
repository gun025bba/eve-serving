package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/gun025bba/eve-serving/internal/session"
	"github.com/jackc/pgx/v5"
)

var _ session.Store = (*Store)(nil)

func (s *Store) UserIDByLogin(ctx context.Context, login string) (int64, error) {
	return s.uid(ctx, login)
}

func (s *Store) PutPairCode(ctx context.Context, uid int64, h string, exp time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM pairing_codes WHERE expires_at <= now()`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO pairing_codes (code_hash, user_id, expires_at) VALUES ($1, $2, $3)`, h, uid, exp)
	return err
}

func (s *Store) TakePairCode(ctx context.Context, h string, now time.Time) (int64, bool, error) {
	var uid int64
	var exp time.Time
	err := s.pool.QueryRow(ctx, `DELETE FROM pairing_codes WHERE code_hash = $1 RETURNING user_id, expires_at`, h).Scan(&uid, &exp)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !now.Before(exp) {
		return 0, false, nil
	}
	return uid, true, nil
}

func (s *Store) DeleteAllPairCodes(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM pairing_codes`)
	return err
}

func (s *Store) CreateSession(ctx context.Context, uid int64, h string, now time.Time) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO sessions (user_id, token_hash, created_at, last_seen) VALUES ($1, $2, $3, $3) RETURNING id`, uid, h, now).Scan(&id)
	return id, err
}

const sessionCols = `s.id, s.user_id, u.login, s.created_at, s.last_seen, s.revoked_at`

func scanSession(r pgx.Row) (session.Session, error) {
	var x session.Session
	err := r.Scan(&x.ID, &x.UserID, &x.Login, &x.CreatedAt, &x.LastSeen, &x.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, session.ErrNotFound
	}
	return x, err
}

func (s *Store) SessionByHash(ctx context.Context, h string) (session.Session, error) {
	return scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = $1`, h))
}

func (s *Store) TouchSession(ctx context.Context, id int64, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen = $2 WHERE id = $1 AND last_seen < $2`, id, now)
	return err
}

func (s *Store) RevokeSession(ctx context.Context, id int64, now time.Time) error {
	t, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, now)
	if err == nil && t.RowsAffected() == 0 {
		return session.ErrNotFound
	}
	return err
}

func (s *Store) ListSessions(ctx context.Context) ([]session.Session, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sessionCols+` FROM sessions s JOIN users u ON u.id = s.user_id ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []session.Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
