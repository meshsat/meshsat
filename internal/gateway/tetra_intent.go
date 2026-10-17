package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"meshsat/internal/tetra"
	"meshsat/internal/transport"
)

// The kit's TETRA radio as a delivery-intent route (HeMB-NG, owner ruling
// 17 Oct 2026). [MESHSAT-1584]
//
// One SDS per submission, the wire text as written, to the contact's SSI:
// today a DMO talk group (both MTP3550s carry one DMO ITSI, so an
// individual address cannot be told apart), later an individual ITSI.
// The radio reporting it sent (+CMGS status 4) is the most it says today,
// not that it arrived: a group SDS carries no delivery report, and two
// radios keying up at once both report it sent while neither arrives
// (half duplex, the TETRA session's collision test, 18 Oct 2026). So the
// outcome is accepted, not confirmed, never stronger; the route is free
// (Direct Mode, our own radio). A text
// over one SDS is refused for intents, not split: a later part failing
// after the first went would leave the message half sent. The gateway's
// Forward does the send, so its refusals keep their meaning: the radio's
// port gone is not connected, CME 24 too long, any other CME error before
// anything went on air not submitted, and everything else unknown. A send
// the radio took and then reported not sent (tetra.ErrRadioNotSent: a late
// +CMGS status other than 4, or +CME ERROR 42, as when both kits sent at
// the same instant on 17 Oct 2026) is the radio's own account that nothing
// went: not sent, tried again. Should the radio be wrong, the retry is a
// duplicate on a free route.

// TetraIntentCorrKind is the correlation kind of a TETRA submission. No
// answer carries it yet (group SDS have no reports).
const TetraIntentCorrKind = "tetra_ref"

// IntentCorrKind implements IntentAdapter.
func (g *TetraGateway) IntentCorrKind() string { return TetraIntentCorrKind }

// IntentNewCorr implements IntentAdapter.
func (g *TetraGateway) IntentNewCorr() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetIntentReceiptSink implements IntentAdapter: no report comes for a
// group SDS.
func (g *TetraGateway) SetIntentReceiptSink(func(IntentReceipt)) {}

// IntentAdmit implements IntentAdapter.
func (g *TetraGateway) IntentAdmit(dest string, wire []byte, _ bool) (reason, detail string) {
	if _, ok := tetra.ParseAddress(dest); !ok {
		return "skipped_unsupported", fmt.Sprintf("%q is not an SDS address (an SSI or tetra:<ssi>)", strings.TrimSpace(dest))
	}
	if len(wire) == 0 {
		return "skipped_encoding", "nothing to send"
	}
	g.mu.RLock()
	limit := g.textLimit()
	g.mu.RUnlock()
	if n := tetra.TextBytes(string(wire)); n > limit {
		return "skipped_oversize", fmt.Sprintf("%d bytes, one SDS holds %d", n, limit)
	}
	return "", ""
}

// IntentSend implements IntentAdapter: one SDS, and how far it got.
func (g *TetraGateway) IntentSend(ctx context.Context, s IntentSend) IntentOutcome {
	if reason, detail := g.IntentAdmit(s.Destination, []byte(s.Text), s.Encrypted); reason != "" {
		return IntentOutcome{Class: IntentNotSent, Reason: reason, Detail: detail}
	}
	err := g.Forward(ctx, &transport.MeshMessage{PortNum: 1, DecodedText: s.Text, Destination: s.Destination,
		AsWritten: true, Encrypted: s.Encrypted, RawText: s.Encrypted, MsgRef: s.MsgRef})
	switch {
	case err == nil:
		// The radio believes it sent it (+CMGS status 4, or no report): not
		// that it arrived. In the TETRA session's collision test both radios
		// once reported 4 and neither SDS arrived (half duplex). [MESHSAT-1584]
		return IntentOutcome{Class: IntentAccepted, AcceptedBy: "radio",
			Detail: "the TETRA radio reports it sent; a group SDS carries no report, so arrival is not known"}
	case errors.Is(err, transport.ErrNotConnected):
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: err.Error()}
	case errors.Is(err, transport.ErrPayloadTooLong):
		return IntentOutcome{Class: IntentNotSent, Reason: "skipped_oversize", Detail: err.Error()}
	case errors.Is(err, transport.ErrNoRecipient):
		return IntentOutcome{Class: IntentNotSent, Reason: "skipped_unsupported", Detail: err.Error()}
	case errors.Is(err, transport.ErrNotSubmitted), errors.Is(err, tetra.ErrRadioNotSent):
		return IntentOutcome{Class: IntentNotSent, Retryable: true, ConsumeRetry: true, Reason: "not_sent", Detail: err.Error()}
	}
	// A timeout or a reply that did not come: the radio may have sent it.
	return IntentOutcome{Class: IntentUnknown, Detail: "may have been sent: " + err.Error()}
}
