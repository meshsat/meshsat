package gateway

import (
	"context"
	"strings"
	"testing"

	"meshsat/internal/transport"
)

// TestZigBeeGateway_RefusesAPortThatBelongsToAnotherDevice: the ZigBee
// gateway is the only one that resolves its own port, so it is the only one
// that can be pointed at another device. An explicitly configured path gets
// the same refusal as an auto-detected one, because opening a CP210x
// asserts DTR/RTS and the PicoAPRS V4 shares the dongle's VID:PID.
// [MESHSAT-1265]
func TestZigBeeGateway_RefusesAPortThatBelongsToAnotherDevice(t *testing.T) {
	const tnc = "/dev/ttyUSB-picoaprs"
	transport.ReservePort(tnc, transport.PortOwnerExcluded)
	defer transport.UnreservePort(tnc)

	gw := NewZigBeeGateway(ZigBeeConfig{SerialPort: tnc})
	err := gw.Start(context.Background())
	if err == nil {
		_ = gw.Stop()
		t.Fatal("the gateway opened a port that belongs to another device")
	}
	if !strings.Contains(err.Error(), "belongs to") {
		t.Fatalf("error = %v, want a refusal naming the owner", err)
	}
}
