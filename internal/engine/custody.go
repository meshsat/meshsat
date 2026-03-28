package engine

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Custody Transfer Protocol
//
// Implements DTN-style custody transfer where a relay node accepts responsibility
// for delivering a bundle. The custodian signs an ACK proving it accepted custody.
//
// Wire formats:
//   Offer: [0x16][16B custody_id UUID][16B source_dest_hash][4B delivery_id LE][payload]
//   ACK:   [0x17][16B custody_id UUID][16B acceptor_dest_hash][64B Ed25519 signature]

const (
	// CustodyOfferType is the wire prefix byte for custody offer packets.
	CustodyOfferType byte = 0x16

	// CustodyACKType is the wire prefix byte for custody ACK packets.
	CustodyACKType byte = 0x17

	// custodyOfferHeaderLen is 1 (type) + 16 (custody_id) + 16 (source_hash) + 4 (delivery_id) = 37
	custodyOfferHeaderLen = 37

	// custodyACKLen is 1 (type) + 16 (custody_id) + 16 (acceptor_hash) + 64 (signature) = 97
	custodyACKLen = 97
)

// CustodyOffer represents a custody transfer offer from a source node.
type CustodyOffer struct {
	CustodyID  [16]byte // Random UUID identifying this custody transfer
	SourceHash [16]byte // Destination hash of the offering node
	DeliveryID uint32   // Delivery ID from message_deliveries
	Payload    []byte   // The bundle payload being offered for custody
}

// CustodyACK represents a custody acceptance acknowledgment from a relay node.
type CustodyACK struct {
	CustodyID    [16]byte // Matches the CustodyOffer.CustodyID
	AcceptorHash [16]byte // Destination hash of the accepting node
	Signature    [64]byte // Ed25519 signature over CustodyID + AcceptorHash
}

// MarshalCustodyOffer serializes a CustodyOffer to wire format.
func MarshalCustodyOffer(o *CustodyOffer) []byte {
	buf := make([]byte, custodyOfferHeaderLen+len(o.Payload))
	buf[0] = CustodyOfferType
	copy(buf[1:17], o.CustodyID[:])
	copy(buf[17:33], o.SourceHash[:])
	binary.LittleEndian.PutUint32(buf[33:37], o.DeliveryID)
	copy(buf[37:], o.Payload)
	return buf
}

// UnmarshalCustodyOffer deserializes a CustodyOffer from wire format.
func UnmarshalCustodyOffer(data []byte) (*CustodyOffer, error) {
	if len(data) < custodyOfferHeaderLen {
		return nil, fmt.Errorf("custody offer too short: %d bytes (minimum %d)", len(data), custodyOfferHeaderLen)
	}
	if data[0] != CustodyOfferType {
		return nil, fmt.Errorf("invalid custody offer type: 0x%02x (expected 0x%02x)", data[0], CustodyOfferType)
	}

	o := &CustodyOffer{}
	copy(o.CustodyID[:], data[1:17])
	copy(o.SourceHash[:], data[17:33])
	o.DeliveryID = binary.LittleEndian.Uint32(data[33:37])

	if len(data) > custodyOfferHeaderLen {
		o.Payload = make([]byte, len(data)-custodyOfferHeaderLen)
		copy(o.Payload, data[custodyOfferHeaderLen:])
	}
	return o, nil
}

// MarshalCustodyACK serializes a CustodyACK to wire format.
func MarshalCustodyACK(a *CustodyACK) []byte {
	buf := make([]byte, custodyACKLen)
	buf[0] = CustodyACKType
	copy(buf[1:17], a.CustodyID[:])
	copy(buf[17:33], a.AcceptorHash[:])
	copy(buf[33:97], a.Signature[:])
	return buf
}

// UnmarshalCustodyACK deserializes a CustodyACK from wire format.
func UnmarshalCustodyACK(data []byte) (*CustodyACK, error) {
	if len(data) < custodyACKLen {
		return nil, fmt.Errorf("custody ACK too short: %d bytes (expected %d)", len(data), custodyACKLen)
	}
	if data[0] != CustodyACKType {
		return nil, fmt.Errorf("invalid custody ACK type: 0x%02x (expected 0x%02x)", data[0], CustodyACKType)
	}

	a := &CustodyACK{}
	copy(a.CustodyID[:], data[1:17])
	copy(a.AcceptorHash[:], data[17:33])
	copy(a.Signature[:], data[33:97])
	return a, nil
}

// SignCustodyACK creates a signed CustodyACK for the given custody ID.
// The signature covers CustodyID + AcceptorHash.
func SignCustodyACK(custodyID, acceptorHash [16]byte, privKey ed25519.PrivateKey) *CustodyACK {
	ack := &CustodyACK{
		CustodyID:    custodyID,
		AcceptorHash: acceptorHash,
	}
	msg := make([]byte, 32)
	copy(msg[:16], custodyID[:])
	copy(msg[16:], acceptorHash[:])
	sig := ed25519.Sign(privKey, msg)
	copy(ack.Signature[:], sig)
	return ack
}

// VerifyCustodyACK verifies the Ed25519 signature on a CustodyACK.
// The pubKey must correspond to the acceptor.
func VerifyCustodyACK(ack *CustodyACK, pubKey ed25519.PublicKey) bool {
	msg := make([]byte, 32)
	copy(msg[:16], ack.CustodyID[:])
	copy(msg[16:], ack.AcceptorHash[:])
	return ed25519.Verify(pubKey, msg, ack.Signature[:])
}

// NewCustodyID generates a random 16-byte UUID for a custody transfer.
func NewCustodyID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("generate custody ID: %w", err)
	}
	// Set version 4 (random) UUID bits
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id, nil
}

// IsCustodyOffer checks if a byte slice starts with the custody offer type byte.
func IsCustodyOffer(data []byte) bool {
	return len(data) >= custodyOfferHeaderLen && data[0] == CustodyOfferType
}

// IsCustodyACK checks if a byte slice starts with the custody ACK type byte.
func IsCustodyACK(data []byte) bool {
	return len(data) >= custodyACKLen && data[0] == CustodyACKType
}

// CustodyState tracks the state of a custody transfer.
type CustodyState int

const (
	CustodyOffered  CustodyState = iota // Offer sent, waiting for ACK
	CustodyAccepted                     // ACK received, custody transferred
	CustodyExpired                      // Timed out waiting for ACK
)

// String returns the human-readable name of a CustodyState.
func (s CustodyState) String() string {
	switch s {
	case CustodyOffered:
		return "offered"
	case CustodyAccepted:
		return "accepted"
	case CustodyExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// pendingCustody tracks a single pending custody offer.
type pendingCustody struct {
	offer     *CustodyOffer
	state     CustodyState
	offeredAt time.Time
	ackCh     chan *CustodyACK // signals when ACK is received
}

// CustodyManager manages outbound custody offers and inbound ACKs.
// It tracks pending offers and matches incoming ACKs by custody ID.
type CustodyManager struct {
	mu      sync.Mutex
	pending map[[16]byte]*pendingCustody // keyed by CustodyID
	timeout time.Duration                // how long to wait for ACK
}

// NewCustodyManager creates a new CustodyManager with the given ACK timeout.
func NewCustodyManager(ackTimeout time.Duration) *CustodyManager {
	return &CustodyManager{
		pending: make(map[[16]byte]*pendingCustody),
		timeout: ackTimeout,
	}
}

// RegisterOffer records a pending custody offer. Returns a channel that will
// receive the ACK when it arrives, or be closed on timeout.
func (cm *CustodyManager) RegisterOffer(offer *CustodyOffer) <-chan *CustodyACK {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	ch := make(chan *CustodyACK, 1)
	cm.pending[offer.CustodyID] = &pendingCustody{
		offer:     offer,
		state:     CustodyOffered,
		offeredAt: time.Now(),
		ackCh:     ch,
	}
	return ch
}

// HandleACK processes an incoming custody ACK. Returns true if the ACK
// matched a pending offer, false if no matching offer was found.
func (cm *CustodyManager) HandleACK(ack *CustodyACK) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	p, ok := cm.pending[ack.CustodyID]
	if !ok {
		return false
	}
	if p.state != CustodyOffered {
		return false
	}

	p.state = CustodyAccepted
	select {
	case p.ackCh <- ack:
	default:
	}
	close(p.ackCh)
	return true
}

// Reap expires pending offers older than the configured timeout.
// Returns the number of offers expired.
func (cm *CustodyManager) Reap() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	now := time.Now()
	expired := 0
	for id, p := range cm.pending {
		if p.state == CustodyOffered && now.Sub(p.offeredAt) > cm.timeout {
			p.state = CustodyExpired
			close(p.ackCh)
			delete(cm.pending, id)
			expired++
			log.Debug().Hex("custody_id", id[:]).Msg("custody offer expired")
		}
	}
	return expired
}

// PendingCount returns the number of offers currently awaiting ACKs.
func (cm *CustodyManager) PendingCount() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	count := 0
	for _, p := range cm.pending {
		if p.state == CustodyOffered {
			count++
		}
	}
	return count
}

// Clear removes all accepted or expired entries from the pending map.
func (cm *CustodyManager) Clear() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, p := range cm.pending {
		if p.state != CustodyOffered {
			delete(cm.pending, id)
		}
	}
}
