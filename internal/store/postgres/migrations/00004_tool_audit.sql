-- Tool audit ledger (docs/tool-platform.md §5, M2). Additive only.
--
-- Must be run by the dedicated migration login (CREATEROLE, owner of the
-- database / public schema), never by the gateway login. The gateway login
-- is granted eve_app separately (see docs/tool-platform.md §5 M2 runbook) and
-- must NOT be a member of eve_audit_owner; the gateway refuses to start
-- otherwise.
--
-- tool_calls is unchanged in M2 (its CHECK constraints stay until M3).

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eve_audit_owner') THEN
        CREATE ROLE eve_audit_owner NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eve_app') THEN
        CREATE ROLE eve_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'eve_retention') THEN
        CREATE ROLE eve_retention NOLOGIN;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname IN ('eve_audit_owner', 'eve_app', 'eve_retention')
               AND (rolcanlogin OR rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls)) THEN
        RAISE EXCEPTION 'eve_audit_owner/eve_app/eve_retention must be plain NOLOGIN roles';
    END IF;
    IF pg_has_role('eve_app', 'eve_audit_owner', 'MEMBER') THEN
        RAISE EXCEPTION 'eve_app must not be a member of eve_audit_owner';
    END IF;
END
$$;
-- +goose StatementEnd

-- The migration login needs SET ROLE eve_audit_owner to create the table as
-- that owner (PostgreSQL 16+: the creating CREATEROLE login holds ADMIN).
GRANT eve_audit_owner TO CURRENT_USER;
GRANT CREATE ON SCHEMA public TO eve_audit_owner;
SET LOCAL ROLE eve_audit_owner;

CREATE TABLE tool_audit (
    seq               BIGSERIAL PRIMARY KEY,
    at                TIMESTAMPTZ NOT NULL,          -- set by the gateway (covered by the hash)
    user_id           BIGINT NOT NULL,               -- no FK: survives message/thread/user deletion
    thread_id         TEXT,
    message_id        TEXT,
    tool_call_id      TEXT,
    pending_action_id TEXT,
    actor             TEXT NOT NULL CHECK (actor IN ('user', 'scheduler', 'system')),  -- no default, server-set only
    job_id            TEXT,
    event             TEXT NOT NULL CHECK (event <> ''),
    tool              TEXT,
    tool_version      TEXT,
    scope             TEXT,
    payload_sha256    TEXT,
    confirm_token_id  TEXT,
    tainted           BOOLEAN,
    status            TEXT,
    detail            JSONB,                          -- hashes / rule ids / counters only, never bodies
    prev_hash         TEXT NOT NULL CHECK (prev_hash ~ '^[0-9a-f]{64}$'),
    hash              TEXT NOT NULL CHECK (hash ~ '^[0-9a-f]{64}$'),
    CHECK (actor <> 'scheduler' OR job_id IS NOT NULL)
);
CREATE UNIQUE INDEX tool_audit_prev_hash_uq ON tool_audit (prev_hash);  -- a fork fails instead of branching
CREATE INDEX tool_audit_at_idx ON tool_audit (at);

-- Append-only: UPDATE/TRUNCATE always fail; DELETE only for eve_retention
-- (oldest rows, after writing a checkpoint). Applies to the owner too.
-- +goose StatementBegin
CREATE FUNCTION tool_audit_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_user = 'eve_retention' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'tool_audit is append-only (% by %)', TG_OP, current_user;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER tool_audit_no_update_delete BEFORE UPDATE OR DELETE ON tool_audit
    FOR EACH ROW EXECUTE FUNCTION tool_audit_append_only();
CREATE TRIGGER tool_audit_no_truncate BEFORE TRUNCATE ON tool_audit
    FOR EACH STATEMENT EXECUTE FUNCTION tool_audit_append_only();

REVOKE ALL ON tool_audit FROM PUBLIC;
REVOKE ALL ON SEQUENCE tool_audit_seq_seq FROM PUBLIC;
REVOKE ALL ON FUNCTION tool_audit_append_only() FROM PUBLIC;
GRANT INSERT, SELECT ON tool_audit TO eve_app;
GRANT USAGE ON SEQUENCE tool_audit_seq_seq TO eve_app;
GRANT SELECT, DELETE ON tool_audit TO eve_retention;

RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM eve_audit_owner;

-- +goose Down
-- Intentionally empty: the audit ledger, its roles and grants are never
-- dropped by a migration (docs/tool-platform.md §5). The 1.2.0 binary ignores
-- the table, so rolling back the gateway needs no schema change.
SELECT 1;
