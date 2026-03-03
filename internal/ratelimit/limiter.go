package ratelimit

import (
	"sync"
	"time"
)

// TokenBucket implements a token bucket rate limiter.
type TokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// NewTokenBucket creates a rate limiter with the given max tokens and refill rate.
// maxTokens is the burst capacity; refillRate is tokens per second.
func NewTokenBucket(maxTokens float64, refillRate float64) *TokenBucket {
	return &TokenBucket{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// Allow checks if a token is available and consumes it. Returns true if allowed.
func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.refill()

	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// Tokens returns the current token count (for monitoring).
func (tb *TokenBucket) Tokens() float64 {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refill()
	return tb.tokens
}

func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.lastRefill = now

	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
}

// NewMeshInjectionLimiter creates the global limiter for injecting external messages
// into the mesh. Default: max 6 messages per minute (1 per 10s).
func NewMeshInjectionLimiter() *TokenBucket {
	return NewTokenBucket(6, 0.1)
}

// NewRuleLimiter creates a per-rule rate limiter from rule config.
// perMin is the max messages per window; window is seconds.
func NewRuleLimiter(perMin int, window int) *TokenBucket {
	if perMin <= 0 || window <= 0 {
		return nil // no rate limit
	}
	rate := float64(perMin) / float64(window)
	return NewTokenBucket(float64(perMin), rate)
}
