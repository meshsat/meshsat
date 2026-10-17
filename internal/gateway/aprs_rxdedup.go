package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// A message a sender transmits again is one message here (APRS 1.0.1: a
// receiver acknowledges every copy of a message but takes it once). A
// sender with an ack id sends the same frame again when our ack is late
// (the PicoAPRS held its ack twice on a busy channel on 17 Oct 2026 and the
// far kit stored the text twice), and a TXRepeat copy is the same frame
// 1.5 s later; the comments that said "the far kit dedups" had no dedup
// behind them. A copy is the same sender, the same ack id and the same body:
// a ciphertext carries a fresh nonce for every message, so an identical one
// is a copy; a plain text without an id is a copy only within a few
// seconds, so a short reply sent twice on purpose still gets through.
// The ack goes out for every copy before this check, so the sender is
// released either way. [MESHSAT-1584]

const (
	// aprsRxDedupWindow covers a sender's whole retry ladder (attempts x
	// ack timeout, a few minutes at most).
	aprsRxDedupWindow = 10 * time.Minute
	// aprsRxDedupPlainWindow covers TXRepeat copies of a plain text that
	// asks for no ack.
	aprsRxDedupPlainWindow = 30 * time.Second
	aprsRxDedupMax         = 1024
)

type aprsRxSeen struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// duplicate says whether this message was taken already within its window,
// and records it when it was not.
func (s *aprsRxSeen) duplicate(from, ackID, body string, encrypted bool, now time.Time) bool {
	window := aprsRxDedupPlainWindow
	if ackID != "" || encrypted {
		window = aprsRxDedupWindow
	}
	sum := sha256.Sum256([]byte(body))
	key := from + "\x00" + ackID + "\x00" + hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]time.Time{}
	}
	if t, ok := s.m[key]; ok && now.Sub(t) < window {
		return true
	}
	s.m[key] = now
	if len(s.m) > aprsRxDedupMax {
		for k, t := range s.m {
			if now.Sub(t) >= aprsRxDedupWindow {
				delete(s.m, k)
			}
		}
	}
	return false
}

// rxDuplicate drops a copy of a message this gateway already took.
func (g *APRSGateway) rxDuplicate(from, ackID, body string, encrypted bool) bool {
	if !g.rxSeen.duplicate(from, ackID, body, encrypted, time.Now()) {
		return false
	}
	g.rxDuplicates.Add(1)
	return true
}
