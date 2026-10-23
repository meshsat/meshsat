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

// The RockBLOCK 9603 as a delivery-intent route (HeMB-NG, a paid route under
// B9; r3 D8, D17, D18). [MESHSAT-1584]
//
// One SBD session per submission, carrying the MO exactly as the legacy path
// builds it for the same row (sendSBD): printable ASCII up to 120 characters
// as text, anything else in the compact frame, From 0 and time 0 as the
// delivery worker's message. The compact frame cuts a text longer than its
// room; here such a text is refused, never cut. How far it got is the
// transport's account (transport.SBDOutcome): MO status 0 to 4 is the
// Iridium gateway taking the MO, and nothing at the device says more, so the
// outcome is never "delivered"; a session that may have completed unseen is
// unknown, and a paid unknown is never sent again by itself (D17).
//
// The 9603 carries no recipient: the provider's delivery groups decide who
// gets an MO. So the route is admitted only for the contact whose SBD
// address is the destination the operator recorded for this modem's
// messages (mo_destination), as on the 9704.

// SBDIntentCorrKind is the correlation kind of an SBD submission. No answer
// carries it; it names the submission in the ledger.
const SBDIntentCorrKind = "sbd_ref"

// compactTextRoom is how much text the compact frame takes whole.
func (g *SBDGateway) compactTextRoom() int {
	head := 10 // flags, portnum, from, timestamp
	if g.config.IncludePosition {
		head += positionFieldSize
	}
	room := maxSBDPayload - head - 1 // the length byte
	if room > 255 {
		room = 255
	}
	return room
}

// sbdMO is the MO the wire text becomes, and whether it goes as text.
func (g *SBDGateway) sbdMO(wire []byte) (data []byte, text bool, reason, detail string) {
	if len(wire) == 0 {
		return nil, false, "skipped_encoding", "nothing to send"
	}
	msg := &transport.MeshMessage{PortNum: 1, PortNumName: "TEXT_MESSAGE_APP", DecodedText: string(wire)}
	if canSendPlaintext(msg) {
		return wire, true, "", ""
	}
	if room := g.compactTextRoom(); len(wire) > room {
		return nil, false, "skipped_oversize", fmt.Sprintf("%d bytes, the SBD frame takes %d whole", len(wire), room)
	}
	data, err := EncodeCompact(msg, g.config.IncludePosition)
	if err != nil {
		return nil, false, "skipped_encoding", err.Error()
	}
	return data, false, "", ""
}

// SBDIntentFrame is the MO the intent path makes of wire on a 9603 set up
// with cfg, or why it refuses it: sbdMO, for the HeMB-NG S3-2 measurement.
func SBDIntentFrame(cfg IridiumConfig, wire []byte) (mo []byte, text bool, reason, detail string) {
	return (&SBDGateway{IridiumGateway: IridiumGateway{config: cfg}}).sbdMO(wire)
}

// IntentCorrKind implements IntentAdapter.
func (g *SBDGateway) IntentCorrKind() string { return SBDIntentCorrKind }

// IntentNewCorr implements IntentAdapter.
func (g *SBDGateway) IntentNewCorr() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetIntentReceiptSink implements IntentAdapter; the session's only answer
// comes within the call.
func (g *SBDGateway) SetIntentReceiptSink(func(IntentReceipt)) {}

// IntentBilled implements IntentBilled: every MO the gateway takes is billed.
func (g *SBDGateway) IntentBilled() bool { return true }

// IntentBilledBytes implements IntentSized: the MO the provider bills is
// the frame, not the wire text.
func (g *SBDGateway) IntentBilledBytes(wire []byte) int {
	data, _, reason, _ := g.sbdMO(wire)
	if reason != "" {
		return len(wire)
	}
	return len(data)
}

// IntentAdmit implements IntentAdapter.
func (g *SBDGateway) IntentAdmit(dest string, wire []byte, _ bool) (reason, detail string) {
	to := strings.TrimSpace(g.config.MODestination)
	if to == "" {
		return "skipped_unsupported", "the 9603 carries no recipient, and no destination is recorded for this modem's messages (mo_destination)"
	}
	if !sameIMTAddress(dest, to) {
		return "skipped_unsupported", fmt.Sprintf("this modem's messages are delivered to %s, not %s; the 9603 carries no recipient", to, strings.TrimSpace(dest))
	}
	if _, _, reason, detail := g.sbdMO(wire); reason != "" {
		return reason, detail
	}
	return "", ""
}

// IntentSend implements IntentAdapter: one session, and how far it got.
func (g *SBDGateway) IntentSend(ctx context.Context, s IntentSend) IntentOutcome {
	if reason, detail := g.IntentAdmit(s.Destination, []byte(s.Text), s.Encrypted); reason != "" {
		return IntentOutcome{Class: IntentNotSent, Reason: reason, Detail: detail}
	}
	data, asText, _, _ := g.sbdMO([]byte(s.Text))
	cost := creditCost(len(data))
	// The gateway's own credit budget still holds (r3 S6: the send-time
	// check, besides the intent's cap); a send it cannot afford is refused.
	if !g.budgetAllows(cost, 1) {
		return IntentOutcome{Class: IntentNotSent, Reason: "skipped_budget",
			Detail: fmt.Sprintf("not sent: the SBD credit budget has no room for %d credit(s)", cost)}
	}
	var res *transport.SatResult
	var err error
	if asText {
		res, err = g.sat.SendText(ctx, string(data))
	} else {
		res, err = g.sat.Send(ctx, data)
	}
	sub := transport.SBDOutcome(res, err)
	if res != nil && sub.MOStatus >= 0 {
		g.recordGSSRegistration(res.MOSuccess(), res.MOStatus)
	} else if err != nil && sub.Phase != transport.SBDNotSent {
		g.recordGSSRegistration(false, 0)
	}
	switch sub.Phase {
	case transport.SBDAccepted:
		g.msgsOut.Add(1)
		g.lastActive.Store(time.Now().Unix())
		feed := s.PlainText
		if !s.Encrypted {
			_, body := codec.StripVersionByte([]byte(s.Text))
			feed = string(body)
		}
		g.noteMOSuccess(res.MOStatus, len(data), FeedText([]byte(feed)), s.MsgRef, "rock7")
		g.emit("forward", fmt.Sprintf("SBD intent MO taken by the Iridium gateway (mo_status=%d, %s)", res.MOStatus, s.CorrValue))
		if g.db != nil {
			g.db.InsertCreditUsage(nil, cost, nil)
		}
		log.Info().Str("to", s.Destination).Str("corr", s.CorrValue).Int("bytes", len(data)).Int("mo_status", res.MOStatus).
			Msg("sbd: intent MO taken by the Iridium gateway")
		if res.MTReceived || res.MTStatus == 1 || res.MTQueued > 0 {
			go g.handleRingAlert(g.runContext())
		}
		return IntentOutcome{Class: IntentAccepted, AcceptedBy: "satellite_gateway", Charged: true, ChargedUnits: cost,
			Detail: fmt.Sprintf("taken by the Iridium gateway (mo_status %d, MOMSN %d)", res.MOStatus, res.MOMSN)}
	case transport.SBDUnknown:
		// It may have left, so its price stays counted as possible and
		// nothing is sent again for it (r3 D17).
		g.errors.Add(1)
		log.Warn().Str("corr", s.CorrValue).Int("mo_status", sub.MOStatus).Str("status", sub.Status).
			Msg("sbd: intent MO may have left")
		return IntentOutcome{Class: IntentUnknown, Detail: "may have left: " + sub.Status}
	}
	if sub.Offline {
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: sub.Status}
	}
	g.errors.Add(1)
	return IntentOutcome{Class: IntentNotSent, Retryable: sub.Retry, ConsumeRetry: true, Reason: "not_sent",
		Detail: "not sent: " + sub.Status, BearerRefused: (err == nil && sub.MOStatus >= 0) || errors.Is(err, transport.ErrModemFailed)}
}
