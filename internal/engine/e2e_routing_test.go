package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// --- Mock gateway for E2E tests ---

type mockGateway struct {
	mu        sync.Mutex
	typ       string
	ifaceID   string
	forwarded []*transport.MeshMessage
	failNext  bool
}

func (m *mockGateway) Start(ctx context.Context) error { return nil }
func (m *mockGateway) Stop() error                     { return nil }
func (m *mockGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext {
		m.failNext = false
		return errDeliveryFailed
	}
	m.forwarded = append(m.forwarded, msg)
	return nil
}
func (m *mockGateway) Receive() <-chan gateway.InboundMessage {
	return make(chan gateway.InboundMessage)
}
func (m *mockGateway) Status() gateway.GatewayStatus {
	return gateway.GatewayStatus{Connected: true}
}
func (m *mockGateway) Enqueue(msg *transport.MeshMessage) error {
	return m.Forward(context.Background(), msg)
}
func (m *mockGateway) Type() string { return m.typ }

func (m *mockGateway) messages() []*transport.MeshMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*transport.MeshMessage, len(m.forwarded))
	copy(cp, m.forwarded)
	return cp
}

var errDeliveryFailed = &deliveryError{"mock delivery failure"}

type deliveryError struct{ msg string }

func (e *deliveryError) Error() string { return e.msg }

// --- Mock gateway provider ---

type mockGWProvider struct {
	gws     map[string]*mockGateway // interface ID → gateway
	gwSlice []gateway.Gateway
}

func (p *mockGWProvider) Gateways() []gateway.Gateway {
	return p.gwSlice
}

func (p *mockGWProvider) GatewayByInterfaceID(id string) gateway.Gateway {
	if gw, ok := p.gws[id]; ok {
		return gw
	}
	return nil
}

// --- Mock mesh transport (implements transport.MeshTransport) ---

type mockMeshTransport struct {
	mu   sync.Mutex
	sent []transport.SendRequest
}

func (m *mockMeshTransport) Subscribe(ctx context.Context) (<-chan transport.MeshEvent, error) {
	return make(chan transport.MeshEvent), nil
}
func (m *mockMeshTransport) SendMessage(ctx context.Context, req transport.SendRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, req)
	return nil
}
func (m *mockMeshTransport) SendRaw(ctx context.Context, req transport.RawRequest) error {
	return nil
}
func (m *mockMeshTransport) GetNodes(ctx context.Context) ([]transport.MeshNode, error) {
	return nil, nil
}
func (m *mockMeshTransport) GetStatus(ctx context.Context) (*transport.MeshStatus, error) {
	return nil, nil
}
func (m *mockMeshTransport) GetMessages(ctx context.Context, limit int) ([]transport.MeshMessage, error) {
	return nil, nil
}
func (m *mockMeshTransport) GetConfig(ctx context.Context) (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockMeshTransport) AdminReboot(ctx context.Context, nodeNum uint32, delay int) error {
	return nil
}
func (m *mockMeshTransport) AdminFactoryReset(ctx context.Context, nodeNum uint32) error {
	return nil
}
func (m *mockMeshTransport) Traceroute(ctx context.Context, nodeNum uint32) error { return nil }
func (m *mockMeshTransport) SetRadioConfig(ctx context.Context, section string, data json.RawMessage) error {
	return nil
}
func (m *mockMeshTransport) SetModuleConfig(ctx context.Context, section string, data json.RawMessage) error {
	return nil
}
func (m *mockMeshTransport) SetChannel(ctx context.Context, req transport.ChannelRequest) error {
	return nil
}
func (m *mockMeshTransport) SendWaypoint(ctx context.Context, wp transport.Waypoint) error {
	return nil
}
func (m *mockMeshTransport) RemoveNode(ctx context.Context, nodeNum uint32) error { return nil }
func (m *mockMeshTransport) GetConfigSection(ctx context.Context, section string) error {
	return nil
}
func (m *mockMeshTransport) GetModuleConfigSection(ctx context.Context, section string) error {
	return nil
}
func (m *mockMeshTransport) SendPosition(ctx context.Context, lat, lon float64, alt int32) error {
	return nil
}
func (m *mockMeshTransport) SetFixedPosition(ctx context.Context, lat, lon float64, alt int32) error {
	return nil
}
func (m *mockMeshTransport) RemoveFixedPosition(ctx context.Context) error { return nil }
func (m *mockMeshTransport) RequestStoreForward(ctx context.Context, nodeNum uint32, window uint32) error {
	return nil
}
func (m *mockMeshTransport) SendRangeTest(ctx context.Context, text string, to uint32) error {
	return nil
}
func (m *mockMeshTransport) SetCannedMessages(ctx context.Context, messages string) error {
	return nil
}
func (m *mockMeshTransport) GetCannedMessages(ctx context.Context) error { return nil }
func (m *mockMeshTransport) GetNeighborInfo(ctx context.Context) ([]transport.NeighborInfo, error) {
	return nil, nil
}
func (m *mockMeshTransport) SendEncryptedRelay(ctx context.Context, encryptedPayload []byte, to uint32, channel uint32, hopLimit uint32) error {
	return nil
}
func (m *mockMeshTransport) SetOwner(_ context.Context, _, _ string) error     { return nil }
func (m *mockMeshTransport) RequestNodeInfo(_ context.Context, _ uint32) error { return nil }
func (m *mockMeshTransport) Close() error                                      { return nil }

func (m *mockMeshTransport) messages() []transport.SendRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]transport.SendRequest, len(m.sent))
	copy(cp, m.sent)
	return cp
}

// --- E2E helper ---

type e2eHarness struct {
	db       *database.DB
	dispatch *Dispatcher
	ifaceMgr *InterfaceManager
	failover *FailoverResolver
	signing  *SigningService
	gwProv   *mockGWProvider
	meshTx   *mockMeshTransport
}

func setupE2E(t *testing.T) *e2eHarness {
	t.Helper()

	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)

	meshTx := &mockMeshTransport{}
	gwProv := &mockGWProvider{gws: make(map[string]*mockGateway)}

	disp := NewDispatcher(db, reg, gwProv, meshTx)

	ifaceMgr := NewInterfaceManager(db)
	fr := NewFailoverResolver(db, ifaceMgr)

	ss, err := NewSigningService(db)
	if err != nil {
		t.Fatal(err)
	}

	disp.SetFailoverResolver(fr)
	disp.SetSigningService(ss)
	disp.SetTransformPipeline(NewTransformPipeline())

	return &e2eHarness{
		db:       db,
		dispatch: disp,
		ifaceMgr: ifaceMgr,
		failover: fr,
		signing:  ss,
		gwProv:   gwProv,
		meshTx:   meshTx,
	}
}

func (h *e2eHarness) addInterface(t *testing.T, id, chanType string, enabled bool) {
	t.Helper()
	// Use INSERT OR IGNORE since migrations may seed default interfaces (e.g. mesh_0)
	_, err := h.db.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config)
		VALUES (?, ?, ?, ?, '{}')`, id, chanType, id, enabled)
	if err != nil {
		t.Fatal(err)
	}
	// Prime the interface manager runtime state
	h.ifaceMgr.mu.Lock()
	h.ifaceMgr.states[id] = &interfaceRuntime{
		iface: database.Interface{ID: id, ChannelType: chanType, Enabled: enabled},
		state: StateOffline,
	}
	h.ifaceMgr.mu.Unlock()
}

func (h *e2eHarness) setOnline(id string) {
	h.ifaceMgr.mu.Lock()
	if rt, ok := h.ifaceMgr.states[id]; ok {
		rt.state = StateOnline
	}
	h.ifaceMgr.mu.Unlock()
}

func (h *e2eHarness) setOffline(id string) {
	h.ifaceMgr.mu.Lock()
	if rt, ok := h.ifaceMgr.states[id]; ok {
		rt.state = StateOffline
	}
	h.ifaceMgr.mu.Unlock()
}

func (h *e2eHarness) addGateway(ifaceID, gwType string) *mockGateway {
	gw := &mockGateway{typ: gwType, ifaceID: ifaceID}
	h.gwProv.gws[ifaceID] = gw
	h.gwProv.gwSlice = append(h.gwProv.gwSlice, gw)
	return gw
}

func (h *e2eHarness) loadRules(t *testing.T) {
	t.Helper()
	ae := rules.NewAccessEvaluator(h.db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	h.dispatch.SetAccessEvaluator(ae)
}

// --- E2E tests ---

// TestE2E_MeshToIridium_SingleRule tests a single message flowing from mesh ingress
// through access rule evaluation to iridium egress, with signing and delivery.
func TestE2E_MeshToIridium_SingleRule(t *testing.T) {
	h := setupE2E(t)

	// Set up interfaces
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")

	// Set up gateway
	iridiumGW := h.addGateway("iridium_0", "iridium")

	// Create access rule: mesh_0 ingress → forward to iridium_0
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0",
		Direction:   "ingress",
		Name:        "Mesh to Iridium",
		Enabled:     true,
		Priority:    10,
		Action:      "forward",
		ForwardTo:   "iridium_0",
		Filters:     "{}",
	})
	h.loadRules(t)

	// Dispatch a message arriving on mesh_0
	msg := rules.RouteMessage{
		Text:    "Emergency: need extraction at grid 4521",
		From:    "!aabbccdd",
		PortNum: 1,
	}
	payload := []byte(msg.Text)

	count := h.dispatch.DispatchAccess("mesh_0", msg, payload)
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	// Verify delivery was created in DB with correct attributes
	deliveries, err := h.db.GetPendingDeliveries("iridium_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 pending delivery, got %d", len(deliveries))
	}

	del := deliveries[0]
	if del.Channel != "iridium_0" {
		t.Errorf("delivery channel: want iridium_0, got %s", del.Channel)
	}
	if del.Status != "queued" {
		t.Errorf("delivery status: want queued, got %s", del.Status)
	}
	if del.Priority != 10 {
		t.Errorf("delivery priority: want 10, got %d", del.Priority)
	}
	// Note: GetPendingDeliveries doesn't select signature/signer_id columns,
	// so we verify signing through the audit log instead.

	// Verify audit log was created
	entries, err := h.db.GetAuditLog(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("expected audit log entry for dispatch")
	}
	foundDispatch := false
	for _, e := range entries {
		if e.EventType == "dispatch" {
			foundDispatch = true
			break
		}
	}
	if !foundDispatch {
		t.Error("expected 'dispatch' audit event")
	}

	// Now simulate the delivery worker processing
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.dispatch.Start(ctx)

	// Wait for delivery worker to pick up and send (polls every 2s)
	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for delivery worker to process message")
		default:
		}

		msgs := iridiumGW.messages()
		if len(msgs) > 0 {
			if msgs[0].DecodedText != msg.Text {
				t.Errorf("forwarded text: want %q, got %q", msg.Text, msgs[0].DecodedText)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Verify delivery status is now "sent"
	allDel, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	if len(allDel) != 0 {
		t.Errorf("expected 0 pending deliveries after send, got %d", len(allDel))
	}
}

// TestE2E_MultiDestination_FanOut tests a message being routed to multiple
// destinations simultaneously (mesh → iridium + mqtt).
func TestE2E_MultiDestination_FanOut(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")
	h.setOnline("mqtt_0")

	h.addGateway("iridium_0", "iridium")
	h.addGateway("mqtt_0", "mqtt")

	// Two rules: mesh → iridium and mesh → mqtt
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Iridium",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "iridium_0", Filters: "{}",
	})
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To MQTT",
		Enabled: true, Priority: 20, Action: "forward", ForwardTo: "mqtt_0", Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "broadcast msg", From: "!11223344", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text))
	if count != 2 {
		t.Fatalf("expected 2 deliveries (fan-out), got %d", count)
	}

	// Verify both delivery targets
	iridiumDels, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	mqttDels, _ := h.db.GetPendingDeliveries("mqtt_0", 10)
	if len(iridiumDels) != 1 {
		t.Errorf("expected 1 iridium delivery, got %d", len(iridiumDels))
	}
	if len(mqttDels) != 1 {
		t.Errorf("expected 1 mqtt delivery, got %d", len(mqttDels))
	}
}

// TestE2E_FailoverGroup_PrimaryDown tests that when the primary interface in a
// failover group is offline, messages route to the backup.
func TestE2E_FailoverGroup_PrimaryDown(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.addInterface(t, "iridium_1", "iridium", true)
	h.setOnline("mesh_0")
	h.setOffline("iridium_0") // primary is down
	h.setOnline("iridium_1")  // backup is up

	h.addGateway("iridium_1", "iridium")

	// Failover group
	h.db.InsertFailoverGroup(&database.FailoverGroup{ID: "sat_group", Label: "Satellite", Mode: "failover"})
	h.db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_0", Priority: 1})
	h.db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_1", Priority: 2})

	// Rule targets the failover group, not a specific interface
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Satellite Group",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "sat_group", Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "failover test", From: "!aabb", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	// Should be routed to iridium_1 (backup), not iridium_0 (down)
	deliveries, _ := h.db.GetPendingDeliveries("iridium_1", 10)
	if len(deliveries) != 1 {
		t.Fatalf("expected delivery on iridium_1 (backup), got %d", len(deliveries))
	}
	iridium0Dels, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	if len(iridium0Dels) != 0 {
		t.Errorf("expected 0 deliveries on iridium_0 (primary down), got %d", len(iridium0Dels))
	}
}

// TestE2E_LoopPrevention_VisitedSet tests that messages don't loop between
// interfaces when forwarding rules create a cycle.
func TestE2E_LoopPrevention_VisitedSet(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	// Rule: mesh_0 → mqtt_0
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To MQTT",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "mqtt_0", Filters: "{}",
	})
	// Rule: mqtt_0 → mesh_0 (would create a loop)
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mqtt_0", Direction: "ingress", Name: "To Mesh",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "mesh_0", Filters: "{}",
	})
	h.loadRules(t)

	// First hop: mesh_0 → mqtt_0 (should succeed)
	msg := rules.RouteMessage{Text: "loop test", From: "!node1", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text))
	if count != 1 {
		t.Fatalf("first hop: expected 1 delivery, got %d", count)
	}

	// Second hop: mqtt_0 → mesh_0 with visited=[mesh_0] (should be blocked)
	msg2 := rules.RouteMessage{
		Text:    "loop test",
		From:    "!node1",
		PortNum: 1,
		Visited: []string{"mesh_0"},
	}
	count2 := h.dispatch.DispatchAccess("mqtt_0", msg2, []byte(msg2.Text))
	if count2 != 0 {
		t.Fatalf("second hop (loop): expected 0 deliveries, got %d", count2)
	}
}

// TestE2E_KeywordFilter tests that keyword-based access rules only forward
// messages containing the keyword.
func TestE2E_KeywordFilter(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")

	// Rule with keyword filter: only forward messages containing "SOS"
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "SOS to Iridium",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "iridium_0",
		Filters: `{"keyword":"SOS"}`,
	})
	h.loadRules(t)

	// Message without keyword → should be dropped (implicit deny)
	msg := rules.RouteMessage{Text: "hello world", From: "!node", PortNum: 1}
	if count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text)); count != 0 {
		t.Errorf("non-SOS message: expected 0 deliveries, got %d", count)
	}

	// Message with keyword → should be forwarded
	msg2 := rules.RouteMessage{Text: "SOS need help at grid 1234", From: "!node", PortNum: 1}
	if count := h.dispatch.DispatchAccess("mesh_0", msg2, []byte(msg2.Text)); count != 1 {
		t.Errorf("SOS message: expected 1 delivery, got %d", count)
	}
}

// TestE2E_ExplicitDrop tests that an explicit drop rule stops evaluation and
// prevents forwarding even when a later rule would match.
func TestE2E_ExplicitDrop(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	// Drop rule (higher priority = evaluated first due to lower number)
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Drop telemetry",
		Enabled: true, Priority: 1, Action: "drop", ForwardTo: "",
		Filters: `{"portnums":"[67]"}`,
	})
	// Forward rule (lower priority)
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Forward all",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// Telemetry message (portnum 67) → should be explicitly dropped
	msg := rules.RouteMessage{Text: "telemetry", From: "!node", PortNum: 67}
	if count := h.dispatch.DispatchAccess("mesh_0", msg, nil); count != 0 {
		t.Errorf("telemetry drop: expected 0 deliveries, got %d", count)
	}

	// Text message (portnum 1) → should be forwarded (drop rule doesn't match)
	msg2 := rules.RouteMessage{Text: "hello", From: "!node", PortNum: 1}
	if count := h.dispatch.DispatchAccess("mesh_0", msg2, nil); count != 1 {
		t.Errorf("text forward: expected 1 delivery, got %d", count)
	}
}

// TestE2E_TTL_ExpiresAt tests that TTL from forward_options is correctly
// propagated to delivery records.
func TestE2E_TTL_ExpiresAt(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "TTL Test",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "iridium_0",
		Filters: "{}", ForwardOptions: `{"ttl_seconds":300}`,
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "ttl test", From: "!node", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	deliveries, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	del := deliveries[0]
	if del.TTLSeconds != 300 {
		t.Errorf("TTL: want 300, got %d", del.TTLSeconds)
	}
	if del.ExpiresAt == nil {
		t.Error("expected expires_at to be set")
	}
}

// TestE2E_MeshToMesh_Delivery tests routing back to mesh transport (loopback
// delivery to a different mesh interface).
func TestE2E_MeshToMesh_Delivery(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.setOnline("mqtt_0")
	h.setOnline("mesh_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mqtt_0", Direction: "ingress", Name: "MQTT to Mesh",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "mesh_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "relay to mesh", From: "mqtt-client", PortNum: 1}
	count := h.dispatch.DispatchAccess("mqtt_0", msg, []byte(msg.Text))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	// Start workers and verify the mesh transport receives the message
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for mesh transport to receive message")
		default:
		}
		msgs := h.meshTx.messages()
		if len(msgs) > 0 {
			if msgs[0].Text != msg.Text {
				t.Errorf("mesh text: want %q, got %q", msg.Text, msgs[0].Text)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestE2E_SigningAndAuditChain verifies that the full dispatch→deliver cycle
// produces a valid hash-chain audit trail with correct signatures.
func TestE2E_SigningAndAuditChain(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	h.addGateway("mqtt_0", "mqtt")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Signed Route",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	payload := []byte("signed message payload")
	msg := rules.RouteMessage{Text: "signed message payload", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, payload)

	// Verify delivery was created
	deliveries, _ := h.db.GetPendingDeliveries("mqtt_0", 10)
	if len(deliveries) != 1 {
		t.Fatal("expected 1 delivery")
	}

	// Let delivery worker process it
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for delivery")
		default:
		}
		gw := h.gwProv.gws["mqtt_0"]
		if len(gw.messages()) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Allow time for audit event to be recorded
	time.Sleep(100 * time.Millisecond)

	// Verify audit chain integrity
	valid, brokenAt := h.signing.VerifyChain(100)
	if brokenAt != -1 {
		t.Errorf("audit chain broken at index %d", brokenAt)
	}
	if valid < 1 {
		t.Errorf("expected at least 1 valid audit entry, got %d", valid)
	}
}

// TestE2E_MeshToAstrocast_SingleRule tests routing a message from mesh ingress
// to an Astrocast gateway egress through the dispatcher.
func TestE2E_MeshToAstrocast_SingleRule(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "astrocast_0", "astrocast", true)
	h.setOnline("mesh_0")
	h.setOnline("astrocast_0")

	astroGW := h.addGateway("astrocast_0", "astrocast")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Mesh to Astrocast",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "astrocast_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "LEO uplink test", From: "!node1", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	deliveries, _ := h.db.GetPendingDeliveries("astrocast_0", 10)
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 pending delivery on astrocast_0, got %d", len(deliveries))
	}

	// Start dispatcher and verify gateway receives the message
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for astrocast delivery")
		default:
		}
		msgs := astroGW.messages()
		if len(msgs) > 0 {
			if msgs[0].DecodedText != "LEO uplink test" {
				t.Errorf("forwarded text: want %q, got %q", "LEO uplink test", msgs[0].DecodedText)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestE2E_AstrocastToMesh_InboundRouting tests routing an inbound Astrocast
// message back to mesh transport.
func TestE2E_AstrocastToMesh_InboundRouting(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "astrocast_0", "astrocast", true)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.setOnline("astrocast_0")
	h.setOnline("mesh_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "astrocast_0", Direction: "ingress", Name: "Astrocast to Mesh",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "mesh_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "downlink from LEO", From: "astrocast", PortNum: 1}
	count := h.dispatch.DispatchAccess("astrocast_0", msg, []byte(msg.Text))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	// Start dispatcher and verify mesh transport receives the message
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for mesh delivery")
		default:
		}
		msgs := h.meshTx.messages()
		if len(msgs) > 0 {
			if msgs[0].Text != "downlink from LEO" {
				t.Errorf("mesh text: want %q, got %q", "downlink from LEO", msgs[0].Text)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestE2E_MeshToAstrocast_FailoverGroup tests failover from iridium to astrocast
// when iridium is offline.
func TestE2E_MeshToAstrocast_FailoverGroup(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.addInterface(t, "astrocast_0", "astrocast", true)
	h.setOnline("mesh_0")
	h.setOffline("iridium_0")  // primary is down
	h.setOnline("astrocast_0") // backup is up

	h.addGateway("astrocast_0", "astrocast")

	// Failover group: iridium primary, astrocast backup
	h.db.InsertFailoverGroup(&database.FailoverGroup{ID: "leo_group", Label: "LEO Satellites", Mode: "failover"})
	h.db.InsertFailoverMember(&database.FailoverMember{GroupID: "leo_group", InterfaceID: "iridium_0", Priority: 1})
	h.db.InsertFailoverMember(&database.FailoverMember{GroupID: "leo_group", InterfaceID: "astrocast_0", Priority: 2})

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To LEO Group",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "leo_group",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "failover to astrocast", From: "!node1", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	// Should route to astrocast_0 (backup), not iridium_0 (down)
	astroDels, _ := h.db.GetPendingDeliveries("astrocast_0", 10)
	if len(astroDels) != 1 {
		t.Fatalf("expected delivery on astrocast_0 (backup), got %d", len(astroDels))
	}
	iridiumDels, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	if len(iridiumDels) != 0 {
		t.Errorf("expected 0 deliveries on iridium_0 (down), got %d", len(iridiumDels))
	}
}

// TestE2E_MeshFanOut_IridiumAndAstrocast tests simultaneous routing to both
// Iridium and Astrocast gateways (dual-constellation).
func TestE2E_MeshFanOut_IridiumAndAstrocast(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.addInterface(t, "astrocast_0", "astrocast", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")
	h.setOnline("astrocast_0")

	h.addGateway("iridium_0", "iridium")
	h.addGateway("astrocast_0", "astrocast")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Iridium",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "iridium_0",
		Filters: "{}",
	})
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Astrocast",
		Enabled: true, Priority: 20, Action: "forward", ForwardTo: "astrocast_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "dual-constellation broadcast", From: "!node1", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text))
	if count != 2 {
		t.Fatalf("expected 2 deliveries (dual-constellation), got %d", count)
	}

	iridiumDels, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	astroDels, _ := h.db.GetPendingDeliveries("astrocast_0", 10)
	if len(iridiumDels) != 1 {
		t.Errorf("expected 1 iridium delivery, got %d", len(iridiumDels))
	}
	if len(astroDels) != 1 {
		t.Errorf("expected 1 astrocast delivery, got %d", len(astroDels))
	}
}

// TestE2E_AstrocastLoopPrevention tests that visited-set prevents loops
// when astrocast and mesh have bidirectional rules.
func TestE2E_AstrocastLoopPrevention(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "astrocast_0", "astrocast", true)
	h.setOnline("mesh_0")
	h.setOnline("astrocast_0")

	// Bidirectional rules
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Mesh to Astrocast",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "astrocast_0",
		Filters: "{}",
	})
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "astrocast_0", Direction: "ingress", Name: "Astrocast to Mesh",
		Enabled: true, Priority: 10, Action: "forward", ForwardTo: "mesh_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// First hop: mesh_0 → astrocast_0
	msg := rules.RouteMessage{Text: "no loop", From: "!node1", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(msg.Text))
	if count != 1 {
		t.Fatalf("first hop: expected 1 delivery, got %d", count)
	}

	// Second hop: astrocast_0 → mesh_0 with visited=[mesh_0] (should be blocked)
	msg2 := rules.RouteMessage{
		Text: "no loop", From: "!node1", PortNum: 1,
		Visited: []string{"mesh_0"},
	}
	count2 := h.dispatch.DispatchAccess("astrocast_0", msg2, []byte(msg2.Text))
	if count2 != 0 {
		t.Fatalf("second hop (loop): expected 0 deliveries, got %d", count2)
	}
}

// TestE2E_DisabledRule_ImplicitDeny tests that disabled rules are skipped and
// messages are implicitly denied when no enabled rule matches.
func TestE2E_DisabledRule_ImplicitDeny(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Disabled Route",
		Enabled: false, Priority: 10, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "should be denied", From: "!node", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, nil)
	if count != 0 {
		t.Errorf("disabled rule: expected 0 deliveries (implicit deny), got %d", count)
	}
}
