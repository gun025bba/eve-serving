package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

// PurgeBriefings: see store.BriefingPurger. memstore keeps no jobs, so a
// briefing message is a proactive message with template_kind "briefing".
func (s *Store) PurgeBriefings(_ context.Context, now time.Time, defaultDays int) ([]store.BriefingPurge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	days := func(login string) int {
		b, _ := s.users[login]["briefing"].(map[string]any)
		switch v := b["retention_days"].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
		return defaultDays
	}
	counts := map[string]*store.BriefingPurge{}
	for id, m := range s.msgs {
		if m.Origin != store.OriginProactive || m.TemplateKind == nil || *m.TemplateKind != store.BriefingKind {
			continue
		}
		d := days(m.user)
		if d <= 0 || !m.CreatedAt.Before(now.Add(-time.Duration(d)*24*time.Hour)) {
			continue
		}
		p := counts[m.user]
		if p == nil {
			p = &store.BriefingPurge{Login: m.user, Days: d}
			counts[m.user] = p
		}
		p.Deleted++
		delete(s.msgs, id)
	}
	var out []store.BriefingPurge
	for _, p := range counts {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Login < out[j].Login })
	return out, nil
}
