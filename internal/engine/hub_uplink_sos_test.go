package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

const sqliteTimeLayout = "2006-01-02 15:04:05"

// alarmTestOpts is how POST /api/sos/test queues the alarm test's satellite
// leg (internal/api sosTestSatellite). [MESHSAT-1430]
func alarmTestOpts(frame []byte) DirectSendOptions {
	return DirectSendOptions{Precedence: "Routine", Class: database.DeliveryClassHubUplink, Payload: frame, TTLSeconds: 1800}
}

// sosFrameOpts is how the satellite fallback's send queues the SOS's Hub
// frame (cmd/meshsat hubUplinkFrameOptions). [MESHSAT-1430]
func sosFrameOpts(frame []byte) DirectSendOptions {
	return DirectSendOptions{Precedence: "Priority", Class: database.DeliveryClassHubUplink, Payload: frame, Critical: true}
}

// positionFrameOpts is how it queues the periodic position and health frames.
func positionFrameOpts(frame []byte) DirectSendOptions {
	return DirectSendOptions{Precedence: "Priority", Class: database.DeliveryClassHubUplink, Payload: frame}
}

func testFrames() (position, sos []byte) {
	now := time.Unix(1790000000, 0)
	return hubreporter.EncodeSatPosition("nllei01tesseract01", 52.1601, 4.4970, 3.9, 1, now),
		hubreporter.EncodeSatSOS("nllei01tesseract01", "bridge", 52.1601, 4.4970, "SOS: Anna needs help.", now)
}

// TTLSeconds gives a direct send a deadline in expires_at, TTLSeconds on;
// a critical send has none, as a priority 0 rule delivery has none.
// [MESHSAT-1430]
func TestQueueDirectSendTo_TTLSetsADeadline(t *testing.T) {
	d, db := setupTestDispatcher(t)
	for _, tc := range []struct {
		name string
		opts DirectSendOptions
		ttl  int
	}{
		{"no TTL", DirectSendOptions{}, 0},
		{"30 minutes", DirectSendOptions{TTLSeconds: 1800}, 1800},
		{"critical: never expires", DirectSendOptions{TTLSeconds: 1800, Critical: true}, 0},
	} {
		before := time.Now().UTC().Truncate(time.Second)
		id, _, err := d.QueueDirectSendTo("iridium_0", "frame", tc.opts)
		if err != nil {
			t.Fatal(err)
		}
		after := time.Now().UTC()
		del, err := db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		if del.TTLSeconds != tc.ttl || (tc.ttl == 0) != (del.ExpiresAt == nil) {
			t.Fatalf("%s: ttl_seconds %d, expires_at %v", tc.name, del.TTLSeconds, del.ExpiresAt)
		}
		if tc.ttl == 0 {
			continue
		}
		exp, err := time.Parse(sqliteTimeLayout, *del.ExpiresAt)
		if err != nil || exp.Before(before.Add(30*time.Minute)) || exp.After(after.Add(30*time.Minute)) {
			t.Fatalf("%s: expires_at %s (%v), want 30 minutes after it was queued", tc.name, *del.ExpiresAt, err)
		}
	}
}

// A direct send past its deadline expires instead of going out: the
// worker's query never hands it over, the reaper marks it expired, and the
// worker's own check before a send stops one it fetched just before the
// deadline. One held while its link was down keeps its deadline when the
// link is back (a rule delivery would get the held time back), so a test
// held for days never goes out days late. Nothing reaches the gateway.
// [MESHSAT-1430]
func TestDirectSendTTL_ExpiresInsteadOfGoingOut(t *testing.T) {
	h := setupE2E(t)
	t.Cleanup(func() { h.db.Close() })
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("iridium_0")
	gw := h.addGateway("iridium_0", "iridium")
	w := newTestWorker(h, "iridium_0", "iridium")
	position, _ := testFrames()

	queue := func() int64 {
		t.Helper()
		id, _, err := h.dispatch.QueueDirectSendTo("iridium_0", "Alarm test: position report to the Hub", alarmTestOpts(position))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	set := func(id int64, sets string, args ...interface{}) {
		t.Helper()
		if _, err := h.db.Exec(`UPDATE message_deliveries SET `+sets+` WHERE id = ?`, append(args, id)...); err != nil {
			t.Fatal(err)
		}
	}
	pendingHas := func(id int64) bool {
		t.Helper()
		rows, err := h.db.GetPendingDeliveries("iridium_0", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == id {
				return true
			}
		}
		return false
	}
	status := func(id int64) string {
		t.Helper()
		del, err := h.db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		return del.Status
	}
	minuteAgo := time.Now().UTC().Add(-time.Minute).Format(sqliteTimeLayout)

	// Waiting: the worker gets it until its deadline, then never; the reaper
	// expires it.
	a := queue()
	if !pendingHas(a) {
		t.Fatal("a direct send inside its deadline is not handed to the worker")
	}
	set(a, `expires_at = ?`, minuteAgo)
	if pendingHas(a) {
		t.Fatal("a direct send past its deadline is handed to the worker")
	}
	if n, err := h.db.ExpireDeliveries(); err != nil || n != 1 || status(a) != "expired" {
		t.Fatalf("reaper expired %d (%v), row %s", n, err, status(a))
	}

	// Fetched just before its deadline, delivered just after: the worker's
	// own check expires it.
	b := queue()
	set(b, `expires_at = ?`, minuteAgo)
	del, err := h.db.GetDelivery(b)
	if err != nil {
		t.Fatal(err)
	}
	w.deliver(context.Background(), *del)
	if status(b) != "expired" {
		t.Fatalf("delivered past its deadline: %s", status(b))
	}

	// Held three days, its deadline passed meanwhile: the link comes back
	// and the test expires instead of going out.
	c := queue()
	if _, err := h.db.HoldDeliveriesForChannel("iridium_0"); err != nil {
		t.Fatal(err)
	}
	threeDaysAgo := time.Now().UTC().Add(-72 * time.Hour)
	deadline := threeDaysAgo.Add(30 * time.Minute).Format(sqliteTimeLayout)
	set(c, `held_at = ?, expires_at = ?`, threeDaysAgo.Add(time.Minute).Format(sqliteTimeLayout), deadline)
	if _, err := h.db.UnholdDeliveriesForChannel("iridium_0"); err != nil {
		t.Fatal(err)
	}
	if del, _ := h.db.GetDelivery(c); del.Status != "queued" || del.ExpiresAt == nil || *del.ExpiresAt != deadline {
		t.Fatalf("after the hold: %s, expires %v, want queued with its deadline %s", del.Status, del.ExpiresAt, deadline)
	}
	if pendingHas(c) {
		t.Fatal("a test held past its deadline is handed to the worker when its link is back")
	}
	if n, err := h.db.ExpireDeliveries(); err != nil || n != 1 || status(c) != "expired" {
		t.Fatalf("reaper expired %d (%v), row %s", n, err, status(c))
	}

	if n := len(gw.messages()); n != 0 {
		t.Fatalf("%d expired sends reached the gateway", n)
	}
}

// The delivery worker marks the SOS's Hub frame, and only it, as an
// emergency for the gateway (MeshMessage.Critical): a hub_uplink row at
// priority 0. Not the fallback's position frame, not the alarm test, not a
// text queued at priority 0, not a routing rule's delivery at priority 0.
// [MESHSAT-1431]
func TestHubUplink_OnlyTheSOSFrameIsCritical(t *testing.T) {
	h := setupE2E(t)
	t.Cleanup(func() { h.db.Close() })
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")
	gw := h.addGateway("iridium_0", "iridium")
	w := newTestWorker(h, "iridium_0", "iridium")
	position, sos := testFrames()

	direct := func(opts DirectSendOptions) int64 {
		t.Helper()
		id, _, err := h.dispatch.QueueDirectSendTo("iridium_0", "label", opts)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// A rule marked priority 0, forwarding the mesh to the satellite.
	if _, err := h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "everything to the sky", Enabled: true,
		Priority: 0, Action: "forward", ForwardTo: "iridium_0", Filters: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	h.loadRules(t)
	if n := h.dispatch.DispatchAccess("mesh_0", rules.RouteMessage{Text: "relay me", From: "!4370c1d8", PortNum: 1}, []byte("relay me")); n != 1 {
		t.Fatalf("the rule queued %d deliveries", n)
	}
	ruleRows, err := h.db.GetDeliveries(database.DeliveryFilter{Channel: "iridium_0", Limit: 10})
	if err != nil || len(ruleRows) != 1 || ruleRows[0].Priority != 0 || ruleRows[0].RuleID == nil {
		t.Fatalf("rule delivery: %+v (%v)", ruleRows, err)
	}

	for i, tc := range []struct {
		name     string
		id       int64
		critical bool
	}{
		{"the SOS's Hub frame", direct(sosFrameOpts(sos)), true},
		{"the fallback's position frame", direct(positionFrameOpts(position)), false},
		{"the alarm test", direct(alarmTestOpts(position)), false},
		{"a text queued at priority 0", direct(DirectSendOptions{Precedence: "Flash", Critical: true}), false},
		{"a rule's delivery at priority 0", ruleRows[0].ID, false},
	} {
		del, err := h.db.GetDelivery(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		w.deliver(context.Background(), *del)
		msgs := gw.messages()
		if len(msgs) != i+1 {
			t.Fatalf("%s: %d messages at the gateway, want %d", tc.name, len(msgs), i+1)
		}
		if msgs[i].Critical != tc.critical {
			t.Fatalf("%s: critical %v, want %v", tc.name, msgs[i].Critical, tc.critical)
		}
	}
}

// budgetModem is a 9603 whose sessions all succeed; it records what it sent.
type budgetModem struct {
	mu   sync.Mutex
	sent [][]byte
}

func (m *budgetModem) Subscribe(context.Context) (<-chan transport.SatEvent, error) {
	return nil, errors.New("no events here")
}
func (m *budgetModem) Send(_ context.Context, data []byte) (*transport.SatResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, append([]byte(nil), data...))
	return &transport.SatResult{MOStatus: 0}, nil
}
func (m *budgetModem) SendText(_ context.Context, text string) (*transport.SatResult, error) {
	return m.Send(context.Background(), []byte(text))
}
func (m *budgetModem) Receive(context.Context) ([]byte, error) { return nil, nil }
func (m *budgetModem) MailboxCheck(context.Context) (*transport.SatResult, error) {
	return &transport.SatResult{NoSession: true}, nil
}
func (m *budgetModem) GetSignal(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (m *budgetModem) GetSignalFast(context.Context) (*transport.SignalInfo, error) {
	return &transport.SignalInfo{}, nil
}
func (m *budgetModem) GetStatus(context.Context) (*transport.SatStatus, error) {
	return &transport.SatStatus{Connected: true, Type: "sbd"}, nil
}
func (m *budgetModem) GetFirmwareVersion(context.Context) (string, error) { return "TA19002", nil }
func (m *budgetModem) Close() error                                       { return nil }

func (m *budgetModem) frames() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.sent...)
}

// oneGateway provides a single real gateway on one interface.
type oneGateway struct {
	iface string
	gw    gateway.Gateway
}

func (p oneGateway) Gateways() []gateway.Gateway { return []gateway.Gateway{p.gw} }
func (p oneGateway) GatewayByInterfaceID(id string) gateway.Gateway {
	if id == p.iface {
		return p.gw
	}
	return nil
}

// With the day's credit budget used up, the real SBD gateway behind the
// delivery worker sends the SOS's Hub frame and refuses the rest with
// "budget exceeded": the fallback's position frame, the alarm test and a
// routing rule's delivery at priority 0. [MESHSAT-1431]
func TestHubUplink_OnlyTheSOSFramePassesAnExhaustedBudget(t *testing.T) {
	h := setupE2E(t)
	t.Cleanup(func() { h.db.Close() })
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")
	modem := &budgetModem{}
	sbd := gateway.NewSBDGateway(gateway.IridiumConfig{DailyBudget: 2}, modem, h.db, nil)
	if err := h.db.InsertCreditUsage(nil, 2, nil); err != nil {
		t.Fatal(err)
	}
	w := newTestWorker(h, "iridium_0", "iridium")
	w.gwProv = oneGateway{iface: "iridium_0", gw: sbd}
	position, sos := testFrames()

	direct := func(opts DirectSendOptions) int64 {
		t.Helper()
		id, _, err := h.dispatch.QueueDirectSendTo("iridium_0", "label", opts)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, err := h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "everything to the sky", Enabled: true,
		Priority: 0, Action: "forward", ForwardTo: "iridium_0", Filters: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	h.loadRules(t)
	if n := h.dispatch.DispatchAccess("mesh_0", rules.RouteMessage{Text: "relay me", From: "!4370c1d8", PortNum: 1}, []byte("relay me")); n != 1 {
		t.Fatalf("the rule queued %d deliveries", n)
	}
	ruleRows, err := h.db.GetDeliveries(database.DeliveryFilter{Channel: "iridium_0", Limit: 10})
	if err != nil || len(ruleRows) != 1 {
		t.Fatalf("rule delivery: %+v (%v)", ruleRows, err)
	}

	for _, tc := range []struct {
		name string
		id   int64
		sent bool
	}{
		{"the fallback's position frame", direct(positionFrameOpts(position)), false},
		{"the alarm test", direct(alarmTestOpts(position)), false},
		{"a rule's delivery at priority 0", ruleRows[0].ID, false},
		{"the SOS's Hub frame", direct(sosFrameOpts(sos)), true},
	} {
		del, err := h.db.GetDelivery(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		w.deliver(context.Background(), *del)
		after, err := h.db.GetDelivery(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if tc.sent && after.Status != "sent" && after.Status != "delivered" {
			t.Fatalf("%s: %s %q, want sent over the used-up budget", tc.name, after.Status, after.LastError)
		}
		// Refused: a retry, or dead for the rule's QoS 0 (best effort).
		if !tc.sent && (after.Status == "sent" || after.Status == "delivered" || !strings.Contains(after.LastError, "budget exceeded")) {
			t.Fatalf("%s: %s %q, want it refused for budget exceeded", tc.name, after.Status, after.LastError)
		}
	}
	if frames := modem.frames(); len(frames) != 1 || !hubreporter.IsSatSOS(frames[0]) {
		t.Fatalf("the modem sent %d sessions, want the SOS frame alone", len(frames))
	}
}

// The delivery worker sends a row only while it is still queued or waiting
// for a retry: one that stopped being so between the worker's fetch and its
// send (held as its link went down, expired, cancelled, taken by another
// send, denied) is left as it is and nothing goes out. The worker marked
// the row 'sending' unconditionally, which undid such a change and sent it:
// an SOS cancelling the alarm test in that moment did not stop it.
// [MESHSAT-1430]
func TestDeliver_SendsOnlyWhatIsStillQueued(t *testing.T) {
	h := setupE2E(t)
	t.Cleanup(func() { h.db.Close() })
	h.addInterface(t, "mqtt_0", "mqtt", true)
	gw := h.addGateway("mqtt_0", "mqtt")
	w := newTestWorker(h, "mqtt_0", "mqtt")
	for _, tc := range []struct {
		name, becomes string
		sent          bool
	}{
		{"still queued", "", true},
		{"waiting for a retry", "retry", true},
		{"held: its link went down", "held", false},
		{"expired by the reaper", "expired", false},
		{"cancelled", "dead", false},
		{"taken by another send", "sending", false},
		{"denied", "denied", false},
	} {
		id, err := h.db.InsertDelivery(database.MessageDelivery{MsgRef: "claim " + tc.name, Channel: "mqtt_0", Status: "queued",
			Priority: 1, Payload: []byte("x"), TextPreview: "x", MaxRetries: 3, Visited: "[]", QoSLevel: 1})
		if err != nil {
			t.Fatal(err)
		}
		fetched, err := h.db.GetDelivery(id) // what the worker fetched
		if err != nil {
			t.Fatal(err)
		}
		if tc.becomes != "" {
			if _, err := h.db.Exec(`UPDATE message_deliveries SET status = ? WHERE id = ?`, tc.becomes, id); err != nil {
				t.Fatal(err)
			}
		}
		before := len(gw.messages())
		w.deliver(context.Background(), *fetched)
		after, err := h.db.GetDelivery(id)
		if err != nil {
			t.Fatal(err)
		}
		if sent := len(gw.messages()) > before; sent != tc.sent {
			t.Fatalf("%s: sent %v, want %v (now %s)", tc.name, sent, tc.sent, after.Status)
		}
		if !tc.sent && after.Status != tc.becomes {
			t.Fatalf("%s: the worker changed it to %s", tc.name, after.Status)
		}
	}
}
