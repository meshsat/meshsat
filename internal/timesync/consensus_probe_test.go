package timesync

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

// response builds a peer's answer to the request stamped echo.
func response(from [DestHashLen]byte, echo int64) []byte {
	b := make([]byte, timeSyncRespLen)
	b[0] = PacketTimeSyncResp
	copy(b[1:17], from[:])
	binary.LittleEndian.PutUint64(b[17:25], uint64(time.Now().UnixNano()))
	b[25] = 2
	binary.LittleEndian.PutUint64(b[26:34], uint64(echo))
	return b
}

type probes struct {
	mu sync.Mutex
	n  map[string]int
}

func (p *probes) add(iface string) {
	p.mu.Lock()
	p.n[iface]++
	p.mu.Unlock()
}

func (p *probes) count(iface string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n[iface]
}

// probe_only_for_an_answer_on_the_link_it_went_out_on (media hopping part
// two): the scheduled request is the probe, so a link's probe is an answer
// arriving on the link the request was sent on. An answer on another link,
// a second answer to the same request on one link, an answer to a request
// we never sent, and our own echo are no probe. The consensus is unchanged:
// the first answer still measures the peer. [MESHSAT-1650]
func TestProbeOnlyForAnAnswerOnTheLinkItWentOutOn(t *testing.T) {
	local := fakeIdentity{hash: [DestHashLen]byte{1}}
	peer := [DestHashLen]byte{7}
	mc := NewMeshTimeConsensus(NewTimeService(nil), local, func([]byte) {})
	mc.SetReplyFunc(func(string, []byte) {})
	mc.SetInterfaces(func() []string { return []string{"mesh_0", "aprs_0"} }, func(string, []byte) {})
	got := &probes{n: map[string]int{}}
	mc.SetProbeFunc(got.add)

	now := time.Now().Add(-200 * time.Millisecond)
	mc.sendRequestAt(now) // first discovery: both links
	stamp := now.UnixNano()

	mc.HandleTimeSyncResponse(response(peer, stamp), "mesh_0")
	if got.count("mesh_0") != 1 {
		t.Fatalf("answer on mesh_0: %d probes, want 1", got.count("mesh_0"))
	}
	if len(mc.Peers()) != 1 {
		t.Fatalf("the consensus no longer measures the peer: %+v", mc.Peers())
	}
	mc.HandleTimeSyncResponse(response(peer, stamp), "mesh_0")
	if got.count("mesh_0") != 1 {
		t.Errorf("a second answer to one request on mesh_0 counted again: %d", got.count("mesh_0"))
	}
	mc.HandleTimeSyncResponse(response(peer, stamp), "tcp_0")
	if got.count("tcp_0") != 0 {
		t.Errorf("an answer on tcp_0, where the request never went, is a probe")
	}
	mc.HandleTimeSyncResponse(response(peer, stamp+1), "aprs_0")
	if got.count("aprs_0") != 0 {
		t.Errorf("an answer to a request never sent is a probe on aprs_0")
	}
	mc.HandleTimeSyncResponse(response(local.hash, stamp), "aprs_0")
	if got.count("aprs_0") != 0 {
		t.Errorf("our own echo is a probe")
	}
	// The same request was sent on aprs_0 too, and the pending entry is
	// gone (the mesh answer took it): an answer there is still aprs_0's probe.
	mc.HandleTimeSyncResponse(response(peer, stamp), "aprs_0")
	if got.count("aprs_0") != 1 {
		t.Errorf("answer on aprs_0 after the consensus took the request: %d probes, want 1", got.count("aprs_0"))
	}
}

// A broadcast request names no link, so its answers are no probe.
func TestProbeNeverForABroadcastRequest(t *testing.T) {
	mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{hash: [DestHashLen]byte{1}}, func([]byte) {})
	got := &probes{n: map[string]int{}}
	mc.SetProbeFunc(got.add)
	now := time.Now()
	mc.sendRequestAt(now)
	mc.HandleTimeSyncResponse(response([DestHashLen]byte{7}, now.UnixNano()), "mesh_0")
	if got.count("mesh_0") != 0 {
		t.Fatalf("a broadcast request's answer is a probe")
	}
}

// The per-link record ages out with the pending requests.
func TestProbeRecordIsPruned(t *testing.T) {
	mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{hash: [DestHashLen]byte{1}}, func([]byte) {})
	mc.SetInterfaces(func() []string { return []string{"mesh_0"} }, func(string, []byte) {})
	got := &probes{n: map[string]int{}}
	mc.SetProbeFunc(got.add)
	old := time.Now().Add(-5 * time.Minute)
	mc.sendRequestAt(old)
	mc.pruneSent(time.Now().Add(-2 * time.Minute))
	mc.HandleTimeSyncResponse(response([DestHashLen]byte{7}, old.UnixNano()), "mesh_0")
	if got.count("mesh_0") != 0 {
		t.Fatalf("an answer to a request older than the prune is a probe")
	}
}
