package rns

import (
	"encoding/hex"
	"sync"
	"time"

	"meshsat/internal/reticulum"
)

// PathEntry is one row of the path table (Transport.path_table).
type PathEntry struct {
	DestHash     [HashLen]byte
	Timestamp    time.Time     // last use
	NextHop      [HashLen]byte // transport id of the next hop (== dest when direct)
	Hops         int
	Expires      time.Time
	RandomBlobs  [][]byte // the 10-byte random hashes heard, newest last
	Iface        string   // received on / next-hop interface
	Announce     *reticulum.Announce
	PacketHash   [FullHashLen]byte
	Unresponsive bool
}

// Direct reports whether the destination is one hop away.
func (e *PathEntry) Direct() bool { return e.Hops <= 1 }

// EmittedAt is the newest emission timestamp among the random blobs.
func (e *PathEntry) EmittedAt() int64 {
	var best int64
	for _, b := range e.RandomBlobs {
		if ts := blobTimestamp(b); ts > best {
			best = ts
		}
	}
	return best
}

func blobTimestamp(b []byte) int64 {
	if len(b) < 10 {
		return 0
	}
	var ts int64
	for _, c := range b[5:10] {
		ts = ts<<8 | int64(c)
	}
	return ts
}

// PathTable holds known paths.
type PathTable struct {
	mu  sync.RWMutex
	m   map[[HashLen]byte]*PathEntry
	ttl time.Duration
	now func() time.Time
}

// NewPathTable creates an empty table with the given expiry.
func NewPathTable(ttl time.Duration, now func() time.Time) *PathTable {
	return &PathTable{m: make(map[[HashLen]byte]*PathEntry), ttl: ttl, now: now}
}

// Get returns the entry for a destination, or nil (expired entries stay
// until swept so that an expired path can still be replaced by a worse one).
func (t *PathTable) Get(dest [HashLen]byte) *PathEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.m[dest]
}

// Has reports whether an unexpired path is known.
func (t *PathTable) Has(dest [HashLen]byte) bool {
	e := t.Get(dest)
	return e != nil && t.now().Before(e.Expires)
}

// Hops returns the hop count or -1.
func (t *PathTable) Hops(dest [HashLen]byte) int {
	if e := t.Get(dest); e != nil {
		return e.Hops
	}
	return -1
}

// Put stores an entry.
func (t *PathTable) Put(e *PathEntry) {
	t.mu.Lock()
	t.m[e.DestHash] = e
	t.mu.Unlock()
}

// Remove drops a destination.
func (t *PathTable) Remove(dest [HashLen]byte) {
	t.mu.Lock()
	delete(t.m, dest)
	t.mu.Unlock()
}

// Expire removes entries whose Expires passed more than a week ago. RNS keeps
// expired entries around as candidates; we keep them one extra TTL.
func (t *PathTable) Expire(now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	removed := 0
	for k, e := range t.m {
		if now.After(e.Expires.Add(t.ttl)) {
			delete(t.m, k)
			removed++
		}
	}
	return removed
}

// All returns a snapshot of all entries.
func (t *PathTable) All() []*PathEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*PathEntry, 0, len(t.m))
	for _, e := range t.m {
		out = append(out, e)
	}
	return out
}

// Count returns the number of entries.
func (t *PathTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.m)
}

// TTL returns the configured path lifetime.
func (t *PathTable) TTL() time.Duration { return t.ttl }

// PathInfo is the JSON view of a path.
type PathInfo struct {
	DestHash  string `json:"dest_hash"`
	NextHop   string `json:"next_hop"`
	Hops      int    `json:"hops"`
	Interface string `json:"interface"`
	Expires   string `json:"expires_at"`
	EmittedAt int64  `json:"emitted_at"`
	Direct    bool   `json:"direct"`
}

// Info converts an entry for the API.
func (e *PathEntry) Info() PathInfo {
	return PathInfo{
		DestHash:  hex.EncodeToString(e.DestHash[:]),
		NextHop:   hex.EncodeToString(e.NextHop[:]),
		Hops:      e.Hops,
		Interface: e.Iface,
		Expires:   e.Expires.UTC().Format(time.RFC3339),
		EmittedAt: e.EmittedAt(),
		Direct:    e.Direct(),
	}
}
