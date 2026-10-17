package gateway

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/tetra"
	"meshsat/internal/transport"
)

// Delivery confirmation between MeshSat Bridges on one TETRA group.
// [MESHSAT-1588]
//
// In Direct Mode two radios that key up at the same instant both report
// the SDS sent and neither arrives (half duplex; 1 of 5 deliberately
// simultaneous rounds on 17 Oct 2026). Radio-level delivery reports need
// each radio to have a number of its own; the kit radios share one, so
// the Bridges confirm receipt themselves, in link control SDS a plain
// handheld does not show (tetra.LinkPID):
//
//   - every text a Bridge receives is acknowledged with its SDS-TL
//     reference and the hash of its bytes (the text itself is unchanged);
//   - a Bridge that knows a MeshSat peer on the group waits for that
//     acknowledgement, and a send nobody acknowledged is ErrNoAck, which
//     the delivery worker retries; a retried text the far end already had
//     is acknowledged again and delivered once;
//   - a Bridge says hello when its radio comes up, and the others answer,
//     so each knows whether anyone will acknowledge. With only plain
//     handhelds on the group nobody does, nothing waits and nothing is
//     sent twice.
//
// Mode (config acks): auto (the above, default), on (always wait), off
// (neither acknowledge nor wait).

var (
	// tetraAckTimeout: the far Bridge acknowledges within a few seconds
	// (receive, a short random pause, one SDS).
	tetraAckTimeout = 15 * time.Second
	// tetraPeerTTL is how long a peer heard from counts as there.
	tetraPeerTTL = 24 * time.Hour
	// tetraRepeatWindow: the same text from the same sender within it is
	// a repeat of one already delivered (a retry whose ack was lost).
	tetraRepeatWindow = 2 * time.Minute
	// tetraJitter is a random pause up to max. Two kits that collided and
	// both wait the delivery worker's 30 s would collide again without it.
	tetraJitter = func(max time.Duration) time.Duration {
		if max <= 0 {
			return 0
		}
		return time.Duration(rand.Int64N(int64(max)))
	}
)

type tetraAckKey struct {
	ref  byte
	hash uint32
}

type tetraAcks struct {
	mu       sync.Mutex
	pending  map[tetraAckKey]chan struct{}
	peers    map[uint32]time.Time
	lastPeer time.Time
	recent   map[string]time.Time
	selfTag  uint32

	acked, unacked, unconfirmed, repeats, linkSent, linkFailed atomic.Int64
	lastAckMs                                                  atomic.Int64
	lastNoAck                                                  atomic.Int64
}

func newTetraAcks() *tetraAcks {
	return &tetraAcks{
		pending: make(map[tetraAckKey]chan struct{}),
		peers:   make(map[uint32]time.Time),
		recent:  make(map[string]time.Time),
		selfTag: rand.Uint32(),
	}
}

func (g *TetraGateway) acksMode() string {
	switch g.config.Acks {
	case "on", "off":
		return g.config.Acks
	}
	return "auto"
}

// peerKnown says a MeshSat Bridge was heard on the group lately.
func (g *TetraGateway) peerKnown() bool {
	g.acks.mu.Lock()
	defer g.acks.mu.Unlock()
	return !g.acks.lastPeer.IsZero() && time.Since(g.acks.lastPeer) < tetraPeerTTL
}

// wantAck says whether a send waits for an acknowledgement.
func (g *TetraGateway) wantAck() bool {
	switch g.acksMode() {
	case "on":
		return true
	case "off":
		return false
	}
	return g.peerKnown()
}

func (g *TetraGateway) expectAck(key tetraAckKey) chan struct{} {
	ch := make(chan struct{}, 1)
	g.acks.mu.Lock()
	g.acks.pending[key] = ch
	g.acks.mu.Unlock()
	return ch
}

func (g *TetraGateway) dropAck(key tetraAckKey) {
	g.acks.mu.Lock()
	delete(g.acks.pending, key)
	g.acks.mu.Unlock()
}

// awaitAck waits for the far Bridge's acknowledgement of one SDS.
func (g *TetraGateway) awaitAck(ctx context.Context, key tetraAckKey, ch chan struct{}) bool {
	defer g.dropAck(key)
	t0 := time.Now()
	t := time.NewTimer(g.ackTimeout)
	defer t.Stop()
	select {
	case <-ch:
		g.acks.acked.Add(1)
		g.acks.lastAckMs.Store(time.Since(t0).Milliseconds())
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	return false
}

// sendPause is the random pause before a transmission: short normally,
// longer after a send nobody acknowledged (a collision is likely, and the
// other kit retries on the same clock).
func (g *TetraGateway) sendPause() time.Duration {
	if last := g.acks.lastNoAck.Load(); last > 0 && time.Since(time.Unix(last, 0)) < 3*time.Minute {
		return g.jitter(8 * time.Second)
	}
	return g.jitter(400 * time.Millisecond)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// sendLink sends one link control SDS to dest after pause, again up to
// twice when the radio reports it not sent. Runs on its own goroutine.
func (g *TetraGateway) sendLink(l tetra.Link, dest string, pause time.Duration) {
	ctx := g.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 3; attempt++ {
		if !sleepCtx(ctx, pause) {
			return
		}
		g.mu.RLock()
		sess := g.sess
		g.mu.RUnlock()
		if sess == nil {
			return
		}
		pdu, bits := tetra.EncodeLink(l)
		g.sendMu.Lock()
		_, err := sess.SendSDS(ctx, dest, byte(g.ref.Add(1)), pdu, bits)
		g.sendMu.Unlock()
		if err == nil {
			g.acks.linkSent.Add(1)
			return
		}
		if !errors.Is(err, tetra.ErrRadioNotSent) {
			g.acks.linkFailed.Add(1)
			log.Debug().Err(err).Int("kind", int(l.Kind)).Msg("tetra: link control SDS not sent")
			return
		}
		pause = 500*time.Millisecond + g.jitter(2*time.Second)
	}
	g.acks.linkFailed.Add(1)
}

// sayHello tells the group a MeshSat Bridge listens, once the radio is up.
func (g *TetraGateway) sayHello() {
	if g.acksMode() == "off" {
		return
	}
	dest, err := g.destination(&transport.MeshMessage{})
	if err != nil {
		return
	}
	g.sendLink(tetra.Link{Kind: tetra.LinkHello, Tag: g.acks.selfTag}, dest, time.Second+g.jitter(3*time.Second))
}

// onLink handles a link control SDS from another Bridge.
func (g *TetraGateway) onLink(r tetra.Received) {
	l, err := tetra.DecodeLink(r.PDU)
	if err != nil {
		g.nonText.Add(1)
		log.Debug().Err(err).Str("from", r.From).Msg("tetra: link control SDS not understood")
		return
	}
	now := time.Now()
	g.acks.mu.Lock()
	g.acks.lastPeer = now
	if l.Tag != 0 && l.Tag != g.acks.selfTag {
		g.acks.peers[l.Tag] = now
	}
	var ch chan struct{}
	if l.Kind == tetra.LinkAck {
		ch = g.acks.pending[tetraAckKey{l.Ref, l.Hash}]
	}
	g.acks.mu.Unlock()
	switch l.Kind {
	case tetra.LinkAck:
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	case tetra.LinkHello:
		if l.Tag == g.acks.selfTag || g.acksMode() == "off" {
			return
		}
		log.Info().Uint32("peer", l.Tag).Msg("tetra: a MeshSat Bridge said hello on the group; answering")
		go g.sendLink(tetra.Link{Kind: tetra.LinkHelloReply, Tag: g.acks.selfTag}, r.To, 500*time.Millisecond+g.jitter(2*time.Second))
	case tetra.LinkHelloReply:
		log.Info().Uint32("peer", l.Tag).Msg("tetra: a MeshSat Bridge answered on the group")
	}
}

// ackText acknowledges a received text to the group it came on.
func (g *TetraGateway) ackText(r tetra.Received) {
	if g.acksMode() == "off" {
		return
	}
	go g.sendLink(tetra.Link{Kind: tetra.LinkAck, Ref: r.Ref, Hash: tetra.TextHash(r.Body)}, r.To,
		100*time.Millisecond+g.jitter(500*time.Millisecond))
}

// isRepeat says the same text from the same sender was delivered within
// tetraRepeatWindow, and records this one.
func (g *TetraGateway) isRepeat(from, text string) bool {
	key := from + "|" + strings.TrimSpace(text)
	now := time.Now()
	g.acks.mu.Lock()
	defer g.acks.mu.Unlock()
	for k, t := range g.acks.recent {
		if now.Sub(t) > tetraRepeatWindow {
			delete(g.acks.recent, k)
		}
	}
	if t, ok := g.acks.recent[key]; ok && now.Sub(t) <= tetraRepeatWindow {
		return true
	}
	g.acks.recent[key] = now
	return false
}

func (g *TetraGateway) peerCount() (int, *time.Time) {
	g.acks.mu.Lock()
	defer g.acks.mu.Unlock()
	n := 0
	for _, t := range g.acks.peers {
		if time.Since(t) < tetraPeerTTL {
			n++
		}
	}
	if g.acks.lastPeer.IsZero() {
		return n, nil
	}
	t := g.acks.lastPeer
	return n, &t
}
