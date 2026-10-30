package store

import (
	"context"
	"time"
)

// BriefingPurge is the result of one retention pass for one user: how many
// briefing messages were deleted and the retention (days) that applied. Only
// counts, never content.
type BriefingPurge struct {
	Login   string
	Deleted int64
	Days    int
}

// BriefingPurger deletes briefing messages older than their owner's
// briefing.retention_days (#73). A briefing message is a proactive message of
// a morning_briefing job; nothing else is touched (other templates, user
// turns, other threads, memory). defaultDays applies to users who never set
// briefing.retention_days; 0 (default or per-user) switches the purge off for
// that user. Users with nothing deleted are not returned.
type BriefingPurger interface {
	PurgeBriefings(ctx context.Context, now time.Time, defaultDays int) ([]BriefingPurge, error)
}
