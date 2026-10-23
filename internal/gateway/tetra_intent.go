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
// The radio reporting it sent (+CMGS status 4) is not that it arrived: a
// group SDS carries no delivery report, and two radios keying up at once
// both report it sent while neither arrives (half duplex, the TETRA
// session's collision test, 18 Oct 2026). What can say more is a MeshSat
// Bridge on the group: it acknowledges every text it receives with a link
// SDS (tetra_ack.go), and while one is known the gateway's Send waits for
// that. Every SDS acknowledged is acked by a station, unauthenticated
// (anyone on the group can send that SDS, as with an APRS ack); sent with
// no peer known is accepted, not confirmed; sent with a peer known and no
// acknowledgement is unknown (a collision both radios missed, or the
// acknowledgement was lost), never sent again on this route. The route is
// free (Direct Mode, our own radio). A text
// over one SDS is refused for intents, not split: a later part failing
// after the first went would leave the message half sent. The gateway's
// Forward does the send, so its refusals keep their meaning: the radio's
// port gone before the SDS was written is not connected, CME 24 too long,
// any other CME error before anything went on air not submitted, and
// everything else unknown (review 2026-10-18 13:47, TETRA-TX1): the port
// closing after the whole SDS was written (tetra.ErrSDSInFlight), and a
// send the radio took and then reported not sent (tetra.ErrRadioNotSent: a
// late +CMGS status other than 4, or +CME ERROR 42, as when both kits sent
// at the same instant on 17 Oct 2026). ETSI EN 300 392-5 keeps the
// terminal taking an SDS apart from its later transmission status, and
// status 5 is a failed transmission, not that no radio heard any of it
// (another may have decoded it by capture), so neither is sent again by
// itself on this route; the radio's report stays in the detail. A
// MeshSat Bridge's acknowledgement of the SDS that came while the radio
// still had it outweighs either (review 2026-10-18 14:53, TETRA-ACK1): acked
// by a station, the radio's report kept in the detail. One that comes
// after Send returned is not collected; the outcome stays unknown.

// TetraIntentCorrKind is the correlation kind of a TETRA submission. Only
// the answer inside the call carries it (a peer's acknowledgement).
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

// SetIntentReceiptSink implements IntentAdapter: a peer's acknowledgement
// comes inside the call or not at all (Send stops waiting for it).
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
	res, err := g.Send(ctx, &transport.MeshMessage{PortNum: 1, DecodedText: s.Text, Destination: s.Destination,
		AsWritten: true, Encrypted: s.Encrypted, RawText: s.Encrypted, MsgRef: s.MsgRef})
	switch {
	case err == nil && res.Confirmed:
		// A MeshSat Bridge on the group acknowledged the SDS (its reference
		// and the hash of its bytes). Unauthenticated: any radio on the
		// group can send that link SDS. [MESHSAT-1584]
		r := &IntentReceipt{CorrKind: TetraIntentCorrKind, CorrValue: s.CorrValue, Peer: s.Destination,
			Source: "station", Event: "ack", Completes: "acked_station", Trust: "unauthenticated"}
		detail := "a MeshSat Bridge on the TETRA group acknowledged the SDS"
		if res.RadioReport != "" {
			// The acknowledgement came first; the radio's report stays on
			// record (TETRA-ACK1).
			detail += "; the radio had reported: " + res.RadioReport
		}
		return IntentOutcome{Class: IntentAccepted, AcceptedBy: "radio", Receipt: r, Detail: detail}
	case err == nil:
		// The radio believes it sent it (+CMGS status 4, or no report) and
		// no MeshSat Bridge is known on the group to acknowledge it: not
		// that it arrived. In the TETRA session's collision test both radios
		// once reported 4 and neither SDS arrived (half duplex). [MESHSAT-1584]
		return IntentOutcome{Class: IntentAccepted, AcceptedBy: "radio",
			Detail: "the TETRA radio reports it sent; no MeshSat Bridge is known on the group to acknowledge it, so arrival is not known"}
	case errors.Is(err, transport.ErrNoAck):
		// Sent, and the MeshSat Bridge known on the group did not
		// acknowledge it: lost in a collision both radios missed, or
		// received and the acknowledgement lost. Not known either way.
		return IntentOutcome{Class: IntentUnknown, Detail: "may have been sent: " + err.Error()}
	case errors.Is(err, tetra.ErrSDSInFlight), errors.Is(err, tetra.ErrRadioNotSent):
		// Written whole before the port closed, or taken and then reported
		// not sent: either may have gone on air.
		return IntentOutcome{Class: IntentUnknown, Detail: "may have been sent: " + err.Error()}
	case errors.Is(err, transport.ErrNotConnected):
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: err.Error()}
	case errors.Is(err, transport.ErrPayloadTooLong):
		return IntentOutcome{Class: IntentNotSent, Reason: "skipped_oversize", Detail: err.Error()}
	case errors.Is(err, transport.ErrNoRecipient):
		return IntentOutcome{Class: IntentNotSent, Reason: "skipped_unsupported", Detail: err.Error()}
	case errors.Is(err, transport.ErrNotSubmitted):
		return IntentOutcome{Class: IntentNotSent, Retryable: true, ConsumeRetry: true, Reason: "not_sent", Detail: err.Error(), BearerRefused: true}
	}
	// A timeout or a reply that did not come: the radio may have sent it.
	return IntentOutcome{Class: IntentUnknown, Detail: "may have been sent: " + err.Error()}
}
