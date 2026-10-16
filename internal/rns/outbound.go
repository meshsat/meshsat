package rns

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"meshsat/internal/reticulum"
)

// ErrUnknownDestination is returned when no announce has been seen for a
// destination, so it cannot be encrypted to.
var ErrUnknownDestination = errors.New("rns: destination identity unknown")

// outbound sends a locally originated non-link packet (Transport.outbound):
// along the path when one is known (HEADER_2 insertion beyond one hop),
// otherwise broadcast on every floodable interface. Returns the interface(s)
// used.
func (n *Node) outbound(raw []byte, dest [HashLen]byte, destType byte) ([]string, error) {
	return n.outboundWith(raw, dest, destType, Constraint{})
}

// outboundWith is outbound under a constraint: a known path on an
// interface the constraint refuses sends nothing (ErrPaidRefused); the
// broadcast without a path goes on the floodable interfaces, which are the
// free ones. [MESHSAT-1584]
func (n *Node) outboundWith(raw []byte, dest [HashLen]byte, destType byte, c Constraint) ([]string, error) {
	hdr, err := reticulum.UnmarshalHeader(raw)
	if err != nil {
		return nil, err
	}
	if hdr.PacketType != reticulum.PacketAnnounce && destType != reticulum.DestPlain && destType != reticulum.DestGroup {
		if e := n.paths.Get(dest); e != nil && n.now().Before(e.Expires) {
			if !c.allows(n, e.Iface) {
				return nil, ErrPaidRefused
			}
			out := raw
			if !e.Direct() {
				out = reticulum.RewriteForTransport(raw, e.NextHop)
			}
			e.Timestamp = n.now()
			return []string{e.Iface}, n.transmit(e.Iface, out)
		}
	}
	ifaces := n.tx.Floodable()
	if len(ifaces) == 0 {
		return nil, ErrNoPath
	}
	for _, id := range ifaces {
		n.transmit(id, raw)
	}
	return ifaces, nil
}

// HopsTo returns the known hop count to a destination or -1.
func (n *Node) HopsTo(dest [HashLen]byte) int { return n.paths.Hops(dest) }

// NextHopInterface returns the interface a destination is reached on.
func (n *Node) NextHopInterface(dest [HashLen]byte) string {
	if e := n.paths.Get(dest); e != nil {
		return e.Iface
	}
	return ""
}

// SendData encrypts plaintext for a remote SINGLE destination and sends it
// as one DATA packet (Packet with destination.encrypt). The destination
// must be known from an announce. The returned Receipt resolves when the
// destination's proof arrives.
func (n *Node) SendData(dest [HashLen]byte, plaintext []byte, wantReceipt bool) (*Receipt, error) {
	return n.SendDataWith(dest, plaintext, wantReceipt, Constraint{})
}

// SendDataWith is SendData under a constraint (outboundWith). [MESHSAT-1584]
func (n *Node) SendDataWith(dest [HashLen]byte, plaintext []byte, wantReceipt bool, c Constraint) (*Receipt, error) {
	return n.SendDataPrepared(dest, plaintext, wantReceipt, c, nil)
}

// PrepareFunc gets a packet's hash and the key that must sign its proof
// once the packet is built and before it is written; an error stops the
// send with nothing written. A caller stores them here so a proof that
// comes after any failure or restart still finds its transmission (A-13,
// red-team A13-R1). [MESHSAT-1584]
type PrepareFunc func(hash [FullHashLen]byte, key ed25519.PublicKey) error

// ErrNotPrepared wraps a PrepareFunc's error: nothing was written.
var ErrNotPrepared = errors.New("rns: the send was not prepared; nothing was written")

// SendDataPrepared is SendDataWith with a prepare step before the write
// (wantReceipt is implied when prepare is set).
func (n *Node) SendDataPrepared(dest [HashLen]byte, plaintext []byte, wantReceipt bool, c Constraint, prepare PrepareFunc) (*Receipt, error) {
	if prepare != nil {
		wantReceipt = true
	}
	k := n.Recall(dest)
	if k == nil || k.Identity == nil {
		return nil, ErrUnknownDestination
	}
	if len(plaintext) > reticulum.EncryptedMDU {
		return nil, fmt.Errorf("rns: %d bytes exceed the single-packet limit of %d", len(plaintext), reticulum.EncryptedMDU)
	}
	var ratchet *ecdh.PublicKey
	if len(k.Ratchet) == reticulum.RatchetKeyLen && n.now().Before(k.RatchetAt.Add(30*24*time.Hour)) {
		ratchet, _ = ecdh.X25519().NewPublicKey(k.Ratchet)
	}
	ct, err := k.Identity.Encrypt(plaintext, ratchet)
	if err != nil {
		return nil, err
	}
	h := &reticulum.Header{HeaderType: reticulum.HeaderType1, DestType: reticulum.DestSingle, PacketType: reticulum.PacketData,
		DestHash: dest, Context: reticulum.ContextNone, Data: ct}
	raw := h.Marshal()
	var r *Receipt
	if wantReceipt {
		hops := n.paths.Hops(dest)
		if hops < 1 {
			hops = 1
		}
		r = &Receipt{Hash: reticulum.PacketHash(raw), Dest: dest, SentAt: n.now(),
			Timeout: n.now().Add(time.Duration(reticulum.DefaultPerHopTimeoutSec*hops)*time.Second + 6*time.Second),
			sigPub:  k.Identity.SigningPublicKey()}
		if prepare != nil {
			if err := prepare(r.Hash, r.sigPub); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrNotPrepared, err)
			}
		}
		n.addReceipt(r)
	}
	if _, err := n.outboundWith(raw, dest, reticulum.DestSingle, c); err != nil {
		if r != nil {
			n.mu.Lock()
			delete(n.receipts, r.Hash)
			n.mu.Unlock()
		}
		return nil, err
	}
	return r, nil
}

// SendRaw transmits an already-built packet for dest along the path rules.
func (n *Node) SendRaw(raw []byte, dest [HashLen]byte, destType byte) error {
	_, err := n.outbound(raw, dest, destType)
	return err
}
