package store

import (
	"testing"
	"time"
)

func TestNewID_MonotonicSameMillisecond(t *testing.T) {
	now := time.Now()
	prev := NewID(now)
	for i := 0; i < 1000; i++ {
		id := NewID(now)
		if id <= prev {
			t.Fatalf("not increasing: %s <= %s", id, prev)
		}
		prev = id
	}
}
