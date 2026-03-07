package engine

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

func TestGwInjectDedupKey_Deterministic(t *testing.T) {
	key1 := gwInjectDedupKey("hello world")
	key2 := gwInjectDedupKey("hello world")
	if key1 != key2 {
		t.Errorf("same input produced different keys: %s vs %s", key1, key2)
	}
}

func TestGwInjectDedupKey_DifferentInputs(t *testing.T) {
	key1 := gwInjectDedupKey("hello")
	key2 := gwInjectDedupKey("world")
	if key1 == key2 {
		t.Error("different inputs produced the same key")
	}
}

func TestGwInjectDedupKey_UsesSHA256WithPrefix(t *testing.T) {
	text := "test message"
	h := sha256.Sum256([]byte("gw_inject|" + text))
	expected := fmt.Sprintf("%x", h[:8])
	got := gwInjectDedupKey(text)
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestMarkAndDetectGatewayInjection(t *testing.T) {
	p := &Processor{
		relayDedup: make(map[string]time.Time),
	}

	// Not yet marked — should not be detected
	if p.isRecentGatewayInjection("hello from matrix") {
		t.Error("expected message to not be detected before marking")
	}

	// Mark it
	p.markGatewayInjection("hello from matrix")

	// Now it should be detected as a recent injection
	if !p.isRecentGatewayInjection("hello from matrix") {
		t.Error("expected message to be detected after marking")
	}
}

func TestIsRecentGatewayInjection_EmptyText(t *testing.T) {
	p := &Processor{
		relayDedup: make(map[string]time.Time),
	}

	// Empty text should never be considered a duplicate
	if p.isRecentGatewayInjection("") {
		t.Error("empty text should not be detected as gateway injection")
	}
}

func TestIsRecentGatewayInjection_DifferentMessages(t *testing.T) {
	p := &Processor{
		relayDedup: make(map[string]time.Time),
	}

	p.markGatewayInjection("message A")

	// Different message should not be detected
	if p.isRecentGatewayInjection("message B") {
		t.Error("different message should not be detected as duplicate")
	}

	// Original should still be detected
	if !p.isRecentGatewayInjection("message A") {
		t.Error("original message should still be detected")
	}
}

func TestIsRecentGatewayInjection_Expired(t *testing.T) {
	p := &Processor{
		relayDedup: make(map[string]time.Time),
	}

	// Insert an entry that's already expired (6 minutes ago)
	key := gwInjectDedupKey("old message")
	p.relayDedup[key] = time.Now().Add(-6 * time.Minute)

	// Should not be detected since it's older than 5 minutes
	if p.isRecentGatewayInjection("old message") {
		t.Error("expired injection should not be detected")
	}
}

func TestPruneRelayDedup(t *testing.T) {
	p := &Processor{
		relayDedup: make(map[string]time.Time),
	}

	// Add a fresh entry and an expired entry
	p.markGatewayInjection("fresh")
	expiredKey := gwInjectDedupKey("stale")
	p.relayDedup[expiredKey] = time.Now().Add(-10 * time.Minute)

	p.pruneRelayDedup()

	// Fresh entry should remain
	if !p.isRecentGatewayInjection("fresh") {
		t.Error("fresh entry should survive pruning")
	}

	// Expired entry should be gone
	p.relayDedupMu.Lock()
	_, exists := p.relayDedup[expiredKey]
	p.relayDedupMu.Unlock()
	if exists {
		t.Error("expired entry should have been pruned")
	}
}

func TestPruneRelayDedup_AllExpired(t *testing.T) {
	p := &Processor{
		relayDedup: make(map[string]time.Time),
	}

	// Add several expired entries
	for i := 0; i < 5; i++ {
		key := gwInjectDedupKey(fmt.Sprintf("msg-%d", i))
		p.relayDedup[key] = time.Now().Add(-10 * time.Minute)
	}

	p.pruneRelayDedup()

	p.relayDedupMu.Lock()
	size := len(p.relayDedup)
	p.relayDedupMu.Unlock()

	if size != 0 {
		t.Errorf("expected 0 entries after pruning all expired, got %d", size)
	}
}

func TestCaptureDedup_OnlyNewLinesPosted(t *testing.T) {
	// Simulates the capture flow: multiple lines arrive, only new ones
	// should pass the dedup check (i.e., would be posted to Matrix).
	p := &Processor{
		relayDedup: make(map[string]time.Time),
	}

	lines := []string{
		"[2026-03-06] sensor reading: 42",
		"[2026-03-06] sensor reading: 43",
		"[2026-03-06] sensor reading: 42", // duplicate of line 1
		"[2026-03-06] sensor reading: 44",
		"[2026-03-06] sensor reading: 43", // duplicate of line 2
	}

	var posted []string
	for _, line := range lines {
		if !p.isRecentGatewayInjection(line) {
			posted = append(posted, line)
			p.markGatewayInjection(line)
		}
	}

	// Only 3 unique lines should have been "posted"
	if len(posted) != 3 {
		t.Errorf("expected 3 unique lines posted, got %d: %v", len(posted), posted)
	}

	expected := []string{
		"[2026-03-06] sensor reading: 42",
		"[2026-03-06] sensor reading: 43",
		"[2026-03-06] sensor reading: 44",
	}
	for i, exp := range expected {
		if posted[i] != exp {
			t.Errorf("posted[%d] = %q, want %q", i, posted[i], exp)
		}
	}
}
