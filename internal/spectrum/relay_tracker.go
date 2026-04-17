package spectrum

import (
	"sync"
	"time"
)

// RelayTracker records the outcome of each MIJI/CoT relay attempt so
// the /spectrum UI can show "last CoT relay: ok 3 s ago" or "last hub
// alert: failed 14 s ago — tak gateway stopped". Without this, the
// only way for an operator to know a relay had failed was to scroll
// the container logs.
//
// Keyed by destination name ("tak_cot", "hub"). Zero-value is ready
// to use; Snapshot() is safe to call from the HTTP handler goroutine.
type RelayTracker struct {
	mu  sync.RWMutex
	per map[string]*RelayStatus
}

// RelayStatus is a per-destination aggregate: counts + the timestamps
// and error text of the most recent success/failure. Exposed verbatim
// over /api/spectrum/relay-status.
type RelayStatus struct {
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	SuccessCount  int64     `json:"success_count"`
	ErrorCount    int64     `json:"error_count"`
}

func NewRelayTracker() *RelayTracker {
	return &RelayTracker{per: make(map[string]*RelayStatus)}
}

func (t *RelayTracker) ensure(dest string) *RelayStatus {
	if t.per == nil {
		t.per = make(map[string]*RelayStatus)
	}
	rs, ok := t.per[dest]
	if !ok {
		rs = &RelayStatus{}
		t.per[dest] = rs
	}
	return rs
}

// RecordSuccess is called after a successful relay send. Safe for
// concurrent use from multiple goroutines (we currently only have one
// relay goroutine, but locking costs nothing and future-proofs this).
func (t *RelayTracker) RecordSuccess(dest string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rs := t.ensure(dest)
	now := time.Now()
	rs.LastAttemptAt = now
	rs.LastSuccessAt = now
	rs.SuccessCount++
}

// RecordFailure is called after a failed relay send with the error
// text. We keep the latest error only — a full error log would bloat
// the payload and belongs in the audit log anyway.
func (t *RelayTracker) RecordFailure(dest string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rs := t.ensure(dest)
	now := time.Now()
	rs.LastAttemptAt = now
	rs.LastErrorAt = now
	if err != nil {
		rs.LastError = err.Error()
	} else {
		rs.LastError = "unknown"
	}
	rs.ErrorCount++
}

// Snapshot returns a deep copy of the current per-destination map
// suitable for JSON serialisation. Values (not pointers) to prevent
// the caller from mutating live state.
func (t *RelayTracker) Snapshot() map[string]RelayStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]RelayStatus, len(t.per))
	for k, v := range t.per {
		out[k] = *v
	}
	return out
}
