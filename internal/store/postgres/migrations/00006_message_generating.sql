-- Detached generation (ui-api-contract.md §3.8): the assistant row is created
-- when generation starts (status 'generating') and finishes as 'complete' or
-- 'failed'. Legacy 'incomplete' rows stay valid (API reports them as failed).
-- Additive: widens the CHECK only.

-- +goose Up
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_status_check;
ALTER TABLE messages ADD CONSTRAINT messages_status_check
    CHECK (status IN ('complete','incomplete','generating','failed'));
CREATE INDEX IF NOT EXISTS messages_generating_idx ON messages (id) WHERE status = 'generating';

-- +goose Down
DROP INDEX IF EXISTS messages_generating_idx;
UPDATE messages SET status = 'incomplete' WHERE status IN ('generating','failed');
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_status_check;
ALTER TABLE messages ADD CONSTRAINT messages_status_check
    CHECK (status IN ('complete','incomplete'));
