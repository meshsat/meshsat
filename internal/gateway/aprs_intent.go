package gateway

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// APRS as a delivery-intent route (HeMB-NG stage 1b, r3 B7(b)(e)(l)).
// [MESHSAT-1584]
//
// Every send carries a message id the engine chose and stored before the
// send ("{ID"): an encrypted frame as {E1}<ciphertext>{ID, which a MeshSat
// peer acks; a plain text as a directed message :CALL:text{ID, which any
// APRS station acks (APRS 1.0.1, chapter 14). The gateway's own ack
// retransmissions belong to the one submission. Nothing is cut or changed:
// a text that does not fit is refused at admission and before the send.

const (
	// APRSIntentCorrKind is the correlation kind of an APRS answer.
	APRSIntentCorrKind = "aprs_msgid"
	// aprsMaxMessageText is the longest directed message text (APRS 1.0.1).
	aprsMaxMessageText = 67
	// aprsMaxInfo is the longest AX.25 information field.
	aprsMaxInfo = 256
)

// aprsIntentState holds this gateway's own receipt sink; without one the
// process-wide sink takes the answers (SetDefaultIntentReceiptSink).
type aprsIntentState struct {
	mu   sync.Mutex
	sink func(IntentReceipt)
}

func (s *aprsIntentState) currentSink() func(IntentReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sink != nil {
		return s.sink
	}
	return defaultIntentReceiptSink()
}

// errAPRSWriteStarted marks a write that failed after bytes went to the TNC
// or the APRS-IS server: the frame may be on the air (APRS-R1).
var errAPRSWriteStarted = errors.New("aprs: the write started before it failed")

// IntentCorrKind implements IntentAdapter.
func (g *APRSGateway) IntentCorrKind() string { return APRSIntentCorrKind }

// SetIntentReceiptSink implements IntentAdapter.
func (g *APRSGateway) SetIntentReceiptSink(fn func(IntentReceipt)) {
	g.intents.mu.Lock()
	g.intents.sink = fn
	g.intents.mu.Unlock()
}

// IntentNewCorr picks a random five-character message id (the longest
// APRS allows; 36^5 values).
func (g *APRSGateway) IntentNewCorr() (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, aprsAckIDLen)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b), nil
}

// IntentAdmit checks the wire text against this gateway's mode: an
// encrypted frame's information field fits 256 bytes with its id; a plain
// directed message is at most 67 characters of printable ASCII without the
// characters APRS reserves in message text; APRS-IS carries no ciphertext.
func (g *APRSGateway) IntentAdmit(dest string, wire []byte, encrypted bool) (reason, detail string) {
	if !aprsAddresseeRE.MatchString(strings.ToUpper(strings.TrimSpace(dest))) {
		return "skipped_bad_address", fmt.Sprintf("%q is not an APRS callsign", dest)
	}
	if encrypted {
		if g.is != nil {
			return "skipped_unsupported", "APRS-IS carries no ciphertext"
		}
		msg := &transport.MeshMessage{DecodedText: string(wire)}
		info := len(aprsEncryptedPrefix) + len(aprsCipherText(msg)) + 1 + aprsAckIDLen
		if info > aprsMaxInfo {
			return "skipped_oversize", fmt.Sprintf("an encrypted frame of %d bytes, the limit is %d", info, aprsMaxInfo)
		}
		return "", ""
	}
	text := string(wire)
	if !utf8.ValidString(text) {
		return "skipped_encoding", "not text"
	}
	for _, r := range text {
		if r < 0x20 || r > 0x7e || r == '|' || r == '~' || r == '{' {
			return "skipped_encoding", fmt.Sprintf("character %q cannot go in an APRS message", r)
		}
	}
	if n := utf8.RuneCountInString(text); n > aprsMaxMessageText {
		return "skipped_oversize", fmt.Sprintf("%d characters, an APRS message takes %d", n, aprsMaxMessageText)
	}
	return "", ""
}

// IntentSend sends one intent attempt and waits for its answer.
func (g *APRSGateway) IntentSend(ctx context.Context, s IntentSend) IntentOutcome {
	if reason, detail := g.IntentAdmit(s.Destination, []byte(s.Text), s.Encrypted); reason != "" {
		return IntentOutcome{Class: IntentNotSent, Reason: reason, Detail: detail}
	}
	id := strings.ToUpper(s.CorrValue)
	if !validAckID(id) {
		return IntentOutcome{Class: IntentNotSent, Reason: "not_sent", Detail: fmt.Sprintf("message id %q is not valid", s.CorrValue)}
	}
	dest := strings.ToUpper(strings.TrimSpace(s.Destination))
	stopped := g.runDone()
	if stopped == nil {
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: "aprs gateway not running"}
	}
	acked := g.acks.registerIntent(id, dest)
	defer g.acks.release(id)

	msg := &transport.MeshMessage{Destination: dest, DecodedText: s.Text, Encrypted: s.Encrypted, MsgRef: s.MsgRef}
	attempts := g.config.ackAttempts()
	if attempts < 1 {
		attempts = 1
	}
	sent, queued, maybe := 0, false, false
	start := time.Now()
	ended := func(why string) IntentOutcome {
		switch {
		case sent > 0:
			return IntentOutcome{Class: IntentAccepted, AcceptedBy: "radio",
				Detail: fmt.Sprintf("%d transmission(s), no answer before %s", sent, why)}
		case queued, maybe:
			// Handed to the write worker, or a write that failed after it
			// started: it may still be, or be, on the air (APRS-R1).
			return IntentOutcome{Class: IntentUnknown, Detail: "may be on the air: " + why}
		}
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: why}
	}
	// waitAnswer waits one ack timeout for the addressed station.
	waitAnswer := func() (IntentOutcome, bool) {
		wait := time.NewTimer(jitterDuration(g.config.ackTimeout(), 0.2))
		defer wait.Stop()
		select {
		case <-acked:
			return g.intentAnswered(id, dest, start), true
		case <-wait.C:
			return IntentOutcome{}, false
		case <-stopped:
			return ended("the gateway stopped"), true
		case <-ctx.Done():
			return ended("the send was abandoned"), true
		}
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		var item *aprsOutbound
		if g.is != nil {
			if err := g.aprsISTransmit(string(EncodeAPRSMessage(dest, s.Text, id))); err != nil {
				if errors.Is(err, errAPRSWriteStarted) {
					// The line may be out: wait for its answer, send no more.
					maybe = true
					if o, done := waitAnswer(); done {
						return o
					}
					return ended("the connection failed during a write: " + err.Error())
				}
				if sent > 0 {
					return ended("the connection failed: " + err.Error())
				}
				return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: err.Error()}
			}
			sent++
		} else {
			item = &aprsOutbound{msg: msg, ackID: id, sent: make(chan error, 1)}
			select {
			case g.outCh <- item:
				queued = true
			case <-acked:
				return g.intentAnswered(id, dest, start)
			case <-stopped:
				return ended("the gateway stopped")
			case <-ctx.Done():
				return ended("the send was abandoned")
			}
			select {
			case err := <-item.sent:
				queued = false
				if err != nil {
					if errors.Is(err, errAPRSWriteStarted) {
						// Bytes went to the TNC: the frame may be on the air.
						// Wait for its answer and send no more (APRS-R1).
						maybe = true
						if o, done := waitAnswer(); done {
							return o
						}
						return ended("the TNC failed during a write: " + err.Error())
					}
					// Nothing of this frame went to the TNC.
					if sent == 0 {
						return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: err.Error()}
					}
					return ended("the radio failed: " + err.Error())
				}
				sent++
			case <-acked:
				return g.intentAnswered(id, dest, start)
			case <-stopped:
				return ended("the gateway stopped")
			case <-ctx.Done():
				return ended("the send was abandoned")
			}
		}
		if o, done := waitAnswer(); done {
			return o
		}
	}
	return ended(fmt.Sprintf("%d attempts", attempts))
}

// intentAnswered is the outcome when an ack or reject came within the call.
func (g *APRSGateway) intentAnswered(id, dest string, start time.Time) IntentOutcome {
	from, reject := g.acks.answerOf(id)
	r := aprsIntentReceipt(id, from, dest, reject)
	out := IntentOutcome{Class: IntentAccepted, AcceptedBy: "radio", Receipt: &r,
		Detail: fmt.Sprintf("%s by %s after %s", r.Event, from, time.Since(start).Round(time.Millisecond))}
	log.Info().Str("ack_id", id).Str("from", from).Bool("reject", reject).Msg("aprs: intent send answered")
	return out
}

// aprsAckedStation is what an APRS ack proves: the station acknowledged
// the message; unauthenticated, so never "delivered" (r3 D11, APRS-R3).
const aprsAckedStation = "acked_station"

// aprsIntentReceipt is an ack or reject as evidence: an ack from the
// addressed station completes acked_station; an ack from another station,
// or a reject, completes nothing.
func aprsIntentReceipt(id, from, dest string, reject bool) IntentReceipt {
	r := IntentReceipt{CorrKind: APRSIntentCorrKind, CorrValue: id, Scope: "aprs_0", Peer: from,
		Source: "station", Event: "ack", Trust: "unauthenticated"}
	switch {
	case reject:
		r.Event = "rej"
	case sameStation(from, dest):
		r.Completes = aprsAckedStation
	}
	return r
}

// lateIntentReceipt hands an answer no send waited for to the ledger,
// which matches its id against the transmissions it kept and completes one
// only when it went to the answering station: the gateway itself keeps
// nothing, so a restart or a new gateway loses no answer (APRS-R2).
func (g *APRSGateway) lateIntentReceipt(id, from string, reject bool) {
	sink := g.intents.currentSink()
	if sink == nil {
		return
	}
	r := IntentReceipt{CorrKind: APRSIntentCorrKind, CorrValue: id, Scope: "aprs_0", Peer: from,
		Source: "station", Event: "ack", Trust: "unauthenticated"}
	if reject {
		r.Event = "rej"
	} else {
		r.CompletesIfPeer = aprsAckedStation
	}
	log.Info().Str("ack_id", id).Str("from", from).Bool("reject", reject).Msg("aprs: answer with no send waiting, to the ledger")
	sink(r)
}
