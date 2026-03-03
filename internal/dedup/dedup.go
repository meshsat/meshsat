package dedup

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Deduplicator provides O(1) in-memory duplicate detection using composite keys.
// It uses a time-windowed LRU to prevent unbounded growth.
type Deduplicator struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	ttl     time.Duration
	maxSize int
}

// New creates a deduplicator with the given TTL and max cache size.
func New(ttl time.Duration, maxSize int) *Deduplicator {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &Deduplicator{
		seen:    make(map[string]time.Time),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

// IsDuplicate checks if a message with the given from+packetID has been seen recently.
// Returns true if duplicate (should be skipped), false if new (first time seen).
func (d *Deduplicator) IsDuplicate(from uint32, packetID uint32) bool {
	key := fmt.Sprintf("%d:%d", from, packetID)

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.seen[key]; exists {
		return true
	}

	// Evict oldest if at capacity
	if len(d.seen) >= d.maxSize {
		d.evictOldest()
	}

	d.seen[key] = time.Now()
	return false
}

// Size returns the number of entries in the cache.
func (d *Deduplicator) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

// evictOldest removes the oldest entry. Caller must hold the lock.
func (d *Deduplicator) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for k, t := range d.seen {
		if oldestKey == "" || t.Before(oldestTime) {
			oldestKey = k
			oldestTime = t
		}
	}
	if oldestKey != "" {
		delete(d.seen, oldestKey)
	}
}

// prune removes all entries older than TTL. Caller must hold the lock.
func (d *Deduplicator) prune() int {
	cutoff := time.Now().Add(-d.ttl)
	pruned := 0
	for k, t := range d.seen {
		if t.Before(cutoff) {
			delete(d.seen, k)
			pruned++
		}
	}
	return pruned
}

// StartPruner runs a background goroutine that prunes expired entries.
// It stops when the context is cancelled.
func (d *Deduplicator) StartPruner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.mu.Lock()
				pruned := d.prune()
				size := len(d.seen)
				d.mu.Unlock()
				if pruned > 0 {
					log.Debug().Int("pruned", pruned).Int("remaining", size).Msg("dedup cache pruned")
				}
			}
		}
	}()
}
