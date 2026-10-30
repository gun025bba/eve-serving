package memstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/store"
)

// I-2: the text of a finished pim write card is blanked after the cutoff; a
// live pending card, a young card and other tools' cards are never touched.
func TestScrubActionContent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := New()
	mk := func(tool string, created time.Time, status string, expires time.Time) string {
		a, err := s.CreateAction(ctx, "alice@example", store.Action{
			ServerFields: store.ServerFields{Tool: tool, CreatedAt: created, ExpiresAt: expires, Provenance: store.Provenance{Actor: "user"}},
			ModelFields:  store.ModelFields{Summary: "s", PayloadView: json.RawMessage(`{"title":"치과"}`)},
			Payload:      json.RawMessage(`{"title":"치과","notes":"보험증"}`)})
		if err != nil {
			t.Fatal(err)
		}
		s.actions[a.ID].Status = status
		return a.ID
	}
	old := now.Add(-48 * time.Hour)
	cutoff := now.Add(-24 * time.Hour)
	tools := []string{"calendar_add_event", "reminders_add"}
	expiredPending := mk("calendar_add_event", old, "pending", old.Add(time.Hour))
	approved := mk("calendar_add_event", old, "approved", old.Add(time.Hour))
	voided := mk("reminders_add", old, "voided", old.Add(time.Hour))
	livePending := mk("calendar_add_event", old, "pending", now.Add(time.Hour)) // still open: never selected
	young := mk("calendar_add_event", now.Add(-time.Hour), "approved", now)     // ended less than 24h ago
	other := mk("memory.propose", old, "approved", old.Add(time.Hour))          // not a pim tool

	n, err := s.ScrubActionContent(ctx, tools, cutoff)
	if err != nil || n != 3 {
		t.Fatalf("scrubbed %d (%v), want 3", n, err)
	}
	blank := func(id string) bool {
		a := s.actions[id]
		return string(a.Payload) == "{}" && string(a.PayloadView) == "{}"
	}
	for name, id := range map[string]string{"expired pending": expiredPending, "approved": approved, "voided": voided} {
		if !blank(id) {
			t.Errorf("%s card still has its content", name)
		}
		if s.actions[id].PayloadHash == "" || s.actions[id].Status == "" {
			t.Errorf("%s card lost its hash or status", name)
		}
	}
	for name, id := range map[string]string{"live pending": livePending, "young": young, "other tool": other} {
		if blank(id) {
			t.Errorf("%s card was scrubbed", name)
		}
	}
	if n, _ := s.ScrubActionContent(ctx, tools, cutoff); n != 0 {
		t.Fatalf("second run scrubbed %d, want 0 (idempotent)", n)
	}
	if n, _ := s.ScrubActionContent(ctx, nil, cutoff); n != 0 {
		t.Fatal("no tools must scrub nothing")
	}
}
