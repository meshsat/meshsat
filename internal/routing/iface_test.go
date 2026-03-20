package routing

import (
	"context"
	"errors"
	"testing"
)

func TestReticulumInterface_Send(t *testing.T) {
	var sent []byte
	iface := NewReticulumInterface("mesh_0", "mesh", 230, func(ctx context.Context, packet []byte) error {
		sent = make([]byte, len(packet))
		copy(sent, packet)
		return nil
	})

	data := []byte("hello")
	if err := iface.Send(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if string(sent) != "hello" {
		t.Errorf("sent: got %q, want %q", sent, "hello")
	}
}

func TestReticulumInterface_MTUExceeded(t *testing.T) {
	iface := NewReticulumInterface("mesh_0", "mesh", 10, func(ctx context.Context, packet []byte) error {
		return nil
	})

	err := iface.Send(context.Background(), make([]byte, 20))
	if err == nil {
		t.Fatal("should reject packet exceeding MTU")
	}
}

func TestReticulumInterface_Offline(t *testing.T) {
	iface := NewReticulumInterface("mesh_0", "mesh", 230, func(ctx context.Context, packet []byte) error {
		return nil
	})
	iface.SetOnline(false)

	err := iface.Send(context.Background(), []byte("test"))
	if err == nil {
		t.Fatal("should reject send on offline interface")
	}
}

func TestReticulumInterface_Cost(t *testing.T) {
	mesh := NewReticulumInterface("mesh_0", "mesh", 230, nil)
	if mesh.Cost() != 0 {
		t.Errorf("mesh cost: got %f, want 0", mesh.Cost())
	}

	iridium := NewReticulumInterface("iridium_0", "iridium", 340, nil)
	if iridium.Cost() != 0.05 {
		t.Errorf("iridium cost: got %f, want 0.05", iridium.Cost())
	}
}

func TestInterfaceRegistry_RegisterAndGet(t *testing.T) {
	reg := NewInterfaceRegistry()

	iface := NewReticulumInterface("mesh_0", "mesh", 230, func(ctx context.Context, packet []byte) error { return nil })
	reg.Register(iface)

	got := reg.Get("mesh_0")
	if got == nil {
		t.Fatal("should find registered interface")
	}
	if got.ID() != "mesh_0" {
		t.Errorf("ID: got %q, want %q", got.ID(), "mesh_0")
	}

	if reg.Get("unknown") != nil {
		t.Error("should return nil for unknown interface")
	}
}

func TestInterfaceRegistry_Send(t *testing.T) {
	reg := NewInterfaceRegistry()

	var sent bool
	reg.Register(NewReticulumInterface("mesh_0", "mesh", 230, func(ctx context.Context, packet []byte) error {
		sent = true
		return nil
	}))

	if err := reg.Send("mesh_0", []byte("test")); err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Error("should have called send function")
	}

	if err := reg.Send("unknown", []byte("test")); err == nil {
		t.Fatal("should error for unknown interface")
	}
}

func TestInterfaceRegistry_SendError(t *testing.T) {
	reg := NewInterfaceRegistry()
	reg.Register(NewReticulumInterface("fail_0", "mesh", 230, func(ctx context.Context, packet []byte) error {
		return errors.New("transport error")
	}))

	err := reg.Send("fail_0", []byte("test"))
	if err == nil {
		t.Fatal("should propagate send error")
	}
}

func TestInterfaceRegistry_OnlineIDs(t *testing.T) {
	reg := NewInterfaceRegistry()
	reg.Register(NewReticulumInterface("mesh_0", "mesh", 230, nil))
	iface2 := NewReticulumInterface("iridium_0", "iridium", 340, nil)
	iface2.SetOnline(false)
	reg.Register(iface2)

	ids := reg.OnlineIDs()
	if len(ids) != 1 {
		t.Fatalf("online IDs: got %d, want 1", len(ids))
	}
	if ids[0] != "mesh_0" {
		t.Errorf("online ID: got %q, want %q", ids[0], "mesh_0")
	}
}
