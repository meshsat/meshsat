package routing

import (
	"context"
	"crypto/sha256"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// RelayConfig controls announce relay behavior.
type RelayConfig struct {
	// MaxHops is the maximum hop count before dropping. Default: MaxAnnounceHops (128).
	MaxHops int
	// DedupTTL is how long to remember seen announces. Default: 30 minutes.
	DedupTTL time.Duration
	// MaxDedupEntries caps the dedup cache size. Default: 10000.
	MaxDedupEntries int
	// MinRelayDelay is the minimum random delay before retransmit. Default: 100ms.
	MinRelayDelay time.Duration
	// MaxRelayDelay is the maximum random delay before retransmit. Default: 2s.
	MaxRelayDelay time.Duration
}

// DefaultRelayConfig returns sensible defaults for announce relay.
func DefaultRelayConfig() RelayConfig {
	return RelayConfig{
		MaxHops:         MaxAnnounceHops,
		DedupTTL:        30 * time.Minute,
		MaxDedupEntries: 10000,
		MinRelayDelay:   100 * time.Millisecond,
		MaxRelayDelay:   2 * time.Second,
	}
}

// RelayCallback is called when an announce should be forwarded to interfaces.
// The relay passes the marshaled packet (with incremented hop count) and the
// parsed announce for metadata.
type RelayCallback func(data []byte, announce *Announce)

// AnnounceRelay handles deduplication, hop count enforcement, and delayed
// retransmission of announce packets across interfaces.
type AnnounceRelay struct {
	config   RelayConfig
	table    *DestinationTable
	callback RelayCallback

	mu           sync.Mutex
	seen         map[[32]byte]time.Time     // SHA-256(destHash+random) → first-seen time
	local        map[[DestHashLen]byte]bool // our own destination hashes (never relay)
	relayedCount int64                      // total announces relayed (monotonic)
}

// NewAnnounceRelay creates a relay that forwards announces to the callback.
func NewAnnounceRelay(config RelayConfig, table *DestinationTable, callback RelayCallback) *AnnounceRelay {
	return &AnnounceRelay{
		config:   config,
		table:    table,
		callback: callback,
		seen:     make(map[[32]byte]time.Time),
		local:    make(map[[DestHashLen]byte]bool),
	}
}

// RegisterLocal marks a destination hash as local (our own identity).
// Announces from local identities are never relayed.
func (r *AnnounceRelay) RegisterLocal(destHash [DestHashLen]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.local[destHash] = true
}

// HandleAnnounce processes an incoming announce packet. It deduplicates,
// validates, updates the destination table, and schedules relay if appropriate.
// Returns true if the announce was new and valid.
func (r *AnnounceRelay) HandleAnnounce(ctx context.Context, data []byte, sourceInterface string) bool {
	announce, err := UnmarshalAnnounce(data)
	if err != nil {
		log.Debug().Err(err).Str("source", sourceInterface).Msg("announce relay: unmarshal failed")
		return false
	}

	if !announce.Verify() {
		log.Warn().Str("source", sourceInterface).
			Str("dest_hash", hashHex(announce.DestHash)).
			Msg("announce relay: verification failed")
		return false
	}

	// Dedup check: hash of destHash + random blob
	dedupKey := r.dedupKey(announce)
	r.mu.Lock()
	if _, exists := r.seen[dedupKey]; exists {
		r.mu.Unlock()
		log.Debug().Str("dest_hash", hashHex(announce.DestHash)).Msg("announce relay: duplicate, ignoring")
		return false
	}
	r.seen[dedupKey] = time.Now()
	isLocal := r.local[announce.DestHash]
	r.mu.Unlock()

	// Update destination table with this announce
	if r.table != nil {
		r.table.Update(announce, sourceInterface)
	}

	// Don't relay our own announces
	if isLocal {
		log.Debug().Str("dest_hash", hashHex(announce.DestHash)).Msg("announce relay: local identity, not relaying")
		return true
	}

	// Hop count check
	if int(announce.HopCount) >= r.config.MaxHops {
		log.Debug().Str("dest_hash", hashHex(announce.DestHash)).
			Int("hops", int(announce.HopCount)).
			Msg("announce relay: max hops exceeded")
		return true // valid but not relayed
	}

	// Schedule relay with random delay
	if r.callback != nil {
		r.scheduleRelay(ctx, announce)
	}

	return true
}

// StartPruner launches a background goroutine to prune the dedup cache.
func (r *AnnounceRelay) StartPruner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.prune()
			}
		}
	}()
}

func (r *AnnounceRelay) prune() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for k, ts := range r.seen {
		if now.Sub(ts) > r.config.DedupTTL {
			delete(r.seen, k)
		}
	}
	// If still over limit, remove oldest entries
	if len(r.seen) > r.config.MaxDedupEntries {
		excess := len(r.seen) - r.config.MaxDedupEntries
		removed := 0
		for k := range r.seen {
			if removed >= excess {
				break
			}
			delete(r.seen, k)
			removed++
		}
	}
}

func (r *AnnounceRelay) scheduleRelay(ctx context.Context, announce *Announce) {
	// Random delay between MinRelayDelay and MaxRelayDelay
	delayRange := r.config.MaxRelayDelay - r.config.MinRelayDelay
	delay := r.config.MinRelayDelay + time.Duration(rand.Int64N(int64(delayRange)))

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Increment hop count for relay
		if !announce.IncrementHop() {
			return
		}

		relayData := announce.Marshal()
		r.callback(relayData, announce)
		r.mu.Lock()
		r.relayedCount++
		r.mu.Unlock()
		log.Debug().Str("dest_hash", hashHex(announce.DestHash)).
			Int("hops", int(announce.HopCount)).
			Dur("delay", delay).
			Msg("announce relayed")
	}()
}

func (r *AnnounceRelay) dedupKey(a *Announce) [32]byte {
	h := sha256.New()
	h.Write(a.DestHash[:])
	h.Write(a.Random[:])
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

// SeenCount returns the number of entries in the dedup cache (for metrics).
func (r *AnnounceRelay) SeenCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// RelayedCount returns the total number of announces relayed since startup.
func (r *AnnounceRelay) RelayedCount() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.relayedCount
}

func hashHex(h [DestHashLen]byte) string {
	const hextable = "0123456789abcdef"
	buf := make([]byte, DestHashLen*2)
	for i, b := range h {
		buf[i*2] = hextable[b>>4]
		buf[i*2+1] = hextable[b&0x0f]
	}
	return string(buf)
}
