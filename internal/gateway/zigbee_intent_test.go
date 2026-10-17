package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"meshsat/internal/transport"
)

// The ZigBee coordinator as a delivery-intent route (code only until a
// dongle is back): one device by IEEE or network address, one frame whole,
// the IEEE address looked up for its current network address, and the
// coordinator's answers read as taken, offline or unknown. [MESHSAT-1584]

type fakeZB struct {
	devices []transport.ZigBeeDevice
	err     error
	sent    []string
}

func (f *fakeZB) Send(dst uint16, ep byte, cluster uint16, data []byte) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, fmt.Sprintf("%04x/%d/%04x:%s", dst, ep, cluster, data))
	return nil
}
func (f *fakeZB) GetDevices() []transport.ZigBeeDevice { return f.devices }

func TestZigBeeIntent_AdmitsOneDeviceAndOneFrameWhole(t *testing.T) {
	g := NewZigBeeGateway(DefaultZigBeeConfig())
	for _, tc := range []struct {
		dest, text, reason string
	}{
		{"00124b0029a1b2c3", "x", ""},
		{"00:12:4b:00:29:a1:b2:c3", "x", ""},
		{"0x1a2b", "x", ""},
		{"0xffff", "x", "skipped_unsupported"}, // broadcast is no recipient
		{"!4370c1d8", "x", "skipped_unsupported"},
		{"0x1a2b", strings.Repeat("z", 100), ""},
		{"0x1a2b", strings.Repeat("z", 101), "skipped_oversize"}, // the legacy worker cuts at 100; an intent never
		{"0x1a2b", "", "skipped_encoding"},
	} {
		if r, d := g.IntentAdmit(tc.dest, []byte(tc.text), false); r != tc.reason {
			t.Fatalf("%q, %d bytes: %q (%s); want %q", tc.dest, len(tc.text), r, d, tc.reason)
		}
	}
}

func TestZigBeeIntent_EachAnswer(t *testing.T) {
	cfg := DefaultZigBeeConfig()
	f := &fakeZB{devices: []transport.ZigBeeDevice{{ShortAddr: 0x5e7a, IEEEAddr: "00124b0029a1b2c3", Endpoint: 8}}}
	g := NewZigBeeGateway(cfg)
	g.intentTx = f
	o := g.IntentSend(context.Background(), IntentSend{Destination: "00:12:4B:00:29:A1:B2:C3", Text: "to the device", CorrValue: "c1"})
	if o.Class != IntentAccepted || o.AcceptedBy != "coordinator" || len(f.sent) != 1 ||
		f.sent[0] != fmt.Sprintf("5e7a/8/%04x:to the device", cfg.DefaultCluster) {
		t.Fatalf("by IEEE: %+v %q; want the current network address and the device's endpoint", o, f.sent)
	}
	if o := g.IntentSend(context.Background(), IntentSend{Destination: "00124b00ffffffff", Text: "x", CorrValue: "c2"}); o.Class != IntentNotSent || o.Reason != "skipped_offline" || !o.Retryable {
		t.Fatalf("a device not on the network: %+v", o)
	}
	f.err = fmt.Errorf("zigbee coordinator not ready (state=HOLD): %w", transport.ErrNotConnected)
	if o := g.IntentSend(context.Background(), IntentSend{Destination: "0x5e7a", Text: "x", CorrValue: "c3"}); o.Class != IntentNotSent || o.Reason != "skipped_offline" || !o.Retryable {
		t.Fatalf("coordinator not ready: %+v", o)
	}
	f.err = errors.New("write /dev/ttyUSB0: input/output error")
	if o := g.IntentSend(context.Background(), IntentSend{Destination: "0x5e7a", Text: "x", CorrValue: "c4"}); o.Class != IntentUnknown {
		t.Fatalf("a failed write: %+v; the frame may have gone", o)
	}
	none := NewZigBeeGateway(cfg)
	if o := none.IntentSend(context.Background(), IntentSend{Destination: "0x5e7a", Text: "x", CorrValue: "c5"}); o.Class != IntentNotSent || o.Reason != "skipped_offline" {
		t.Fatalf("no coordinator: %+v", o)
	}
}
