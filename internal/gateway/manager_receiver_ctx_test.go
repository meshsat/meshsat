package gateway

import (
	"context"
	"testing"
	"time"

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
