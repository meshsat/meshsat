package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"meshsat/internal/smpp"
	"meshsat/internal/transport"
)

// The MeshSat 2G cell as a delivery-intent route (owner ruling 17 Oct 2026:
// a contact kind of its own, CELL, the phone's short MSISDN on our cell).
// [MESHSAT-1584]
//
// One submit_sm per submission, the wire text as written, in one SMPP part:
// a text over one part is refused for intents, so a phone never holds half
// of it. In forward mode osmo-msc answers a submit_sm only once the phone
// acknowledged it (RP-ACK) or the delivery failed (r3 section 4): status 0
// is the handset's receipt, delivered_handset, inside the call; a phone not
// on the cell (0x0B) is not sent and may be tried again; a delivery failure
// (0xFE) may have reached the phone (ErrCellDeliveryFailure), so it is
// unknown and never sent again by itself (review 2026-10-18 13:47,
// CELL-R1); any other refusal reached no phone. The cell is licensed
// airtime, no charge.

// CellIntentCorrKind is the correlation kind of a cell submission.
const CellIntentCorrKind = "cell_ref"

// IntentCorrKind implements IntentAdapter.
func (g *CellGateway) IntentCorrKind() string { return CellIntentCorrKind }

// IntentNewCorr implements IntentAdapter.
func (g *CellGateway) IntentNewCorr() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetIntentReceiptSink implements IntentAdapter: the handset's answer comes
// within the call.
func (g *CellGateway) SetIntentReceiptSink(func(IntentReceipt)) {}

// IntentAdmit implements IntentAdapter.
func (g *CellGateway) IntentAdmit(dest string, wire []byte, _ bool) (reason, detail string) {
	if _, ok := ParseCellAddress(dest); !ok {
		return "skipped_unsupported", fmt.Sprintf("%q is not a phone on the cell (digits, or cell:<digits>)", strings.TrimSpace(dest))
	}
	if len(wire) == 0 {
		return "skipped_encoding", "nothing to send"
	}
	parts, err := smpp.SplitText(string(wire), 0)
	if err != nil {
		return "skipped_encoding", err.Error()
	}
	if len(parts) > 1 {
		return "skipped_oversize", fmt.Sprintf("%d SMS parts; an intent goes to the cell in one", len(parts))
	}
	return "", ""
}

// IntentSend implements IntentAdapter: one submit_sm, and what the cell
// said.
func (g *CellGateway) IntentSend(ctx context.Context, s IntentSend) IntentOutcome {
	if reason, detail := g.IntentAdmit(s.Destination, []byte(s.Text), s.Encrypted); reason != "" {
		return IntentOutcome{Class: IntentNotSent, Reason: reason, Detail: detail}
	}
	msisdn, _ := ParseCellAddress(s.Destination)
	err := g.Forward(ctx, &transport.MeshMessage{PortNum: 1, DecodedText: s.Text, Destination: CellAddress(msisdn),
		AsWritten: true, Encrypted: s.Encrypted, RawText: s.Encrypted, MsgRef: s.MsgRef})
	switch {
	case err == nil:
		r := &IntentReceipt{CorrKind: CellIntentCorrKind, CorrValue: s.CorrValue, Peer: s.Destination,
			Source: "handset", Event: "submit_sm_resp:0", Completes: "delivered_handset", Trust: "smpp_session"}
		return IntentOutcome{Class: IntentAccepted, AcceptedBy: "handset", Receipt: r,
			Detail: "the phone acknowledged it on the cell (submit_sm in forward mode)"}
	case errors.Is(err, transport.ErrNotConnected):
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: err.Error()}
	case errors.Is(err, ErrCellDeliveryFailure):
		// Paging expired, the phone refused it, or the cell lost track:
		// the cell's answer alone cannot say the phone did not get it.
		return IntentOutcome{Class: IntentUnknown, Detail: "may have been sent: " + err.Error()}
	case errors.Is(err, transport.ErrSubscriberAbsent):
		// Not on the cell now; a phone that comes back registers at once.
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "not_sent", Detail: err.Error()}
	case errors.Is(err, transport.ErrNoRecipient):
		return IntentOutcome{Class: IntentNotSent, Reason: "skipped_unsupported", Detail: err.Error()}
	case errors.Is(err, transport.ErrNotSubmitted):
		return IntentOutcome{Class: IntentNotSent, Retryable: true, ConsumeRetry: true, Reason: "not_sent", Detail: err.Error(), BearerRefused: true}
	}
	// A submit_sm whose answer never came: the phone may have it.
	return IntentOutcome{Class: IntentUnknown, Detail: "may have been sent: " + err.Error()}
}
