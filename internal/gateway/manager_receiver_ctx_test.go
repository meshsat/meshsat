package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/database"

	"meshsat/internal/transport"
)

// ctxFakeGateway is the smallest Gateway that can hand a message to a receiver.
type ctxFakeGateway struct{ in chan InboundMessage }

func (f *ctxFakeGateway) Start(context.Context) error                           { return nil }
func (f *ctxFakeGateway) Stop() error                                           { return nil }
func (f *ctxFakeGateway) Forward(context.Context, *transport.MeshMessage) error { return nil }
func (f *ctxFakeGateway) Enqueue(*transport.MeshMessage) error                  { return nil }
func (f *ctxFakeGateway) Receive() <-chan InboundMessage                        { return f.in }
func (f *ctxFakeGateway) Status() GatewayStatus                                 { return GatewayStatus{} }
func (f *ctxFakeGateway) Type() string                                          { return "fake" }

// A receiver started for a gateway must outlive the context of whoever
// (re)started that gateway: the APRS receive watchdog restarts aprs_0 under
// a 90 s step context, and a receiver bound to it died with the step while
// the gateway kept decoding into a channel nobody read. [MESHSAT-858]
func TestReceiverContext_OutlivesCaller(t *testing.T) {
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	m := &Manager{running: map[string]Gateway{}, runningByIface: map[string]Gateway{}}

	// Before Start, receivers still get a live context.
	if err := m.receiverContext().Err(); err != nil {
		t.Fatalf("receiver context before Start must be live, got %v", err)
	}
	m.receiverCtx = appCtx // what Start(ctx) records

	got := make(chan InboundMessage, 1)
	m.SetReceiverStartFunc(func(ctx context.Context, gw Gateway) {
		go func() {
			ch := gw.Receive()
			for {
				select {
				case <-ctx.Done():
					return
				case msg := <-ch:
					got <- msg
				}
			}
		}()
	})

	// The caller's context is the short-lived one a watchdog step hands in.
	callerCtx, callerCancel := context.WithCancel(context.Background())
	gw := &ctxFakeGateway{in: make(chan InboundMessage, 1)}
	m.onReceiverStart(m.receiverContext(), gw) // the call the start paths make
	_ = callerCtx
	callerCancel()
	time.Sleep(20 * time.Millisecond)

	gw.in <- InboundMessage{Text: "after the step ended", Source: "aprs"}
	select {
	case msg := <-got:
		if msg.Text != "after the step ended" {
			t.Fatalf("unexpected message %q", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("receiver died with the caller's context; inbound message lost")
	}

	// The manager's own context still ends the receiver.
	appCancel()
	time.Sleep(20 * time.Millisecond)
	gw.in <- InboundMessage{Text: "after shutdown", Source: "aprs"}
	select {
	case msg := <-got:
		t.Fatalf("receiver outlived the manager: got %q", msg.Text)
	case <-time.After(150 * time.Millisecond):
	}
}

// A gateway's OWN lifetime must also outlive the caller's context, not just
// its receiver's. ConfigureInstance is reached from an HTTP handler
// (PUT /api/gateways/{type}, POST /api/ttc/flow/setup), so a gateway started
// on the request context lost everything bound to it the moment net/http
// cancelled that context on return. On tesseract, 16 Sep 2026: two inbound
// SMS reached the transport and the SMS history and neither reached the rules
// engine, because CellularGateway.Start derives a child ctx and hands it to
// cell.Subscribe — the transport dropped the gateway's event subscription
// while the gateway still reported connected and could still SEND. Only a
// bridge restart brought it back. This drives the real path. [MESHSAT-1179]
func TestGatewayStartContext_OutlivesCaller(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer db.Close()

	cell := newFakeCellTransport()
	m := NewManager(db, nil)
	m.SetCellTransport(cell)

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	if err := m.Start(appCtx); err != nil {
		t.Fatalf("manager start: %v", err)
	}

	// Drain whatever the gateway hands its receiver, the way the processor does.
	got := make(chan InboundMessage, 4)
	m.SetReceiverStartFunc(func(ctx context.Context, gw Gateway) {
		go func() {
			ch := gw.Receive()
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-ch:
					if !ok {
						return
					}
					got <- msg
				}
			}
		}()
	})

	// The context an HTTP handler hands in. net/http cancels it on return.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	cfg := `{"destination_numbers":["+31653618463"],"allowed_senders":["+31653207829"],"max_sms_segments":1}`
	if err := m.ConfigureInstance(reqCtx, "cellular", "cellular_0", true, cfg); err != nil {
		t.Fatalf("configure: %v", err)
	}
	reqCancel()
	time.Sleep(100 * time.Millisecond)

	// An SMS arrives after the HTTP response was written.
	data, _ := json.Marshal(transport.SMSMessage{Sender: "+31653207829", Text: "after the response"})
	cell.events <- transport.CellEvent{Type: "sms_received", Message: "after the response", Data: data}

	select {
	case msg := <-got:
		if msg.Text != "after the response" {
			t.Fatalf("unexpected inbound %q", msg.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inbound SMS lost: the gateway died with the HTTP request, which on a kit is the relay going silent until the bridge restarts")
	}

	// The manager's own shutdown must still bring the gateway down.
	appCancel()
	m.Stop()
}
