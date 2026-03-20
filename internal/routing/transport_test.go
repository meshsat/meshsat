package routing

import (
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

func TestTransportNode_ProcessAnnounce(t *testing.T) {
	id := testIdentity(t)
	tn := NewTransportNode(id, 30*time.Minute, nil)

	remote := testIdentity(t)
	ann, _ := NewAnnounce(remote, []byte("remote-node"))

	if !tn.ProcessAnnounce(ann, "mesh_0") {
		t.Fatal("should accept new announce")
	}
	if tn.RouteCount() != 1 {
		t.Fatalf("route count: got %d, want 1", tn.RouteCount())
	}

	// Same announce again — should refresh
	if !tn.ProcessAnnounce(ann, "mesh_0") {
		t.Fatal("should accept refresh announce")
	}

	// Check best interface
	best := tn.BestInterface(remote.DestHash())
	if best != "mesh_0" {
		t.Errorf("best interface: got %q, want %q", best, "mesh_0")
	}
}

func TestTransportNode_ForwardPacket(t *testing.T) {
	id := testIdentity(t)
	remote := testIdentity(t)

	var mu sync.Mutex
	var sentIface string
	var sentPacket []byte

	sendFn := func(ifaceID string, packet []byte) error {
		mu.Lock()
		sentIface = ifaceID
		sentPacket = make([]byte, len(packet))
		copy(sentPacket, packet)
		mu.Unlock()
		return nil
	}

	tn := NewTransportNode(id, 30*time.Minute, sendFn)
	tn.Enable()

	// Add route: remote reachable via iridium_0
	ann, _ := NewAnnounce(remote, nil)
	tn.ProcessAnnounce(ann, "iridium_0")

	// Create a data packet addressed to remote
	hdr := &reticulum.Header{
		HeaderType:    reticulum.HeaderType1,
		TransportType: reticulum.TransportBroadcast,
		DestType:      reticulum.DestSingle,
		PacketType:    reticulum.PacketData,
		Hops:          0,
		DestHash:      remote.DestHash(),
		Context:       reticulum.ContextNone,
		Data:          []byte("hello"),
	}
	packet := hdr.Marshal()

	// Forward from mesh_0 → should go to iridium_0
	if !tn.ForwardPacket(packet, "mesh_0") {
		t.Fatal("should forward packet")
	}

	mu.Lock()
	defer mu.Unlock()
	if sentIface != "iridium_0" {
		t.Errorf("forwarded to %q, want %q", sentIface, "iridium_0")
	}

	// Verify the forwarded packet has HEADER_2 with our transport ID
	fwdHdr, err := reticulum.UnmarshalHeader(sentPacket)
	if err != nil {
		t.Fatal(err)
	}
	if fwdHdr.HeaderType != reticulum.HeaderType2 {
		t.Error("forwarded packet should have HEADER_2")
	}
	if fwdHdr.TransportID != id.DestHash() {
		t.Error("transport ID should be our dest hash")
	}
	if fwdHdr.DestHash != remote.DestHash() {
		t.Error("dest hash should be preserved")
	}
	if fwdHdr.Hops != 1 {
		t.Errorf("hops: got %d, want 1", fwdHdr.Hops)
	}
}

func TestTransportNode_NoForwardToSameInterface(t *testing.T) {
	id := testIdentity(t)
	remote := testIdentity(t)

	tn := NewTransportNode(id, 30*time.Minute, func(string, []byte) error { return nil })
	tn.Enable()

	ann, _ := NewAnnounce(remote, nil)
	tn.ProcessAnnounce(ann, "mesh_0")

	hdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestHash:   remote.DestHash(),
	}

	// Arriving on mesh_0, route says mesh_0 — should NOT forward
	if tn.ForwardPacket(hdr.Marshal(), "mesh_0") {
		t.Fatal("should not forward back to same interface")
	}
}

func TestTransportNode_NoForwardToSelf(t *testing.T) {
	id := testIdentity(t)

	tn := NewTransportNode(id, 30*time.Minute, func(string, []byte) error { return nil })
	tn.Enable()

	hdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestHash:   id.DestHash(), // addressed to us
	}

	if tn.ForwardPacket(hdr.Marshal(), "mesh_0") {
		t.Fatal("should not forward packets addressed to ourselves")
	}
}

func TestTransportNode_NoForwardAnnounces(t *testing.T) {
	id := testIdentity(t)
	remote := testIdentity(t)

	tn := NewTransportNode(id, 30*time.Minute, func(string, []byte) error { return nil })
	tn.Enable()

	ann, _ := NewAnnounce(remote, nil)
	tn.ProcessAnnounce(ann, "iridium_0")

	// Create an announce packet
	hdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketAnnounce,
		DestHash:   remote.DestHash(),
	}

	if tn.ForwardPacket(hdr.Marshal(), "mesh_0") {
		t.Fatal("should not forward announce packets (handled by AnnounceRelay)")
	}
}

func TestTransportNode_Disabled(t *testing.T) {
	id := testIdentity(t)
	remote := testIdentity(t)

	tn := NewTransportNode(id, 30*time.Minute, func(string, []byte) error { return nil })
	// NOT enabled

	ann, _ := NewAnnounce(remote, nil)
	tn.ProcessAnnounce(ann, "iridium_0")

	hdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestHash:   remote.DestHash(),
	}

	if tn.ForwardPacket(hdr.Marshal(), "mesh_0") {
		t.Fatal("disabled transport node should not forward")
	}
}

func TestTransportNode_NoRoute(t *testing.T) {
	id := testIdentity(t)
	remote := testIdentity(t)

	tn := NewTransportNode(id, 30*time.Minute, func(string, []byte) error { return nil })
	tn.Enable()

	// No announce processed — no route
	hdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestHash:   remote.DestHash(),
	}

	if tn.ForwardPacket(hdr.Marshal(), "mesh_0") {
		t.Fatal("should not forward with no route")
	}
}

func TestTransportNode_MaxHops(t *testing.T) {
	id := testIdentity(t)
	remote := testIdentity(t)

	tn := NewTransportNode(id, 30*time.Minute, func(string, []byte) error { return nil })
	tn.Enable()

	ann, _ := NewAnnounce(remote, nil)
	tn.ProcessAnnounce(ann, "iridium_0")

	hdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		Hops:       reticulum.PathfinderM, // already at max
		DestHash:   remote.DestHash(),
	}

	if tn.ForwardPacket(hdr.Marshal(), "mesh_0") {
		t.Fatal("should not forward at max hops")
	}
}
