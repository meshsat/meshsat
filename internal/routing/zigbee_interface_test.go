package routing

import (
	"context"
	"testing"

	"meshsat/internal/transport"
)

// The interface resolves the gateway's current transport per call, so a
// coordinator restart (new transport object) is picked up and a missing
// transport reads as offline instead of a stale pointer. [MESHSAT-815]
func TestZigBeeInterface_FollowsTransportProvider(t *testing.T) {
	zi := NewZigBeeInterface(ZigBeeInterfaceConfig{Name: "zigbee_0"}, nil, func([]byte) {})
	if err := zi.Send(context.Background(), []byte{1, 2}); err == nil {
		t.Fatal("send with no transport succeeded")
	}
	if zi.transport() != nil {
		t.Fatal("nil transport expected")
	}

	first := transport.NewDirectZigBeeTransport()
	second := transport.NewDirectZigBeeTransport()
	current := first
	zi.SetTransportProvider(func() *transport.DirectZigBeeTransport { return current })
	if zi.transport() != first {
		t.Fatal("provider not used")
	}
	current = second
	if zi.transport() != second {
		t.Fatal("provider swap not visible")
	}
	// Neither transport runs, so the interface stays offline and Send
	// reports it rather than dereferencing a dead coordinator.
	if err := zi.Send(context.Background(), []byte{1, 2}); err == nil {
		t.Fatal("send on a stopped transport succeeded")
	}
	if zi.IsOnline() {
		t.Fatal("online with a stopped transport")
	}
}
