package provider

import (
	"context"
	"sync"
	"time"
)

// tokenBucket implements a simple token-bucket rate limiter.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	rate     float64 // tokens per second
	burst    float64 // max tokens (burst)
	lastTime time.Time
}

func newTokenBucket(ratePerSec, burst float64) *tokenBucket {
	return &tokenBucket{
		tokens:   burst,
		rate:     ratePerSec,
		burst:    burst,
		lastTime: time.Now(),
	}
}

func (tb *tokenBucket) wait(ctx context.Context) error {
	for {
		tb.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(tb.lastTime).Seconds()
		tb.tokens += elapsed * tb.rate
		if tb.tokens > tb.burst {
			tb.tokens = tb.burst
		}
		tb.lastTime = now
		if tb.tokens >= 1 {
			tb.tokens--
			tb.mu.Unlock()
			return nil
		}
		// Calculate wait time for 1 token.
		wait := time.Duration((1 - tb.tokens) / tb.rate * float64(time.Second))
		if wait < 50*time.Millisecond {
			wait = 50 * time.Millisecond
		}
		tb.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// RateLimiter enforces per-host download rate limits.
type RateLimiter struct {
	mu    sync.Mutex
	hosts map[string]*tokenBucket
	rate  float64
	burst float64
}

// NewRateLimiter creates a rate limiter with the given per-host req/s and burst.
func NewRateLimiter(reqPerSec, burst float64) *RateLimiter {
	return &RateLimiter{
		hosts: make(map[string]*tokenBucket),
		rate:  reqPerSec,
		burst: burst,
	}
}

// Wait blocks until a token is available for the given host.
func (rl *RateLimiter) Wait(ctx context.Context, host string) error {
	rl.mu.Lock()
	tb, ok := rl.hosts[host]
	if !ok {
		tb = newTokenBucket(rl.rate, rl.burst)
		rl.hosts[host] = tb
	}
	rl.mu.Unlock()
	return tb.wait(ctx)
}
