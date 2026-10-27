package postgres

import (
	"context"
	"encoding/json"
)

func (s *Store) UserSettings(ctx context.Context, login string) (json.RawMessage, error) {
	uid, err := s.uid(ctx, login)
	if err != nil {
		return nil, err
	}
	var b []byte
	err = s.pool.QueryRow(ctx, `SELECT settings FROM users WHERE id = $1`, uid).Scan(&b)
	return b, err
}

func (s *Store) MergeUserSettings(ctx context.Context, login, ns string, patch json.RawMessage) (json.RawMessage, error) {
	uid, err := s.uid(ctx, login)
	if err != nil {
		return nil, err
	}
	var b []byte
	err = s.pool.QueryRow(ctx, `UPDATE users
		SET settings = settings || jsonb_build_object($2::text,
			(CASE WHEN jsonb_typeof(settings->$2) = 'object' THEN settings->$2 ELSE '{}'::jsonb END) || $3::jsonb)
		WHERE id = $1 RETURNING settings`, uid, ns, string(patch)).Scan(&b)
	return b, err
}
