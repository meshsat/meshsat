package timesync

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

type fakeIdentity struct{ hash [DestHashLen]byte }

func (f fakeIdentity) DestHash() [DestHashLen]byte { return f.hash }

func buildRequest(sender [DestHashLen]byte, ts int64) []byte {
	pkt := make([]byte, timeSyncReqLen)
	pkt[0] = PacketTimeSyncReq
	copy(pkt[1:17], sender[:])
	binary.LittleEndian.PutUint64(pkt[17:25], uint64(ts))
	pkt[25] = 3
	return pkt
}

// A request that reaches us over two links (both TCP connections between
// the kits) is answered once, on the interface it first arrived on; a
// request without a source interface falls back to the broadcast sender.
// [MESHSAT-778]
func TestHandleTimeSyncRequest_DedupAndReplyToSource(t *testing.T) {
	var mu sync.Mutex
	var broadcast [][]byte
	replies := map[string]int{}
	local := fakeIdentity{hash: [DestHashLen]byte{1}}
	mc := NewMeshTimeConsensus(NewTimeService(nil), local, func(data []byte) {
		mu.Lock()
		broadcast = append(broadcast, data)
		mu.Unlock()
	})
	mc.SetReplyFunc(func(iface string, data []byte) {
		mu.Lock()
		replies[iface]++
		mu.Unlock()
		if len(data) != timeSyncRespLen || data[0] != PacketTimeSyncResp {
			t.Errorf("bad response frame: %v", data)
		}
	})

	peer := [DestHashLen]byte{2}
	req := buildRequest(peer, time.Now().UnixNano())
	mc.HandleTimeSyncRequest(req, "tcp_0")
	mc.HandleTimeSyncRequest(req, "tcp_0") // second copy over the other TCP link
	mc.HandleTimeSyncRequest(req, "ax25_0")

	mu.Lock()
	if replies["tcp_0"] != 1 || replies["ax25_0"] != 0 || len(broadcast) != 0 {
		mu.Unlock()
		t.Fatalf("replies %v broadcast %d, want one reply on tcp_0 only", replies, len(broadcast))
	}
	mu.Unlock()

	// A fresh request from the same peer is answered again.
	req2 := buildRequest(peer, time.Now().UnixNano()+1)
	mc.HandleTimeSyncRequest(req2, "ax25_0")
	// Our own request echoed back is ignored.
	mc.HandleTimeSyncRequest(buildRequest(local.hash, 42), "tcp_0")
	// No source interface: broadcast fallback.
	mc.HandleTimeSyncRequest(buildRequest(peer, 7), "")
	mu.Lock()
	defer mu.Unlock()
	if replies["ax25_0"] != 1 || len(broadcast) != 1 {
		t.Fatalf("replies %v broadcast %d after second round", replies, len(broadcast))
	}
}

// The first request is delayed by a random offset inside one period so two
// bridges restarted a multiple of the period apart do not beacon in lockstep.
func TestNewMeshTimeConsensus_StartOffsetWithinPeriod(t *testing.T) {
	for range 20 {
		mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{}, func([]byte) {})
		if mc.startOffset < 0 || mc.startOffset >= requestInterval {
			t.Fatalf("start offset %s outside [0, %s)", mc.startOffset, requestInterval)
		}
	}
}

// A kit whose own clock could not be established must not offer itself as a
// time reference: it still answers, so the peer gets its echo and its
// round-trip measurement, but with the unsynchronised stratum. [MESHSAT-1056]
func TestHandleTimeSyncRequest_UntrustedClockAnswersUnsynchronised(t *testing.T) {
	tests := []struct {
		name        string
		trusted     func() bool
		wantStratum byte
	}{
		{"no trust function behaves as before", nil, 5},
		{"clock trusted", func() bool { return true }, 5},
		{"clock not trusted", func() bool { return false }, StratumUnsynchronised},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []byte
			mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{hash: [DestHashLen]byte{1}}, func(data []byte) {
				got = data
			})
			if tt.trusted != nil {
				mc.SetClockTrustFn(tt.trusted)
			}

			mc.HandleTimeSyncRequest(buildRequest([DestHashLen]byte{2}, time.Now().UnixNano()), "")

			if len(got) != timeSyncRespLen {
				t.Fatalf("no response sent (%d bytes)", len(got))
			}
			if got[25] != tt.wantStratum {
				t.Errorf("stratum = %d, want %d", got[25], tt.wantStratum)
			}
			// The echo must survive either way, or the peer cannot match the
			// response to its request.
			if binary.LittleEndian.Uint64(got[26:34]) == 0 {
				t.Error("echo timestamp missing from the response")
			}
		})
	}
}

// An unsynchronised peer is discarded outright rather than down-weighted:
// at 1/(stratum+1) a kit eight hours out would still drag the weighted
// average by a large fraction of its own error. [MESHSAT-1056]
func TestRecalculateConsensus_SkipsUnsynchronisedPeers(t *testing.T) {
	ts := NewTimeService(nil)
	mc := NewMeshTimeConsensus(ts, fakeIdentity{hash: [DestHashLen]byte{1}}, func([]byte) {})

	now := time.Now()
	good := int64(2 * time.Millisecond)
	bad := int64(8 * time.Hour)
	mc.peers[[DestHashLen]byte{2}] = &peerClock{stratum: 3, offsetEWMA: float64(good), lastSeen: now, sampleCount: 4}
	mc.peers[[DestHashLen]byte{3}] = &peerClock{stratum: StratumUnsynchronised, offsetEWMA: float64(bad), lastSeen: now, sampleCount: 4}

	mc.recalculateConsensus()

	off := ts.Offset()
	if off > int64(time.Second) {
		t.Fatalf("offset %v was dragged by the unsynchronised peer", time.Duration(off))
	}
	if got := ts.Stratum(); got != 4 {
		t.Errorf("stratum = %d, want 4 (the good peer at 3, plus one)", got)
	}
}

// The request schedule per interface: at once on an interface never sent on,
// every requestInterval while a peer spoke within peerTTL, every discovery
// period otherwise, with half a request period of slack for ticker jitter.
// [MESHSAT-778]
func TestDueInterfaces_Schedule(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		peerAgo  time.Duration // 0 = never
		sentAgo  time.Duration // 0 = never
		wantDue  bool
		wantMode string
	}{
		{"never sent, no peer: first discovery goes out", 0, 0, true, "discovery"},
		{"no peer, sent 30 s ago", 0, 30 * time.Second, false, "discovery"},
		{"no peer, sent 5 min ago", 0, 5 * time.Minute, false, "discovery"},
		{"no peer, sent 9 min ago", 0, 9 * time.Minute, false, "discovery"},
		{"no peer, sent 9 min 45 s ago (slack)", 0, 9*time.Minute + 45*time.Second, true, "discovery"},
		{"no peer, sent 10 min ago", 0, 10 * time.Minute, true, "discovery"},
		{"peer 1 min ago, sent 10 s ago", time.Minute, 10 * time.Second, false, "peer"},
		{"peer 1 min ago, sent 29 s ago (jitter)", time.Minute, 29 * time.Second, true, "peer"},
		{"peer 1 min ago, sent 30 s ago", time.Minute, 30 * time.Second, true, "peer"},
		{"peer 9 min 30 s ago, still within the window", 9*time.Minute + 30*time.Second, 30 * time.Second, true, "peer"},
		{"peer 11 min ago, back to discovery", 11 * time.Minute, time.Minute, false, "discovery"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{}, func([]byte) {})
			st := &ifaceSchedule{}
			if tt.peerAgo > 0 {
				st.peerSeen = now.Add(-tt.peerAgo)
			}
			if tt.sentAgo > 0 {
				st.lastSent = now.Add(-tt.sentAgo)
			}
			mc.ifaces["mesh_0"] = st
			mc.SetInterfaces(func() []string { return []string{"mesh_0"} }, func(string, []byte) {})

			got := mc.dueInterfaces([]string{"mesh_0"}, now)
			if due := len(got) == 1; due != tt.wantDue {
				t.Errorf("due = %v, want %v", due, tt.wantDue)
			}
			if tt.wantDue && !mc.ifaces["mesh_0"].lastSent.Equal(now) {
				t.Error("a due interface must be marked sent")
			}
			states := mc.Interfaces()
			if len(states) != 1 || states[0].Mode != tt.wantMode {
				t.Errorf("Interfaces() = %+v, want mode %q", states, tt.wantMode)
			}
		})
	}
}

type ifaceSends struct {
	mu sync.Mutex
	n  map[string]int
}

func (s *ifaceSends) send(iface string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(data) != timeSyncReqLen || data[0] != PacketTimeSyncReq {
		panic("not a time sync request")
	}
	s.n[iface]++
}

func (s *ifaceSends) count(iface string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n[iface]
}

// Over an hour with nobody answering, the mesh carries one discovery request
// per period instead of one every 30 s (twice, before the fix); an interface
// where another bridge asks goes to the full rate at the next tick while the
// others stay quiet, and drops back to discovery once the peer falls silent.
// [MESHSAT-778]
func TestSendRequest_PerInterfaceGating(t *testing.T) {
	ifaces := []string{"mesh_0", "tcp_0", "zigbee_0"}
	sends := &ifaceSends{n: map[string]int{}}
	var broadcast int
	mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{hash: [DestHashLen]byte{1}}, func([]byte) { broadcast++ })
	mc.SetReplyFunc(func(string, []byte) {})
	mc.SetInterfaces(func() []string { return ifaces }, sends.send)

	// One hour of 30 s ticks, no peer anywhere.
	t0 := time.Now().Add(-2 * time.Hour)
	for tick := time.Duration(0); tick < time.Hour; tick += requestInterval {
		mc.sendRequestAt(t0.Add(tick))
	}
	for _, id := range ifaces {
		if got := sends.count(id); got != 6 {
			t.Errorf("%s: %d requests in an hour without a peer, want 6 (one per 10 min)", id, got)
		}
	}
	if broadcast != 0 {
		t.Errorf("broadcast sender used %d times with per-interface sends set", broadcast)
	}

	// Another bridge asks on tcp_0 now; the next tick sends there only.
	now := time.Now()
	mc.HandleTimeSyncRequest(buildRequest([DestHashLen]byte{2}, now.UnixNano()), "tcp_0")
	before := map[string]int{}
	for _, id := range ifaces {
		before[id] = sends.count(id)
	}
	for tick := requestInterval; tick <= 5*time.Minute; tick += requestInterval {
		mc.sendRequestAt(now.Add(tick))
	}
	if got := sends.count("tcp_0") - before["tcp_0"]; got != 10 {
		t.Errorf("tcp_0 with a peer: %d requests in 5 min, want 10", got)
	}
	for _, id := range []string{"mesh_0", "zigbee_0"} {
		if got := sends.count(id) - before[id]; got != 1 {
			t.Errorf("%s without a peer: %d requests in 5 min, want the 1 discovery that fell due", id, got)
		}
	}

	// The peer falls silent: past peerTTL tcp_0 is back on the discovery
	// period, three requests in the next 30 min instead of sixty.
	late := now.Add(peerTTL + time.Minute)
	n := sends.count("tcp_0")
	for tick := time.Duration(0); tick < 30*time.Minute; tick += requestInterval {
		mc.sendRequestAt(late.Add(tick))
	}
	if got := sends.count("tcp_0") - n; got != 3 {
		t.Errorf("tcp_0 after the peer went silent: %d requests in 30 min, want 3", got)
	}
}

// Without per-interface senders the request is broadcast every tick, as
// before; a tick with nothing due sends nothing and leaves no pending entry.
func TestSendRequest_BroadcastFallbackAndNoPendingWhenIdle(t *testing.T) {
	var broadcast int
	mc := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{}, func([]byte) { broadcast++ })
	now := time.Now()
	mc.sendRequestAt(now)
	mc.sendRequestAt(now.Add(requestInterval))
	if broadcast != 2 {
		t.Fatalf("broadcast %d, want 2", broadcast)
	}

	idle := NewMeshTimeConsensus(NewTimeService(nil), fakeIdentity{}, func([]byte) {})
	idle.SetInterfaces(func() []string { return []string{"mesh_0"} }, func(string, []byte) {})
	idle.sendRequestAt(now)                  // first discovery
	idle.sendRequestAt(now.Add(time.Minute)) // nothing due
	idle.pendingMu.Lock()
	defer idle.pendingMu.Unlock()
	if len(idle.pending) != 1 {
		t.Errorf("pending = %d, want 1 (no entry for a tick that sent nothing)", len(idle.pending))
	}
}

// A response marks a peer on the interface it came in on, and records the
// round trip and interface for GET /api/timesync/peers; a response carrying
// our own hash marks nothing. [MESHSAT-778]
func TestHandleTimeSyncResponse_MarksPeerInterface(t *testing.T) {
	local := fakeIdentity{hash: [DestHashLen]byte{1}}
	mc := NewMeshTimeConsensus(NewTimeService(nil), local, func([]byte) {})
	mc.SetInterfaces(func() []string { return []string{"ax25_0", "mesh_0"} }, func(string, []byte) {})

	sent := time.Now().Add(-120 * time.Millisecond)
	mc.pending[sent.UnixNano()] = sent
	resp := func(from [DestHashLen]byte) []byte {
		b := make([]byte, timeSyncRespLen)
		b[0] = PacketTimeSyncResp
		copy(b[1:17], from[:])
		binary.LittleEndian.PutUint64(b[17:25], uint64(time.Now().UnixNano()))
		b[25] = 2
		binary.LittleEndian.PutUint64(b[26:34], uint64(sent.UnixNano()))
		return b
	}

	mc.HandleTimeSyncResponse(resp(local.hash), "mesh_0")
	mc.HandleTimeSyncResponse(resp([DestHashLen]byte{9}), "ax25_0")

	modes := map[string]string{}
	for _, s := range mc.Interfaces() {
		modes[s.Iface] = s.Mode
	}
	if modes["ax25_0"] != "peer" || modes["mesh_0"] != "discovery" {
		t.Errorf("modes = %v, want ax25_0 peer and mesh_0 discovery", modes)
	}
	peers := mc.Peers()
	if len(peers) != 1 || peers[0].Iface != "ax25_0" || peers[0].LastRTTMs < 100 || peers[0].Stratum != 2 {
		t.Errorf("Peers() = %+v, want one peer on ax25_0 with a ~120 ms round trip at stratum 2", peers)
	}
}
