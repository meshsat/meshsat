package api

import (
	"testing"
	"time"

	"meshsat/internal/engine"
	"meshsat/internal/transport"
)

// A node whose newest LoRa frame was undecryptable is flagged as living on
// another mesh; a node whose newest frame decoded is not, and carries the
// time of that frame. Nodes with no frame in the ring are left alone.
func TestAnnotateNodes_OtherMesh(t *testing.T) {
	now := time.Now().UTC()
	ring := []engine.PacketRecord{ // newest first, as PacketRing.Newest returns
		{Time: now, Bearer: "lora", Dir: "rx", From: "!6bcc53b2", PortNum: 0},
		{Time: now.Add(-time.Minute), Bearer: "lora", Dir: "rx", From: "!f0ccc009", PortNum: 1, Text: "hoi"},
		{Time: now.Add(-2 * time.Minute), Bearer: "lora", Dir: "rx", From: "!6bcc53b2", PortNum: 4},
	}
	nodes := []transport.MeshNode{{UserID: "!6bcc53b2"}, {UserID: "!f0ccc009"}, {UserID: "!deadbeef"}}
	v := annotateNodes(nodes, ring)
	if !v[0].OtherMesh {
		t.Fatalf("parallax radio: want other_mesh true (newest frame undecryptable)")
	}
	if v[0].LastReadableAt == "" {
		t.Fatalf("parallax radio: want last_readable_at from the older decoded frame")
	}
	if v[1].OtherMesh || v[1].LastReadableAt == "" {
		t.Fatalf("T-Echo: want other_mesh false with last_readable_at, got %+v", v[1])
	}
	if v[2].OtherMesh || v[2].LastReadableAt != "" {
		t.Fatalf("unknown node with no frames: want untouched, got %+v", v[2])
	}
}
