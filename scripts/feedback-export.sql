-- Feedback export for model evaluation; same rows as
-- GET /v1/feedback/export?format=jsonl. Usage:
--   psql "$GW_DATABASE_URL" -v since='2026-01-01T00:00:00Z' -At -f scripts/feedback-export.sql > feedback.jsonl
-- One JSON object per line: message, response, rating, reasons, comment,
-- created_at, thread_id, message_id. "message" is the latest user message
-- in the same thread created before the rated assistant message.
SELECT json_build_object(
         'message',    up.content,
         'response',   m.content,
         'rating',     f.rating,
         'reasons',    f.reasons,
         'comment',    f.comment,
         'created_at', f.created_at,
         'thread_id',  m.thread_id,
         'message_id', m.id)
FROM feedback f
JOIN messages m ON m.id = f.message_id
LEFT JOIN LATERAL (
    SELECT u.content FROM messages u
    WHERE u.thread_id = m.thread_id AND u.role = 'user' AND (u.created_at, u.id) < (m.created_at, m.id)
    ORDER BY u.created_at DESC, u.id DESC LIMIT 1) up ON true
WHERE f.updated_at >= :'since'::timestamptz
ORDER BY f.updated_at, f.message_id;
