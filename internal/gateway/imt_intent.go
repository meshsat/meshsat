package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/codec"
	"meshsat/internal/transport"
)

// The RockBLOCK 9704 as a delivery-intent route (HeMB-NG, a paid route under
// B9; r3 D9, B7(f)). [MESHSAT-1584]
//
// One MO per submission on the raw topic, the wire as the egress chain made
// it (the version byte and base64 ciphertext the Hub reads today), in one
// segment, never cut. mo_ack_received is the network acknowledging the MO:
// accepted by the satellite gateway, and nothing at the device says more
// (Cloudloop and the Hub's routes take it on), so the outcome is never
// "delivered". How far a send got is the transport's account of the modem's
// final status (transport.SubmitMO): not sent, accepted, or may have left.
//
// The 9704 carries no recipient: who gets an MO is set by the provider's
// routes. So the route is admitted only for the contact whose IMT address is
// the destination the operator recorded for this modem (mo_destination);
// another address would get its message delivered there instead.

// IMTIntentCorrKind is the correlation kind of an IMT submission. No answer
// carries it; it names the submission in the ledger.
const IMTIntentCorrKind = "imt_ref"

// imtSubmitter is a modem that can say how far an MO got.
type imtSubmitter interface {
	SubmitMO(ctx context.Context, data []byte) (transport.IMTSubmit, error)
}

// sameIMTAddress compares two IMT addresses as an operator writes them.
func sameIMTAddress(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && strings.EqualFold(a, b)
}

// IntentCorrKind implements IntentAdapter.
func (g *IMTGateway) IntentCorrKind() string { return IMTIntentCorrKind }

// IntentNewCorr implements IntentAdapter.
func (g *IMTGateway) IntentNewCorr() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetIntentReceiptSink implements IntentAdapter; the MO's only answer comes
// within the call.
func (g *IMTGateway) SetIntentReceiptSink(func(IntentReceipt)) {}

// IntentBilled implements IntentBilled: every MO the network takes is billed.
func (g *IMTGateway) IntentBilled() bool { return true }

// IntentAdmit implements IntentAdapter.
func (g *IMTGateway) IntentAdmit(dest string, wire []byte, _ bool) (reason, detail string) {
	if _, ok := g.sat.(imtSubmitter); !ok {
		return "skipped_unsupported", "this modem cannot say whether an MO left"
	}
	to := strings.TrimSpace(g.config.MODestination)
	if to == "" {
		return "skipped_unsupported", "the 9704 carries no recipient, and no destination is recorded for this modem's messages (mo_destination)"
	}
	if !sameIMTAddress(dest, to) {
		return "skipped_unsupported", fmt.Sprintf("this modem's messages are delivered to %s, not %s; the 9704 carries no recipient", to, strings.TrimSpace(dest))
	}
	if len(wire) == 0 {
		return "skipped_encoding", "nothing to send"
	}
	if len(wire) > transport.IMTOneSegmentBytes {
		return "skipped_oversize", fmt.Sprintf("%d bytes, one MO segment takes %d", len(wire), transport.IMTOneSegmentBytes)
	}
	return "", ""
}

// IntentSend implements IntentAdapter: one MO, and how far it got.
func (g *IMTGateway) IntentSend(ctx context.Context, s IntentSend) IntentOutcome {
	if reason, detail := g.IntentAdmit(s.Destination, []byte(s.Text), s.Encrypted); reason != "" {
		return IntentOutcome{Class: IntentNotSent, Reason: reason, Detail: detail}
	}
	data := []byte(s.Text)
	res, err := g.sat.(imtSubmitter).SubmitMO(ctx, data)
	detail := res.Status
	if err != nil {
		g.recordGSSRegistration(false, 0)
		if detail != "" {
			detail += ": "
		}
		detail += err.Error()
	} else {
		g.recordGSSRegistration(res.Phase == transport.IMTAccepted, res.MOStatus)
	}
	switch res.Phase {
	case transport.IMTAccepted:
		g.msgsOut.Add(1)
		g.lastActive.Store(time.Now().Unix())
		feed := s.PlainText
		if !s.Encrypted {
			_, body := codec.StripVersionByte(data)
			feed = string(body)
		}
		g.noteMOSuccess(res.MOStatus, len(data), FeedText([]byte(feed)), s.MsgRef, "cloudloop")
		g.emit("forward", fmt.Sprintf("IMT intent MO acknowledged by the network (%s)", s.CorrValue))
		log.Info().Str("to", s.Destination).Str("corr", s.CorrValue).Int("bytes", len(data)).
			Msg("imt: intent MO acknowledged by the network")
		return IntentOutcome{Class: IntentAccepted, AcceptedBy: "satellite_gateway", Charged: true, ChargedUnits: 1,
			Detail: "acknowledged by the Iridium network (mo_ack_received)"}
	case transport.IMTUnknown:
		// It may have left, so its price stays counted as possible and
		// nothing is sent again for it (r3 D17).
		g.errors.Add(1)
		log.Warn().Str("corr", s.CorrValue).Str("status", res.Status).Err(err).Msg("imt: intent MO may have left")
		return IntentOutcome{Class: IntentUnknown, Detail: "may have left: " + detail}
	}
	if errors.Is(err, transport.ErrNotConnected) {
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: detail}
	}
	g.errors.Add(1)
	return IntentOutcome{Class: IntentNotSent, Retryable: res.Retry, ConsumeRetry: true, Reason: "not_sent",
		Detail: "not sent: " + detail}
}
