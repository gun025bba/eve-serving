-- +goose Up
-- Extensions: pg_trgm (Korean/substring search) is a trusted extension.
-- vector (pgvector) is reserved for future RAG; deploy/pg-setup.sh creates it
-- as superuser. IF NOT EXISTS makes both no-ops when already present.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE users (
    id         BIGSERIAL PRIMARY KEY,
    login      TEXT NOT NULL UNIQUE,
    provider   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE threads (
    id         TEXT PRIMARY KEY,               -- ULID
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived   BOOLEAN NOT NULL DEFAULT false,
    client_id  TEXT,
    UNIQUE (user_id, client_id)
);
CREATE INDEX threads_list_idx ON threads (user_id, archived, updated_at DESC, id DESC);
CREATE INDEX threads_title_fts ON threads USING GIN (to_tsvector('simple', title));
CREATE INDEX threads_title_trgm ON threads USING GIN (title gin_trgm_ops);

CREATE TABLE messages (
    id         TEXT PRIMARY KEY,               -- ULID
    thread_id  TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('system','user','assistant','tool')),
    content    TEXT NOT NULL DEFAULT '',
    reasoning  TEXT,
    model      TEXT,
    params     JSONB,
    usage      JSONB,                          -- {prompt_tokens, completion_tokens}
    latency_ms BIGINT,
    metadata   JSONB,
    status     TEXT NOT NULL DEFAULT 'complete' CHECK (status IN ('complete','incomplete')),
    client_id  TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ,
    UNIQUE (user_id, client_id)
);
CREATE INDEX messages_thread_idx ON messages (thread_id, created_at, id);
CREATE INDEX messages_user_idx ON messages (user_id, created_at DESC);
CREATE INDEX messages_content_fts ON messages USING GIN (to_tsvector('simple', content));
CREATE INDEX messages_content_trgm ON messages USING GIN (content gin_trgm_ops);

CREATE TABLE feedback (
    message_id TEXT PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rating     TEXT NOT NULL CHECK (rating IN ('up','down')),
    comment    TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE feedback;
DROP TABLE messages;
DROP TABLE threads;
DROP TABLE users;
