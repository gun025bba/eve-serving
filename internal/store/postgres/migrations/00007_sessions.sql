-- +goose Up
-- 4.0.0 browser identity (docs/architecture/identity-transport.md): pairing
-- codes and session cookies bound to the existing users.id. Only
-- HMAC-SHA256(pepper, value) is stored, never a code or token. No existing
-- row is changed or deleted.
CREATE TABLE sessions (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE pairing_codes (
    code_hash  TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
-- Drops only the 4.0.0 tables: every browser must pair again after re-upgrade.
DROP TABLE pairing_codes;
DROP TABLE sessions;
