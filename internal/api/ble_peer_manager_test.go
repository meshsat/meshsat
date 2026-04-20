package api

import (
	"context"
	"testing"
)

// TestBLEPeerManagerDormant confirms EnsurePeer + RemovePeer are safe
// no-ops when the manager hasn't been wired to a live Processor +
// registry (e.g. during tests or early boot).
func TestBLEPeerManagerDormant(t *testing.T) {
	m := NewBLEPeerManager("hci0", nil, nil)
	if err := m.EnsurePeer(context.Background(), "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatalf("dormant EnsurePeer must not error, got %v", err)
	}
	// RemovePeer on an unknown key must not panic.
	m.RemovePeer("AA:BB:CC:DD:EE:FF")
	if len(m.Names()) != 0 {
		t.Fatalf("dormant manager should have no peers, got %v", m.Names())
	}
}

// TestMACNormalization — inputs we might get from chi path params vs
// BlueZ's canonical uppercase.
func TestMACNormalization(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF"},
		{"  AA:BB:CC:DD:EE:FF  ", "AA:BB:CC:DD:EE:FF"},
		{"Aa:Bb:Cc:Dd:Ee:Ff", "AA:BB:CC:DD:EE:FF"},
	}
	for _, c := range cases {
		if got := normalizeMAC(c.in); got != c.want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBlePeerNameSeq confirms the allocator emits stable
// incrementing names without leaking state.
func TestBlePeerNameSeq(t *testing.T) {
	m := NewBLEPeerManager("hci0", nil, nil)
	got := []string{
		m.allocNameLocked(),
		m.allocNameLocked(),
		m.allocNameLocked(),
	}
	want := []string{"ble_peer_0", "ble_peer_1", "ble_peer_2"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("alloc[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}
