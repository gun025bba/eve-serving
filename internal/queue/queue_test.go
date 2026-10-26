package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gun025bba/llm-serving/internal/apierr"
)

func code(err error) string {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestQueueFullAndTimeout(t *testing.T) {
	q := New(1, 1, 50*time.Millisecond)
	ctx := context.Background()
	if _, err := q.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { _, err := q.Acquire(ctx); done <- err }()
	time.Sleep(10 * time.Millisecond)
	if _, err := q.Acquire(ctx); code(err) != "queue_full" {
		t.Fatalf("want queue_full, got %v", err)
	}
	if err := <-done; code(err) != "queue_timeout" {
		t.Fatalf("want queue_timeout, got %v", err)
	}
	if q.Waiting() != 0 || q.Inflight() != 1 {
		t.Fatal("counters", q.Waiting(), q.Inflight())
	}
	q.Release()
	if q.Inflight() != 0 {
		t.Fatal("inflight")
	}
}

func TestQueueHandoff(t *testing.T) {
	q := New(1, 4, time.Second)
	ctx := context.Background()
	q.Acquire(ctx)
	got := make(chan time.Duration)
	go func() { w, _ := q.Acquire(ctx); got <- w }()
	time.Sleep(30 * time.Millisecond)
	q.Release()
	if w := <-got; w < 20*time.Millisecond {
		t.Fatalf("wait too short %v", w)
	}
}
