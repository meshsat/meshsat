package routing

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestAX25Interface_GatewayMode covers the serial-TNC wiring: frames arrive
// through the APRS gateway's fan-out, a closed feed triggers a
// re-subscription, and TX goes through the shared sender. [MESHSAT-821]
func TestAX25Interface_GatewayMode(t *testing.T) {
	var received [][]byte
	got := make(chan []byte, 4)
	iface := NewAX25Interface(AX25InterfaceConfig{
		Name:     "ax25_0",
		KISSAddr: KISSAddrGateway,
		Callsign: "TEST-1",
	}, func(p []byte) {
		received = append(received, p)
		got <- p
	})

	feed := make(chan []byte, 4)
	subscriptions := 0
	iface.SetKISSRXProvider(func() (<-chan []byte, func()) {
		subscriptions++
		return feed, func() {}
	})
	var sent [][]byte
	iface.SetKISSTXProvider(func() KISSTXFunc {
		return func(frame []byte) error { sent = append(sent, frame); return nil }
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := iface.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !iface.IsOnline() {
		t.Fatal("should be online after subscribing")
	}

	// An AX.25 UI frame: 14 address bytes, control, PID, then the info.
	frame := append(make([]byte, 16), 0xAA, 0xBB, 0xCC)
	feed <- frame
	select {
	case p := <-got:
		if !bytes.Equal(p, []byte{0xAA, 0xBB, 0xCC}) {
			t.Fatalf("callback got %x", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no packet delivered through the gateway feed")
	}

	if err := iface.Send(ctx, []byte{1, 2, 3}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("shared TX not used: %d frames", len(sent))
	}

	// Gateway restart: the feed closes, the interface re-subscribes.
	close(feed)
	feed = make(chan []byte, 4)
	deadline := time.Now().Add(5 * time.Second)
	for subscriptions < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if subscriptions < 2 {
		t.Fatalf("expected a re-subscription, got %d", subscriptions)
	}
	feed <- frame
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("no packet after re-subscription")
	}
	iface.Stop()
	if iface.IsOnline() {
		t.Fatal("offline after stop")
	}
}

func TestAX25Interface_GatewayModeWithoutProvider(t *testing.T) {
	iface := NewAX25Interface(AX25InterfaceConfig{Name: "ax25_0", KISSAddr: KISSAddrGateway, Callsign: "T"}, func([]byte) {})
	if err := iface.Start(context.Background()); err == nil {
		t.Fatal("start must fail without a RX provider")
	}
}
