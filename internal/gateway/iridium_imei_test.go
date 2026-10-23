package gateway

import (
	"context"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// lateIMEISat answers the status read at the gateway's start without an
// IMEI and learns it during Subscribe, as the direct 9603 transport does when
// the modem's AT+CGSN comes after the gateway started (parallax, 23 Oct 2026).
type lateIMEISat struct {
	fakeSat
	events chan transport.SatEvent
}

func (f *lateIMEISat) Subscribe(ctx context.Context) (<-chan transport.SatEvent, error) {
	f.imei = "300234067504200"
	return f.events, nil
}

// The birth names the modem by the gateway's IMEI, so the post-subscribe
// connected check must take the IMEI the start read missed. [MESHSAT-1659]
func TestSBDGateway_IMEITakenAfterSubscribe(t *testing.T) {
	sat := &lateIMEISat{events: make(chan transport.SatEvent)}
	gw := NewSBDGateway(IridiumConfig{}, sat, nil, nil)

	// What Start does: one status read, no IMEI yet.
	st, err := sat.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gw.rememberIMEI(st)
	if gw.IMEI() != "" {
		t.Fatalf("IMEI before the modem answered: %q", gw.IMEI())
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if err := gw.ringAlertListenOnce(ctx); err != nil {
		t.Fatalf("ringAlertListenOnce: %v", err)
	}
	if gw.IMEI() != "300234067504200" {
		t.Fatalf("IMEI after subscribe: %q", gw.IMEI())
	}
}
