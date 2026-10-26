// Package queue bounds concurrency (semaphore) and waiting requests (queue).
package queue

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gun025bba/eve-serving/internal/apierr"
)

type Queue struct {
	sem      chan struct{}
	maxQueue int64
	timeout  time.Duration
	waiting  atomic.Int64
	inflight atomic.Int64
	OnChange func(waiting, inflight int64) // optional gauge hook
}

func New(maxConc, maxQueue int, timeout time.Duration) *Queue {
	return &Queue{sem: make(chan struct{}, maxConc), maxQueue: int64(maxQueue), timeout: timeout}
}

func (q *Queue) Waiting() int64  { return q.waiting.Load() }
func (q *Queue) Inflight() int64 { return q.inflight.Load() }
func (q *Queue) MaxConc() int    { return cap(q.sem) }
func (q *Queue) MaxQueue() int   { return int(q.maxQueue) }

func (q *Queue) notify() {
	if q.OnChange != nil {
		q.OnChange(q.waiting.Load(), q.inflight.Load())
	}
}

// Acquire waits for a slot. Returns queue wait time; caller must Release on success.
func (q *Queue) Acquire(ctx context.Context) (time.Duration, error) {
	t0 := time.Now()
	select { // fast path: free slot, no queueing
	case q.sem <- struct{}{}:
		q.inflight.Add(1)
		q.notify()
		return time.Since(t0), nil
	default:
	}
	if q.waiting.Add(1) > q.maxQueue {
		q.waiting.Add(-1)
		return 0, apierr.New(429, "queue_full", fmt.Sprintf("Queue full (%d waiting). Retry later.", q.maxQueue), "rate_limit_error")
	}
	q.notify()
	defer func() { q.waiting.Add(-1); q.notify() }()
	tm := time.NewTimer(q.timeout)
	defer tm.Stop()
	select {
	case q.sem <- struct{}{}:
		q.inflight.Add(1)
		return time.Since(t0), nil
	case <-tm.C:
		return 0, apierr.New(429, "queue_timeout", fmt.Sprintf("Waited >%gs for a slot", q.timeout.Seconds()), "rate_limit_error")
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (q *Queue) Release() {
	q.inflight.Add(-1)
	<-q.sem
	q.notify()
}
