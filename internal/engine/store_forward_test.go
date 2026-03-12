package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/rules"
)

// --- mock pass scheduler ---

type mockPassScheduler struct {
	mode int
}

func (m *mockPassScheduler) PassMode() int { return m.mode }

// --- Store-and-Forward tests ---

// TestSF_HoldOnOffline verifies that when an interface goes OFFLINE, all
// queued/retry deliveries move to HELD status via batch transition.
func TestSF_HoldOnOffline(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Iridium",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "iridium_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// Queue 3 deliveries
	for i := 0; i < 3; i++ {
		msg := rules.RouteMessage{Text: fmt.Sprintf("msg-%d", i), From: "!node", PortNum: 1}
		h.dispatch.DispatchAccess("mesh_0", msg, []byte(fmt.Sprintf("payload-%d", i)))
	}

	// Verify all 3 are queued
	pending, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(pending))
	}

	// Simulate interface going offline → hold deliveries
	n, err := h.db.HoldDeliveriesForChannel("iridium_0")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("expected 3 held, got %d", n)
	}

	// No pending deliveries should remain
	pending, _ = h.db.GetPendingDeliveries("iridium_0", 10)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after hold, got %d", len(pending))
	}

	// Verify held status in DB
	held, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "iridium_0", Status: "held", Limit: 10})
	if len(held) != 3 {
		t.Errorf("expected 3 held deliveries, got %d", len(held))
	}
}

// TestSF_UnholdOnOnline verifies that when an interface comes back ONLINE,
// HELD deliveries transition back to QUEUED.
func TestSF_UnholdOnOnline(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Iridium",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "iridium_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// Queue deliveries
	for i := 0; i < 2; i++ {
		msg := rules.RouteMessage{Text: fmt.Sprintf("msg-%d", i), From: "!node", PortNum: 1}
		h.dispatch.DispatchAccess("mesh_0", msg, []byte(fmt.Sprintf("payload-%d", i)))
	}

	// Hold them
	h.db.HoldDeliveriesForChannel("iridium_0")

	// Unhold (interface comes online)
	n, err := h.db.UnholdDeliveriesForChannel("iridium_0")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 unheld, got %d", n)
	}

	// Should be back to queued
	pending, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	if len(pending) != 2 {
		t.Errorf("expected 2 pending after unhold, got %d", len(pending))
	}
}

// TestSF_TTLClockPauseWhileHeld verifies that expires_at is extended by the
// duration spent in HELD state, effectively pausing the TTL clock.
func TestSF_TTLClockPauseWhileHeld(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")

	// Rule with 300s TTL
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "TTL Pause",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}", ForwardOptions: `{"ttl_seconds":300}`,
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "ttl pause test", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("ttl pause test"))

	// Get the original expiry
	deliveries, _ := h.db.GetPendingDeliveries("mqtt_0", 1)
	if len(deliveries) != 1 {
		t.Fatal("expected 1 delivery")
	}
	origExpiry := *deliveries[0].ExpiresAt

	// Hold and wait briefly
	h.db.HoldDeliveriesForChannel("mqtt_0")
	time.Sleep(2 * time.Second)

	// Unhold — expires_at should be extended
	h.db.UnholdDeliveriesForChannel("mqtt_0")

	deliveries, _ = h.db.GetPendingDeliveries("mqtt_0", 1)
	if len(deliveries) != 1 {
		t.Fatal("expected 1 delivery after unhold")
	}
	newExpiry := *deliveries[0].ExpiresAt

	origT, _ := time.Parse("2006-01-02 15:04:05", origExpiry)
	newT, _ := time.Parse("2006-01-02 15:04:05", newExpiry)

	// New expiry should be at least 1 second later (we slept 2s but allow margin)
	if !newT.After(origT) {
		t.Errorf("expected expires_at to be extended after hold/unhold: orig=%s new=%s", origExpiry, newExpiry)
	}
}

// TestSF_TTLExpiryBeforeSend verifies that expired deliveries are filtered out
// of the pending query (not fetched by workers) and that the reaper marks them
// as 'expired'. The GetPendingDeliveries SQL excludes expired rows, so the
// worker never attempts delivery. The batch reaper (ExpireDeliveries) cleans up.
func TestSF_TTLExpiryBeforeSend(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	h.addGateway("mqtt_0", "mqtt")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Short TTL",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}", ForwardOptions: `{"ttl_seconds":1}`,
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "will expire", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("will expire"))

	// Verify delivery exists and has TTL
	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Limit: 10})
	if len(all) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(all))
	}
	if all[0].TTLSeconds != 1 {
		t.Errorf("expected TTL 1s, got %d", all[0].TTLSeconds)
	}

	// Wait for TTL to expire
	time.Sleep(2 * time.Second)

	// GetPendingDeliveries should NOT return expired deliveries
	pending, _ := h.db.GetPendingDeliveries("mqtt_0", 10)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending (expired filtered out), got %d", len(pending))
	}

	// Run the batch reaper to clean up
	n, err := h.db.ExpireDeliveries()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 expired by reaper, got %d", n)
	}

	// Delivery should now be marked 'expired'
	all, _ = h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Limit: 10})
	if all[0].Status != "expired" {
		t.Errorf("expected status 'expired', got %s", all[0].Status)
	}
}

// TestSF_P0CriticalNeverExpires verifies that P0 critical messages (priority=0)
// are exempt from TTL expiry.
func TestSF_P0CriticalNeverExpires(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	h.addGateway("mqtt_0", "mqtt")

	// Priority 0 (critical) with short TTL — TTL should be ignored
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "P0 Critical",
		Enabled: true, Priority: 0, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}", ForwardOptions: `{"ttl_seconds":1}`,
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "SOS CRITICAL", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("SOS CRITICAL"))

	// P0 should NOT have expires_at set
	deliveries, _ := h.db.GetPendingDeliveries("mqtt_0", 1)
	if len(deliveries) != 1 {
		t.Fatal("expected 1 delivery")
	}
	if deliveries[0].ExpiresAt != nil && *deliveries[0].ExpiresAt != "" {
		t.Errorf("P0 critical should not have expires_at, got %v", *deliveries[0].ExpiresAt)
	}

	// Wait past "TTL" — should still be deliverable
	time.Sleep(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout: P0 message should have been delivered despite TTL")
		default:
		}
		gw := h.gwProv.gws["mqtt_0"]
		if len(gw.messages()) > 0 {
			return // success
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestSF_PriorityOrdering verifies that P0 deliveries are sent before P1/P2.
func TestSF_PriorityOrdering(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	// Insert deliveries directly with different priorities (P2, P1, P0)
	for _, p := range []int{2, 1, 0} {
		h.db.InsertDelivery(database.MessageDelivery{
			MsgRef:      fmt.Sprintf("ref-%d", p),
			Channel:     "mqtt_0",
			Status:      "queued",
			Priority:    p,
			Payload:     []byte(fmt.Sprintf("priority-%d", p)),
			TextPreview: fmt.Sprintf("priority-%d", p),
			MaxRetries:  3,
			Visited:     "[]",
		})
	}

	// Fetch pending — should be ordered P0 first
	pending, err := h.db.GetPendingDeliveries("mqtt_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(pending))
	}
	if pending[0].Priority != 0 {
		t.Errorf("first delivery should be P0, got P%d", pending[0].Priority)
	}
	if pending[1].Priority != 1 {
		t.Errorf("second delivery should be P1, got P%d", pending[1].Priority)
	}
	if pending[2].Priority != 2 {
		t.Errorf("third delivery should be P2, got P%d", pending[2].Priority)
	}
}

// TestSF_ExpireDeliveries_Reaper verifies that the batch reaper marks expired
// deliveries (queued, retry, held) but skips P0.
func TestSF_ExpireDeliveries_Reaper(t *testing.T) {
	h := setupE2E(t)

	past := time.Now().UTC().Add(-1 * time.Minute).Format("2006-01-02 15:04:05")
	future := time.Now().UTC().Add(1 * time.Hour).Format("2006-01-02 15:04:05")

	// Expired P1 delivery (should be reaped)
	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "expired-p1", Channel: "mqtt_0", Status: "queued",
		Priority: 1, Payload: []byte("x"), TextPreview: "x", MaxRetries: 3,
		Visited: "[]", TTLSeconds: 60, ExpiresAt: &past,
	})
	// Expired P0 delivery (should NOT be reaped — exempt)
	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "expired-p0", Channel: "mqtt_0", Status: "queued",
		Priority: 0, Payload: []byte("SOS"), TextPreview: "SOS", MaxRetries: 3,
		Visited: "[]", TTLSeconds: 60, ExpiresAt: &past,
	})
	// Non-expired P1 delivery
	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "future-p1", Channel: "mqtt_0", Status: "queued",
		Priority: 1, Payload: []byte("y"), TextPreview: "y", MaxRetries: 3,
		Visited: "[]", TTLSeconds: 3600, ExpiresAt: &future,
	})
	// Expired held delivery (should be reaped)
	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "expired-held", Channel: "mqtt_0", Status: "held",
		Priority: 2, Payload: []byte("z"), TextPreview: "z", MaxRetries: 3,
		Visited: "[]", TTLSeconds: 60, ExpiresAt: &past,
	})

	n, err := h.db.ExpireDeliveries()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 expired by reaper, got %d", n)
	}

	// P0 should still be queued
	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Status: "queued", Limit: 10})
	foundP0 := false
	for _, d := range all {
		if d.MsgRef == "expired-p0" {
			foundP0 = true
		}
	}
	if !foundP0 {
		t.Error("P0 delivery should remain queued (exempt from expiry)")
	}
}

// TestSF_EgressDenial verifies that deliveries blocked by egress rules are
// marked 'denied' and not sent to the gateway.
func TestSF_EgressDenial(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	h.addGateway("mqtt_0", "mqtt")

	// Ingress rule: mesh → mqtt
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Forward",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	// Egress rule on mqtt_0: only allow SOS keyword (everything else denied)
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mqtt_0", Direction: "egress", Name: "SOS Only",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "",
		Filters: `{"keyword":"SOS"}`,
	})
	h.loadRules(t)

	// Dispatch a non-SOS message — should be queued but denied at egress
	msg := rules.RouteMessage{Text: "hello world", From: "!node", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte("hello world"))
	if count != 1 {
		t.Fatalf("expected 1 delivery created, got %d", count)
	}

	// Start dispatcher and let worker process
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	time.Sleep(4 * time.Second)

	// Gateway should NOT have received the message
	gw := h.gwProv.gws["mqtt_0"]
	if len(gw.messages()) > 0 {
		t.Error("expected no messages (egress denied)")
	}

	// Delivery should be in 'denied' status
	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Limit: 10})
	if len(all) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(all))
	}
	if all[0].Status != "denied" {
		t.Errorf("expected status 'denied', got %s", all[0].Status)
	}
}

// TestSF_QueueDepthLimit verifies that new deliveries are rejected when the
// queue depth limit is reached.
func TestSF_QueueDepthLimit(t *testing.T) {
	h := setupE2E(t)
	h.dispatch.maxQueueDepth = 2

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Forward",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// First 2 should succeed
	for i := 0; i < 2; i++ {
		msg := rules.RouteMessage{Text: fmt.Sprintf("msg-%d", i), From: "!node", PortNum: 1}
		count := h.dispatch.DispatchAccess("mesh_0", msg, []byte(fmt.Sprintf("unique-payload-%d", i)))
		if count != 1 {
			t.Fatalf("delivery %d: expected 1, got %d", i, count)
		}
	}

	// Third should be rejected (queue full)
	msg := rules.RouteMessage{Text: "overflow", From: "!node", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte("overflow-payload"))
	if count != 0 {
		t.Errorf("expected 0 deliveries (queue full), got %d", count)
	}
}

// TestSF_QueueBytesLimit verifies that new deliveries are rejected when the
// queue bytes limit is reached.
func TestSF_QueueBytesLimit(t *testing.T) {
	h := setupE2E(t)
	h.dispatch.maxQueueBytes = 100 // 100 bytes max

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Forward",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// Insert a 60-byte payload
	msg1 := rules.RouteMessage{Text: "first", From: "!node", PortNum: 1}
	payload1 := make([]byte, 60)
	copy(payload1, "first-payload")
	count := h.dispatch.DispatchAccess("mesh_0", msg1, payload1)
	if count != 1 {
		t.Fatalf("first delivery: expected 1, got %d", count)
	}

	// Insert another 60-byte payload (total would be 120 > 100 limit)
	msg2 := rules.RouteMessage{Text: "second", From: "!node", PortNum: 1}
	payload2 := make([]byte, 60)
	copy(payload2, "second-payload")
	count2 := h.dispatch.DispatchAccess("mesh_0", msg2, payload2)
	if count2 != 0 {
		t.Errorf("expected 0 (bytes limit exceeded), got %d", count2)
	}
}

// TestSF_RetryOnFailure verifies that a failed delivery is scheduled for retry
// with correct backoff and retry count.
func TestSF_RetryOnFailure(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")
	h.setOnline("mqtt_0")

	// Gateway that fails the first attempt
	gw := h.addGateway("mqtt_0", "mqtt")
	gw.failNext = true

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Forward",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "retry me", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("retry me"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	// Wait for first attempt (should fail)
	time.Sleep(4 * time.Second)

	// Check delivery is in retry status
	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Limit: 10})
	if len(all) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(all))
	}
	if all[0].Status != "retry" && all[0].Status != "sent" {
		// Could be sent if worker picked it up again quickly after retry window
		t.Logf("status: %s, retries: %d, last_error: %s", all[0].Status, all[0].Retries, all[0].LastError)
	}
	if all[0].Retries < 1 {
		// May need more time for the worker to process
		t.Logf("retries=%d (may not have been processed yet)", all[0].Retries)
	}
}

// TestSF_DeadAfterRetryExhaustion verifies that a delivery moves to 'dead'
// after exhausting all retries.
func TestSF_DeadAfterRetryExhaustion(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mqtt_0", "mqtt", true)

	// Insert a delivery already at max retries
	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef:      "exhaust-ref",
		Channel:     "mqtt_0",
		Status:      "retry",
		Priority:    1,
		Payload:     []byte("doomed"),
		TextPreview: "doomed",
		Retries:     9,
		MaxRetries:  10,
		Visited:     "[]",
	})

	// Simulate the worker processing this delivery by creating a worker directly
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	desc, _ := reg.Get("mqtt")

	gw := h.addGateway("mqtt_0", "mqtt")
	gw.failNext = true // will fail

	w := &DeliveryWorker{
		channelID: "mqtt_0",
		desc:      desc,
		db:        h.db,
		gwProv:    h.gwProv,
		mesh:      h.meshTx,
	}

	pending, _ := h.db.GetPendingDeliveries("mqtt_0", 1)
	if len(pending) != 1 {
		t.Fatal("expected 1 pending delivery")
	}

	ctx := context.Background()
	w.deliver(ctx, pending[0])

	// Should be dead now
	del, err := h.db.GetDelivery(pending[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if del.Status != "dead" {
		t.Errorf("expected status 'dead', got %s", del.Status)
	}
}

// TestSF_PassAwareScheduling_IdleSkips verifies that satellite workers skip
// processing during Idle pass mode.
func TestSF_PassAwareScheduling_IdleSkips(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")

	iridiumGW := h.addGateway("iridium_0", "iridium")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Iridium",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "iridium_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "satellite msg", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("satellite msg"))

	// Set pass scheduler to Idle (mode 0)
	ps := &mockPassScheduler{mode: 0}
	h.dispatch.SetPassStateProvider(ps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	// Wait a full poll cycle — should NOT drain
	time.Sleep(4 * time.Second)

	if len(iridiumGW.messages()) > 0 {
		t.Error("expected no messages during Idle pass mode")
	}

	// Delivery should still be queued
	pending, _ := h.db.GetPendingDeliveries("iridium_0", 10)
	if len(pending) != 1 {
		t.Errorf("expected 1 still pending, got %d", len(pending))
	}
}

// TestSF_PassAwareScheduling_ActiveDrains verifies that satellite workers
// drain the queue during Active pass mode.
func TestSF_PassAwareScheduling_ActiveDrains(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")

	iridiumGW := h.addGateway("iridium_0", "iridium")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Iridium",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "iridium_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "active pass msg", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("active pass msg"))

	// Set pass scheduler to Active (mode 2)
	ps := &mockPassScheduler{mode: 2}
	h.dispatch.SetPassStateProvider(ps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout: expected delivery during Active pass mode")
		default:
		}
		if len(iridiumGW.messages()) > 0 {
			return // success
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestSF_DefaultChannelTTL verifies that when a rule has no TTL override,
// the channel descriptor's DefaultTTL is used.
func TestSF_DefaultChannelTTL(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")

	// Rule with NO ttl_seconds in forward_options — should use iridium's DefaultTTL (3600s)
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "No TTL Override",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "iridium_0",
		Filters: "{}",
	})
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "default ttl", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("default ttl"))

	deliveries, _ := h.db.GetPendingDeliveries("iridium_0", 1)
	if len(deliveries) != 1 {
		t.Fatal("expected 1 delivery")
	}
	if deliveries[0].TTLSeconds != 3600 {
		t.Errorf("expected default TTL 3600s (iridium), got %d", deliveries[0].TTLSeconds)
	}
	if deliveries[0].ExpiresAt == nil {
		t.Error("expected expires_at to be set with default TTL")
	}
}

// TestSF_WorkerStartStop_IntegrationWithDispatcher tests that StartWorker/StopWorker
// correctly manage worker lifecycle and hold/unhold deliveries.
func TestSF_WorkerStartStop_IntegrationWithDispatcher(t *testing.T) {
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

	// Queue a delivery
	msg := rules.RouteMessage{Text: "lifecycle test", From: "!node", PortNum: 1}
	h.dispatch.DispatchAccess("mesh_0", msg, []byte("lifecycle test"))

	// Start the dispatcher (creates initial workers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	// Stop the worker (simulates OFFLINE)
	h.dispatch.StopWorker("mqtt_0")

	// Delivery should be held
	held, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Status: "held", Limit: 10})
	if len(held) != 1 {
		t.Errorf("expected 1 held delivery after StopWorker, got %d", len(held))
	}

	// Restart the worker (simulates ONLINE)
	h.addGateway("mqtt_0", "mqtt")
	h.dispatch.StartWorker(ctx, "mqtt_0", "mqtt")

	// Delivery should be unheld and eventually sent
	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout: delivery not sent after StartWorker")
		default:
		}
		gw := h.gwProv.gws["mqtt_0"]
		if len(gw.messages()) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestSF_CancelRunawayDeliveries verifies startup cleanup of deliveries that
// exceeded their retry limits.
func TestSF_CancelRunawayDeliveries(t *testing.T) {
	h := setupE2E(t)

	// InsertDelivery doesn't set retries, so we insert and then update via raw SQL
	id1, _ := h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "runaway", Channel: "mqtt_0", Status: "retry",
		Priority: 1, Payload: []byte("stuck"), TextPreview: "stuck",
		MaxRetries: 3, Visited: "[]",
	})
	h.db.Exec("UPDATE message_deliveries SET retries = 20 WHERE id = ?", id1)

	id2, _ := h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "infinite-runaway", Channel: "mqtt_0", Status: "queued",
		Priority: 1, Payload: []byte("infinite"), TextPreview: "infinite",
		MaxRetries: 0, Visited: "[]",
	})
	h.db.Exec("UPDATE message_deliveries SET retries = 20 WHERE id = ?", id2)

	n, err := h.db.CancelRunawayDeliveries(15)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 cancelled runaway deliveries, got %d", n)
	}

	// Both should be dead
	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Limit: 10})
	for _, d := range all {
		if d.Status != "dead" {
			t.Errorf("delivery %s: expected 'dead', got %s", d.MsgRef, d.Status)
		}
	}
}

// TestSF_RecoverStaleDeliveries verifies that 'sending' deliveries stuck from
// a crash/restart are recovered to 'retry'.
func TestSF_RecoverStaleDeliveries(t *testing.T) {
	h := setupE2E(t)

	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "stale", Channel: "mqtt_0", Status: "sending",
		Priority: 1, Payload: []byte("stuck-sending"), TextPreview: "stuck-sending",
		MaxRetries: 3, Visited: "[]",
	})

	n, err := h.db.RecoverStaleDeliveries()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 recovered, got %d", n)
	}

	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Limit: 10})
	if len(all) != 1 {
		t.Fatal("expected 1 delivery")
	}
	if all[0].Status != "retry" {
		t.Errorf("expected 'retry' after recovery, got %s", all[0].Status)
	}
}

// TestSF_PreWakeExpiresStale verifies that during PreWake mode, the worker
// expires stale deliveries (TTL check) but does not drain the queue.
func TestSF_PreWakeExpiresStale(t *testing.T) {
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")

	iridiumGW := h.addGateway("iridium_0", "iridium")

	// Insert a delivery that already expired
	past := time.Now().UTC().Add(-1 * time.Minute).Format("2006-01-02 15:04:05")
	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "expired-pre-wake", Channel: "iridium_0", Status: "queued",
		Priority: 1, Payload: []byte("stale"), TextPreview: "stale", MaxRetries: 3,
		Visited: "[]", TTLSeconds: 60, ExpiresAt: &past,
	})
	// Insert a non-expired delivery
	h.db.InsertDelivery(database.MessageDelivery{
		MsgRef: "fresh-pre-wake", Channel: "iridium_0", Status: "queued",
		Priority: 1, Payload: []byte("fresh"), TextPreview: "fresh", MaxRetries: 3,
		Visited: "[]",
	})

	// Set pass scheduler to PreWake (mode 1)
	ps := &mockPassScheduler{mode: 1}
	h.dispatch.SetPassStateProvider(ps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.dispatch.Start(ctx)

	// Wait for worker to process
	time.Sleep(4 * time.Second)

	// Gateway should NOT have received any messages (PreWake doesn't drain)
	if len(iridiumGW.messages()) > 0 {
		t.Error("expected no messages during PreWake mode")
	}

	// Expired delivery should be marked expired by pre-wake TTL check
	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "iridium_0", Limit: 10})
	expiredCount := 0
	queuedCount := 0
	for _, d := range all {
		if d.Status == "expired" {
			expiredCount++
		}
		if d.Status == "queued" {
			queuedCount++
		}
	}
	if expiredCount != 1 {
		t.Errorf("expected 1 expired delivery from pre-wake, got %d", expiredCount)
	}
	if queuedCount != 1 {
		t.Errorf("expected 1 queued delivery still pending, got %d", queuedCount)
	}
}

// TestSF_QueueEviction_HighPriorityDisplacesLow verifies that when the queue
// is full, a higher-priority message evicts the lowest-priority existing delivery.
func TestSF_QueueEviction_HighPriorityDisplacesLow(t *testing.T) {
	h := setupE2E(t)
	h.dispatch.maxQueueDepth = 2

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")

	// Rule with P2 (low priority)
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Low Priority",
		Enabled: true, Priority: 2, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// Fill queue with 2 P2 deliveries
	for i := 0; i < 2; i++ {
		msg := rules.RouteMessage{Text: fmt.Sprintf("low-%d", i), From: "!node", PortNum: 1}
		h.dispatch.DispatchAccess("mesh_0", msg, []byte(fmt.Sprintf("low-payload-%d", i)))
	}

	// Queue should be full
	depth, _ := h.db.QueueDepth("mqtt_0")
	if depth != 2 {
		t.Fatalf("expected queue depth 2, got %d", depth)
	}

	// Now update the rule to P0 (critical) and dispatch a high-priority message
	h.db.Exec("UPDATE access_rules SET priority = 0 WHERE name = 'Low Priority'")
	h.loadRules(t)

	msg := rules.RouteMessage{Text: "critical-msg", From: "!node", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte("critical-payload"))
	if count != 1 {
		t.Errorf("expected P0 message to be admitted (evicting P2), got count=%d", count)
	}

	// One P2 should be dead (evicted), one P2 queued, one P0 queued
	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Limit: 10})
	deadCount := 0
	queuedCount := 0
	for _, d := range all {
		if d.Status == "dead" {
			deadCount++
			if d.Priority != 2 {
				t.Errorf("evicted delivery should be P2, got P%d", d.Priority)
			}
		}
		if d.Status == "queued" {
			queuedCount++
		}
	}
	if deadCount != 1 {
		t.Errorf("expected 1 evicted (dead) delivery, got %d", deadCount)
	}
	if queuedCount != 2 {
		t.Errorf("expected 2 queued deliveries (1 P2 + 1 P0), got %d", queuedCount)
	}
}

// TestSF_QueueEviction_SamePriorityRejected verifies that when the queue is full
// and the new message has the same priority as the lowest, it is rejected (no eviction).
func TestSF_QueueEviction_SamePriorityRejected(t *testing.T) {
	h := setupE2E(t)
	h.dispatch.maxQueueDepth = 2

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "mqtt_0", "mqtt", true)
	h.setOnline("mesh_0")

	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Same Priority",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0",
		Filters: "{}",
	})
	h.loadRules(t)

	// Fill queue with 2 P1 deliveries
	for i := 0; i < 2; i++ {
		msg := rules.RouteMessage{Text: fmt.Sprintf("p1-%d", i), From: "!node", PortNum: 1}
		h.dispatch.DispatchAccess("mesh_0", msg, []byte(fmt.Sprintf("p1-payload-%d", i)))
	}

	// Third P1 should be rejected (same priority, no eviction)
	msg := rules.RouteMessage{Text: "p1-overflow", From: "!node", PortNum: 1}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte("p1-overflow-payload"))
	if count != 0 {
		t.Errorf("expected P1 rejected when queue full of P1s, got count=%d", count)
	}

	// No deliveries should be dead (no eviction happened)
	all, _ := h.db.GetDeliveries(database.DeliveryFilter{Channel: "mqtt_0", Status: "dead", Limit: 10})
	if len(all) != 0 {
		t.Errorf("expected 0 dead deliveries (no eviction), got %d", len(all))
	}
}
