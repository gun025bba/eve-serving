package handlers

import (
	"context"
	"strconv"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/briefing"
	"github.com/gun025bba/eve-serving/internal/store"
)

// PurgeBriefings deletes briefing messages older than each user's
// briefing.retention_days (default 30, 0 = off; #73) and writes one tool_audit
// row per affected user with the deleted count only. It returns the total
// deleted. It runs whether or not briefings are currently enabled: messages
// written while they were on must still age out.
func (s *Server) PurgeBriefings(ctx context.Context, now time.Time) (int64, error) {
	bp, ok := s.Store.(store.BriefingPurger)
	if !ok {
		return 0, nil
	}
	res, err := bp.PurgeBriefings(ctx, now, briefing.Defaults().RetentionDays)
	var total int64
	for _, p := range res {
		total += p.Deleted
		s.appendAudit(ctx, p.Login, audit.Event{At: now, Actor: audit.ActorSystem, Event: audit.EvBriefingRetention, Status: "ok",
			Detail: map[string]string{"deleted": strconv.FormatInt(p.Deleted, 10), "retention_days": strconv.Itoa(p.Days)}})
	}
	return total, err
}

// RunBriefingRetention purges now and then every interval (daily in main)
// until ctx is done.
func (s *Server) RunBriefingRetention(ctx context.Context, interval time.Duration) {
	run := func() {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		n, err := s.PurgeBriefings(c, time.Now())
		if err != nil {
			s.Log.Error("briefing_retention_failed", "error", logTrunc(err.Error(), 200))
			return
		}
		if n > 0 {
			s.Log.Info("briefing_retention", "deleted_rows", n) // a count only, never content
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
