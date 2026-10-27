package rnsstack

import (
	"sync"
	"time"
)

// defaultInboundPerMinute is what the kit's LXMF address takes in a minute
// when MESHSAT_LXMF_INBOUND_PER_MINUTE is not set, as the Hub's tenant
// addresses do (lxmfhub defaultInboundPerMinute).
const defaultInboundPerMinute = 60

// inboundLimit is a token bucket over every inbound LXMF message: a peer that
// floods the kit's address (anybody on a network it joined, MESHSAT-1715)
// cannot fill its inbox or drive the access rules that carry lxmf_0 on over a
// satellite. capacity tokens, refilled at capacity per minute.
type inboundLimit struct {
	mu                     sync.Mutex
	tokens, capacity, rate float64
	last                   time.Time
	dropped                uint64
	lastLog                time.Time
}

func newInboundLimit(perMinute int, now time.Time) *inboundLimit {
	if perMinute <= 0 {
		perMinute = defaultInboundPerMinute
	}
	c := float64(perMinute)
	return &inboundLimit{tokens: c, capacity: c, rate: c / 60, last: now}
}

// allow takes a token; false counts a drop. logNow is true at most once a
// minute for a drop, so a flood does not flood the log too.
func (b *inboundLimit) allow(now time.Time) (ok, logNow bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, false
	}
	b.dropped++
	if now.Sub(b.lastLog) >= time.Minute {
		b.lastLog = now
		return false, true
	}
	return false, false
}

func (b *inboundLimit) droppedCount() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}
