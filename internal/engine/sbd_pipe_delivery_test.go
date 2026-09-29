package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// countingGateway fails every Forward with err and counts the attempts.
type countingGateway struct {
	mu       sync.Mutex
	err      error
	attempts int
}

func (g *countingGateway) Start(context.Context) error { return nil }
func (g *countingGateway) Stop() error                 { return nil }
func (g *countingGateway) Forward(context.Context, *transport.MeshMessage) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attempts++
	return g.err
}
func (g *countingGateway) Receive() <-chan gateway.InboundMessage { return nil }
func (g *countingGateway) Status() gateway.GatewayStatus {
	return gateway.GatewayStatus{Connected: true}
}
func (g *countingGateway) Enqueue(*transport.MeshMessage) error { return nil }
func (g *countingGateway) Type() string                         { return "iridium" }

func (g *countingGateway) tries() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.attempts
}

// A satellite session whose outcome is unknown (the link to the node dropped
// under it and the node's account could not settle it) may have delivered
// the message. Whatever its QoS, the delivery is never retried or failed
// over by itself: it ends dead, the reason saying it may have been sent, and
// the queue's Retry, a person's, sends it again. [MESHSAT-1391]
func TestHandleFailure_AnUnknownSatelliteOutcomeIsNeverRetried(t *testing.T) {
	for _, qos := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("QoS %d", qos), func(t *testing.T) {
			h := setupE2E(t)
			h.addInterface(t, "iridium_0", "iridium", true)
			gw := &countingGateway{err: fmt.Errorf("sbd: send failed: SBDIX failed: %w (the node restarted)", transport.ErrOutcomeUnknown)}

			id, err := h.db.InsertDelivery(database.MessageDelivery{
				MsgRef: "sat-unknown", Channel: "iridium_0", Status: "queued", Priority: 1,
				TextPreview: "hello", MaxRetries: 5, Visited: "[]", QoSLevel: qos,
			})
			if err != nil {
				t.Fatal(err)
			}
			w := newTestWorker(h, "iridium_0", "iridium")
			w.gwProv = oneGateway{iface: "iridium_0", gw: gw}
			var events []transport.MeshEvent
			w.emit = func(e transport.MeshEvent) { events = append(events, e) }
			w.processBatch(context.Background())

			got, err := h.db.GetDelivery(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "dead" || got.Retries != 0 || got.NextRetry != nil || !strings.HasPrefix(got.LastError, UnconfirmedPrefix) ||
				!strings.Contains(got.LastError, "never reached this Bridge") {
				t.Fatalf("delivery %+v; want dead, no retry, a reason that says it may have been sent", got)
			}
			if len(events) != 1 || events[0].Type != "delivery_dead" || !strings.Contains(string(events[0].Data), `"unconfirmed":true`) {
				t.Fatalf("events %+v", events)
			}
			w.processBatch(context.Background())
			if n := gw.tries(); n != 1 {
				t.Fatalf("%d attempts; the queue sent it again by itself", n)
			}

			// The queue's Retry, a person's action, sends it again.
			if err := h.db.RetryDelivery(id); err != nil {
				t.Fatalf("a person's retry: %v", err)
			}
			w.processBatch(context.Background())
			if n := gw.tries(); n != 2 {
				t.Fatalf("%d attempts after a person's retry", n)
			}
		})
	}
}

// The SOS's frame to the Hub (class hub_uplink, priority 0) whose satellite
// session has an unknown outcome is not given up: a person's Retry never
// sends it again (database.ErrSOSFrameNotRetried), so given up it was lost
// for good. It keeps the queue's own retries, as any other failure of it
// does: a duplicate SOS frame within minutes is the lesser harm.
// [MESHSAT-1391]
func TestHandleFailure_AnUnknownOutcomeKeepsTheSOSFrameRetried(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unknown outcome", fmt.Errorf("sbd: send failed: SBDIX failed: %w (the node restarted)", transport.ErrOutcomeUnknown)},
		{"ordinary failure", errors.New("sbd: session failed (mo_status=32)")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setupE2E(t)
			h.addInterface(t, "iridium_0", "iridium", true)
			gw := &countingGateway{err: tc.err}
			id, err := h.db.InsertDelivery(database.MessageDelivery{
				MsgRef: "sos-frame", Channel: "iridium_0", Status: "queued", Priority: 0,
				TextPreview: "hub uplink frame, 40 B", Payload: []byte{0x53, 0x4f, 0x53}, MaxRetries: 3, Visited: "[]",
				QoSLevel: 1, Class: database.DeliveryClassHubUplink,
			})
			if err != nil {
				t.Fatal(err)
			}
			w := newTestWorker(h, "iridium_0", "iridium")
			w.gwProv = oneGateway{iface: "iridium_0", gw: gw}
			w.processBatch(context.Background())
			got, err := h.db.GetDelivery(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "retry" || got.Retries != 1 || got.NextRetry == nil || strings.HasPrefix(got.LastError, UnconfirmedPrefix) {
				t.Fatalf("the SOS frame: status %q, retries %d, next retry %v, %q; want a retry", got.Status, got.Retries, got.NextRetry, got.LastError)
			}
			// The queue sends it again by itself once its retry is due.
			if _, err := h.db.Exec(`UPDATE message_deliveries SET next_retry = datetime('now', '-1 minute') WHERE id = ?`, id); err != nil {
				t.Fatal(err)
			}
			w.processBatch(context.Background())
			if n := gw.tries(); n != 2 {
				t.Fatalf("%d attempts; want the queue's own retry", n)
			}
		})
	}
}

// pipeSat is the SBD transport as the SBD gateway sees it: every session
// sends, MO status 0.
type pipeSat struct {
	mu    sync.Mutex
	texts []string
}

func (s *pipeSat) Subscribe(context.Context) (<-chan transport.SatEvent, error) {
	return nil, errors.New("no events in this test")
}
func (s *pipeSat) Send(_ context.Context, data []byte) (*transport.SatResult, error) {
	return s.SendText(context.Background(), string(data))
}
func (s *pipeSat) SendText(_ context.Context, text string) (*transport.SatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, text)
	return &transport.SatResult{MOStatus: 0, MOMSN: len(s.texts), MTStatus: 0}, nil
}
func (s *pipeSat) Receive(context.Context) ([]byte, error) { return nil, nil }
func (s *pipeSat) MailboxCheck(context.Context) (*transport.SatResult, error) {
	return &transport.SatResult{NoSession: true}, nil
}
func (s *pipeSat) GetSignal(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (s *pipeSat) GetSignalFast(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (s *pipeSat) GetStatus(context.Context) (*transport.SatStatus, error) {
	return &transport.SatStatus{Connected: true, Type: "sbd", Port: "ble"}, nil
}
func (s *pipeSat) GetFirmwareVersion(context.Context) (string, error) { return "test", nil }
func (s *pipeSat) Close() error                                       { return nil }

func (s *pipeSat) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

// With MESHSAT_IRIDIUM_PORT=ble the SBD gateway comes and goes with the
// Bluetooth node. While it is away, the deliveries of its link are held, not
// tried: a QoS 0 message met "gateway iridium_0 not found or not running" and
// died at once, and a QoS 1+ message spent its retries, in an ordinary
// Bluetooth outage. Once the node is back both go out, their retries
// unchanged. The manager and the dispatcher are wired as main.go wires them.
// [MESHSAT-1391]
func TestSBDPipe_ADetachHoldsTheLinksDeliveriesAndAnAttachSendsThem(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	sat := &pipeSat{}
	mgr := gateway.NewManager(db, sat)
	mgr.SetSBDPipe(func() bool { return true })
	disp := NewDispatcher(db, reg, mgr, &mockMeshTransport{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		disp.Wait()
		mgr.Stop()
		db.Close()
	})
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AttachSBDPipe(ctx); err != nil { // the node's link comes up: iridium_0 and its link
		t.Fatal(err)
	}
	disp.Start(ctx)
	mgr.SetSBDPipeWorkers(disp.StopWorker, disp.ResumeWorker)
	if !disp.HasWorker("iridium_0") {
		t.Fatal("no delivery worker for iridium_0")
	}

	// A Bluetooth outage: the node's modem goes, and messages queue for it.
	mgr.DetachSBDPipe()
	var ids []int64
	for _, qos := range []int{0, 1} {
		id, err := db.InsertDelivery(database.MessageDelivery{
			MsgRef: fmt.Sprintf("outage-%d", qos), Channel: "iridium_0", Status: "queued", Priority: 1,
			TextPreview: fmt.Sprintf("qos %d", qos), MaxRetries: 3, Visited: "[]", QoSLevel: qos,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	time.Sleep(2500 * time.Millisecond) // more than a worker's round
	for _, id := range ids {
		d, err := db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status == "dead" || d.Status == "sent" || d.Status == "delivered" || d.Retries != 0 {
			t.Fatalf("delivery %d during the outage: status %s, retries %d, %q", id, d.Status, d.Retries, d.LastError)
		}
	}
	if got := sat.sent(); len(got) != 0 {
		t.Fatalf("sent during the outage: %v", got)
	}

	// The node is back.
	if err := mgr.AttachSBDPipe(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		done := 0
		for _, id := range ids {
			if d, err := db.GetDelivery(id); err == nil && (d.Status == "sent" || d.Status == "delivered") {
				done++
			}
		}
		if done == len(ids) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not sent once the node was back")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, id := range ids {
		d, _ := db.GetDelivery(id)
		if d.Retries != 0 {
			t.Fatalf("delivery %d spent %d retries on the outage", id, d.Retries)
		}
	}
	if got := sat.sent(); len(got) != 2 {
		t.Fatalf("sent %v, want each message once", got)
	}
}

// Deliveries a detach held when the Bridge stopped stay 'held' in the
// database. At the next start the dispatcher starts the link's worker
// (releasing nothing held), and when the node's pipe is up by the time the
// availability callback is wired (its first report "up"), AttachSBDPipe's
// resume is a StartWorker on a worker that exists: it releases what is held
// all the same, and it goes out, instead of waiting for the next Bluetooth
// drop. [MESHSAT-1391]
func TestSBDPipe_DeliveriesHeldAtTheLastStopGoOutWhenTheFirstReportIsUp(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	sat := &pipeSat{}
	mgr := gateway.NewManager(db, sat)
	mgr.SetSBDPipe(func() bool { return true })
	disp := NewDispatcher(db, reg, mgr, &mockMeshTransport{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		disp.Wait()
		mgr.Stop()
		db.Close()
	})
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// The start's reconcile finds the pipe there: iridium_0, its config and
	// link (the dispatcher is not started yet, so nothing to resume).
	if err := mgr.AttachSBDPipe(ctx); err != nil {
		t.Fatal(err)
	}
	// What the previous run's detach left behind.
	id, err := db.InsertDelivery(database.MessageDelivery{
		MsgRef: "held-from-last-run", Channel: "iridium_0", Status: "held", Priority: 1,
		TextPreview: "waiting for the node", MaxRetries: 3, Visited: "[]", QoSLevel: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	disp.Start(ctx) // a worker for iridium_0, nothing unheld
	mgr.SetSBDPipeWorkers(disp.StopWorker, disp.ResumeWorker)
	if err := mgr.AttachSBDPipe(ctx); err != nil { // the availability callback's first report: up
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		d, err := db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status == "sent" || d.Status == "delivered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the held delivery never went out: status %q (worker running %v)", d.Status, disp.HasWorker("iridium_0"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := sat.sent(); len(got) != 1 || got[0] != "waiting for the node" {
		t.Fatalf("sent %v", got)
	}
}

// A satellite delivery a stop left 'sending' (the process stopped while its
// session ran, or while its outcome was being settled) may have gone out:
// the next start gives it up, "may have been sent", never sends it again by
// itself. Other links' rows are recovered for a retry, as before.
// [MESHSAT-1391]
func TestDispatcherStart_EndsTheSatelliteSendsTheLastStopLeftUnderWay(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	sat, err := h.db.InsertDelivery(database.MessageDelivery{MsgRef: "sat", Channel: "iridium_0", Status: "sending",
		Priority: 1, TextPreview: "under way", MaxRetries: 3, Visited: "[]", QoSLevel: 1})
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.db.InsertDelivery(database.MessageDelivery{MsgRef: "mqtt", Channel: "mqtt_0", Status: "sending",
		Priority: 1, TextPreview: "under way", MaxRetries: 3, Visited: "[]", QoSLevel: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); h.dispatch.Wait() })
	h.dispatch.Start(ctx)
	d, _ := h.db.GetDelivery(sat)
	if d.Status != "dead" || !strings.HasPrefix(d.LastError, UnconfirmedPrefix) {
		t.Fatalf("the satellite send: status %q, %q; want dead, may have been sent", d.Status, d.LastError)
	}
	if o, _ := h.db.GetDelivery(other); o.Status != "retry" && o.Status != "sending" && o.Status != "sent" && o.Status != "delivered" {
		t.Fatalf("the other link's row: %q", o.Status)
	}
}
