package timesync

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func TestTimeService_Now_ZeroOffset(t *testing.T) {
	ts := NewTimeService(nil)

	before := time.Now()
	corrected := ts.Now()
	after := time.Now()

	if corrected.Before(before) || corrected.After(after) {
		t.Fatalf("Now() with zero offset should be ~time.Now(): got %v, window [%v, %v]",
			corrected, before, after)
	}
}

func TestTimeService_ApplyCorrection(t *testing.T) {
	ts := NewTimeService(nil)

	// Apply a +500ms correction from GPS (stratum 1).
	ts.ApplyCorrection("gps", 1, 500_000_000, 100_000_000)

	if ts.Stratum() != 1 {
		t.Fatalf("expected stratum 1, got %d", ts.Stratum())
	}
	if ts.Offset() != 500_000_000 {
		t.Fatalf("expected offset 500ms, got %dns", ts.Offset())
	}

	// Now() should be ~500ms ahead of time.Now().
	corrected := ts.Now()
	raw := time.Now()
	diff := corrected.Sub(raw)
	if diff < 450*time.Millisecond || diff > 550*time.Millisecond {
		t.Fatalf("expected ~500ms offset, got %v", diff)
	}
}

func TestTimeService_StratumPriority(t *testing.T) {
	ts := NewTimeService(nil)

	// Apply stratum 2 correction.
	ts.ApplyCorrection("hub_ntp", 2, 100_000_000, 500_000_000)
	if ts.Stratum() != 2 {
		t.Fatalf("expected stratum 2, got %d", ts.Stratum())
	}

	// Stratum 1 should override.
	ts.ApplyCorrection("gps", 1, 200_000_000, 100_000_000)
	if ts.Stratum() != 1 {
		t.Fatalf("expected stratum 1 after GPS, got %d", ts.Stratum())
	}
	if ts.Offset() != 200_000_000 {
		t.Fatalf("expected 200ms offset, got %dns", ts.Offset())
	}

	// Stratum 3 should NOT override stratum 1.
	ts.ApplyCorrection("mesh_peer", 3, 999_000_000, 1_000_000_000)
	if ts.Stratum() != 1 {
		t.Fatalf("stratum 3 should not override stratum 1, got %d", ts.Stratum())
	}
	if ts.Offset() != 200_000_000 {
		t.Fatalf("offset should remain 200ms, got %dns", ts.Offset())
	}
}

func TestTimeService_SameStratumBetterUncertainty(t *testing.T) {
	ts := NewTimeService(nil)

	// Apply stratum 1 with 500ms uncertainty.
	ts.ApplyCorrection("msstm", 1, 100_000_000, 500_000_000)

	// Same stratum, better uncertainty should override.
	ts.ApplyCorrection("gps", 1, 150_000_000, 100_000_000)
	if ts.Offset() != 150_000_000 {
		t.Fatalf("expected 150ms offset after better uncertainty, got %dns", ts.Offset())
	}

	// Same stratum, worse uncertainty should NOT override.
	ts.ApplyCorrection("msstm", 1, 999_000_000, 900_000_000)
	if ts.Offset() != 150_000_000 {
		t.Fatalf("offset should remain 150ms, got %dns", ts.Offset())
	}
}

func TestTimeService_GetStatus(t *testing.T) {
	ts := NewTimeService(nil)
	ts.ApplyCorrection("gps", 1, 42_000_000, 100_000_000)

	status := ts.GetStatus()
	if status.Source != "gps" {
		t.Fatalf("expected source gps, got %s", status.Source)
	}
	if status.Stratum != 1 {
		t.Fatalf("expected stratum 1, got %d", status.Stratum)
	}
	if math.Abs(status.OffsetMs-42.0) > 0.001 {
		t.Fatalf("expected 42ms offset, got %f", status.OffsetMs)
	}
}

func TestMeshConsensus_RequestMarshal(t *testing.T) {
	// Verify the wire format of a time sync request.
	pkt := make([]byte, timeSyncReqLen)
	pkt[0] = PacketTimeSyncReq
	// Set a fake dest hash.
	for i := 1; i < 17; i++ {
		pkt[i] = byte(i)
	}
	ts := int64(1234567890123456789)
	binary.LittleEndian.PutUint64(pkt[17:25], uint64(ts))
	pkt[25] = 2 // stratum

	// Verify parsing.
	if pkt[0] != PacketTimeSyncReq {
		t.Fatal("wrong type byte")
	}
	parsed := int64(binary.LittleEndian.Uint64(pkt[17:25]))
	if parsed != ts {
		t.Fatalf("timestamp mismatch: %d vs %d", parsed, ts)
	}
	if pkt[25] != 2 {
		t.Fatalf("stratum mismatch: %d", pkt[25])
	}
}

func TestMeshConsensus_ResponseMarshal(t *testing.T) {
	// Verify the wire format of a time sync response.
	pkt := make([]byte, timeSyncRespLen)
	pkt[0] = PacketTimeSyncResp
	for i := 1; i < 17; i++ {
		pkt[i] = byte(i + 0x10)
	}
	remoteTs := int64(1709999999000000000)
	binary.LittleEndian.PutUint64(pkt[17:25], uint64(remoteTs))
	pkt[25] = 1
	echoTs := int64(1111111111000000000)
	binary.LittleEndian.PutUint64(pkt[26:34], uint64(echoTs))

	if pkt[0] != PacketTimeSyncResp {
		t.Fatal("wrong type byte")
	}
	if int64(binary.LittleEndian.Uint64(pkt[17:25])) != remoteTs {
		t.Fatal("remote timestamp mismatch")
	}
	if int64(binary.LittleEndian.Uint64(pkt[26:34])) != echoTs {
		t.Fatal("echo timestamp mismatch")
	}
}

func TestMeshConsensus_OffsetCalculation(t *testing.T) {
	// Simulate a request/response exchange:
	// Local send time: T1
	// Remote receive+respond: T2 (remote clock)
	// Local receive time: T3
	// RTT = T3 - T1
	// One-way delay = RTT / 2
	// Offset = T2 + RTT/2 - T3

	T1 := time.Now()
	T2_remote := T1.Add(50 * time.Millisecond) // remote is 50ms ahead
	rtt := 20 * time.Millisecond               // 20ms round trip
	T3 := T1.Add(rtt)

	oneWayDelay := rtt / 2
	offset := T2_remote.UnixNano() + oneWayDelay.Nanoseconds() - T3.UnixNano()

	// Expected offset: remote is 50ms ahead, RTT is 20ms.
	// offset = (T1+50ms) + 10ms - (T1+20ms) = T1 + 60ms - T1 - 20ms = 40ms
	expectedMs := 40.0
	gotMs := float64(offset) / 1e6

	if math.Abs(gotMs-expectedMs) > 1.0 {
		t.Fatalf("expected ~%.0fms offset, got %.2fms", expectedMs, gotMs)
	}
}

func TestHexHash(t *testing.T) {
	var h [DestHashLen]byte
	h[0] = 0xDE
	h[1] = 0xAD
	h[15] = 0xFF
	got := hexHash(h)
	if got[:4] != "dead" {
		t.Fatalf("expected dead..., got %s", got)
	}
	if got[30:] != "ff" {
		t.Fatalf("expected ...ff, got %s", got)
	}
}
