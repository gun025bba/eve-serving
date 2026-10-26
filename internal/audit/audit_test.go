package audit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestEvent_ActorRequiredNoDefault(t *testing.T) {
	now := time.Now()
	for _, e := range []Event{
		{At: now, Event: EvCallDone},                        // no actor
		{At: now, Actor: "admin", Event: EvCallDone},        // unknown
		{At: now, Actor: ActorScheduler, Event: EvCallDone}, // scheduler without job
		{At: now, Actor: ActorUser},                         // no event
		{Actor: ActorUser, Event: EvCallDone},               // no time
	} {
		if err := e.Validate(); err == nil {
			t.Errorf("must reject %+v", e)
		}
	}
	if err := (&Event{At: now, Actor: ActorScheduler, JobID: "j1", Event: EvCallDone}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryChain_ConcurrentAndTamper(t *testing.T) {
	m := &Memory{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Append(context.Background(), "u", Event{At: time.Now(), Actor: ActorUser, Event: EvCallDone, Tool: "web_search"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rows := m.Rows()
	if res, err := Verify(rows, ""); err != nil || res.Rows != 50 {
		t.Fatalf("verify: %v %+v", err, res)
	}
	rows[10].Event.Status = "ok-but-edited"
	if _, err := Verify(rows, ""); err == nil {
		t.Fatal("tampered row must fail verify")
	}
	rows = m.Rows()
	rows = append(rows[:5], rows[6:]...)
	if _, err := Verify(rows, ""); err == nil {
		t.Fatal("deleted row must fail verify")
	}
}

func TestCanonical_Stable(t *testing.T) {
	tr := true
	e := Event{At: time.Date(2026, 10, 26, 1, 2, 3, 456789123, time.FixedZone("KST", 9*3600)), Actor: ActorUser, Event: EvDomainBlocked,
		Tainted: &tr, Detail: map[string]string{"rule": "deny:0", "hop": "1"}}
	want := `{"at":"2026-10-25T16:02:03.456789Z","user_id":0,"actor":"user","event":"domain_blocked","tainted":true,"detail":{"hop":"1","rule":"deny:0"}}`
	if got := string(Canonical(e)); got != want {
		t.Fatalf("canonical changed:\n got %s\nwant %s", got, want)
	}
}
