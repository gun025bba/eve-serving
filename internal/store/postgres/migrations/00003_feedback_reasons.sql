-- Feedback reasons (docs/threads-api.md "Feedback"). Backward compatible:
-- existing rows get reasons='{}', old binaries ignore the column.
-- +goose Up
ALTER TABLE feedback ADD COLUMN reasons TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE feedback ADD CONSTRAINT feedback_reasons_valid CHECK (
    (rating = 'down' AND reasons <@ ARRAY['wrong_info','too_long','misunderstood','over_refusal','bad_source','other']::text[])
 OR (rating = 'up'   AND reasons <@ ARRAY['accurate','concise','helpful','good_source']::text[])
);
ALTER TABLE feedback ADD CONSTRAINT feedback_comment_len CHECK (comment IS NULL OR char_length(comment) <= 140);
CREATE INDEX feedback_updated_at_idx ON feedback (updated_at);

-- +goose Down
DROP INDEX feedback_updated_at_idx;
ALTER TABLE feedback DROP CONSTRAINT feedback_comment_len;
ALTER TABLE feedback DROP CONSTRAINT feedback_reasons_valid;
ALTER TABLE feedback DROP COLUMN reasons;
