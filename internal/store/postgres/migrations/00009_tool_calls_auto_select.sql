-- Automatic tool selection (docs/architecture/auto-tool-selection.md §4.7,
-- §10.1). ADDITIVE ONLY, and it must be applied before the 6.0.0 code runs
-- (the gateway migrates at startup, before it serves; with
-- GW_DB_AUTO_MIGRATE=false run `gateway -migrate` first). 5.x binaries keep
-- working on the migrated schema: every new column is nullable and the
-- widened status CHECK only adds a value.
--
--   selected_by      who chose the call: always 'model' in 6.0.0
--   error_code       public error code of a failed/denied call (errcode.go)
--   result_count     number of results of a successful web_search
--   args_expires_at  set on new rows only; the daily job clears `args`
--                    (sets it to '{}') once this time has passed
--
-- tool_calls.status gains 'denied' (stored as 'denied', no longer folded into
-- 'blocked'). The tool CHECK is not touched.

-- +goose Up
ALTER TABLE tool_calls
    ADD COLUMN selected_by     TEXT,
    ADD COLUMN error_code      TEXT,
    ADD COLUMN result_count    INTEGER,
    ADD COLUMN args_expires_at TIMESTAMPTZ;
ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_status_check;
ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_status_check
    CHECK (status IN ('ok','error','blocked','timeout','denied'));
CREATE INDEX tool_calls_args_expires_idx ON tool_calls (args_expires_at) WHERE args_expires_at IS NOT NULL;

-- +goose Down
-- Development only. No row is deleted: 'denied' rows are folded back into
-- 'blocked' (the 5.x vocabulary) before the old CHECK returns.
DROP INDEX tool_calls_args_expires_idx;
UPDATE tool_calls SET status = 'blocked' WHERE status = 'denied';
ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_status_check;
ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_status_check
    CHECK (status IN ('ok','error','blocked','timeout'));
ALTER TABLE tool_calls
    DROP COLUMN args_expires_at,
    DROP COLUMN result_count,
    DROP COLUMN error_code,
    DROP COLUMN selected_by;
