package routing

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// Destination represents a known remote identity discovered via announce.
type Destination struct {
	DestHash      [DestHashLen]byte
	SigningPub    ed25519.PublicKey
	EncryptionPub *ecdh.PublicKey
	AppData       []byte
	HopCount      int
	SourceIface   string    // interface the announce arrived on
	FirstSeen     time.Time // first announce received
	LastSeen      time.Time // most recent announce
	AnnounceCount int       // total announces received
}

// DestinationTable maintains known destinations discovered via announces.
// Thread-safe in-memory table with optional database persistence.
type DestinationTable struct {
	mu    sync.RWMutex
	dests map[[DestHashLen]byte]*Destination
	db    *database.DB // optional, nil for in-memory only
}

// NewDestinationTable creates a destination table. If db is non-nil,
// destinations are persisted to the routing_destinations table.
func NewDestinationTable(db *database.DB) *DestinationTable {
	return &DestinationTable{
		dests: make(map[[DestHashLen]byte]*Destination),
		db:    db,
	}
}

// Update records or updates a destination from an announce packet.
// Returns true if this is a new destination (not previously known).
func (t *DestinationTable) Update(announce *Announce, sourceIface string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	existing, exists := t.dests[announce.DestHash]

	if exists {
		// Update existing: prefer lower hop count (closer path)
		if int(announce.HopCount) <= existing.HopCount {
			existing.HopCount = int(announce.HopCount)
			existing.SourceIface = sourceIface
		}
		existing.LastSeen = now
		existing.AnnounceCount++
		if len(announce.AppData) > 0 {
			existing.AppData = announce.AppData
		}
		t.persistAsync(existing)
		return false
	}

	// New destination
	dest := &Destination{
		DestHash:      announce.DestHash,
		SigningPub:    announce.SigningPub,
		EncryptionPub: announce.EncryptionPub,
		AppData:       announce.AppData,
		HopCount:      int(announce.HopCount),
		SourceIface:   sourceIface,
		FirstSeen:     now,
		LastSeen:      now,
		AnnounceCount: 1,
	}
	t.dests[announce.DestHash] = dest
	t.persistAsync(dest)

	log.Info().Str("dest_hash", hashHex(announce.DestHash)).
		Int("hops", int(announce.HopCount)).
		Str("source", sourceIface).
		Msg("new destination discovered")

	return true
}

// Lookup returns the destination for a given hash, or nil if unknown.
func (t *DestinationTable) Lookup(destHash [DestHashLen]byte) *Destination {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.dests[destHash]
}

// All returns a snapshot of all known destinations.
func (t *DestinationTable) All() []*Destination {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]*Destination, 0, len(t.dests))
	for _, d := range t.dests {
		result = append(result, d)
	}
	return result
}

// Count returns the number of known destinations.
func (t *DestinationTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.dests)
}

// Remove deletes a destination by hash.
func (t *DestinationTable) Remove(destHash [DestHashLen]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.dests, destHash)
}

// persistAsync persists a destination to the database in the background.
func (t *DestinationTable) persistAsync(dest *Destination) {
	if t.db == nil {
		return
	}
	go func() {
		if err := t.db.UpsertRoutingDestination(
			hashHex(dest.DestHash),
			dest.SigningPub,
			dest.EncryptionPub.Bytes(),
			dest.AppData,
			dest.HopCount,
			dest.SourceIface,
			dest.FirstSeen,
			dest.LastSeen,
			dest.AnnounceCount,
		); err != nil {
			log.Error().Err(err).Str("dest", hashHex(dest.DestHash)).Msg("persist destination failed")
		}
	}()
}

// LoadFromDB populates the in-memory table from the database.
func (t *DestinationTable) LoadFromDB() error {
	if t.db == nil {
		return nil
	}

	rows, err := t.db.GetRoutingDestinations()
	if err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for _, row := range rows {
		var destHash [DestHashLen]byte
		if decoded, err := hexDecode(row.DestHash); err == nil && len(decoded) == DestHashLen {
			copy(destHash[:], decoded)
		} else {
			continue
		}

		sigPub := ed25519.PublicKey(row.SigningPub)
		encPub, err := ecdh.X25519().NewPublicKey(row.EncryptionPub)
		if err != nil {
			continue
		}

		t.dests[destHash] = &Destination{
			DestHash:      destHash,
			SigningPub:    sigPub,
			EncryptionPub: encPub,
			AppData:       row.AppData,
			HopCount:      row.HopCount,
			SourceIface:   row.SourceIface,
			FirstSeen:     row.FirstSeen,
			LastSeen:      row.LastSeen,
			AnnounceCount: row.AnnounceCount,
		}
	}

	log.Info().Int("count", len(t.dests)).Msg("loaded routing destinations from database")
	return nil
}

func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errOddHex
	}
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		high := unhex(s[i])
		low := unhex(s[i+1])
		if high == 0xff || low == 0xff {
			return nil, errBadHex
		}
		b[i/2] = high<<4 | low
	}
	return b, nil
}

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10
	default:
		return 0xff
	}
}

var (
	errOddHex = errors.New("odd hex length")
	errBadHex = errors.New("invalid hex character")
)
