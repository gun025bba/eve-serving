package audit

import (
	"sync"
	"time"
)

// Breaker is the M2 audit circuit breaker (security review of PR #13, Med 2).
// While auditing is fail-open (M2), repeated audit write or chain-verify
// failures disable the web tools: Threshold failures inside Window trip it.
// A tripped breaker stays open until the gateway restarts (an operator must
// look at the audit store first); it never closes on its own.
type Breaker struct {
	Threshold int           // default 3
	Window    time.Duration // default 5m
	Now       func() time.Time
	OnTrip    func()

	mu    sync.Mutex
	fails []time.Time
	open  bool
}

// NewBreaker returns a breaker with the reviewed defaults (3 in 5 minutes).
func NewBreaker() *Breaker { return &Breaker{Threshold: 3, Window: 5 * time.Minute} }

func (b *Breaker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Fail records one audit write or verify failure. It reports whether this
// failure tripped the breaker.
func (b *Breaker) Fail() bool {
	if b == nil {
		return false
	}
	th, win := b.Threshold, b.Window
	if th <= 0 {
		th = 3
	}
	if win <= 0 {
		win = 5 * time.Minute
	}
	b.mu.Lock()
	now := b.now()
	kept := b.fails[:0]
	for _, t := range b.fails {
		if now.Sub(t) < win {
			kept = append(kept, t)
		}
	}
	b.fails = append(kept, now)
	tripped := !b.open && len(b.fails) >= th
	if tripped {
		b.open = true
	}
	cb := b.OnTrip
	b.mu.Unlock()
	if tripped && cb != nil {
		cb()
	}
	return tripped
}

// Open reports whether the web tools must stay disabled.
func (b *Breaker) Open() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}
