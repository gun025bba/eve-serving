-- Per-user settings (ui-api-contract.md §2, §3.9.7). One JSONB object with
-- namespaced keys (e.g. {"memory":{"enabled":true}}). A missing key reads as
-- its default (off). Additive; Down drops the column (settings are lost).

-- +goose Up
ALTER TABLE users ADD COLUMN IF NOT EXISTS settings JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS settings;
