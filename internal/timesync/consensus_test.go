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
