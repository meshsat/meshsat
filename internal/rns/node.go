// Package rns implements the Reticulum Network Stack node semantics on top of
// the wire formats in internal/reticulum: packet filtering and deduplication,
// the path table with RNS replacement rules, announce rebroadcast as a
// transport node, HEADER_2 forwarding with link and reverse tables, path
// requests, links, packet proofs and receipts. It is written against Python
// RNS 1.5.4 (RNS/Transport.py, RNS/Link.py) and verified against it by the
// tests in internal/interop.
//
// The node knows nothing about serial ports or sockets: a Transmitter puts
// raw packets on named interfaces and the owner feeds received packets to
// Inbound. Paid bearers never appear in Transmitter.Floodable, so nothing in
// here can flood them.
package rns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// Hash sizes used throughout.
const (
	HashLen     = reticulum.TruncatedHashLen
	FullHashLen = reticulum.FullHashLen
)

// Transmitter is the node's view of the interfaces.
type Transmitter interface {
	// Transmit puts one raw packet on the named interface.
	Transmit(ifaceID string, raw []byte) error
	// Floodable lists the online interfaces that may carry broadcast
	// traffic (announces, path requests, packets with no known path).
	// Paid bearers must never be listed.
	Floodable() []string
	// HWMTU returns the interface's hardware MTU, or 0 when unknown.
	HWMTU(ifaceID string) int
	// Bitrate returns the interface bit rate in bits per second, or 0 when
	// unknown or effectively unlimited (no announce cap applied).
	Bitrate(ifaceID string) int
}

// Config configures a Node.
type Config struct {
	Identity *reticulum.Identity
	// ForwardHeader1From lists interfaces whose HEADER_1 packets for a
	// destination that is not local are forwarded along a known path even
	// though a stock RNS node would not (its peers cannot address a
	// transport). MeshSat extension for the Meshtastic bearer.
	ForwardHeader1From map[string]bool
	// PathTTL overrides PATHFINDER_E (one week) when > 0.
	PathTTL time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Packet is a received packet handed to destination and link callbacks.
type Packet struct {
	Raw   []byte
	Hdr   *reticulum.Header
	Hash  [FullHashLen]byte
	Iface string
}

// TruncatedHash returns the first 16 bytes of the packet hash.
func (p *Packet) TruncatedHash() [HashLen]byte {
	var t [HashLen]byte
	copy(t[:], p.Hash[:HashLen])
	return t
}

// Node is one Reticulum transport instance.
type Node struct {
	cfg    Config
	tx     Transmitter
	now    func() time.Time
	idHash [HashLen]byte

	mu           sync.Mutex
	dests        map[[HashLen]byte]*Destination
	paths        *PathTable
	identities   map[[HashLen]byte]*KnownIdentity
	hashlist     map[[FullHashLen]byte]struct{}
	hashlistPrev map[[FullHashLen]byte]struct{}

	announceTable map[[HashLen]byte]*announceEntry
	heldAnnounces map[[HashLen]byte]*announceEntry
	announceQueue map[string]*announceQueue

	linkTable    map[[HashLen]byte]*linkEntry
	reverseTable map[[HashLen]byte]*reverseEntry

	discoveryPRs map[[HashLen]byte]*discoveryEntry
	prTags       map[[2 * HashLen]byte]struct{}
	prTagsPrev   map[[2 * HashLen]byte]struct{}
	inflightPRs  map[[HashLen]byte]time.Time
	pathRequests map[[HashLen]byte]time.Time
	pathWaiters  map[[HashLen]byte][]chan struct{}

	receipts map[[FullHashLen]byte]*Receipt
	ifac     map[string]*reticulum.IFAC

	links *LinkManager

	// OnAnnounce is called for every valid announce that was accepted into
	// the path table (not for our own destinations).
	OnAnnounce func(ann *reticulum.Announce, pkt *Packet)
	// OnPathChanged is called when a path table entry is added or replaced.
	OnPathChanged func(dest [HashLen]byte, entry *PathEntry)

	stats Stats
}

// Stats are monotonic counters for the API.
type Stats struct {
	RxPackets, TxPackets     uint64
	Dropped, Filtered        uint64
	AnnouncesRx, AnnouncesTx uint64
	Forwarded, LinkTransit   uint64
	PathRequestsRx           uint64
	IFACViolations           uint64
}

// ErrNoPath is returned when a packet cannot be delivered.
var ErrNoPath = errors.New("rns: no path to destination")

// New creates a node. Register destinations with AddDestination before Start.
func New(cfg Config, tx Transmitter) (*Node, error) {
	if cfg.Identity == nil || !cfg.Identity.HasPrivateKey() {
		return nil, errors.New("rns: identity with a private key is required")
	}
	if tx == nil {
		return nil, errors.New("rns: transmitter is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	ttl := cfg.PathTTL
	if ttl <= 0 {
		ttl = reticulum.PathfinderE * time.Second
	}
	n := &Node{
		cfg:           cfg,
		tx:            tx,
		now:           now,
		idHash:        cfg.Identity.IdentityHash(),
		dests:         make(map[[HashLen]byte]*Destination),
		paths:         NewPathTable(ttl, now),
		identities:    make(map[[HashLen]byte]*KnownIdentity),
		hashlist:      make(map[[FullHashLen]byte]struct{}),
		hashlistPrev:  make(map[[FullHashLen]byte]struct{}),
		announceTable: make(map[[HashLen]byte]*announceEntry),
		heldAnnounces: make(map[[HashLen]byte]*announceEntry),
		announceQueue: make(map[string]*announceQueue),
		linkTable:     make(map[[HashLen]byte]*linkEntry),
		reverseTable:  make(map[[HashLen]byte]*reverseEntry),
		discoveryPRs:  make(map[[HashLen]byte]*discoveryEntry),
		prTags:        make(map[[2 * HashLen]byte]struct{}),
		prTagsPrev:    make(map[[2 * HashLen]byte]struct{}),
		inflightPRs:   make(map[[HashLen]byte]time.Time),
		pathRequests:  make(map[[HashLen]byte]time.Time),
		pathWaiters:   make(map[[HashLen]byte][]chan struct{}),
		receipts:      make(map[[FullHashLen]byte]*Receipt),
		ifac:          make(map[string]*reticulum.IFAC),
	}
	n.links = newLinkManager(n)
	return n, nil
}

// IdentityHash is the transport id this node writes into HEADER_2 packets.
func (n *Node) IdentityHash() [HashLen]byte { return n.idHash }

// Identity returns the node identity.
func (n *Node) Identity() *reticulum.Identity { return n.cfg.Identity }

// Links returns the link manager.
func (n *Node) Links() *LinkManager { return n.links }

// Paths returns the path table.
func (n *Node) Paths() *PathTable { return n.paths }

// Stats returns a snapshot of the counters.
func (n *Node) Stats() Stats {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.stats
}

// SetIFAC enables interface access codes on one interface (nil disables).
func (n *Node) SetIFAC(ifaceID string, f *reticulum.IFAC) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if f == nil {
		delete(n.ifac, ifaceID)
		return
	}
	n.ifac[ifaceID] = f
}

// Start runs the periodic jobs until ctx is done.
func (n *Node) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				n.links.closeAll()
				return
			case <-t.C:
				n.jobs()
			}
		}
	}()
}

// ---------------------------------------------------------------------------
// Local destinations and known identities
// ---------------------------------------------------------------------------

// Destination is a local SINGLE destination the node answers for.
type Destination struct {
	Name     string
	Hash     [HashLen]byte
	Identity *reticulum.Identity
	// AcceptLinks allows peers to open links to this destination.
	AcceptLinks bool
	// ProveAll answers every delivered packet with a proof (LXMF needs it).
	ProveAll bool
	// AppData supplies the announce app data, may be nil.
	AppData func() []byte
	// OnPacket receives decrypted single-packet data.
	OnPacket func(plain []byte, pkt *Packet)
	// OnLink is called when a peer's link to this destination becomes active.
	OnLink func(l *Link)
	// OnLinkPacket receives decrypted data on links to this destination.
	OnLinkPacket func(l *Link, plain []byte, pkt *Packet)
	// OnLinkClosed is called when such a link closes.
	OnLinkClosed func(l *Link)

	ratchetMu sync.Mutex
	ratchets  [][]byte // newest first
}

// AddDestination registers a local destination; its hash is computed from
// the identity and name.
func (n *Node) AddDestination(d *Destination) *Destination {
	if d.Identity == nil {
		d.Identity = n.cfg.Identity
	}
	d.Hash = d.Identity.DestHash(d.Name)
	n.mu.Lock()
	n.dests[d.Hash] = d
	n.mu.Unlock()
	return d
}

// LocalDestination returns the local destination for a hash, or nil.
func (n *Node) LocalDestination(h [HashLen]byte) *Destination {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dests[h]
}

// KnownIdentity is what an announce taught us about a remote destination.
type KnownIdentity struct {
	DestHash  [HashLen]byte
	PublicKey []byte // 64 bytes
	Identity  *reticulum.Identity
	AppData   []byte
	Ratchet   []byte // 32-byte x25519 pub or nil
	RatchetAt time.Time
	LastSeen  time.Time
}

// Recall returns what is known about a destination, or nil.
func (n *Node) Recall(dest [HashLen]byte) *KnownIdentity {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.identities[dest]
}

// Remember stores a destination's public key without an announce (e.g. a
// row loaded from the database or a link-identified peer).
func (n *Node) Remember(dest [HashLen]byte, pub []byte, appData []byte) error {
	id, err := reticulum.IdentityFromPublicBytes(pub)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	k := n.identities[dest]
	if k == nil {
		k = &KnownIdentity{DestHash: dest}
		n.identities[dest] = k
	}
	k.PublicKey = append([]byte(nil), pub...)
	k.Identity = id
	if appData != nil {
		k.AppData = append([]byte(nil), appData...)
	}
	k.LastSeen = n.now()
	return nil
}

// ---------------------------------------------------------------------------
// Inbound
// ---------------------------------------------------------------------------

// Inbound processes one raw packet received on ifaceID. It returns true when
// the packet was consumed by the RNS layer and false when it is not an RNS
// packet the node handles (the caller may then try legacy MeshSat framing).
func (n *Node) Inbound(raw []byte, ifaceID string) bool {
	if len(raw) < reticulum.HeaderMinSize {
		return false
	}
	// Interface access codes.
	n.mu.Lock()
	f := n.ifac[ifaceID]
	n.mu.Unlock()
	if f != nil {
		clean, err := f.Unwrap(raw)
		if err != nil {
			n.count(func(s *Stats) { s.IFACViolations++ })
			return true
		}
		raw = clean
	} else if reticulum.HasIFACFlag(raw) {
		n.count(func(s *Stats) { s.IFACViolations++ })
		return true
	}

	hdr, err := reticulum.UnmarshalHeader(raw)
	if err != nil {
		return false
	}
	pkt := &Packet{Raw: raw, Hdr: hdr, Hash: reticulum.PacketHash(raw), Iface: ifaceID}
	n.count(func(s *Stats) { s.RxPackets++ })

	if !n.packetFilter(pkt) {
		n.count(func(s *Stats) { s.Filtered++ })
		return true
	}

	// hops++ as Transport.inbound does before anything else.
	hdr.Hops++
	raw[1] = hdr.Hops

	n.mu.Lock()
	_, inLinkTable := n.linkTable[hdr.DestHash]
	remember := !inLinkTable && !(hdr.PacketType == reticulum.PacketProof && hdr.Context == reticulum.ContextLRProof)
	if remember {
		n.hashlist[pkt.Hash] = struct{}{}
	}
	n.mu.Unlock()

	// Packets in transport: are we the designated next hop?
	if hdr.HeaderType == reticulum.HeaderType2 && hdr.PacketType != reticulum.PacketAnnounce {
		if hdr.TransportID != n.idHash {
			return true
		}
		if n.forwardTransport(pkt) {
			return true
		}
		// No path: Python drops; a link request to a local destination
		// carrying our own transport id still gets delivered below.
		if hdr.PacketType != reticulum.PacketLinkRequest {
			return true
		}
	}

	// Link transport: packets for links we carry in transit.
	if hdr.PacketType != reticulum.PacketAnnounce && hdr.PacketType != reticulum.PacketLinkRequest && hdr.Context != reticulum.ContextLRProof {
		if n.linkTransit(pkt) {
			return true
		}
	}

	switch hdr.PacketType {
	case reticulum.PacketAnnounce:
		return n.handleAnnounce(pkt)

	case reticulum.PacketLinkRequest:
		if hdr.DestType != reticulum.DestSingle {
			return true
		}
		if d := n.LocalDestination(hdr.DestHash); d != nil {
			n.links.accept(d, pkt)
			return true
		}
		return n.forwardHeader1(pkt)

	case reticulum.PacketData:
		switch hdr.DestType {
		case reticulum.DestLink:
			if l := n.links.active(hdr.DestHash); l != nil {
				n.links.receive(l, pkt)
				return true
			}
			// Legacy MeshSat resource contexts on a LINK dest are not ours.
			return false
		case reticulum.DestPlain:
			if hdr.DestHash == reticulum.PathRequestDestHash() {
				n.handlePathRequest(pkt)
				return true
			}
			return false // legacy MeshSat path request/response, HeMB, etc.
		case reticulum.DestSingle:
			if d := n.LocalDestination(hdr.DestHash); d != nil {
				n.deliverLocal(d, pkt)
				return true
			}
			return n.forwardHeader1(pkt)
		}
		return false

	case reticulum.PacketProof:
		switch hdr.Context {
		case reticulum.ContextLRProof:
			if n.lrProofTransit(pkt) {
				return true
			}
			n.links.handleProof(pkt)
			return true
		case reticulum.ContextResourcePRF:
			if l := n.links.active(hdr.DestHash); l != nil {
				n.links.receive(l, pkt)
				return true
			}
			return false
		default:
			n.proofTransit(pkt)
			n.handleReceiptProof(pkt)
			return true
		}
	}
	return false
}

// packetFilter is Transport.packet_filter for a transport instance.
func (n *Node) packetFilter(pkt *Packet) bool {
	h := pkt.Hdr
	if h.HeaderType == reticulum.HeaderType2 && h.PacketType != reticulum.PacketAnnounce && h.TransportID != n.idHash {
		return false
	}
	switch h.Context {
	case reticulum.ContextKeepalive, reticulum.ContextResourceReq, reticulum.ContextResourcePRF,
		reticulum.ContextResource, reticulum.ContextCacheRequest, reticulum.ContextChannel:
		return true
	}
	if h.DestType == reticulum.DestPlain || h.DestType == reticulum.DestGroup {
		if h.PacketType == reticulum.PacketAnnounce {
			return false
		}
		return h.Hops <= 1
	}
	n.mu.Lock()
	_, seen := n.hashlist[pkt.Hash]
	if !seen {
		_, seen = n.hashlistPrev[pkt.Hash]
	}
	n.mu.Unlock()
	if !seen {
		return true
	}
	return h.PacketType == reticulum.PacketAnnounce && h.DestType == reticulum.DestSingle
}

// deliverLocal decrypts a single-packet DATA for a local destination.
func (n *Node) deliverLocal(d *Destination, pkt *Packet) {
	if pkt.Hdr.Context != reticulum.ContextNone {
		return
	}
	d.ratchetMu.Lock()
	ratchets := d.ratchets
	d.ratchetMu.Unlock()
	plain, _, err := d.Identity.Decrypt(pkt.Hdr.Data, ratchets)
	if err != nil {
		log.Debug().Str("dest", d.Name).Str("iface", pkt.Iface).Err(err).Msg("rns: could not decrypt packet for local destination")
		return
	}
	if d.OnPacket != nil {
		d.OnPacket(plain, pkt)
	}
	if d.ProveAll {
		n.prove(d, pkt)
	}
}

// prove answers a delivered packet with an implicit proof on the receiving
// interface, as Identity.prove does.
func (n *Node) prove(d *Destination, pkt *Packet) {
	proof := reticulum.BuildProofPacket(pkt.Raw, d.Identity.ImplicitProof(pkt.Hash))
	n.transmit(pkt.Iface, proof)
}

func (n *Node) count(f func(*Stats)) {
	n.mu.Lock()
	f(&n.stats)
	n.mu.Unlock()
}

func (n *Node) transmit(ifaceID string, raw []byte) error {
	n.mu.Lock()
	f := n.ifac[ifaceID]
	n.stats.TxPackets++
	n.mu.Unlock()
	if f != nil {
		raw = f.Wrap(raw)
	}
	if err := n.tx.Transmit(ifaceID, raw); err != nil {
		log.Debug().Err(err).Str("iface", ifaceID).Msg("rns: transmit failed")
		return err
	}
	return nil
}

func randomHash() [HashLen]byte {
	var h [HashLen]byte
	_, _ = rand.Read(h[:])
	return h
}

func hexh(h [HashLen]byte) string { return hex.EncodeToString(h[:]) }

// jobs runs the periodic maintenance (Transport.jobs).
func (n *Node) jobs() {
	now := n.now()
	n.processAnnounceTable(now)
	n.expireTables(now)
	n.expireReceipts(now)
	n.expireDiscovery(now)
	n.links.watchdog(now)
	n.mu.Lock()
	if len(n.hashlist) > 50000 {
		n.hashlistPrev = n.hashlist
		n.hashlist = make(map[[FullHashLen]byte]struct{})
	}
	if len(n.prTags) > 20000 {
		n.prTagsPrev = n.prTags
		n.prTags = make(map[[2 * HashLen]byte]struct{})
	}
	n.mu.Unlock()
}
