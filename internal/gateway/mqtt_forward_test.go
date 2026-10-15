package gateway

import (
	"context"
	"errors"
	"testing"

	"meshsat/internal/transport"
)

// With no broker connection Forward fails, so the delivery ledger keeps the
// message for a retry instead of marking it delivered. [MESHSAT-1581]
func TestMQTTForward_DisconnectedIsAnError(t *testing.T) {
	g := NewMQTTGateway(DefaultMQTTConfig())
	err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: "hello"})
	if !errors.Is(err, transport.ErrNotConnected) {
		t.Fatalf("Forward while disconnected: %v; want ErrNotConnected", err)
	}
	if n := g.msgsOut.Load(); n != 0 {
		t.Fatalf("msgsOut %d after a failed forward", n)
	}
}
