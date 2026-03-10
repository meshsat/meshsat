package engine

import (
	"testing"

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
