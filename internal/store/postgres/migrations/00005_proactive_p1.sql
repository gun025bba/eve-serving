-- Proactive P1 API surface (docs/architecture/ui-api-contract.md §3.5–§3.7).
-- Additive only: new columns are nullable or defaulted, new tables are empty.
-- The scheduler itself (scheduled_jobs, dedupe keys, push_status) lands in a
-- later migration; job_id is plain TEXT until scheduled_jobs exists.

-- +goose Up
ALTER TABLE messages
    ADD COLUMN origin        TEXT NOT NULL DEFAULT 'user_turn' CHECK (origin IN ('user_turn','proactive')),
    ADD COLUMN job_id        TEXT NULL,
    ADD COLUMN job_name      TEXT NULL,
    ADD COLUMN template_kind TEXT NULL,
    ADD COLUMN read_at       TIMESTAMPTZ NULL;
ALTER TABLE messages ADD CONSTRAINT messages_proactive_job CHECK (origin <> 'proactive' OR job_id IS NOT NULL);
CREATE INDEX messages_unread_idx ON messages (thread_id) WHERE origin = 'proactive' AND read_at IS NULL;

-- Confirmation proposals (docs/tool-platform.md §3.2, §10.3). Tokens are never
-- stored in clear: only token_sha256. root_id is server-set only.
CREATE TABLE pending_actions (
    id               TEXT PRIMARY KEY,                -- ULID
    root_id          TEXT NOT NULL,                   -- first proposal of a revise chain
    user_id          BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    thread_id        TEXT NULL REFERENCES threads(id) ON DELETE CASCADE,
    message_id       TEXT NULL REFERENCES messages(id) ON DELETE SET NULL,
    tool             TEXT NOT NULL,
    tool_version     TEXT NOT NULL DEFAULT '',
    scopes           TEXT[] NOT NULL DEFAULT '{}',
    summary          TEXT NOT NULL DEFAULT '',
    payload          JSONB NOT NULL,
    payload_view     JSONB NOT NULL,
    payload_hash     TEXT NOT NULL,
    actor            TEXT NOT NULL CHECK (actor IN ('user','scheduler')),
    job_id           TEXT NULL,
    job_name         TEXT NULL,
    tainted          BOOLEAN NOT NULL DEFAULT false,
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','voided','expired')),
    issue_count      INT NOT NULL DEFAULT 0 CHECK (issue_count >= 0),
    token_sha256     TEXT NULL,
    token_expires_at TIMESTAMPTZ NULL,
    expires_at       TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (actor <> 'scheduler' OR job_id IS NOT NULL)
);
CREATE INDEX pending_actions_user_status_idx ON pending_actions (user_id, status, created_at);
CREATE INDEX pending_actions_root_idx ON pending_actions (root_id);

CREATE TABLE push_subscriptions (
    id           TEXT PRIMARY KEY,                    -- ULID
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    endpoint     TEXT NOT NULL UNIQUE CHECK (octet_length(endpoint) <= 1024),
    p256dh       TEXT NOT NULL,
    auth         TEXT NOT NULL,
    vapid_key_id TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_ok_at   TIMESTAMPTZ NULL,
    disabled_at  TIMESTAMPTZ NULL
);
CREATE INDEX push_subscriptions_user_idx ON push_subscriptions (user_id);

-- +goose Down
-- Intentionally empty: rollback is the feature flag, not dropping data
-- (docs/architecture/proactive-messaging.md §3).
SELECT 1;
