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

	engine := &rules.Engine{}
	d := NewDispatcher(db, engine, reg, nil, nil)
	return d, db
}

func TestDispatch_CreatesDeliveries(t *testing.T) {
	d, db := setupTestDispatcher(t)

	// Insert a rule
	rule := &database.ForwardingRule{
		Name:       "Test",
		Enabled:    true,
		Priority:   1,
		SourceType: "any",
		DestType:   "mqtt",
	}
	if _, err := db.InsertForwardingRule(rule); err != nil {
		t.Fatal(err)
	}

	// Create a real rules engine with the rule loaded
	re := rules.NewEngine(db)
	if err := re.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.rules = re

	msg := rules.RouteMessage{Text: "hello", From: "!12345678", PortNum: 1}
	count := d.Dispatch("mesh", msg, []byte("hello"))
	if count != 1 {
		t.Fatalf("expected 1 delivery, got %d", count)
	}

	// Verify delivery was persisted
	deliveries, err := db.GetPendingDeliveries("mqtt", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 pending delivery, got %d", len(deliveries))
	}
	if deliveries[0].Channel != "mqtt" {
		t.Errorf("expected channel mqtt, got %s", deliveries[0].Channel)
	}
	if deliveries[0].Status != "queued" {
		t.Errorf("expected status queued, got %s", deliveries[0].Status)
	}
}

func TestDispatch_MultiMatch(t *testing.T) {
	d, db := setupTestDispatcher(t)

	// Two rules, different destinations
	for _, dest := range []string{"mqtt", "iridium"} {
		r := &database.ForwardingRule{
			Name: "To " + dest, Enabled: true, Priority: 1,
			SourceType: "any", DestType: dest,
		}
		if _, err := db.InsertForwardingRule(r); err != nil {
			t.Fatal(err)
		}
	}

	re := rules.NewEngine(db)
	if err := re.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.rules = re

	count := d.Dispatch("mesh", rules.RouteMessage{Text: "test", PortNum: 1}, nil)
	if count != 2 {
		t.Fatalf("expected 2 deliveries, got %d", count)
	}
}

func TestDispatch_SelfLoopPrevented(t *testing.T) {
	d, db := setupTestDispatcher(t)

	r := &database.ForwardingRule{
		Name: "Loop", Enabled: true, Priority: 1,
		SourceType: "any", DestType: "mqtt",
	}
	if _, err := db.InsertForwardingRule(r); err != nil {
		t.Fatal(err)
	}

	re := rules.NewEngine(db)
	if err := re.ReloadFromDB(); err != nil {
		t.Fatal(err)
	}
	d.rules = re

	// Source is mqtt, dest is mqtt — should be prevented
	count := d.Dispatch("mqtt", rules.RouteMessage{Text: "test"}, nil)
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
