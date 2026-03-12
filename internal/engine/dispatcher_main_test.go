package engine

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/rules"
)

// --- Tests for Dispatcher.Start() initialization ---

// TestStart_CancelsRunawayDeliveries verifies that Start() cancels deliveries
// with excessive retries on startup (safety net for past bugs).
func TestStart_CancelsRunawayDeliveries(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)

	// Insert a delivery with excessive retries
	id, _ := h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "runaway", Channel: "mqtt_0", Status: "retry",
		Priority: 1, Payload: []byte("stuck"), TextPreview: "stuck",
		MaxRetries: 3, Visited: "[]",
	})
	h.db.Exec("UPDATE message_deliveries SET retries = 20 WHERE id = ?", id)

	// Start dispatcher — should cancel runaway on init
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so workers don't process
	h.dispatch.Start(ctx)

	del, err := h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if del.Status != "dead" {
		t.Errorf("expected runaway delivery to be 'dead' after Start(), got %s", del.Status)
	}
}

// TestStart_RecoversStaleDeliveries verifies that Start() recovers 'sending'
// deliveries from a previous crash/restart back to 'retry'.
func TestStart_RecoversStaleDeliveries(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)

	id, _ := h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "stale-sending", Channel: "mqtt_0", Status: "sending",
		Priority: 1, Payload: []byte("crash"), TextPreview: "crash",
		MaxRetries: 3, Visited: "[]",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.dispatch.Start(ctx)

	del, err := h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if del.Status != "retry" {
		t.Errorf("expected stale delivery recovered to 'retry', got %s", del.Status)
	}
}

// TestStart_LaunchesWorkersForEnabledInterfaces verifies that Start() creates
// a delivery worker for each enabled interface.
func TestStart_LaunchesWorkersForEnabledInterfaces(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.addInterface(t, "disabled_0", "mqtt", false) // disabled

	// Mark disabled_0 as disabled in DB
	h.db.Exec("UPDATE interfaces SET enabled = 0 WHERE id = 'disabled_0'")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	h.dispatch.mu.RLock()
	defer h.dispatch.mu.RUnlock()

	if _, ok := h.dispatch.workers["mqtt_0"]; !ok {
		t.Error("expected worker for enabled mqtt_0")
	}
	if _, ok := h.dispatch.workers["iridium_0"]; !ok {
		t.Error("expected worker for enabled iridium_0")
	}
	if _, ok := h.dispatch.workers["disabled_0"]; ok {
		t.Error("expected NO worker for disabled_0")
	}
}

// --- Tests for startInterfaceWorkers() ---

// TestStartInterfaceWorkers_SkipsExistingWorker verifies that startInterfaceWorkers
// does not create duplicate workers for interfaces that already have one.
func TestStartInterfaceWorkers_SkipsExistingWorker(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Pre-populate a worker
	h.dispatch.mu.Lock()
	h.dispatch.workers["mqtt_0"] = &DeliveryWorker{channelID: "mqtt_0"}
	existingWorker := h.dispatch.workers["mqtt_0"]
	h.dispatch.mu.Unlock()

	// Call startInterfaceWorkers — should skip mqtt_0
	h.dispatch.mu.Lock()
	h.dispatch.startInterfaceWorkers(ctx)
	replacedWorker := h.dispatch.workers["mqtt_0"]
	h.dispatch.mu.Unlock()

	if replacedWorker != existingWorker {
		t.Error("startInterfaceWorkers should not replace existing worker")
	}
}

// TestStartInterfaceWorkers_UnknownChannelType verifies that workers are created
// even for unknown channel types (with default descriptor).
func TestStartInterfaceWorkers_UnknownChannelType(t *testing.T) {
	h := setupE2E(t)

	// Insert interface with an unknown channel type
	h.db.Exec(`INSERT OR IGNORE INTO interfaces (id, channel_type, label, enabled, config)
		VALUES ('custom_0', 'custom', 'Custom', 1, '{}')`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.dispatch.mu.Lock()
	h.dispatch.startInterfaceWorkers(ctx)
	_, hasWorker := h.dispatch.workers["custom_0"]
	h.dispatch.mu.Unlock()

	if !hasWorker {
		t.Error("expected worker created for unknown channel type with default descriptor")
	}
}

// --- Tests for satellitePassSched() ---

// TestSatellitePassSched_ReturnsSched verifies that satellitePassSched returns
// the pass scheduler for satellite channel descriptors.
func TestSatellitePassSched(t *testing.T) {
	h := setupE2E(t)
	ps := &mockPassScheduler{mode: 2}
	h.dispatch.SetPassStateProvider(ps)

	satDesc := channel.ChannelDescriptor{ID: "iridium", IsSatellite: true}
	nonSatDesc := channel.ChannelDescriptor{ID: "mqtt", IsSatellite: false}

	// Satellite channel should get the pass scheduler
	result := h.dispatch.satellitePassSched(satDesc)
	if result != ps {
		t.Error("expected pass scheduler for satellite channel")
	}

	// Non-satellite channel should get nil
	result = h.dispatch.satellitePassSched(nonSatDesc)
	if result != nil {
		t.Error("expected nil for non-satellite channel")
	}

	// Satellite channel with nil passSched should return nil
	h.dispatch.passSched = nil
	result = h.dispatch.satellitePassSched(satDesc)
	if result != nil {
		t.Error("expected nil when passSched is not set")
	}
}

// --- Tests for forwardToGateway() ---

// TestForwardToGateway_InterfaceIDLookup verifies that forwardToGateway uses
// GatewayByInterfaceID for v0.3.0 interface-based routing.
func TestForwardToGateway_InterfaceIDLookup(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	gw := h.addGateway("mqtt_0", "mqtt")

	w := newTestWorker(h, "mqtt_0", "mqtt")

	del := database.MessageDelivery{
		ID: 1, Channel: "mqtt_0", Status: "queued",
		Payload: []byte("test"), TextPreview: "test",
	}
	err := w.forwardToGateway(context.Background(), del, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gw.messages()) != 1 {
		t.Errorf("expected 1 message forwarded via interface ID lookup, got %d", len(gw.messages()))
	}
}

// TestForwardToGateway_LegacyTypeFallback verifies that when interface ID lookup
// fails, forwardToGateway falls back to legacy gateway type matching.
func TestForwardToGateway_LegacyTypeFallback(t *testing.T) {
	h := setupE2E(t)

	// Create gateway registered by type but NOT by interface ID
	legacyGW := &mockGateway{typ: "legacy_0", ifaceID: "legacy_0"}
	h.gwProv.gwSlice = append(h.gwProv.gwSlice, legacyGW)
	// Don't add to gws map (interface ID lookup will fail)

	w := &DeliveryWorker{
		channelID: "legacy_0",
		db:        h.db,
		gwProv:    h.gwProv,
		mesh:      h.meshTx,
	}

	del := database.MessageDelivery{
		Channel: "legacy_0", Status: "queued",
		TextPreview: "legacy msg",
	}
	err := w.forwardToGateway(context.Background(), del, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(legacyGW.messages()) != 1 {
		t.Errorf("expected 1 message via legacy type fallback, got %d", len(legacyGW.messages()))
	}
}

// TestForwardToGateway_NoProviderError verifies that forwardToGateway returns
// an error when no gateway provider is configured.
func TestForwardToGateway_NoProviderError(t *testing.T) {
	w := &DeliveryWorker{
		channelID: "mqtt_0",
		gwProv:    nil,
	}

	del := database.MessageDelivery{
		Channel: "mqtt_0", TextPreview: "no provider",
	}
	err := w.forwardToGateway(context.Background(), del, false)
	if err == nil {
		t.Error("expected error when gwProv is nil")
	}
}

// TestForwardToGateway_GatewayNotFoundError verifies that forwardToGateway returns
// an error when no matching gateway exists.
func TestForwardToGateway_GatewayNotFoundError(t *testing.T) {
	h := setupE2E(t)
	// Don't add any gateways

	w := newTestWorker(h, "nonexistent_0", "nonexistent")

	del := database.MessageDelivery{
		Channel: "nonexistent_0", TextPreview: "orphan",
	}
	err := w.forwardToGateway(context.Background(), del, false)
	if err == nil {
		t.Error("expected error when no matching gateway found")
	}
}

// TestForwardToGateway_SMSContactResolution verifies that forwardToGateway
// resolves SMS contacts from forward_options and adds them to the message.
func TestForwardToGateway_SMSContactResolution(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "cellular_0", "cellular", true)

	// Create SMS contacts
	cid1, _ := h.db.CreateSMSContact("Alice", "+1234567890", "", false)
	cid2, _ := h.db.CreateSMSContact("Bob", "+0987654321", "", false)

	// Create access rule with SMS contacts in forward_options
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Cell",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "cellular_0",
		Filters:        "{}",
		ForwardOptions: fmt.Sprintf(`{"sms_contacts":[%d,%d]}`, cid1, cid2),
	})
	h.loadRules(t)

	// Dispatch to get a delivery with a valid rule_id
	msg := rules.RouteMessage{Text: "sms test", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("sms test"))

	deliveries, _ := h.db.GetPendingDeliveries("cellular_0", 1)
	if len(deliveries) != 1 {
		t.Fatal("expected 1 delivery")
	}

	// Set up a capture gateway to inspect the forwarded message
	captureGW := h.addGateway("cellular_0", "cellular")

	w := newTestWorker(h, "cellular_0", "cellular")
	err := w.forwardToGateway(context.Background(), deliveries[0], false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := captureGW.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 forwarded message, got %d", len(msgs))
	}
	if len(msgs[0].SMSDestinations) != 2 {
		t.Errorf("expected 2 SMS destinations, got %d", len(msgs[0].SMSDestinations))
	}
}

// TestForwardToGateway_EncryptedFlag verifies that the encrypted flag is
// propagated to the forwarded message.
func TestForwardToGateway_EncryptedFlag(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	gw := h.addGateway("mqtt_0", "mqtt")

	w := newTestWorker(h, "mqtt_0", "mqtt")

	del := database.MessageDelivery{
		Channel: "mqtt_0", Status: "queued",
		Payload: []byte("encrypted data"), TextPreview: "encrypted data",
	}
	err := w.forwardToGateway(context.Background(), del, true)
	if err != nil {
		t.Fatal(err)
	}

	msgs := gw.messages()
	if len(msgs) != 1 {
		t.Fatal("expected 1 message")
	}
	if !msgs[0].Encrypted {
		t.Error("expected Encrypted flag to be true on forwarded message")
	}
}

// --- Tests for NewDispatcher() env var parsing ---

// TestNewDispatcher_DefaultValues verifies that NewDispatcher uses correct defaults
// when no environment variables are set.
func TestNewDispatcher_DefaultValues(t *testing.T) {
	os.Unsetenv("MESHSAT_MAX_HOPS")
	os.Unsetenv("MESHSAT_MAX_QUEUE_DEPTH")
	os.Unsetenv("MESHSAT_MAX_QUEUE_BYTES")

	db, _ := database.New(":memory:")
	defer db.Close()
	reg := channel.NewRegistry()

	d := NewDispatcher(db, reg, nil, nil)

	if d.maxHops != DefaultMaxHops {
		t.Errorf("maxHops: want %d, got %d", DefaultMaxHops, d.maxHops)
	}
	if d.maxQueueDepth != DefaultMaxQueueDepth {
		t.Errorf("maxQueueDepth: want %d, got %d", DefaultMaxQueueDepth, d.maxQueueDepth)
	}
	if d.maxQueueBytes != int64(DefaultMaxQueueBytes) {
		t.Errorf("maxQueueBytes: want %d, got %d", DefaultMaxQueueBytes, d.maxQueueBytes)
	}
	if d.deliveryDedupTTL != DefaultDeliveryDedupTTL {
		t.Errorf("deliveryDedupTTL: want %v, got %v", DefaultDeliveryDedupTTL, d.deliveryDedupTTL)
	}
}

// TestNewDispatcher_EnvOverrides verifies that NewDispatcher reads env vars
// for maxHops, maxQueueDepth, and maxQueueBytes.
func TestNewDispatcher_EnvOverrides(t *testing.T) {
	os.Setenv("MESHSAT_MAX_HOPS", "4")
	os.Setenv("MESHSAT_MAX_QUEUE_DEPTH", "100")
	os.Setenv("MESHSAT_MAX_QUEUE_BYTES", "2097152")
	defer os.Unsetenv("MESHSAT_MAX_HOPS")
	defer os.Unsetenv("MESHSAT_MAX_QUEUE_DEPTH")
	defer os.Unsetenv("MESHSAT_MAX_QUEUE_BYTES")

	db, _ := database.New(":memory:")
	defer db.Close()
	reg := channel.NewRegistry()

	d := NewDispatcher(db, reg, nil, nil)

	if d.maxHops != 4 {
		t.Errorf("maxHops: want 4, got %d", d.maxHops)
	}
	if d.maxQueueDepth != 100 {
		t.Errorf("maxQueueDepth: want 100, got %d", d.maxQueueDepth)
	}
	if d.maxQueueBytes != 2097152 {
		t.Errorf("maxQueueBytes: want 2097152, got %d", d.maxQueueBytes)
	}
}

// TestNewDispatcher_InvalidEnvIgnored verifies that invalid env var values
// are silently ignored and defaults are used.
func TestNewDispatcher_InvalidEnvIgnored(t *testing.T) {
	os.Setenv("MESHSAT_MAX_HOPS", "not-a-number")
	os.Setenv("MESHSAT_MAX_QUEUE_DEPTH", "-5")
	os.Setenv("MESHSAT_MAX_QUEUE_BYTES", "")
	defer os.Unsetenv("MESHSAT_MAX_HOPS")
	defer os.Unsetenv("MESHSAT_MAX_QUEUE_DEPTH")
	defer os.Unsetenv("MESHSAT_MAX_QUEUE_BYTES")

	db, _ := database.New(":memory:")
	defer db.Close()
	reg := channel.NewRegistry()

	d := NewDispatcher(db, reg, nil, nil)

	if d.maxHops != DefaultMaxHops {
		t.Errorf("invalid MESHSAT_MAX_HOPS should default: want %d, got %d", DefaultMaxHops, d.maxHops)
	}
	if d.maxQueueDepth != DefaultMaxQueueDepth {
		t.Errorf("negative MESHSAT_MAX_QUEUE_DEPTH should default: want %d, got %d", DefaultMaxQueueDepth, d.maxQueueDepth)
	}
}

// --- Tests for DeliveryWorker.Run() lifecycle ---

// TestRun_ExitsOnContextCancel verifies that the Run loop exits promptly
// when the context is cancelled.
func TestRun_ExitsOnContextCancel(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)

	w := newTestWorker(h, "mqtt_0", "mqtt")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Cancel immediately
	cancel()

	select {
	case <-done:
		// Run exited as expected
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit within 5s after context cancellation")
	}
}

// TestRun_ProcessesDeliveries verifies that Run picks up and processes
// deliveries from the queue via the 2-second polling loop.
func TestRun_ProcessesDeliveries(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	gw := h.addGateway("mqtt_0", "mqtt")

	w := newTestWorker(h, "mqtt_0", "mqtt")

	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "run-test", Channel: "mqtt_0", Status: "queued",
		Priority: 1, Payload: []byte("run loop msg"), TextPreview: "run loop msg",
		MaxRetries: 3, Visited: "[]",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Run(ctx)

	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout: Run did not process delivery within 6s")
		default:
		}
		if len(gw.messages()) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// --- Tests for StartWorker / StopWorker ---

// TestStartWorker_UnholdsDeliveries verifies that StartWorker unholds deliveries
// when an interface transitions to ONLINE.
func TestStartWorker_UnholdsDeliveries(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)

	// Insert held deliveries
	for i := 0; i < 3; i++ {
		h.db.InsertDelivery(database.MessageDelivery{
			MsgRef: fmt.Sprintf("held-%d", i), Channel: "mqtt_0", Status: "held",
			Priority: 1, Payload: []byte("held"), TextPreview: "held",
			MaxRetries: 3, Visited: "[]",
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.dispatch.StartWorker(ctx, "mqtt_0", "mqtt")

	// Deliveries should be unheld
	pending, _ := h.db.GetPendingDeliveries("mqtt_0", 10)
	if len(pending) != 3 {
		t.Errorf("expected 3 unheld deliveries, got %d", len(pending))
	}
}

// TestStopWorker_HoldsDeliveries verifies that StopWorker holds pending
// deliveries when an interface goes OFFLINE.
func TestStopWorker_HoldsDeliveries(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)

	// Insert queued deliveries
	for i := 0; i < 2; i++ {
		h.db.InsertDelivery(database.MessageDelivery{
			MsgRef: fmt.Sprintf("queued-%d", i), Channel: "mqtt_0", Status: "queued",
			Priority: 1, Payload: []byte("queued"), TextPreview: "queued",
			MaxRetries: 3, Visited: "[]",
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start then stop
	h.dispatch.StartWorker(ctx, "mqtt_0", "mqtt")
	h.dispatch.StopWorker("mqtt_0")

	held, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Status: "held", Limit: 10})
	if len(held) != 2 {
		t.Errorf("expected 2 held deliveries after StopWorker, got %d", len(held))
	}

	// Worker should be removed
	h.dispatch.mu.RLock()
	_, exists := h.dispatch.workers["mqtt_0"]
	h.dispatch.mu.RUnlock()
	if exists {
		t.Error("expected worker to be removed after StopWorker")
	}
}

// TestStartWorker_Idempotent verifies that calling StartWorker twice for the
// same interface doesn't create a duplicate worker.
func TestStartWorker_Idempotent(t *testing.T) {
	h := setupE2E(t)
	h.addInterface(t, "mqtt_0", "mqtt", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.dispatch.StartWorker(ctx, "mqtt_0", "mqtt")

	h.dispatch.mu.RLock()
	w1 := h.dispatch.workers["mqtt_0"]
	h.dispatch.mu.RUnlock()

	h.dispatch.StartWorker(ctx, "mqtt_0", "mqtt")

	h.dispatch.mu.RLock()
	w2 := h.dispatch.workers["mqtt_0"]
	h.dispatch.mu.RUnlock()

	if w1 != w2 {
		t.Error("second StartWorker call should not replace existing worker")
	}
}

// TestStopWorker_Idempotent verifies that calling StopWorker on an interface
// with no worker doesn't panic.
func TestStopWorker_Idempotent(t *testing.T) {
	h := setupE2E(t)
	// Should not panic
	h.dispatch.StopWorker("nonexistent_0")
}

// --- Tests for DispatchAccess edge cases ---

// TestDispatchAccess_NilAccessEvaluator verifies that DispatchAccess returns 0
// when no access evaluator is configured.
func TestDispatchAccess_NilAccessEvaluator(t *testing.T) {
	db, _ := database.New(":memory:")
	defer db.Close()
	reg := channel.NewRegistry()
	d := NewDispatcher(db, reg, nil, nil)
	// d.access is nil

	msg := rules.RouteMessage{Text: "no evaluator", PortNum: 1}
	count := d.DispatchAccess("mesh_0", msg, nil)
	if count != 0 {
		t.Errorf("expected 0 with nil access evaluator, got %d", count)
	}
}

// TestDispatchAccess_VisitedSetMergeDedup verifies that the visited set is
// properly deduplicated when merging source + previous visited.
func TestDispatchAccess_VisitedSetMergeDedup(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Forward",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// Message already visited mesh_0 (duplicate of source)
	msg := rules.RouteMessage{
		Text: "dedup visited", From: "!node", PortNum: 1,
		Visited: []string{"mesh_0", "iridium_0"},
	}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte("dedup visited"))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	deliveries, _ := h.db.GetPendingDeliveries("mqtt_0", 1)
	if len(deliveries) != 1 {
		t.Fatal("expected 1 delivery")
	}
	// Visited should contain mesh_0 and iridium_0 (no duplicates)
	v := deliveries[0].Visited
	if v != `["mesh_0","iridium_0"]` {
		t.Errorf("expected deduplicated visited set, got %s", v)
	}
}

// TestDispatchAccess_TextPreviewTruncation verifies that text preview is
// truncated to 200 characters.
func TestDispatchAccess_TextPreviewTruncation(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Forward",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// Create a 300-char message
	longText := ""
	for i := 0; i < 300; i++ {
		longText += "x"
	}
	msg := rules.RouteMessage{Text: longText, From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte(longText))

	deliveries, _ := h.db.GetPendingDeliveries("mqtt_0", 1)
	if len(deliveries) != 1 {
		t.Fatal("expected 1 delivery")
	}
	if len(deliveries[0].TextPreview) != 200 {
		t.Errorf("expected text preview truncated to 200, got %d", len(deliveries[0].TextPreview))
	}
}
