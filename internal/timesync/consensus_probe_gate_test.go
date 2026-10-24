package timesync

import (
	"testing"
	"time"
)

func sentOn(mc *MeshTimeConsensus, iface string) int {
	mc.ifaceMu.Lock()
	defer mc.ifaceMu.Unlock()
	if st := mc.ifaces[iface]; st != nil {
		return len(st.sent)
	}
	return 0
}

// probe_record_only_while_probing (R4): the requests sent per link are
// recorded only while something takes probes and media hopping is not off;
// otherwise the consensus runs exactly as before part two.
func TestProbeRecordOnlyWhileProbing(t *testing.T) {
	mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{hash: [DestHashLen]byte{1}}, func([]byte) {})
	mc.SetInterfaces(func() []string { return []string{"mesh_0"} }, func(string, []byte) {})
	now := time.Now()
	mc.sendRequestAt(now)
	if n := sentOn(mc, "mesh_0"); n != 0 {
		t.Fatalf("no probe taker: %d requests recorded", n)
	}
	probeGate(mc, false)
	mc.sendRequestAt(now.Add(DefaultDiscoveryInterval))
	if n := sentOn(mc, "mesh_0"); n != 0 {
		t.Fatalf("media hopping off: %d requests recorded", n)
	}
	probeGate(mc, true)
	mc.sendRequestAt(now.Add(2 * DefaultDiscoveryInterval))
	if n := sentOn(mc, "mesh_0"); n != 1 {
		t.Fatalf("probing: %d requests recorded, want 1", n)
	}
}
