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
