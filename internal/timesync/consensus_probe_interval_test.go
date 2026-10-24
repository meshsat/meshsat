package timesync

import (
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/engine"
)

// The probes on media hopping's real switch: a request sent in one enabled
// interval is no probe in the next, and dropping the records leaves the
// consensus as it was (R55-B1). [MESHSAT-1650]

func probeRig(t *testing.T) (*MeshTimeConsensus, *engine.HopSetManager) {
	t.Helper()
	links := []database.Interface{{ID: "mesh_0", ChannelType: "mesh", Enabled: true}}
	hs := engine.NewHopSetManager(engine.DefaultHopSetConfig(), func() ([]database.Interface, error) { return links, nil }, engine.HopWatch)
	mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{hash: [DestHashLen]byte{1}}, func([]byte) {})
	mc.SetReplyFunc(func(string, []byte) {})
	mc.SetInterfaces(func() []string { return []string{"mesh_0"} }, func(string, []byte) {})
	mc.FollowHopSet(hs)
	return mc, hs
}

func lastProbe(t *testing.T, hs *engine.HopSetManager) *time.Time {
	t.Helper()
	for _, l := range hs.Status().Links {
		if l.InterfaceID == "mesh_0" {
			return l.LastProbe
		}
	}
	t.Fatal("no mesh_0")
	return nil
}

func setModes(t *testing.T, hs *engine.HopSetManager, modes ...engine.HopMode) {
	t.Helper()
	for _, m := range modes {
		if _, err := hs.SetMode(m); err != nil {
			t.Fatal(err)
		}
	}
}

func sentRecords(mc *MeshTimeConsensus) int {
	mc.ifaceMu.Lock()
	defer mc.ifaceMu.Unlock()
	n := 0
	for _, st := range mc.ifaces {
		n += len(st.sent)
	}
	return n
}

// probe_of_an_ended_interval_not_taken: the request goes, the switch goes
// off and comes back, the answer arrives: no probe, and the consensus still
// measures the peer from it.
func TestProbeOfEndedIntervalNotTaken(t *testing.T) {
	mc, hs := probeRig(t)
	at := time.Now().Add(-200 * time.Millisecond)
	mc.sendRequestAt(at)
	if sentRecords(mc) != 1 {
		t.Fatalf("request not recorded for the probe (%d)", sentRecords(mc))
	}
	setModes(t, hs, engine.HopOff)
	if sentRecords(mc) != 0 {
		t.Fatalf("off kept %d request records", sentRecords(mc))
	}
	setModes(t, hs, engine.HopWatch)
	mc.HandleTimeSyncResponse(response([DestHashLen]byte{7}, at.UnixNano()), "mesh_0")
	if p := lastProbe(t, hs); p != nil {
		t.Fatalf("a request of the ended interval was taken as a probe at %v", p)
	}
	if len(mc.Peers()) != 1 {
		t.Fatalf("dropping the probe records changed the consensus: peers %+v", mc.Peers())
	}
	// Inside one interval the same exchange is a probe (the link made due
	// again at once).
	mc.ifaceMu.Lock()
	mc.ifaces["mesh_0"].lastSent = time.Time{}
	mc.ifaceMu.Unlock()
	at3 := time.Now()
	mc.sendRequestAt(at3)
	mc.HandleTimeSyncResponse(response([DestHashLen]byte{7}, at3.UnixNano()), "mesh_0")
	if p := lastProbe(t, hs); p == nil {
		t.Fatal("an answer in its own interval was no probe")
	}
}

// probe_record_of_an_ended_interval_not_taken_even_if_kept: a record that
// outlived the reset (made as the switch went off) is no probe in the next
// interval, and is dropped on reading.
func TestProbeRecordOfEndedIntervalNotTakenEvenIfKept(t *testing.T) {
	mc, hs := probeRig(t)
	at := time.Now().Add(-200 * time.Millisecond)
	mc.sendRequestAt(at)
	mc.ifaceMu.Lock()
	kept := map[int64]sentMark{}
	for k, v := range mc.ifaces["mesh_0"].sent {
		kept[k] = v
	}
	mc.ifaceMu.Unlock()
	setModes(t, hs, engine.HopOff, engine.HopOn)
	mc.ifaceMu.Lock()
	mc.ifaces["mesh_0"].sent = kept
	mc.ifaceMu.Unlock()
	mc.HandleTimeSyncResponse(response([DestHashLen]byte{7}, at.UnixNano()), "mesh_0")
	if p := lastProbe(t, hs); p != nil {
		t.Fatalf("a kept record of the ended interval was taken as a probe at %v", p)
	}
	if sentRecords(mc) != 0 {
		t.Fatalf("the stale record was not dropped on reading (%d left)", sentRecords(mc))
	}
}

// reset_leaves_the_schedule_and_the_consensus: ResetProbes drops the per-link
// records and nothing else.
func TestResetProbesLeavesTheSchedule(t *testing.T) {
	mc, hs := probeRig(t)
	at := time.Now()
	mc.sendRequestAt(at)
	mc.notePeer("mesh_0", at)
	mc.ifaceMu.Lock()
	before := *mc.ifaces["mesh_0"]
	mc.ifaceMu.Unlock()
	mc.pendingMu.Lock()
	pending := len(mc.pending)
	mc.pendingMu.Unlock()
	setModes(t, hs, engine.HopOff)
	mc.ifaceMu.Lock()
	after := *mc.ifaces["mesh_0"]
	mc.ifaceMu.Unlock()
	mc.pendingMu.Lock()
	pendingAfter := len(mc.pending)
	mc.pendingMu.Unlock()
	if !after.lastSent.Equal(before.lastSent) || !after.peerSeen.Equal(before.peerSeen) || pendingAfter != pending || pending == 0 {
		t.Fatalf("schedule %v/%v -> %v/%v, pending %d -> %d", before.lastSent, before.peerSeen, after.lastSent, after.peerSeen, pending, pendingAfter)
	}
	if len(after.sent) != 0 {
		t.Fatalf("records kept: %d", len(after.sent))
	}
}
