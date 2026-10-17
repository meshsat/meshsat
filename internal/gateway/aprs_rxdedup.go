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
// 1.5 s later. A copy is the same sender, the same ack id and the same body:
// a ciphertext carries a fresh nonce for every message, so an identical one
// is a copy; a plain text without an id is a copy only within a few
// seconds, so a short reply sent twice on purpose still gets through.
//
// Admission and memory are one step (review APRS-RX1): a message is
// remembered only once the inbound queue took it. A frame the full queue
// refused is not remembered and not acked, so the sender transmits it again
// (APRS retransmits until acknowledged) and that copy is taken when there is
// room; acking it would have released the sender with the message lost.
// A copy of a message that was taken is acked again, also while the queue
// is full. [MESHSAT-1584]

const (
	// aprsRxDedupWindow covers a sender's whole retry ladder (attempts x
	// ack timeout, a few minutes at most).
	aprsRxDedupWindow = 10 * time.Minute
	// aprsRxDedupPlainWindow covers TXRepeat copies of a plain text that
	// asks for no ack.
	aprsRxDedupPlainWindow = 30 * time.Second
	aprsRxDedupMax         = 1024
)

// rxAdmission is what became of a received message.
type rxAdmission int

const (
	rxNotInbound rxAdmission = iota // protocol traffic or heard-list only: never for the queue
	rxAdmitted                      // a new message, now on the inbound queue
	rxCopy                          // a copy of a message taken before: dropped
	rxRejected                      // the queue was full: not taken, not remembered
)

// ackable says whether a message with an ack id may be acked: everything
// except a frame the full queue refused.
func (a rxAdmission) ackable() bool { return a != rxRejected }

type aprsRxSeen struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// admit takes one message once: under one lock it looks the message up,
// hands a new one to enqueue (which must not block) and remembers it only
// when enqueue took it, so two copies arriving at once cannot both be taken
// and a refused one stays eligible.
func (s *aprsRxSeen) admit(from, ackID, body string, encrypted bool, now time.Time, enqueue func() bool) rxAdmission {
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
		return rxCopy
	}
	if !enqueue() {
		return rxRejected
	}
	s.m[key] = now
	if len(s.m) > aprsRxDedupMax {
		for k, t := range s.m {
			if now.Sub(t) >= aprsRxDedupWindow {
				delete(s.m, k)
			}
		}
	}
	return rxAdmitted
}

// admitInbound puts msg on the inbound queue once, without blocking.
func (g *APRSGateway) admitInbound(msg InboundMessage, ackID string, encrypted bool) rxAdmission {
	a := g.rxSeen.admit(msg.FromAddr, ackID, msg.Text, encrypted, time.Now(), func() bool {
		select {
		case g.inCh <- msg:
			return true
		default:
			return false
		}
	})
	switch a {
	case rxAdmitted:
		g.msgsIn.Add(1)
		g.lastActive.Store(time.Now().Unix())
	case rxCopy:
		g.rxDuplicates.Add(1)
	}
	return a
}
