package channel

import (
	"testing"
	"time"
)

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	d := ChannelDescriptor{
		ID:    "test",
		Label: "Test Channel",
	}

	if err := r.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := r.Get("test")
	if !ok {
		t.Fatal("Get returned false for registered channel")
	}
	if got.Label != "Test Channel" {
		t.Fatalf("Label = %q, want %q", got.Label, "Test Channel")
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Fatal("Get returned true for nonexistent channel")
	}
}

func TestRegistryDuplicatePrevention(t *testing.T) {
	r := NewRegistry()
	d := ChannelDescriptor{ID: "dup"}

	if err := r.Register(d); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(d); err == nil {
		t.Fatal("second Register should return error")
	}
}

func TestRegistryList(t *testing.T) {
	r := NewRegistry()
	r.Register(ChannelDescriptor{ID: "a", Label: "A"})
	r.Register(ChannelDescriptor{ID: "b", Label: "B"})
	r.Register(ChannelDescriptor{ID: "c", Label: "C"})

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List len = %d, want 3", len(list))
	}
	if list[0].ID != "a" || list[1].ID != "b" || list[2].ID != "c" {
		t.Fatalf("List order wrong: %v", list)
	}
}

func TestRegistryIDs(t *testing.T) {
	r := NewRegistry()
	r.Register(ChannelDescriptor{ID: "x"})
	r.Register(ChannelDescriptor{ID: "y"})

	ids := r.IDs()
	if len(ids) != 2 || ids[0] != "x" || ids[1] != "y" {
		t.Fatalf("IDs = %v, want [x y]", ids)
	}
}

func TestRegistryIsPaid(t *testing.T) {
	r := NewRegistry()
	r.Register(ChannelDescriptor{ID: "free", IsPaid: false})
	r.Register(ChannelDescriptor{ID: "paid", IsPaid: true})

	if r.IsPaid("free") {
		t.Fatal("free should not be paid")
	}
	if !r.IsPaid("paid") {
		t.Fatal("paid should be paid")
	}
	if r.IsPaid("nonexistent") {
		t.Fatal("nonexistent should not be paid")
	}
}

func TestRegistryCanSendCanReceive(t *testing.T) {
	r := NewRegistry()
	r.Register(ChannelDescriptor{ID: "both", CanSend: true, CanReceive: true})
	r.Register(ChannelDescriptor{ID: "send_only", CanSend: true, CanReceive: false})

	if !r.CanSend("both") {
		t.Fatal("both should CanSend")
	}
	if !r.CanReceive("both") {
		t.Fatal("both should CanReceive")
	}
	if r.CanReceive("send_only") {
		t.Fatal("send_only should not CanReceive")
	}
}

func TestRegisterDefaults(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	list := r.List()
	if len(list) != 6 {
		t.Fatalf("RegisterDefaults produced %d channels, want 6", len(list))
	}

	expectedIDs := []string{"mesh", "iridium", "astrocast", "cellular", "webhook", "mqtt"}
	ids := r.IDs()
	for i, want := range expectedIDs {
		if ids[i] != want {
			t.Fatalf("channel %d = %q, want %q", i, ids[i], want)
		}
	}

	// Verify paid channels
	if !r.IsPaid("iridium") {
		t.Fatal("iridium should be paid")
	}
	if !r.IsPaid("astrocast") {
		t.Fatal("astrocast should be paid")
	}
	if !r.IsPaid("cellular") {
		t.Fatal("cellular should be paid")
	}
	if r.IsPaid("mesh") {
		t.Fatal("mesh should not be paid")
	}

	// Verify iridium retry config
	ird, _ := r.Get("iridium")
	if ird.RetryConfig.BackoffFunc != "isu" {
		t.Fatalf("iridium backoff = %q, want isu", ird.RetryConfig.BackoffFunc)
	}
	if ird.RetryConfig.InitialWait != 180*time.Second {
		t.Fatalf("iridium initial wait = %v, want 180s", ird.RetryConfig.InitialWait)
	}

	// Verify max payload
	mesh, _ := r.Get("mesh")
	if mesh.MaxPayload != 237 {
		t.Fatalf("mesh MaxPayload = %d, want 237", mesh.MaxPayload)
	}
	wh, _ := r.Get("webhook")
	if wh.MaxPayload != 0 {
		t.Fatalf("webhook MaxPayload = %d, want 0 (unlimited)", wh.MaxPayload)
	}
}
