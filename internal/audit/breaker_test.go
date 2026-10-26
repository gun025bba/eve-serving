package audit

import (
	"testing"
	"time"
)

func TestBreaker_ThreeFailuresInFiveMinutesTrips(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	trips := 0
	b := NewBreaker()
	b.Now = func() time.Time { return now }
	b.OnTrip = func() { trips++ }
	b.Fail()
	now = now.Add(2 * time.Minute)
	b.Fail()
	if b.Open() {
		t.Fatal("2 failures must not trip")
	}
	now = now.Add(2*time.Minute + 59*time.Second)
	if !b.Fail() || !b.Open() {
		t.Fatal("3rd failure within 5m must trip")
	}
	b.Fail()
	if trips != 1 {
		t.Fatalf("OnTrip once, got %d", trips)
	}
	now = now.Add(time.Hour)
	if !b.Open() {
		t.Fatal("breaker must stay open until restart")
	}
}

func TestBreaker_SpreadFailuresDoNotTrip(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	b := NewBreaker()
	b.Now = func() time.Time { return now }
	for i := 0; i < 10; i++ {
		b.Fail()
		now = now.Add(3 * time.Minute) // never 3 inside a 5m window
	}
	if b.Open() {
		t.Fatal("failures outside the window must not trip")
	}
	var nilB *Breaker
	if nilB.Fail() || nilB.Open() {
		t.Fatal("nil breaker is a no-op")
	}
}
