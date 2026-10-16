package rns

import (
	"crypto/ed25519"
	"time"

	"meshsat/internal/reticulum"
)

// Receipt tracks a packet sent to a SINGLE destination (or on a link) until
// its proof arrives or the timeout passes (RNS.PacketReceipt).
type Receipt struct {
	Hash    [FullHashLen]byte
	Dest    [HashLen]byte
	SentAt  time.Time
	Timeout time.Time
	sigPub  ed25519.PublicKey // key that must sign the proof
	link    *Link             // non-nil for link packets
	done    chan ReceiptResult
}

// ReceiptResult is the outcome of a Receipt.
type ReceiptResult struct {
	Delivered bool
	RTT       time.Duration
}

// Done yields the result once (delivered or timed out).
func (r *Receipt) Done() <-chan ReceiptResult { return r.done }

// SigningKey is the key that must sign this packet's proof: the
// destination's identity, or the peer's link key for a link packet.
func (r *Receipt) SigningKey() ed25519.PublicKey { return r.sigPub }

// afterProofValidated, when set by a test, runs between a proof's
// validation and its hand-over: where a ForgetReceipt can come in between.
var afterProofValidated func()

// ForgetReceipt stops waiting for a receipt's proof: a proof that arrives
// later goes to OnUnmatchedProof, as one for a receipt that timed out or
// was never in this process does. Either the receipt takes a proof (and
// Done yields it) or the hook does, never both and never neither, because
// a proof is handed to a receipt only while it is still held. [MESHSAT-1584]
func (n *Node) ForgetReceipt(hash [FullHashLen]byte) {
	n.mu.Lock()
	delete(n.receipts, hash)
	n.mu.Unlock()
}

func (n *Node) addReceipt(r *Receipt) {
	r.done = make(chan ReceiptResult, 1)
	n.mu.Lock()
	n.receipts[r.Hash] = r
	n.mu.Unlock()
}

// handleReceiptProof matches a PROOF packet against outstanding receipts.
func (n *Node) handleReceiptProof(pkt *Packet) {
	data := pkt.Hdr.Data
	var candidates []*Receipt
	n.mu.Lock()
	if len(data) == reticulum.ExplicitProofLen {
		var h [FullHashLen]byte
		copy(h[:], data[:FullHashLen])
		if r, ok := n.receipts[h]; ok {
			candidates = []*Receipt{r}
		}
	} else if len(data) == reticulum.ImplicitProofLen {
		for _, r := range n.receipts {
			if r.Dest == pkt.Hdr.DestHash || pkt.Hdr.DestType == reticulum.DestLink {
				candidates = append(candidates, r)
			}
		}
		// A proof is addressed to the truncated packet hash, so try those first.
		var tr [HashLen]byte
		copy(tr[:], pkt.Hdr.DestHash[:])
		for _, r := range n.receipts {
			var rt [HashLen]byte
			copy(rt[:], r.Hash[:HashLen])
			if rt == tr {
				candidates = append([]*Receipt{r}, candidates...)
			}
		}
	}
	n.mu.Unlock()
	for _, r := range candidates {
		if r.sigPub == nil {
			continue
		}
		if err := reticulum.ValidateProof(data, r.Hash, r.sigPub); err == nil {
			if afterProofValidated != nil {
				afterProofValidated()
			}
			// Handed over only while the receipt is still held, under the
			// lock ForgetReceipt takes: done has room for this one result.
			n.mu.Lock()
			_, held := n.receipts[r.Hash]
			if held {
				delete(n.receipts, r.Hash)
				r.done <- ReceiptResult{Delivered: true, RTT: n.now().Sub(r.SentAt)}
			}
			n.mu.Unlock()
			if held {
				if r.link != nil {
					r.link.lastProof = n.now()
				}
				return
			}
			break
		}
	}
	if hook := n.OnUnmatchedProof; hook != nil {
		hook(pkt)
	}
}

func (n *Node) expireReceipts(now time.Time) {
	n.mu.Lock()
	var expired []*Receipt
	for k, r := range n.receipts {
		if now.After(r.Timeout) {
			delete(n.receipts, k)
			expired = append(expired, r)
		}
	}
	n.mu.Unlock()
	for _, r := range expired {
		select {
		case r.done <- ReceiptResult{}:
		default:
		}
	}
}
