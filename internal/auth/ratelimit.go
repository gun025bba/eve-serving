package auth

import "context"

// RateLimiter is a per-user hook called after authentication, keyed by
// Identity.User. Return false to reject with 429. Default: NoopLimiter.
type RateLimiter interface {
	Allow(ctx context.Context, id Identity) bool
}

// NoopLimiter allows everything (placeholder until a real limiter is needed).
type NoopLimiter struct{}

func (NoopLimiter) Allow(context.Context, Identity) bool { return true }
