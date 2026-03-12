package routing

import (
	"sync"
	"time"
)

// Default channel bandwidths in bits per second.
var DefaultBandwidths = map[string]int{
	"mesh":      1200,   // LoRa SF12 BW125
	"iridium":   2400,   // SBD uplink
	"astrocast": 600,    // LEO uplink
	"cellular":  9600,   // SMS GSM 2G
	"zigbee":    250000, // IEEE 802.15.4
	"webhook":   0,      // unlimited (no limiting)
	"mqtt":      0,      // unlimited (no limiting)
}

// DefaultAnnounceBudgetPct is the default percentage of interface bandwidth
// allocated to announce propagation. Reticulum uses 2%.
const DefaultAnnounceBudgetPct = 2

// AnnounceBandwidthLimiter enforces per-interface bandwidth caps on announce
// propagation. Each interface gets a token bucket that refills at 2% of the
// interface's bandwidth. Announces that would exceed the budget are dropped.
type AnnounceBandwidthLimiter struct {
	mu      sync.Mutex
	buckets map[string]*announceBucket
}

type announceBucket struct {
	tokens     float64   // available bits
	maxTokens  float64   // bucket capacity (1 second of budget)
	refillRate float64   // bits per second
	lastRefill time.Time // last refill time
}

// NewAnnounceBandwidthLimiter creates a limiter with no pre-configured interfaces.
// Call SetBandwidth to configure each interface.
func NewAnnounceBandwidthLimiter() *AnnounceBandwidthLimiter {
	return &AnnounceBandwidthLimiter{
		buckets: make(map[string]*announceBucket),
	}
}

// SetBandwidth configures the announce bandwidth budget for an interface.
// bandwidthBps is the total interface bandwidth in bits per second.
// budgetPct is the percentage allocated to announces (typically 2).
// If bandwidthBps is 0, the interface is unlimited (no limiting).
func (l *AnnounceBandwidthLimiter) SetBandwidth(ifaceID string, bandwidthBps int, budgetPct int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if bandwidthBps <= 0 || budgetPct <= 0 {
		delete(l.buckets, ifaceID)
		return
	}

	rate := float64(bandwidthBps) * float64(budgetPct) / 100.0
	l.buckets[ifaceID] = &announceBucket{
		tokens:     rate, // start full
		maxTokens:  rate, // 1 second of budget
		refillRate: rate,
		lastRefill: time.Now(),
	}
}

// SetDefaultBandwidth configures an interface using the default bandwidth
// for its channel type and the default 2% budget.
func (l *AnnounceBandwidthLimiter) SetDefaultBandwidth(ifaceID, channelType string) {
	bw, ok := DefaultBandwidths[channelType]
	if !ok || bw <= 0 {
		return
	}
	l.SetBandwidth(ifaceID, bw, DefaultAnnounceBudgetPct)
}

// Allow checks whether an announce of the given size (bytes) can be relayed
// on the interface. Returns true and consumes tokens if allowed; returns false
// if the budget is exhausted. Interfaces without a configured budget always allow.
func (l *AnnounceBandwidthLimiter) Allow(ifaceID string, sizeBytes int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[ifaceID]
	if !ok {
		return true // no budget configured = unlimited
	}

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	cost := float64(sizeBytes * 8) // convert bytes to bits
	if b.tokens >= cost {
		b.tokens -= cost
		return true
	}
	return false
}

// Available returns the currently available announce budget in bytes for an
// interface. Returns -1 for unlimited interfaces.
func (l *AnnounceBandwidthLimiter) Available(ifaceID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[ifaceID]
	if !ok {
		return -1
	}

	// Refill
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	return int(b.tokens / 8) // bits to bytes
}
