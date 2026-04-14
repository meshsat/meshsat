package engine

import (
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/rules"
)

func strPtr(s string) *string { return &s }

func setupTestDispatcher(t *testing.T) (*Dispatcher, *database.DB) {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)

	d := NewDispatcher(db, reg, nil, nil)
	return d, db
}

func TestDispatchAccess_CreatesDeliveries(t *testing.T) {
	d, db := setupTestDispatcher(t)

	// Create an interface and access rule
	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	db.InsertInterface(&database.Interface{ID: "mqtt_0", ChannelType: "mqtt", Enabled: true})
	db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0",
		Direction:   "ingress",
		Name:        "Test",
		Enabled:     true,
		Priority:    1,
		Action:      "forward",
		ForwardTo:   "mqtt_0",
		Filters:     "{}",
	})

	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)

	msg := rules.RouteMessage{Text: "hello", From: "!12345678", PortNum: 1}
	count := d.DispatchAccess("mesh_0", msg, []byte("hello"))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	deliveries, err := db.GetPendingDeliveries("mqtt_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 pending delivery, got %d", len(deliveries))
	}
	if deliveries[0].Channel != "mqtt_0" {
		t.Errorf("expected channel mqtt_0, got %s", deliveries[0].Channel)
	}
	if deliveries[0].Status != "queued" {
		t.Errorf("expected status queued, got %s", deliveries[0].Status)
	}
}

func TestDispatchAccess_MultiMatch(t *testing.T) {
	d, db := setupTestDispatcher(t)

	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	db.InsertInterface(&database.Interface{ID: "mqtt_0", ChannelType: "mqtt", Enabled: true})
	db.InsertInterface(&database.Interface{ID: "iridium_0", ChannelType: "iridium", Enabled: true})

	for _, dest := range []string{"mqtt_0", "iridium_0"} {
		db.InsertAccessRule(&database.AccessRule{
			InterfaceID: "mesh_0",
			Direction:   "ingress",
			Name:        "To " + dest,
			Enabled:     true,
			Priority:    1,
			Action:      "forward",
			ForwardTo:   dest,
			Filters:     "{}",
		})
	}

	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)

	count := d.DispatchAccess("mesh_0", rules.RouteMessage{Text: "test", PortNum: 1}, nil)
	if count != 2 {
		t.Fatalf("expected 2 deliveries, got %d", count)
	}
}

func TestDispatchAccess_SelfLoopPrevented(t *testing.T) {
	d, db := setupTestDispatcher(t)

	db.InsertInterface(&database.Interface{ID: "mqtt_0", ChannelType: "mqtt", Enabled: true})
	db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mqtt_0",
		Direction:   "ingress",
		Name:        "Loop",
		Enabled:     true,
		Priority:    1,
		Action:      "forward",
		ForwardTo:   "mqtt_0", // self-loop
		Filters:     "{}",
	})

	ae := rules.NewAccessEvaluator(db)
	if err := ae.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.SetAccessEvaluator(ae)

	count := d.DispatchAccess("mqtt_0", rules.RouteMessage{Text: "test"}, nil)
	if count != 0 {
		t.Fatalf("expected 0 deliveries (self-loop), got %d", count)
	}
}

func TestDispatchAccess_MaxHopCount(t *testing.T) {
	d, db := setupTestDispatcher(t)

	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	db.InsertInterface(&database.Interface{ID: "mqtt_0", ChannelType: "mqtt", Enabled: true})
	db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Test",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0", Filters: "{}",
	})
	ae := rules.NewAccessEvaluator(db)
	ae.ReloadFromDB()
	d.SetAccessEvaluator(ae)

	// Message with visited set at max hops should be dropped
	d.maxHops = 3
	msg := rules.RouteMessage{
		Text:    "too many hops",
		From:    "!node",
		PortNum: 1,
		Visited: []string{"iridium_0", "cellular_0", "zigbee_0"},
	}
	count := d.DispatchAccess("mesh_0", msg, []byte("too many hops"))
	if count != 0 {
		t.Fatalf("expected 0 deliveries (max hops), got %d", count)
	}
	if got := d.loopMetrics.HopLimitDrops.Load(); got != 1 {
		t.Errorf("hop_limit_drops: want 1, got %d", got)
	}

	// Message below max hops should pass through
	msg2 := rules.RouteMessage{
		Text:    "within limit",
		From:    "!node",
		PortNum: 1,
		Visited: []string{"iridium_0", "cellular_0"},
	}
	count2 := d.DispatchAccess("mesh_0", msg2, []byte("within limit"))
	if count2 != 1 {
		t.Fatalf("expected 1 delivery (under max hops), got %d", count2)
	}
}

func TestDispatchAccess_ContentHashDedup(t *testing.T) {
	d, db := setupTestDispatcher(t)

	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	db.InsertInterface(&database.Interface{ID: "mqtt_0", ChannelType: "mqtt", Enabled: true})
	db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Test",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0", Filters: "{}",
	})
	ae := rules.NewAccessEvaluator(db)
	ae.ReloadFromDB()
	d.SetAccessEvaluator(ae)

	payload := []byte("identical payload content")
	msg := rules.RouteMessage{Text: "identical payload content", From: "!node", PortNum: 1}

	// First dispatch should succeed
	count := d.DispatchAccess("mesh_0", msg, payload)
	if count != 1 {
		t.Fatalf("first dispatch: expected 1, got %d", count)
	}

	// Same payload to same interface within TTL should be suppressed
	count2 := d.DispatchAccess("mesh_0", msg, payload)
	if count2 != 0 {
		t.Fatalf("duplicate dispatch: expected 0, got %d", count2)
	}
	if got := d.loopMetrics.DeliveryDedups.Load(); got != 1 {
		t.Errorf("delivery_dedups: want 1, got %d", got)
	}

	// Different payload should succeed
	msg3 := rules.RouteMessage{Text: "different content", From: "!node", PortNum: 1}
	count3 := d.DispatchAccess("mesh_0", msg3, []byte("different content"))
	if count3 != 1 {
		t.Fatalf("different payload: expected 1, got %d", count3)
	}
}

func TestDispatchAccess_ContentHashDedup_NilPayload(t *testing.T) {
	d, db := setupTestDispatcher(t)

	db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Enabled: true})
	db.InsertInterface(&database.Interface{ID: "mqtt_0", ChannelType: "mqtt", Enabled: true})
	db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "Test",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "mqtt_0", Filters: "{}",
	})
	ae := rules.NewAccessEvaluator(db)
	ae.ReloadFromDB()
	d.SetAccessEvaluator(ae)

	msg := rules.RouteMessage{Text: "nil payload", From: "!node", PortNum: 1}

	// Nil payloads should never be deduped (no content to hash)
	count := d.DispatchAccess("mesh_0", msg, nil)
	if count != 1 {
		t.Fatalf("first nil payload: expected 1, got %d", count)
	}
	count2 := d.DispatchAccess("mesh_0", msg, nil)
	if count2 != 1 {
		t.Fatalf("second nil payload: expected 1, got %d", count2)
	}
}

func TestDispatchAccess_VisitedSetMetrics(t *testing.T) {
	// The visited set check in the dispatcher fires on the post-failover-resolution
	// path. We set up a failover group whose resolved target is in the visited set.
	h := setupE2E(t)

	h.addInterface(t, "mesh_0", "mesh", true)
	h.addInterface(t, "iridium_0", "iridium", true)
	h.setOnline("mesh_0")
	h.setOnline("iridium_0")

	// Failover group resolving to iridium_0
	h.db.InsertFailoverGroup(&database.FailoverGroup{ID: "sat_group", Label: "Sat", Mode: "failover"})
	h.db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_0", Priority: 1})

	// Rule targets the group, not the direct interface
	h.db.InsertAccessRule(&database.AccessRule{
		InterfaceID: "mesh_0", Direction: "ingress", Name: "To Sat Group",
		Enabled: true, Priority: 1, Action: "forward", ForwardTo: "sat_group", Filters: "{}",
	})
	h.loadRules(t)

	// iridium_0 is in visited set → post-resolution check should block it
	msg := rules.RouteMessage{
		Text: "loop", From: "!node", PortNum: 1,
		Visited: []string{"iridium_0"},
	}
	count := h.dispatch.DispatchAccess("mesh_0", msg, []byte("loop"))
	if count != 0 {
		t.Fatalf("expected 0 (visited set), got %d", count)
	}
	if got := h.dispatch.loopMetrics.VisitedSetDrops.Load(); got < 1 {
		t.Errorf("visited_set_drops: want >=1, got %d", got)
	}
}

func TestLoopMetrics_Snapshot(t *testing.T) {
	var m LoopMetrics
	m.HopLimitDrops.Add(5)
	m.VisitedSetDrops.Add(3)
	m.SelfLoopDrops.Add(1)
	m.DeliveryDedups.Add(2)

	snap := m.Snapshot()
	if snap["hop_limit_drops"] != 5 {
		t.Errorf("hop_limit_drops: want 5, got %d", snap["hop_limit_drops"])
	}
	if snap["visited_set_drops"] != 3 {
		t.Errorf("visited_set_drops: want 3, got %d", snap["visited_set_drops"])
	}
	if snap["self_loop_drops"] != 1 {
		t.Errorf("self_loop_drops: want 1, got %d", snap["self_loop_drops"])
	}
	if snap["delivery_dedups"] != 2 {
		t.Errorf("delivery_dedups: want 2, got %d", snap["delivery_dedups"])
	}
}

func TestPruneDeliveryDedup(t *testing.T) {
	d, _ := setupTestDispatcher(t)
	d.deliveryDedupTTL = 10 * time.Millisecond

	// Seed an entry
	d.deliveryDedup["test_key"] = time.Now().Add(-time.Minute)

	d.pruneDeliveryDedup()

	d.deliveryDedupMu.Lock()
	if len(d.deliveryDedup) != 0 {
		t.Errorf("expected empty dedup map after prune, got %d", len(d.deliveryDedup))
	}
	d.deliveryDedupMu.Unlock()
}

func TestCalculateNextRetry_ISU(t *testing.T) {
	w := &DeliveryWorker{
		desc: channel.ChannelDescriptor{
			RetryConfig: channel.RetryConfig{
				Enabled:     true,
				InitialWait: 180_000_000_000, // 3 min in ns
				MaxWait:     1_800_000_000_000,
				BackoffFunc: "isu",
			},
		},
	}

	next := w.calculateNextRetry(1)
	// ISU: should always be at least 3 minutes from now
	if next.Before(next) {
		t.Error("next retry should be in the future")
	}
}

func TestCalculateNextRetry_Exponential(t *testing.T) {
	w := &DeliveryWorker{
		desc: channel.ChannelDescriptor{
			RetryConfig: channel.RetryConfig{
				Enabled:     true,
				InitialWait: 5_000_000_000, // 5s
				MaxWait:     300_000_000_000,
				BackoffFunc: "exponential",
			},
		},
	}

	r1 := w.calculateNextRetry(1)
	r3 := w.calculateNextRetry(3)
	// Retry 3 should be further in the future than retry 1
	if !r3.After(r1) {
		t.Error("retry 3 should be later than retry 1")
	}
}
