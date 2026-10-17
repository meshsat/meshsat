package engine

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
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

// AcceptCustody queues the payload of a custody offer this node takes, for
// its own mesh: on the enabled interface of channel type "mesh" (mesh_0),
// whose worker sends it. It returns the row's id; it queues nothing and
// fails when there is no enabled mesh interface or the worker could not
// send the payload whole (custodyFits), and then no ACK may go. The accept
// used to queue on channel "mesh", which no worker serves (workers run per
// interface id), while the ACK told the offering node custody was taken:
// the payload was lost with nobody told (r3 F13, B12). [MESHSAT-1584]
func (d *Dispatcher) AcceptCustody(payload []byte) (int64, error) {
	ifaces, err := d.db.GetInterfacesByType("mesh")
	if err != nil {
		return 0, fmt.Errorf("custody: look up the mesh interface: %w", err)
	}
	for _, iface := range ifaces {
		if !iface.Enabled {
			continue
		}
		if err := d.custodyFits(iface, payload); err != nil {
			return 0, err
		}
		// Custody-transferred bundles carry no precedence on the wire;
		// "" lets the schema default (Routine) win.
		id, _, err := d.QueueDirectSend(iface.ID, string(payload), "")
		return id, err
	}
	return 0, fmt.Errorf("custody: no enabled mesh interface to deliver to")
}

// custodyFits says whether the mesh worker sends this payload whole, the
// contract of this first, text-only version (review B12-R1). The worker
// sends a direct row as a text message: with no egress transforms that is
// the row's preview, which keeps directPreviewMax bytes; with transforms it
// is their output plus the version byte. Either must fit the mesh's
// payload limit. A payload that is not UTF-8 text is not taken: the mesh
// carries it as a text message.
func (d *Dispatcher) custodyFits(iface database.Interface, payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("custody: the payload is empty")
	}
	if !utf8.Valid(payload) {
		return fmt.Errorf("custody: the payload is not text; this node takes custody of text only, which its mesh sends as a text message")
	}
	limit := 237
	if d.registry != nil {
		if desc, ok := d.registry.Get("mesh"); ok && desc.MaxPayload > 0 {
			limit = desc.MaxPayload
		}
	}
	if chain := iface.EgressTransforms; chain != "" && chain != "[]" {
		if d.transforms == nil {
			return fmt.Errorf("custody: %s has egress transforms and no transform pipeline", iface.ID)
		}
		out, err := d.transforms.ApplyEgress(payload, chain)
		if err != nil {
			return fmt.Errorf("custody: %s's egress transforms failed: %w", iface.ID, err)
		}
		if n := len(out) + 1; n > limit {
			return fmt.Errorf("custody: %d bytes become %d on %s after its transforms; it takes %d", len(payload), n, iface.ID, limit)
		}
		return nil
	}
	if len(payload) > directPreviewMax || len(payload) > limit {
		return fmt.Errorf("custody: %d bytes; %s's worker sends %d whole", len(payload), iface.ID, min(directPreviewMax, limit))
	}
	return nil
}

// custodyRepeatWindow is how long a custody id this node took is
// remembered: an offering node whose ACK was lost offers the same id again
// with its delivery's retries.
const custodyRepeatWindow = 24 * time.Hour

// CustodyAcceptor takes the custody offers that reach this node (the
// offer's whole path: decode, take, ACK), so the ACK boundary is one piece
// of code and testable (review B12-R1). The ACK goes only once the payload
// is queued whole. An offer whose id it took within the window is ACKed
// again and not queued again: the offering node missed the first ACK. The
// memory is this process's only; after a restart a repeat is taken again.
type CustodyAcceptor struct {
	d   *Dispatcher
	ack func(sourceIface string, offer *CustodyOffer) error
	now func() time.Time

	mu   sync.Mutex
	seen map[[16]byte]time.Time
}

// CustodyTake is what one offer came to.
type CustodyTake struct {
	RowID  int64 // the queued row; 0 for a repeat
	Repeat bool  // an id taken within the window
	Acked  bool  // the ACK went
}

// NewCustodyAcceptor makes an acceptor over d that ACKs through ack.
func NewCustodyAcceptor(d *Dispatcher, ack func(sourceIface string, offer *CustodyOffer) error) *CustodyAcceptor {
	return &CustodyAcceptor{d: d, ack: ack, now: time.Now, seen: map[[16]byte]time.Time{}}
}

// HandleOffer takes one offer packet that arrived on sourceIface. An error
// means nothing was taken and no ACK went.
func (a *CustodyAcceptor) HandleOffer(data []byte, sourceIface string) (CustodyTake, error) {
	offer, err := UnmarshalCustodyOffer(data)
	if err != nil {
		return CustodyTake{}, err
	}
	a.mu.Lock()
	now := a.now()
	if at, ok := a.seen[offer.CustodyID]; ok && now.Sub(at) < custodyRepeatWindow {
		a.mu.Unlock()
		return CustodyTake{Repeat: true, Acked: a.sendACK(sourceIface, offer)}, nil
	}
	// The check, the queue and the memory are one step: two copies of one
	// offer arriving together cannot both be queued.
	id, err := a.d.AcceptCustody(offer.Payload)
	if err != nil {
		a.mu.Unlock()
		return CustodyTake{}, err
	}
	a.seen[offer.CustodyID] = now
	if len(a.seen) > 1024 {
		for k, at := range a.seen {
			if now.Sub(at) >= custodyRepeatWindow {
				delete(a.seen, k)
			}
		}
	}
	a.mu.Unlock()
	return CustodyTake{RowID: id, Acked: a.sendACK(sourceIface, offer)}, nil
}

func (a *CustodyAcceptor) sendACK(sourceIface string, offer *CustodyOffer) bool {
	if a.ack == nil {
		return false
	}
	if err := a.ack(sourceIface, offer); err != nil {
		log.Warn().Err(err).Str("custody_id", fmt.Sprintf("%x", offer.CustodyID[:8])).Msg("DTN: custody ACK not sent")
		return false
	}
	return true
}
