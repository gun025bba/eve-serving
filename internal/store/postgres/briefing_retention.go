package postgres

import (
	"context"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

// briefingTemplateID is scheduled_jobs.template_id of the morning briefing
// (internal/briefing.TemplateID; not imported to keep the store layer free of
// feature packages).
const briefingTemplateID = "morning_briefing"

// PurgeBriefings: see store.BriefingPurger. One DELETE; the per-user cutoff
// comes from users.settings->briefing->retention_days (a 1-3 digit integer,
// anything else counts as unset). Only messages of the same user's
// morning_briefing job with origin 'proactive' match.
func (s *Store) PurgeBriefings(ctx context.Context, now time.Time, defaultDays int) ([]store.BriefingPurge, error) {
	rows, err := s.pool.Query(ctx, `
WITH eff AS (
	SELECT id, login,
		CASE WHEN settings->'briefing'->>'retention_days' ~ '^[0-9]{1,3}$'
			THEN (settings->'briefing'->>'retention_days')::int ELSE $3::int END AS days
	FROM users
), del AS (
	DELETE FROM messages m
	USING scheduled_jobs j, eff
	WHERE m.job_id = j.id AND j.user_id = m.user_id AND j.template_id = $1
		AND m.origin = 'proactive' AND eff.id = m.user_id
		AND eff.days > 0 AND m.created_at < $2::timestamptz - make_interval(days => eff.days)
	RETURNING m.user_id
)
SELECT eff.login, count(*), eff.days FROM del JOIN eff ON eff.id = del.user_id GROUP BY eff.login, eff.days ORDER BY eff.login`,
		briefingTemplateID, now, defaultDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.BriefingPurge
	for rows.Next() {
		var p store.BriefingPurge
		if err := rows.Scan(&p.Login, &p.Deleted, &p.Days); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
