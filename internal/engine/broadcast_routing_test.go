package engine

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// A routing broadcast reaches LoRa exactly once. main.go registers a packet
// sender for mesh_0 with the routing subsystem, and BroadcastRoutingPacket
// used to send on the mesh directly as well, so every time sync request went
// out twice: 240 LoRa packets an hour. Paid interfaces are never reached.
// [MESHSAT-778]
func TestBroadcastRoutingPacket_MeshOnceAndNeverPaid(t *testing.T) {
	tests := []struct {
		name         string
		registerMesh bool
		wantMeshRaw  int // SendRaw on the mesh transport itself
		wantSenders  map[string]int
		wantFreeList []string
	}{
		{
			name:         "mesh_0 has its own sender",
			registerMesh: true,
			wantMeshRaw:  0,
			wantSenders:  map[string]int{"mesh_0": 1, "tcp_0": 1, "iridium_0": 0, "cellular_0": 0, "sms_0": 0},
			wantFreeList: []string{"mesh_0", "tcp_0"},
		},
		{
			name:         "no mesh_0 sender, mesh reached directly",
			registerMesh: false,
			wantMeshRaw:  1,
			wantSenders:  map[string]int{"tcp_0": 1, "iridium_0": 0, "cellular_0": 0, "sms_0": 0},
			wantFreeList: []string{"mesh_0", "tcp_0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := database.New(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			meshTx := &mockMeshTransport{}
			p := NewProcessor(db, meshTx)

			var mu sync.Mutex
			got := map[string]int{}
			register := func(id string) {
				p.RegisterPacketSender(id, func(ctx context.Context, data []byte) error {
					mu.Lock()
					got[id]++
					mu.Unlock()
					return nil
				})
				got[id] = 0
			}
			if tt.registerMesh {
				register("mesh_0")
			}
			for _, id := range []string{"tcp_0", "iridium_0", "cellular_0", "sms_0"} {
				register(id)
			}

			p.BroadcastRoutingPacket([]byte{0x14, 1, 2, 3})

			meshTx.mu.Lock()
			raw := len(meshTx.raw)
			meshTx.mu.Unlock()
			if raw != tt.wantMeshRaw {
				t.Errorf("mesh SendRaw = %d, want %d", raw, tt.wantMeshRaw)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(got, tt.wantSenders) {
				t.Errorf("senders = %v, want %v", got, tt.wantSenders)
			}
			if onMesh := raw + got["mesh_0"]; onMesh != 1 {
				t.Errorf("packet reached LoRa %d times, want exactly 1", onMesh)
			}
			if free := p.FreeRoutingInterfaces(); !reflect.DeepEqual(free, tt.wantFreeList) {
				t.Errorf("FreeRoutingInterfaces = %v, want %v", free, tt.wantFreeList)
			}
		})
	}
}

var _ transport.MeshTransport = (*mockMeshTransport)(nil)
