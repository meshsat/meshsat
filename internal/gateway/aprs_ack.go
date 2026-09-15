package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/codec"
	"meshsat/internal/transport"
)

// Per-message acknowledgement for encrypted APRS frames. [MESHSAT-1021]
//
// An APRS UI frame carries no acknowledgement. Between the kits a relayed text
// lost both of its copies about one time in fifteen (112 of 120 on 13 Sep
// 2026), and nothing told the sender, so the SMS fallback never engaged for
// it. Only a MeshSat peer can read an encrypted frame, so for those the gateway
// uses the APRS message-ack convention: the sender appends a message id to the
// info field ("{E1}<ciphertext>{ABCDE"), the receiver answers with a standard
// APRS ack message addressed to the sender (":MSTSRT-10:ackABCDE"), and a frame
// that is not acked in time goes out again, one copy per attempt. When every
// attempt goes unanswered Forward fails with ErrAPRSNoAck. Plaintext frames and
// directed messages are unchanged.

// ErrAPRSNoAck means the peer never acknowledged an encrypted frame.
var ErrAPRSNoAck = fmt.Errorf("aprs: %w", transport.ErrNoAck)

const (
	defaultAPRSAckAttempts = 4
	defaultAPRSAckTimeout  = 8 * time.Second
	// aprsAckIDLen is the message id length: five alphanumerics, the longest
	// APRS 1.0.1 allows.
	aprsAckIDLen = 5
	// aprsReAckAfter keeps one burst of a frame from being acked twice. The
	// repeat copy arrives later than this and is acked again, in case the
	// first ack was lost.
	aprsReAckAfter = 2 * time.Second
)

// ackAttempts is how many times an encrypted frame is sent while waiting for
// its ack; 0 means acknowledgement is off.
func (c APRSConfig) ackAttempts() int {
	switch {
	case c.AckAttempts < 0:
		return 0
	case c.AckAttempts == 0:
		return defaultAPRSAckAttempts
	}
	return c.AckAttempts
}

// ackTimeout is how long the sender waits for an ack after an attempt's last copy.
func (c APRSConfig) ackTimeout() time.Duration {
	if c.AckTimeoutMs > 0 {
		return time.Duration(c.AckTimeoutMs) * time.Millisecond
	}
	return defaultAPRSAckTimeout
}

// aprsCipherText is the ciphertext an encrypted message puts on the air: the
// DeliveryWorker's output without its binary protocol version byte, which APRS
// replaces with the {E1} prefix.
func aprsCipherText(msg *transport.MeshMessage) []byte {
	cipherText := []byte(msg.DecodedText)
	if _, stripped := codec.StripVersionByte(cipherText); stripped != nil {
		cipherText = stripped
	}
	return cipherText
}

// aprsAckID derives a frame's message id from its ciphertext, so every copy and
// every retransmission of one message carries the same id and an ack for any of
// them releases the sender.
func aprsAckID(cipherText []byte) string {
	sum := sha256.Sum256(cipherText)
	const space = 36 * 36 * 36 * 36 * 36
	id := strings.ToUpper(strconv.FormatUint(binary.BigEndian.Uint64(sum[:8])%space, 36))
	return strings.Repeat("0", aprsAckIDLen-len(id)) + id
}

// splitAckRequest separates a trailing "{ID" from an encrypted frame's body.
// Base64 never contains '{', so a body without a valid id comes back whole.
func splitAckRequest(body string) (cipherText, id string) {
	i := strings.LastIndexByte(body, '{')
	if i < 0 || !validAckID(body[i+1:]) {
		return body, ""
	}
	return body[:i], body[i+1:]
}

func validAckID(id string) bool {
	if id == "" || len(id) > aprsAckIDLen {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

// aprsAckReply reports whether an APRS message is an ack or a reject and which
// message id it answers. Neither is a message for a person, so the gateway
// never forwards one.
func aprsAckReply(pkt *APRSPacket) (id string, reject, ok bool) {
	if pkt == nil || pkt.DataType != ':' {
		return "", false, false
	}
	m := strings.TrimSpace(pkt.Message)
	if len(m) <= 3 {
		return "", false, false
	}
	switch m[:3] {
	case "ack":
	case "rej":
		reject = true
	default:
		return "", false, false
	}
	id = m[3:]
	// The reply-ack form ("ackAB}CD") carries the answered id before the brace.
	if i := strings.IndexByte(id, '}'); i >= 0 {
		id = id[:i]
	}
	if !validAckID(id) {
		return "", false, false
	}
	return id, reject, true
}

// aprsOutbound is one message for the write worker. With an ack id the frame
// carries it and goes out once, and the outcome of the transmission is reported
// on sent.
type aprsOutbound struct {
	msg   *transport.MeshMessage
	ackID string
	sent  chan error // buffered; nil for a message nobody waits on
}

func (o *aprsOutbound) report(err error) {
	if o.sent == nil {
		return
	}
	select {
	case o.sent <- err:
	default:
	}
}

// aprsAckWaiters tracks the frames waiting for an ack, by message id.
type aprsAckWaiters struct {
	mu sync.Mutex
	m  map[string]*aprsAckWaiter
}

type aprsAckWaiter struct {
	acked chan struct{}
	done  bool
}

func (w *aprsAckWaiters) register(id string) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m == nil {
		w.m = make(map[string]*aprsAckWaiter)
	}
	if e, ok := w.m[id]; ok {
		return e.acked
	}
	e := &aprsAckWaiter{acked: make(chan struct{})}
	w.m[id] = e
	return e.acked
}

// pending is how many frames this gateway is currently waiting an ack for.
func (w *aprsAckWaiters) pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, e := range w.m {
		if !e.done {
			n++
		}
	}
	return n
}

func (w *aprsAckWaiters) release(id string) {
	w.mu.Lock()
	delete(w.m, id)
	w.mu.Unlock()
}

// resolve marks id acked and reports whether anyone was waiting for it.
func (w *aprsAckWaiters) resolve(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.m[id]
	if !ok {
		return false
	}
	if !e.done {
		e.done = true
		close(e.acked)
	}
	return true
}

// aprsAckLedger remembers when each (station, id) was last acked.
type aprsAckLedger struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *aprsAckLedger) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = make(map[string]time.Time)
	}
	if t, ok := l.last[key]; ok && now.Sub(t) < aprsReAckAfter {
		return false
	}
	for k, t := range l.last {
		if now.Sub(t) > time.Minute {
			delete(l.last, k)
		}
	}
	l.last[key] = now
	return true
}

// runDone is closed when the running gateway stops; nil before Start.
func (g *APRSGateway) runDone() <-chan struct{} {
	g.lifeMu.Lock()
	defer g.lifeMu.Unlock()
	if g.done == nil {
		return nil
	}
	return g.done
}

// forwardAcked sends an encrypted message and waits for the peer's ack, sending
// it again up to ackAttempts times. It blocks, as Forward's contract says, so
// the delivery worker learns whether the message arrived.
func (g *APRSGateway) forwardAcked(ctx context.Context, msg *transport.MeshMessage) error {
	stopped := g.runDone()
	if stopped == nil {
		return fmt.Errorf("aprs: gateway not running: %w", transport.ErrNotConnected)
	}
	errStopped := fmt.Errorf("aprs: gateway stopped: %w", transport.ErrNotConnected)

	id := aprsAckID(aprsCipherText(msg))
	acked := g.acks.register(id)
	defer g.acks.release(id)

	attempts := g.config.ackAttempts()
	start := time.Now()
	for attempt := 1; attempt <= attempts; attempt++ {
		item := &aprsOutbound{msg: msg, ackID: id, sent: make(chan error, 1)}
		select {
		case g.outCh <- item:
		case <-acked:
			return g.acknowledged(msg, id, attempt-1, start)
		case <-stopped:
			return errStopped
		case <-ctx.Done():
			return ctx.Err()
		}

		select {
		case err := <-item.sent:
			if err != nil {
				return err
			}
		case <-acked:
			return g.acknowledged(msg, id, attempt, start)
		case <-stopped:
			return errStopped
		case <-ctx.Done():
			return ctx.Err()
		}

		wait := time.NewTimer(jitterDuration(g.config.ackTimeout(), 0.2))
		select {
		case <-acked:
			wait.Stop()
			return g.acknowledged(msg, id, attempt, start)
		case <-wait.C:
		case <-stopped:
			wait.Stop()
			return errStopped
		case <-ctx.Done():
			wait.Stop()
			return ctx.Err()
		}
		if attempt < attempts {
			g.ackRetries.Add(1)
			log.Info().Str("ack_id", id).Str("msg_ref", msg.MsgRef).Int("attempt", attempt+1).Int("of", attempts).
				Msg("aprs: no ack from the peer, sending the message again")
		}
	}
	g.ackFailures.Add(1)
	log.Warn().Str("ack_id", id).Str("msg_ref", msg.MsgRef).Int("attempts", attempts).
		Dur("after", time.Since(start).Round(time.Second)).Msg("aprs: the peer never acknowledged the message")
	return fmt.Errorf("%w (id %s, %d attempts)", ErrAPRSNoAck, id, attempts)
}

func (g *APRSGateway) acknowledged(msg *transport.MeshMessage, id string, attempts int, start time.Time) error {
	log.Info().Str("ack_id", id).Str("msg_ref", msg.MsgRef).Int("attempts", attempts).
		Dur("after", time.Since(start).Round(time.Millisecond)).Msg("aprs: message acknowledged by the peer")
	return nil
}

// sendAck answers an encrypted frame's message id with a standard APRS ack
// addressed to its sender. It is a plain message on purpose: it carries no
// content, and any APRS station can tell what it is.
func (g *APRSGateway) sendAck(to AX25Address, id string) {
	addressee := FormatCallsign(to)
	if !g.ackLedger.allow(addressee+"|"+id, time.Now()) {
		return
	}
	src := AX25Address{Call: g.config.Callsign, SSID: g.config.SSID}
	dst := AX25Address{Call: "APMSHT", SSID: 0}
	frame := EncodeAX25Frame(dst, src, nil, EncodeAPRSMessage(addressee, "ack"+id, ""))
	select {
	case g.rawOut <- frame:
		g.acksSent.Add(1)
	default:
		log.Debug().Str("ack_id", id).Msg("aprs: ack skipped, transmit queue busy")
	}
}

// handleAckReply releases the sender waiting on an ack addressed to this
// gateway. Acks for other stations and rejects release nothing.
func (g *APRSGateway) handleAckReply(pkt *APRSPacket, id string, reject bool) {
	own := FormatCallsign(AX25Address{Call: strings.ToUpper(strings.TrimSpace(g.config.Callsign)), SSID: g.config.SSID})
	if !strings.EqualFold(strings.TrimSpace(pkt.MsgTo), own) {
		return
	}
	if reject {
		log.Warn().Str("from", pkt.Source).Str("ack_id", id).Msg("aprs: the peer rejected a message")
		return
	}
	if g.acks.resolve(strings.ToUpper(id)) {
		g.acksReceived.Add(1)
		log.Debug().Str("from", pkt.Source).Str("ack_id", id).Msg("aprs: ack received")
	}
}
