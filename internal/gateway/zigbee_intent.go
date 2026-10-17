package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"meshsat/internal/transport"
)

// The ZigBee coordinator as a delivery-intent route (owner ruling 17 Oct
// 2026: in code now, live once a dongle is back in a kit). [MESHSAT-1584]
//
// One AF_DATA_REQUEST per submission to one device, the wire as written,
// never cut (the legacy send worker cuts at 100 bytes). The address is the
// device's IEEE address, which stays with the device and is looked up in
// the coordinator's device list for its current network address, or a
// network address itself (0x1A2B). Broadcast addresses are no recipient.
// The send returns once the frame is written to the coordinator; no APS
// acknowledgement is asked for and AF_DATA_CONFIRM is not read (r3 section
// 4), so it is taken by the coordinator, accepted, not confirmed. Free.

// ZigBeeIntentCorrKind is the correlation kind of a ZigBee submission.
const ZigBeeIntentCorrKind = "zigbee_ref"

// zigbeeIntentMax is the payload a ZigBee frame carries after the APS and
// NWK headers (the registry's MaxPayload).
const zigbeeIntentMax = 100

// zigbeeSender is what an intent needs of the coordinator.
type zigbeeSender interface {
	Send(dstAddr uint16, dstEP byte, clusterID uint16, data []byte) error
	GetDevices() []transport.ZigBeeDevice
}

func (g *ZigBeeGateway) sender() zigbeeSender {
	if g.intentTx != nil {
		return g.intentTx
	}
	if g.transport == nil {
		return nil
	}
	return g.transport
}

// parseZigBeeAddress reads an IEEE address (16 hex digits, colons allowed)
// or a network address (0x and up to 4 hex digits).
func parseZigBeeAddress(s string) (ieee string, short uint16, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "0x") && len(s) > 2 && len(s) <= 6 {
		n, err := strconv.ParseUint(s[2:], 16, 16)
		if err != nil || n >= 0xFFF8 {
			return "", 0, false // 0xFFF8-0xFFFF are broadcast and reserved
		}
		return "", uint16(n), true
	}
	h := strings.ReplaceAll(s, ":", "")
	if len(h) == 16 {
		if _, err := hex.DecodeString(h); err == nil {
			return h, 0, true
		}
	}
	return "", 0, false
}

// IntentCorrKind implements IntentAdapter.
func (g *ZigBeeGateway) IntentCorrKind() string { return ZigBeeIntentCorrKind }

// IntentNewCorr implements IntentAdapter.
func (g *ZigBeeGateway) IntentNewCorr() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetIntentReceiptSink implements IntentAdapter: no answer is read.
func (g *ZigBeeGateway) SetIntentReceiptSink(func(IntentReceipt)) {}

// IntentAdmit implements IntentAdapter.
func (g *ZigBeeGateway) IntentAdmit(dest string, wire []byte, _ bool) (reason, detail string) {
	if _, _, ok := parseZigBeeAddress(dest); !ok {
		return "skipped_unsupported", fmt.Sprintf("%q is not a ZigBee device address (an IEEE address, or 0x and a network address)", strings.TrimSpace(dest))
	}
	if len(wire) == 0 {
		return "skipped_encoding", "nothing to send"
	}
	if len(wire) > zigbeeIntentMax {
		return "skipped_oversize", fmt.Sprintf("%d bytes, a ZigBee frame carries %d", len(wire), zigbeeIntentMax)
	}
	return "", ""
}

// IntentSend implements IntentAdapter: one frame, and how far it got.
func (g *ZigBeeGateway) IntentSend(ctx context.Context, s IntentSend) IntentOutcome {
	if reason, detail := g.IntentAdmit(s.Destination, []byte(s.Text), s.Encrypted); reason != "" {
		return IntentOutcome{Class: IntentNotSent, Reason: reason, Detail: detail}
	}
	tx := g.sender()
	if tx == nil {
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: "no ZigBee coordinator"}
	}
	ieee, short, _ := parseZigBeeAddress(s.Destination)
	ep := g.config.DefaultDstEP
	if ieee != "" {
		found := false
		for _, d := range tx.GetDevices() {
			if strings.EqualFold(strings.ReplaceAll(d.IEEEAddr, ":", ""), ieee) {
				short, found = d.ShortAddr, true
				if d.Endpoint != 0 {
					ep = d.Endpoint
				}
				break
			}
		}
		if !found {
			// Not on the network now; a device that rejoins is seen again.
			return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline",
				Detail: "the device " + ieee + " is not on the coordinator's network"}
		}
	}
	err := tx.Send(short, ep, g.config.DefaultCluster, []byte(s.Text))
	switch {
	case err == nil:
		g.msgsOut.Add(1)
		return IntentOutcome{Class: IntentAccepted, AcceptedBy: "coordinator",
			Detail: fmt.Sprintf("written to the coordinator for 0x%04x; no APS acknowledgement asked for", short)}
	case errors.Is(err, transport.ErrNotConnected):
		return IntentOutcome{Class: IntentNotSent, Retryable: true, Reason: "skipped_offline", Detail: err.Error()}
	}
	// The serial write failed part way: the frame may have gone.
	g.errors.Add(1)
	return IntentOutcome{Class: IntentUnknown, Detail: "may have been sent: " + err.Error()}
}
