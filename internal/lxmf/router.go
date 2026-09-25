package lxmf

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
)

// Timing (LXMRouter.py).
const (
	PathRequestWait      = 7 * time.Second
	DeliveryRetryWait    = 10 * time.Second
	LinkEstablishTimeout = 20 * time.Second
	LinkMaxInactivity    = 10 * time.Minute
	MaxDeliveryAttempts  = 5
)

// Errors.
var (
	ErrNoPath             = errors.New("lxmf: no path to destination")
	ErrUnknownDestination = errors.New("lxmf: destination identity unknown, no announce seen")
	ErrNotDelivered       = errors.New("lxmf: no delivery proof received")
	ErrLinkFailed         = errors.New("lxmf: link could not be established")
	ErrTooLarge           = errors.New("lxmf: message needs a resource transfer, not implemented yet")
	ErrStampCostTooHigh   = errors.New("lxmf: destination requires a stamp cost above the configured maximum")
)

// Config configures a Router.
type Config struct {
	Identity    *reticulum.Identity
	DisplayName string
	// StampCost announced for inbound messages (0 = none).
	StampCost int
	// EnforceStamps drops inbound messages with a missing or invalid stamp.
	EnforceStamps bool
	// MaxOutboundStampCost refuses destinations that demand more work.
	MaxOutboundStampCost int
	// OnMessage receives every accepted inbound message.
	OnMessage func(m *Message)
}

// Router is the LXMF endpoint: one lxmf.delivery destination on a node.
type Router struct {
	cfg  Config
	node *rns.Node
	Dest *rns.Destination

	mu          sync.Mutex
	seen        map[[32]byte]time.Time
	directLinks map[[DestLen]byte]*rns.Link // links we initiated, by delivery hash
	backchannel map[[DestLen]byte]*rns.Link // links peers initiated and identified on
}

// New registers the delivery destination on the node.
func New(node *rns.Node, cfg Config) *Router {
	if cfg.MaxOutboundStampCost <= 0 {
		cfg.MaxOutboundStampCost = 16
	}
	r := &Router{cfg: cfg, node: node, seen: make(map[[32]byte]time.Time),
		directLinks: make(map[[DestLen]byte]*rns.Link), backchannel: make(map[[DestLen]byte]*rns.Link)}
	r.Dest = node.AddDestination(&rns.Destination{
		Name:        DeliveryName,
		Identity:    cfg.Identity,
		AcceptLinks: true,
		ProveAll:    true,
		AppData:     func() []byte { return EncodeAnnounceAppData(cfg.DisplayName, cfg.StampCost) },
		OnPacket: func(plain []byte, pkt *rns.Packet) {
			// Opportunistic: the packet carried packed[16:], prepend our hash.
			data := append(append([]byte(nil), r.Dest.Hash[:]...), plain...)
			r.Inbound(data, Opportunistic, pkt.Iface)
		},
		OnLink: func(l *rns.Link) {
			l.OnIdentified = r.onIdentified
		},
		OnLinkPacket: func(l *rns.Link, plain []byte, pkt *rns.Packet) {
			r.Inbound(plain, Direct, pkt.Iface)
		},
		OnLinkClosed:    func(l *rns.Link) { r.forgetLink(l) },
		AcceptResources: r.acceptResource,
		OnLinkResource: func(l *rns.Link, data []byte) {
			r.Inbound(data, Direct, l.Iface)
		},
	})
	return r
}

// MaxTransferBytes bounds an inbound resource (LXMRouter delivery_per_transfer_limit).
const MaxTransferBytes = 1024 * 1024

func (r *Router) acceptResource(size int) bool { return size > 0 && size <= MaxTransferBytes }

// Hash is the delivery destination hash.
func (r *Router) Hash() [DestLen]byte { return r.Dest.Hash }

// HashHex is the delivery destination hash in hex.
func (r *Router) HashHex() string { return hex.EncodeToString(r.Dest.Hash[:]) }

// Announce emits the delivery announce on every floodable interface.
func (r *Router) Announce() error { return r.node.Announce(r.Dest, "", false) }

// onIdentified records a peer's link as a backchannel to its delivery
// destination (LXMRouter.delivery_link_established + identify).
func (r *Router) onIdentified(l *rns.Link, pub []byte) {
	id, err := reticulum.IdentityFromPublicBytes(pub)
	if err != nil {
		return
	}
	dest := id.DestHash(DeliveryName)
	r.mu.Lock()
	r.backchannel[dest] = l
	r.mu.Unlock()
	_ = r.node.Remember(dest, pub, nil)
	log.Debug().Str("peer", hex.EncodeToString(dest[:])).Msg("lxmf: backchannel link identified")
}

func (r *Router) forgetLink(l *rns.Link) {
	r.mu.Lock()
	for k, v := range r.directLinks {
		if v == l {
			delete(r.directLinks, k)
		}
	}
	for k, v := range r.backchannel {
		if v == l {
			delete(r.backchannel, k)
		}
	}
	r.mu.Unlock()
}

// sourceSigPub looks a source delivery hash up in the node's known identities.
func (r *Router) sourceSigPub(source [DestLen]byte) []byte {
	if k := r.node.Recall(source); k != nil && k.Identity != nil {
		return k.Identity.SigningPublicKey()
	}
	return nil
}

// Inbound validates and delivers a packed message (LXMRouter.lxmf_delivery).
func (r *Router) Inbound(packed []byte, method Method, iface string) (*Message, error) {
	m, err := Unpack(packed, r.sourceSigPub)
	if err != nil {
		return nil, err
	}
	if m.Dest != r.Dest.Hash {
		return nil, fmt.Errorf("lxmf: message for %x, not for us", m.Dest[:4])
	}
	m.Method = method
	m.Iface = iface
	m.ReceivedAt = time.Now()
	if r.cfg.StampCost > 0 {
		m.StampChecked = true
		if m.Stamp != nil {
			wb := Workblock(m.Hash)
			m.StampValid = StampValid(wb, m.Stamp, r.cfg.StampCost)
			if m.StampValid {
				m.StampValue = stampValueOf(wb, m.Stamp)
			}
		}
		if !m.StampValid && r.cfg.EnforceStamps {
			log.Info().Str("msg", m.String()).Msg("lxmf: dropping message with invalid stamp")
			return nil, errors.New("lxmf: invalid stamp")
		}
	}
	r.mu.Lock()
	if _, dup := r.seen[m.Hash]; dup {
		r.mu.Unlock()
		return m, nil
	}
	r.seen[m.Hash] = time.Now()
	if len(r.seen) > 10000 {
		cutoff := time.Now().Add(-24 * time.Hour)
		for k, t := range r.seen {
			if t.Before(cutoff) {
				delete(r.seen, k)
			}
		}
	}
	r.mu.Unlock()
	if !m.SourceKnown {
		// Ask for the source so the next message verifies; the message is
		// still delivered, flagged unverified, as upstream does.
		go r.node.RequestPath(context.Background(), m.Source, PathRequestWait)
	}
	log.Info().Str("msg", m.String()).Str("from", hex.EncodeToString(m.Source[:])).Str("method", method.String()).
		Bool("signature_valid", m.SignatureValid).Int("bytes", len(m.Content)).Msg("lxmf: message received")
	if r.cfg.OnMessage != nil {
		r.cfg.OnMessage(m)
	}
	return m, nil
}

// SendResult describes a completed send.
type SendResult struct {
	Method    Method
	Attempts  int
	RTT       time.Duration
	StampCost int
}

// Send builds, signs and delivers one message, waiting for the delivery
// proof. wantMethod 0 lets the size decide (opportunistic when it fits one
// packet, otherwise a link). The caller (the delivery ledger) retries.
func (r *Router) Send(ctx context.Context, dest [DestLen]byte, content, title []byte, fields Value, wantMethod Method) (*Message, *SendResult, error) {
	known := r.node.Recall(dest)
	if known == nil || known.Identity == nil {
		if !r.node.RequestPath(ctx, dest, PathRequestWait) {
			return nil, nil, ErrUnknownDestination
		}
		known = r.node.Recall(dest)
		if known == nil || known.Identity == nil {
			return nil, nil, ErrUnknownDestination
		}
	}
	stampCost := ParseAnnounceAppData(known.AppData).StampCost
	if stampCost > r.cfg.MaxOutboundStampCost {
		return nil, nil, ErrStampCostTooHigh
	}
	m := &Message{Dest: dest, Source: r.Dest.Hash, Title: title, Content: content, Fields: fields}
	stamper := func(h [32]byte, cost int) []byte {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		s, _ := GenerateStamp(sctx, h, cost)
		return s
	}
	if err := m.Pack(r.cfg.Identity, stampCost, stamper); err != nil {
		return nil, nil, err
	}
	method := wantMethod
	if method == 0 {
		method = Direct
		if m.ContentSize() <= PacketMaxContent {
			method = Opportunistic
		}
	}
	if method == Opportunistic && m.ContentSize() > PacketMaxContent {
		method = Direct
	}
	res := &SendResult{Method: method, StampCost: stampCost, Attempts: 1}
	if !r.node.Paths().Has(dest) {
		if !r.node.RequestPath(ctx, dest, PathRequestWait) {
			return m, res, ErrNoPath
		}
	}
	start := time.Now()
	switch method {
	case Opportunistic:
		rc, err := r.node.SendData(dest, m.Packed[DestLen:], true)
		if err != nil {
			return m, res, err
		}
		select {
		case out := <-rc.Done():
			if !out.Delivered {
				return m, res, ErrNotDelivered
			}
			res.RTT = out.RTT
			return m, res, nil
		case <-ctx.Done():
			return m, res, ctx.Err()
		}
	case Direct:
		l, err := r.linkTo(ctx, dest)
		if err != nil {
			return m, res, err
		}
		if m.ContentSize() <= LinkMaxContent {
			rc, err := r.node.Links().Send(l, m.Packed)
			if err != nil {
				r.forgetLink(l)
				return m, res, err
			}
			select {
			case out := <-rc.Done():
				if !out.Delivered {
					r.node.Links().Teardown(l)
					return m, res, ErrNotDelivered
				}
				res.RTT = out.RTT
			case <-ctx.Done():
				return m, res, ctx.Err()
			}
		} else {
			// Larger messages travel as an RNS resource on the link.
			or, err := r.node.Links().SendResource(l, m.Packed)
			if err != nil {
				return m, res, err
			}
			select {
			case out := <-or.Done():
				if !out.Delivered {
					if out.Err != nil {
						return m, res, out.Err
					}
					return m, res, ErrNotDelivered
				}
				res.RTT = time.Since(start)
			case <-ctx.Done():
				return m, res, ctx.Err()
			}
		}
		// Backchannel identification so the peer can answer on this link.
		if l.Initiator && l.RemoteIdentity() == nil {
			_ = r.node.Links().Identify(l, r.cfg.Identity)
		}
		return m, res, nil
	}
	return m, res, errors.New("lxmf: unsupported method")
}

// linkTo returns an active link to a delivery destination: a backchannel
// the peer opened, a direct link we already hold, or a new one.
func (r *Router) linkTo(ctx context.Context, dest [DestLen]byte) (*rns.Link, error) {
	r.mu.Lock()
	if l := r.backchannel[dest]; l != nil && l.State() == rns.LinkActive {
		r.mu.Unlock()
		return l, nil
	}
	if l := r.directLinks[dest]; l != nil && l.State() == rns.LinkActive {
		r.mu.Unlock()
		return l, nil
	}
	r.mu.Unlock()
	l, err := r.node.Links().Initiate(dest)
	if err != nil {
		return nil, err
	}
	est := make(chan struct{}, 1)
	l.OnEstablished = func(*rns.Link) { est <- struct{}{} }
	l.OnClosed = func(l *rns.Link) { r.forgetLink(l) }
	// The peer may answer on this link (LXMF backchannel), by packet or resource.
	l.OnPacket = func(l *rns.Link, plain []byte, pkt *rns.Packet) { r.Inbound(plain, Direct, pkt.Iface) }
	l.AcceptResources = r.acceptResource
	l.OnResource = func(l *rns.Link, data []byte) { r.Inbound(data, Direct, l.Iface) }
	select {
	case <-est:
	case <-time.After(LinkEstablishTimeout):
		r.node.Links().Teardown(l)
		return nil, ErrLinkFailed
	case <-ctx.Done():
		r.node.Links().Teardown(l)
		return nil, ctx.Err()
	}
	r.mu.Lock()
	r.directLinks[dest] = l
	r.mu.Unlock()
	return l, nil
}

// Peer is what we know about a remote LXMF destination.
type Peer struct {
	DestHash    string `json:"dest_hash"`
	DisplayName string `json:"display_name"`
	StampCost   int    `json:"stamp_cost"`
	Hops        int    `json:"hops"`
	Interface   string `json:"interface"`
	LastSeen    string `json:"last_seen"`
}

// Peers lists known lxmf.delivery destinations from the path table.
func (r *Router) Peers() []Peer {
	var out []Peer
	nameHash := reticulum.ComputeNameHash(DeliveryName)
	for _, e := range r.node.Paths().All() {
		if e.Announce == nil || e.Announce.NameHash != nameHash {
			continue
		}
		info := ParseAnnounceAppData(e.Announce.AppData)
		p := Peer{DestHash: hex.EncodeToString(e.DestHash[:]), DisplayName: info.DisplayName, StampCost: info.StampCost,
			Hops: e.Hops, Interface: e.Iface}
		if k := r.node.Recall(e.DestHash); k != nil {
			p.LastSeen = k.LastSeen.UTC().Format(time.RFC3339)
		}
		out = append(out, p)
	}
	return out
}
