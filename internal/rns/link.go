package rns

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/msgpack"
	"meshsat/internal/reticulum"
)

// LinkState is the state of a Link (RNS.Link status).
type LinkState int

const (
	LinkPending LinkState = iota
	LinkHandshake
	LinkActive
	LinkStale
	LinkClosed
)

func (s LinkState) String() string {
	switch s {
	case LinkPending:
		return "pending"
	case LinkHandshake:
		return "handshake"
	case LinkActive:
		return "active"
	case LinkStale:
		return "stale"
	default:
		return "closed"
	}
}

// Link is an RNS link, initiated by us or accepted for a local destination.
type Link struct {
	ID        [HashLen]byte
	Dest      [HashLen]byte // remote destination (initiator) or local destination (responder)
	Initiator bool
	Iface     string

	mu            sync.Mutex
	state         LinkState
	prv           *ecdh.PrivateKey
	sigPrv        ed25519.PrivateKey // ephemeral for the initiator, the identity key for the responder
	peerPub       *ecdh.PublicKey
	peerSigPub    ed25519.PublicKey
	key           []byte
	token         *reticulum.Token
	mtu, mdu      int
	rtt           time.Duration
	keepalive     time.Duration
	staleTime     time.Duration
	requestTime   time.Time
	establishTO   time.Duration
	activatedAt   time.Time
	lastInbound   time.Time
	lastOutbound  time.Time
	lastKeepalive time.Time
	lastProof     time.Time
	staleSince    time.Time
	expectedHops  int
	remoteIdent   []byte // 64-byte public key once identified
	local         *Destination
	destIdentity  *reticulum.Identity // initiator: the destination identity
	resources     *linkResources

	// AcceptResources decides whether an advertised resource of the given
	// data size is received; nil rejects every resource.
	AcceptResources func(size int) bool
	// OnResource receives a completed incoming resource.
	OnResource func(*Link, []byte)

	// callbacks (initiator side; responder side uses the Destination's)
	OnEstablished func(*Link)
	OnPacket      func(*Link, []byte, *Packet)
	OnClosed      func(*Link)
	OnIdentified  func(*Link, []byte)
	OnContext     func(*Link, *Packet) // resource and other contexts
}

// State returns the link state.
func (l *Link) State() LinkState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// RTT returns the measured round trip time.
func (l *Link) RTT() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rtt
}

// MTU returns the negotiated link MTU.
func (l *Link) MTU() int { return l.mtu }

// MDU returns the largest plaintext one link packet carries.
func (l *Link) MDU() int { return l.mdu }

// RemoteIdentity returns the identified peer's public key, or nil.
func (l *Link) RemoteIdentity() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.remoteIdent
}

// Keepalive returns the keepalive interval in use.
func (l *Link) Keepalive() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.keepalive
}

// Encrypt returns a Token for plaintext with the link key.
func (l *Link) Encrypt(plain []byte) ([]byte, error) {
	if l.token == nil {
		return nil, errors.New("rns: link has no key yet")
	}
	return l.token.Encrypt(plain)
}

// Decrypt reverses Encrypt.
func (l *Link) Decrypt(ct []byte) ([]byte, error) {
	if l.token == nil {
		return nil, errors.New("rns: link has no key yet")
	}
	return l.token.Decrypt(ct)
}

// sign signs with the link's signing key (Link.sign).
func (l *Link) sign(data []byte) []byte { return ed25519.Sign(l.sigPrv, data) }

// LinkManager holds pending and active links.
type LinkManager struct {
	n       *Node
	mu      sync.Mutex
	pending map[[HashLen]byte]*Link
	links   map[[HashLen]byte]*Link
}

func newLinkManager(n *Node) *LinkManager {
	return &LinkManager{n: n, pending: make(map[[HashLen]byte]*Link), links: make(map[[HashLen]byte]*Link)}
}

// active returns an active (or stale) link by id.
func (m *LinkManager) active(id [HashLen]byte) *Link {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.links[id]
}

// Get returns any known link by id.
func (m *LinkManager) Get(id [HashLen]byte) *Link {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.links[id]; l != nil {
		return l
	}
	return m.pending[id]
}

// All returns every pending and active link.
func (m *LinkManager) All() []*Link {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Link, 0, len(m.links)+len(m.pending))
	for _, l := range m.pending {
		out = append(out, l)
	}
	for _, l := range m.links {
		out = append(out, l)
	}
	return out
}

func (m *LinkManager) updateKeepalive(l *Link) {
	ka := time.Duration(float64(l.rtt) * (reticulum.KeepaliveMaxSec / 1.75))
	if ka > reticulum.KeepaliveMaxSec*time.Second {
		ka = reticulum.KeepaliveMaxSec * time.Second
	}
	if ka < reticulum.KeepaliveMinSec*time.Second {
		ka = reticulum.KeepaliveMinSec * time.Second
	}
	l.keepalive = ka
	l.staleTime = 2 * ka
}

// Initiate opens a link to a remote destination known from an announce.
func (m *LinkManager) Initiate(dest [HashLen]byte) (*Link, error) {
	n := m.n
	k := n.Recall(dest)
	if k == nil || k.Identity == nil {
		return nil, ErrUnknownDestination
	}
	prv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sigPub, sigPrv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	mtu := reticulum.MTU
	if ifc := n.NextHopInterface(dest); ifc != "" {
		if hw := n.tx.HWMTU(ifc); hw > 0 && hw < mtu {
			mtu = hw
		}
	}
	lr := &reticulum.RNSLinkRequest{EphPub: prv.PublicKey(), EphSigPub: sigPub, MTU: mtu, Mode: reticulum.LinkModeAES256CBC}
	h := &reticulum.Header{HeaderType: reticulum.HeaderType1, DestType: reticulum.DestSingle, PacketType: reticulum.PacketLinkRequest,
		DestHash: dest, Context: reticulum.ContextNone, Data: lr.Marshal()}
	raw := h.Marshal()
	id, err := reticulum.LinkIDFromRequestPacket(raw)
	if err != nil {
		return nil, err
	}
	hops := n.paths.Hops(dest)
	if hops < 1 {
		hops = 1
	}
	now := n.now()
	l := &Link{ID: id, Dest: dest, Initiator: true, state: LinkPending, prv: prv, sigPrv: sigPrv,
		peerSigPub: k.Identity.SigningPublicKey(), destIdentity: k.Identity, mtu: mtu, requestTime: now,
		establishTO:  time.Duration(reticulum.DefaultPerHopTimeoutSec*hops)*time.Second + 6*time.Second,
		expectedHops: hops, keepalive: reticulum.KeepaliveMaxSec * time.Second, staleTime: 2 * reticulum.KeepaliveMaxSec * time.Second}
	l.mdu = reticulum.LinkMDU(mtu)
	m.mu.Lock()
	m.pending[id] = l
	m.mu.Unlock()
	ifaces, err := n.outbound(raw, dest, reticulum.DestSingle)
	if err != nil {
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
		return nil, err
	}
	if len(ifaces) == 1 {
		l.Iface = ifaces[0]
	}
	l.lastOutbound = now
	log.Debug().Str("link", hexh(id)).Str("dest", hexh(dest)).Msg("rns: link request sent")
	return l, nil
}

// accept answers a link request for a local destination (Link.validate_request + prove).
func (m *LinkManager) accept(d *Destination, pkt *Packet) {
	n := m.n
	if !d.AcceptLinks {
		return
	}
	h := pkt.Hdr
	lr, err := reticulum.UnmarshalRNSLinkRequest(h.Data)
	if err != nil {
		log.Debug().Err(err).Msg("rns: bad link request")
		return
	}
	id, err := reticulum.LinkIDFromRequestPacket(pkt.Raw)
	if err != nil {
		return
	}
	mtu := reticulum.MTU
	if lr.Signalled {
		mtu = lr.MTU
		if hw := n.tx.HWMTU(pkt.Iface); hw > 0 && hw < mtu {
			mtu = hw
		}
	}
	prv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return
	}
	shared, err := prv.ECDH(lr.EphPub)
	if err != nil {
		return
	}
	key := reticulum.DeriveLinkKey(shared, id)
	tok, _ := reticulum.NewToken(key)
	now := n.now()
	l := &Link{ID: id, Dest: d.Hash, Initiator: false, Iface: pkt.Iface, state: LinkHandshake, prv: prv,
		sigPrv: ed25519.PrivateKey(d.Identity.SigningPrivateBytes()), peerPub: lr.EphPub, peerSigPub: lr.EphSigPub,
		key: key, token: tok, mtu: mtu, requestTime: now, local: d,
		establishTO: time.Duration(reticulum.DefaultPerHopTimeoutSec*max(1, int(h.Hops)))*time.Second + 6*time.Second,
		keepalive:   reticulum.KeepaliveMaxSec * time.Second, staleTime: 2 * reticulum.KeepaliveMaxSec * time.Second}
	l.mdu = reticulum.LinkMDU(mtu)
	l.AcceptResources = d.AcceptResources
	m.mu.Lock()
	m.links[id] = l
	m.mu.Unlock()

	signed := reticulum.LinkProofSignedData(id, prv.PublicKey(), d.Identity.SigningPublicKey(), mtu, reticulum.LinkModeAES256CBC)
	lp := &reticulum.RNSLinkProof{Signature: d.Identity.Sign(signed), EphPub: prv.PublicKey(), MTU: mtu, Mode: reticulum.LinkModeAES256CBC}
	ph := &reticulum.Header{HeaderType: reticulum.HeaderType1, DestType: reticulum.DestLink, PacketType: reticulum.PacketProof,
		DestHash: id, Context: reticulum.ContextLRProof, Data: lp.Marshal()}
	l.lastOutbound = now
	n.transmit(pkt.Iface, ph.Marshal())
	log.Debug().Str("link", hexh(id)).Str("dest", d.Name).Str("iface", pkt.Iface).Msg("rns: link request accepted, proof sent")
}

// handleProof validates a link request proof for one of our pending links.
func (m *LinkManager) handleProof(pkt *Packet) {
	n := m.n
	h := pkt.Hdr
	m.mu.Lock()
	l := m.pending[h.DestHash]
	m.mu.Unlock()
	if l == nil {
		return
	}
	lp, err := reticulum.UnmarshalRNSLinkProof(h.Data)
	if err != nil || lp.Mode != reticulum.LinkModeAES256CBC {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != LinkPending {
		return
	}
	confirmedMTU := reticulum.MTU
	signed := reticulum.LinkProofSignedData(l.ID, lp.EphPub, l.destIdentity.SigningPublicKey(), lp.MTU, lp.Mode)
	if lp.Signalled {
		confirmedMTU = lp.MTU
	} else {
		signed = signed[:len(signed)-reticulum.LinkMTUSize]
	}
	if !reticulum.VerifySignature(l.destIdentity.SigningPublicKey(), signed, lp.Signature) {
		log.Debug().Str("link", hexh(l.ID)).Msg("rns: invalid link proof signature")
		return
	}
	shared, err := l.prv.ECDH(lp.EphPub)
	if err != nil {
		return
	}
	l.peerPub = lp.EphPub
	l.key = reticulum.DeriveLinkKey(shared, l.ID)
	l.token, _ = reticulum.NewToken(l.key)
	l.mtu = confirmedMTU
	l.mdu = reticulum.LinkMDU(confirmedMTU)
	now := n.now()
	l.rtt = now.Sub(l.requestTime)
	l.Iface = pkt.Iface
	l.state = LinkActive
	l.activatedAt = now
	l.lastProof = now
	l.lastInbound = now
	m.updateKeepalive(l)
	m.mu.Lock()
	delete(m.pending, l.ID)
	m.links[l.ID] = l
	m.mu.Unlock()
	// RTT packet activates the responder side.
	ct, err := l.token.Encrypt(reticulum.RTTData(l.rtt.Seconds()))
	if err == nil {
		m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextLRRTT, ct)
	}
	log.Info().Str("link", hexh(l.ID)).Str("dest", hexh(l.Dest)).Dur("rtt", l.rtt).Int("mtu", l.mtu).Msg("rns: link established")
	if l.OnEstablished != nil {
		go l.OnEstablished(l)
	}
}

// sendLinkPacket transmits a packet on the link's interface (Transport.outbound for LINK destinations).
func (m *LinkManager) sendLinkPacket(l *Link, ptype, context byte, data []byte) []byte {
	h := &reticulum.Header{HeaderType: reticulum.HeaderType1, DestType: reticulum.DestLink, PacketType: ptype,
		DestHash: l.ID, Context: context, Data: data}
	raw := h.Marshal()
	l.lastOutbound = m.n.now()
	if context == reticulum.ContextKeepalive {
		l.lastKeepalive = l.lastOutbound
	}
	m.n.transmit(l.Iface, raw)
	return raw
}

// Send encrypts plaintext and sends it as a link DATA packet, returning a
// receipt that resolves when the peer's proof arrives.
func (m *LinkManager) Send(l *Link, plain []byte) (*Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != LinkActive && l.state != LinkStale {
		return nil, errors.New("rns: link not active")
	}
	if len(plain) > l.mdu {
		return nil, errors.New("rns: plaintext exceeds link MDU")
	}
	ct, err := l.token.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	raw := m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextNone, ct)
	r := &Receipt{Hash: reticulum.PacketHash(raw), Dest: l.ID, SentAt: m.n.now(), link: l,
		Timeout: m.n.now().Add(time.Duration(reticulum.DefaultPerHopTimeoutSec*max(1, l.expectedHops))*time.Second + 6*time.Second)}
	// Link proofs are signed by the peer's link signing key: the destination
	// identity when we initiated, the initiator's ephemeral key otherwise.
	r.sigPub = l.peerSigPub
	m.n.addReceipt(r)
	return r, nil
}

// SendContext sends an already-encrypted (or plaintext, per context rules)
// payload with a specific context on the link; used by resource transfer.
func (m *LinkManager) SendContext(l *Link, ptype, context byte, data []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m.sendLinkPacket(l, ptype, context, data)
}

// Identify tells the responder who we are (Link.identify).
func (m *LinkManager) Identify(l *Link, id *reticulum.Identity) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.Initiator || l.state != LinkActive {
		return errors.New("rns: identify needs an active initiated link")
	}
	ct, err := l.token.Encrypt(reticulum.LinkIdentifyData(id, l.ID))
	if err != nil {
		return err
	}
	m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextLinkIdentify, ct)
	return nil
}

// Teardown closes a link and tells the peer (Link.teardown).
func (m *LinkManager) Teardown(l *Link) {
	l.mu.Lock()
	if l.state == LinkClosed {
		l.mu.Unlock()
		return
	}
	if l.state == LinkActive || l.state == LinkStale {
		if ct, err := l.token.Encrypt(l.ID[:]); err == nil {
			m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextLinkClose, ct)
		}
	}
	l.state = LinkClosed
	l.mu.Unlock()
	m.closed(l)
}

func (m *LinkManager) closed(l *Link) {
	m.mu.Lock()
	delete(m.links, l.ID)
	delete(m.pending, l.ID)
	m.mu.Unlock()
	log.Debug().Str("link", hexh(l.ID)).Msg("rns: link closed")
	if l.OnClosed != nil {
		go l.OnClosed(l)
	}
	if l.local != nil && l.local.OnLinkClosed != nil {
		go l.local.OnLinkClosed(l)
	}
}

// receive dispatches a packet addressed to an active link (Link.receive).
func (m *LinkManager) receive(l *Link, pkt *Packet) {
	n := m.n
	h := pkt.Hdr
	l.mu.Lock()
	if pkt.Iface != l.Iface {
		l.mu.Unlock()
		log.Warn().Str("link", hexh(l.ID)).Str("iface", pkt.Iface).Str("want", l.Iface).Msg("rns: link packet on unexpected interface")
		return
	}
	now := n.now()
	l.lastInbound = now
	if l.state == LinkStale {
		l.state = LinkActive
	}
	state := l.state
	l.mu.Unlock()

	if h.PacketType == reticulum.PacketProof {
		if h.Context == reticulum.ContextResourcePRF {
			m.handleResourceProof(l, h.Data)
		}
		return
	}
	switch h.Context {
	case reticulum.ContextNone:
		plain, err := l.Decrypt(h.Data)
		if err != nil {
			return
		}
		if l.OnPacket != nil {
			l.OnPacket(l, plain, pkt)
		}
		if l.local != nil {
			if l.local.OnLinkPacket != nil {
				l.local.OnLinkPacket(l, plain, pkt)
			}
			if l.local.ProveAll {
				l.mu.Lock()
				m.sendLinkPacket(l, reticulum.PacketProof, reticulum.ContextNone, append(append([]byte(nil), pkt.Hash[:]...), l.sign(pkt.Hash[:])...))
				l.mu.Unlock()
			}
		}

	case reticulum.ContextLRRTT:
		if l.Initiator || state != LinkHandshake {
			return
		}
		plain, err := l.Decrypt(h.Data)
		if err != nil {
			m.Teardown(l)
			return
		}
		rtt, err := reticulum.ParseRTTData(plain)
		if err != nil {
			m.Teardown(l)
			return
		}
		l.mu.Lock()
		measured := now.Sub(l.requestTime)
		l.rtt = time.Duration(rtt * float64(time.Second))
		if measured > l.rtt {
			l.rtt = measured
		}
		l.state = LinkActive
		l.activatedAt = now
		l.expectedHops = int(h.Hops)
		m.updateKeepalive(l)
		l.mu.Unlock()
		log.Info().Str("link", hexh(l.ID)).Str("dest", l.local.Name).Dur("rtt", l.rtt).Msg("rns: link active")
		if l.local != nil && l.local.OnLink != nil {
			go l.local.OnLink(l)
		}

	case reticulum.ContextKeepalive:
		if !l.Initiator && len(h.Data) == 1 && h.Data[0] == reticulum.KeepaliveInitiator {
			l.mu.Lock()
			if !now.Before(l.lastOutbound.Add(l.keepalive)) {
				m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextKeepalive, []byte{reticulum.KeepaliveResponse})
			}
			l.mu.Unlock()
		}

	case reticulum.ContextLinkClose:
		plain, err := l.Decrypt(h.Data)
		if err == nil && len(plain) == HashLen && string(plain) == string(l.ID[:]) {
			l.mu.Lock()
			l.state = LinkClosed
			l.mu.Unlock()
			m.closed(l)
		}

	case reticulum.ContextResourceAdv:
		if plain, err := l.Decrypt(h.Data); err == nil {
			m.handleResourceAdv(l, plain)
		}
	case reticulum.ContextResourceReq:
		if plain, err := l.Decrypt(h.Data); err == nil {
			m.handleResourceRequest(l, plain)
		}
	case reticulum.ContextResource:
		m.receivePart(l, h.Data)
	case reticulum.ContextResourceHMU:
		if plain, err := l.Decrypt(h.Data); err == nil && len(plain) > FullHashLen {
			var hash [FullHashLen]byte
			copy(hash[:], plain[:FullHashLen])
			rs := l.res()
			rs.mu.Lock()
			r := rs.incoming[hash]
			rs.mu.Unlock()
			if r != nil {
				if v, _, err := msgpack.UnpackValue(plain[FullHashLen:]); err == nil && v.Kind == msgpack.KindArray && len(v.Array) == 2 {
					seg, _ := v.Array[0].AsInt()
					r.hashmapUpdate(int(seg), v.Array[1].Bin)
					m.requestNext(r)
				}
			}
		}
	case reticulum.ContextResourceICL, reticulum.ContextResourceRCL:
		if plain, err := l.Decrypt(h.Data); err == nil && len(plain) == FullHashLen {
			var hash [FullHashLen]byte
			copy(hash[:], plain)
			rs := l.res()
			rs.mu.Lock()
			in := rs.incoming[hash]
			out := rs.outgoing[hash]
			rs.mu.Unlock()
			if in != nil {
				m.concludeIncoming(in, nil, errors.New("cancelled by sender"))
			}
			if out != nil {
				out.fail(errors.New("rejected by receiver"))
			}
		}

	case reticulum.ContextLinkIdentify:
		if l.Initiator {
			return
		}
		plain, err := l.Decrypt(h.Data)
		if err != nil {
			return
		}
		pub, err := reticulum.ParseLinkIdentify(plain, l.ID)
		if err != nil {
			return
		}
		l.mu.Lock()
		first := l.remoteIdent == nil
		l.remoteIdent = pub
		l.mu.Unlock()
		if first && l.OnIdentified != nil {
			l.OnIdentified(l, pub)
		}

	default:
		if l.OnContext != nil {
			l.OnContext(l, pkt)
		}
	}
}

// watchdog is Link.__watchdog_job for every link, run from the node jobs.
func (m *LinkManager) watchdog(now time.Time) {
	m.resourceWatchdog(now)
	for _, l := range m.All() {
		l.mu.Lock()
		switch l.state {
		case LinkPending, LinkHandshake:
			if now.After(l.requestTime.Add(l.establishTO)) {
				l.state = LinkClosed
				l.mu.Unlock()
				log.Debug().Str("link", hexh(l.ID)).Msg("rns: link establishment timed out")
				m.closed(l)
				continue
			}
		case LinkActive:
			lastIn := l.lastInbound
			if l.lastProof.After(lastIn) {
				lastIn = l.lastProof
			}
			if l.activatedAt.After(lastIn) {
				lastIn = l.activatedAt
			}
			if !now.Before(lastIn.Add(l.keepalive)) || !now.Before(l.lastOutbound.Add(l.keepalive)) {
				if l.Initiator && !now.Before(l.lastKeepalive.Add(l.keepalive)) {
					m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextKeepalive, []byte{reticulum.KeepaliveInitiator})
				}
				if !now.Before(lastIn.Add(l.staleTime)) {
					l.state = LinkStale
					l.staleSince = now
				}
			}
		case LinkStale:
			grace := time.Duration(float64(l.rtt)*4) + reticulum.StaleGraceSec*time.Second
			if now.After(l.staleSince.Add(grace)) {
				if ct, err := l.token.Encrypt(l.ID[:]); err == nil {
					m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextLinkClose, ct)
				}
				l.state = LinkClosed
				l.mu.Unlock()
				m.closed(l)
				continue
			}
		}
		l.mu.Unlock()
	}
}

func (m *LinkManager) closeAll() {
	for _, l := range m.All() {
		m.Teardown(l)
	}
}
