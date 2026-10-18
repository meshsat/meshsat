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
//   - every text a Bridge receives and takes in (a part held, a whole text
//     in the processor's queue, a repeat of one that was) is acknowledged
//     with its SDS-TL reference and the hash of its bytes (the text itself
//     is unchanged); one a full queue dropped is not, so it comes again;
//   - a Bridge that knows a MeshSat peer on the group waits for that
//     acknowledgement, and a send nobody acknowledged is ErrNoAck, which
//     the delivery worker retries; a retried text the far end already had
//     is acknowledged again and delivered once; an acknowledgement that
//     came while the radio still had the SDS outweighs the radio's later
//     report that it failed (the text arrived);
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
	// tetraHelloEvery: a Bridge that knows no peer says hello again this
	// often. Two kits whose radios come up in the same second (a deploy
	// restarts both) say hello at once and miss each other: seen live on
	// 18 Oct 2026, both kits ready at 00:00:09, neither heard the other.
	tetraHelloEvery = 5 * time.Minute
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
	lastHello                                                  atomic.Int64
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

// takeAck lets the key go and says whether its acknowledgement had
// already come, in one step with onLink's signal.
func (g *TetraGateway) takeAck(key tetraAckKey, ch chan struct{}) bool {
	g.acks.mu.Lock()
	defer g.acks.mu.Unlock()
	delete(g.acks.pending, key)
	select {
	case <-ch:
		g.acks.acked.Add(1)
		return true
	default:
		return false
	}
}

// afterSubmission says a send's error came once the SDS may have reached
// the radio: not a port that closed before the SDS was written, not the
// radio refusing the command (+CME ERROR as its answer). An
// acknowledgement matched to the SDS's reference and hash then outweighs
// the error (review 2026-10-18 14:53, TETRA-ACK1); before that it can
// only be an old one for the same reference and text. [MESHSAT-1584]
func afterSubmission(err error) bool {
	var cme *tetra.CMEError
	switch {
	case errors.Is(err, tetra.ErrSDSInFlight):
		return true
	case errors.Is(err, tetra.ErrClosed), errors.As(err, &cme):
		return false
	}
	return true
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

// sayHello tells the group a MeshSat Bridge listens, once the radio is up
// and then every tetraHelloEvery while no peer has answered (helloDue).
func (g *TetraGateway) sayHello() {
	if g.acksMode() == "off" {
		return
	}
	g.acks.lastHello.Store(time.Now().Unix())
	dest, err := g.destination(&transport.MeshMessage{})
	if err != nil {
		return
	}
	g.sendLink(tetra.Link{Kind: tetra.LinkHello, Tag: g.acks.selfTag}, dest, time.Second+g.jitter(3*time.Second))
}

// helloDue says a Bridge that knows no peer should say hello again.
func (g *TetraGateway) helloDue() bool {
	if g.acksMode() == "off" || g.peerKnown() {
		return false
	}
	last := g.acks.lastHello.Load()
	return last == 0 || time.Since(time.Unix(last, 0)) >= tetraHelloEvery
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
	if l.Kind == tetra.LinkAck {
		// Signalled under the lock, so takeAck either sees it or the send
		// has already let the key go: an ack is never left in between.
		if ch := g.acks.pending[tetraAckKey{l.Ref, l.Hash}]; ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	g.acks.mu.Unlock()
	switch l.Kind {
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

// admit puts a whole text in the processor's queue unless the same text
// from the same sender was queued within tetraRepeatWindow (a repeat). A
// text is remembered only once it is in the queue: one the full queue
// refused is not, so the sender's retry, which the missing ack brings, is
// taken in (review 2026-10-18 13:47, TETRA-RX1). The queue never blocks
// the PEI reader. Taken in is the processor's queue in memory, not the
// database: a restart in between loses an acknowledged text. [MESHSAT-1584]
func (g *TetraGateway) admit(from, text string, in InboundMessage) tetraTaken {
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
		return tetraRepeat
	}
	select {
	case g.inCh <- in:
	default:
		return tetraRefused
	}
	g.acks.recent[key] = now
	return tetraQueued
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
