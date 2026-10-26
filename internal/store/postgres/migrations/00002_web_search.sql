-- Web search history (docs/web-search.md §2.9). Raw tool results / page text
-- are never stored. Rows are deleted after the retention period (default 90
-- days) by the gateway's daily purge job; deleting a message or thread
-- cascades.

-- +goose Up
CREATE TABLE tool_calls (
    id           TEXT PRIMARY KEY,               -- ULID
    message_id   TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE, -- assistant message
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    round        SMALLINT NOT NULL,
    seq          SMALLINT NOT NULL,
    tool         TEXT NOT NULL CHECK (tool IN ('web_search','web_fetch')),
    provider     TEXT,
    args         JSONB NOT NULL DEFAULT '{}'::jsonb, -- {query} | {url}; purged after retention
    status       TEXT NOT NULL CHECK (status IN ('ok','error','blocked','timeout')),
    error        TEXT,
    latency_ms   INTEGER,
    result_chars INTEGER,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (message_id, round, seq)
);
CREATE INDEX tool_calls_created_idx ON tool_calls (created_at);

CREATE TABLE web_sources (
    message_id     TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tool_call_id   TEXT REFERENCES tool_calls(id) ON DELETE SET NULL,
    n              SMALLINT NOT NULL,           -- citation number
    url            TEXT NOT NULL,
    title          TEXT,
    domain         TEXT,
    snippet        TEXT,
    fetched        BOOLEAN NOT NULL DEFAULT false,
    content_sha256 TEXT,                         -- hash only, never the page text
    cited          BOOLEAN NOT NULL DEFAULT false,
    final_url      TEXT,                         -- set when web_fetch was redirected (same-site or allowlisted)
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, n)
);
CREATE INDEX web_sources_domain_idx ON web_sources (domain);
CREATE INDEX web_sources_created_idx ON web_sources (created_at);

ALTER TABLE threads ADD COLUMN settings JSONB NOT NULL DEFAULT '{}'::jsonb; -- {"web_search":"auto"}

-- +goose Down
ALTER TABLE threads DROP COLUMN settings;
DROP TABLE web_sources;
DROP TABLE tool_calls;
