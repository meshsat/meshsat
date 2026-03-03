package ratelimit

import (
	"testing"
	"time"
)

func TestTokenBucket_Allow(t *testing.T) {
	tb := NewTokenBucket(3, 1.0) // 3 tokens, 1 per second refill

	// Should allow first 3
	for i := 0; i < 3; i++ {
		if !tb.Allow() {
			t.Errorf("expected allow on attempt %d", i)
		}
	}

	// 4th should be denied
	if tb.Allow() {
		t.Error("expected deny after burst exhausted")
	}

	// Wait for refill
	time.Sleep(1100 * time.Millisecond)
	if !tb.Allow() {
		t.Error("expected allow after refill")
	}
}

func TestTokenBucket_Tokens(t *testing.T) {
	tb := NewTokenBucket(10, 1.0)
	tokens := tb.Tokens()
	if tokens != 10 {
		t.Errorf("expected 10 tokens, got %f", tokens)
	}

	tb.Allow()
	tokens = tb.Tokens()
	if tokens < 8.9 || tokens > 9.1 {
		t.Errorf("expected ~9 tokens after 1 consume, got %f", tokens)
	}
}

func TestMeshInjectionLimiter(t *testing.T) {
	l := NewMeshInjectionLimiter()
	// Should allow 6 burst
	for i := 0; i < 6; i++ {
		if !l.Allow() {
			t.Errorf("expected allow on attempt %d", i)
		}
	}
	if l.Allow() {
		t.Error("expected deny after burst")
	}
}

func TestNewRuleLimiter_NoLimit(t *testing.T) {
	l := NewRuleLimiter(0, 60)
	if l != nil {
		t.Error("expected nil limiter for zero perMin")
	}
}
