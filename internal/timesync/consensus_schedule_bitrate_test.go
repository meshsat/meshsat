package timesync

import (
	"testing"
	"time"
)

// A request on a slow bearer must stay inside the 2 % airtime share. The
// mesh at 1200 bps keeps its 30 s period (a 48 byte request needs 16 s of
// budget), a KISS modem at 149 bps does not. [MESHSAT-778]
func TestPeriodScalesWithBitRate(t *testing.T) {
	rates := map[string]int{"mesh_0": 1200, "ax25_0": 149, "zigbee_0": 250000, "tcp_0": 0}
	mc := &MeshTimeConsensus{discoveryInterval: DefaultDiscoveryInterval, ifaces: map[string]*ifaceSchedule{}}
	mc.SetBitRate(func(id string) int { return rates[id] }, 2)
	now := time.Now()
	peer := &ifaceSchedule{peerSeen: now}
	lonely := &ifaceSchedule{}

	for _, tc := range []struct {
		iface    string
		st       *ifaceSchedule
		min, max time.Duration
	}{
		{"mesh_0", peer, requestInterval, requestInterval},     // unchanged
		{"zigbee_0", peer, requestInterval, requestInterval},   // fast: unchanged
		{"tcp_0", peer, requestInterval, requestInterval},      // no known limit
		{"ax25_0", peer, 125 * time.Second, 135 * time.Second}, // 384 bits at 2.98 bps
		{"mesh_0", lonely, DefaultDiscoveryInterval, DefaultDiscoveryInterval},
		{"ax25_0", lonely, DefaultDiscoveryInterval, DefaultDiscoveryInterval}, // discovery already longer
	} {
		got := mc.periodLocked(tc.iface, tc.st, now)
		if got < tc.min || got > tc.max {
			t.Errorf("%s (peer=%v): period %s, want %s..%s", tc.iface, !tc.st.peerSeen.IsZero(), got, tc.min, tc.max)
		}
	}

	// A bearer slower than the discovery floor stretches discovery too.
	rates["ax25_0"] = 10
	if got := mc.periodLocked("ax25_0", lonely, now); got <= DefaultDiscoveryInterval {
		t.Errorf("10 bps: discovery period %s, want longer than %s", got, DefaultDiscoveryInterval)
	}
}

// Without a bit-rate source nothing changes.
func TestPeriodWithoutBitRateIsUnchanged(t *testing.T) {
	mc := &MeshTimeConsensus{discoveryInterval: DefaultDiscoveryInterval, ifaces: map[string]*ifaceSchedule{}}
	now := time.Now()
	if got := mc.periodLocked("ax25_0", &ifaceSchedule{peerSeen: now}, now); got != requestInterval {
		t.Fatalf("period %s, want %s", got, requestInterval)
	}
}
