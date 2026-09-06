package transport

import "testing"

// The packet feed reads the local node id and the per-node RSSI without
// I/O through two small optional interfaces. [MESHSAT-826]
func TestDirectMeshTransport_PacketFeedProviders(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null-mesh")
	var _ LocalNodeProvider = tr
	var _ NodeRSSIProvider = tr

	if got := tr.LocalNodeID(); got != "" {
		t.Errorf("before config download LocalNodeID = %q, want empty", got)
	}
	if _, ok := tr.NodeRSSI(0x11223344); ok {
		t.Error("unknown node must report no RSSI")
	}

	tr.mu.Lock()
	tr.myNodeNum = 0x0a0b0c0d
	tr.mu.Unlock()
	tr.nodesMu.Lock()
	tr.nodes[0x11223344] = &MeshNode{Num: 0x11223344, RSSI: -77}
	tr.nodesMu.Unlock()

	if got := tr.LocalNodeID(); got != "!0a0b0c0d" {
		t.Errorf("LocalNodeID = %q", got)
	}
	rssi, ok := tr.NodeRSSI(0x11223344)
	if !ok || rssi != -77 {
		t.Errorf("NodeRSSI = %d, %v", rssi, ok)
	}
}
