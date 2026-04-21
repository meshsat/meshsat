package hemb

import (
	"context"
	"fmt"
	"testing"
)

type fakeRNS struct {
	got     map[string]int
	unknown map[string]bool // ifaces to reject with "unknown interface:" so fallback kicks in
}

func (f *fakeRNS) Send(id string, _ []byte) error {
	if f.got == nil {
		f.got = map[string]int{}
	}
	if f.unknown[id] {
		return fmt.Errorf("unknown interface: %s", id)
	}
	f.got[id]++
	return nil
}

type fakeFwd struct{ got map[string]int }

func (f *fakeFwd) ForwardHeMBFrame(id string, _ []byte) error {
	if f.got == nil {
		f.got = map[string]int{}
	}
	f.got[id]++
	return nil
}

func TestIsReticulumNativeIface(t *testing.T) {
	// Legacy classifier — retained for reference, not used by the
	// runtime send path anymore.
	cases := []struct {
		id   string
		want bool
	}{
		{"tcp_0", true},
		{"tcp_5", true},
		{"ble_0", true},
		{"ble_peer_0", true},
		{"mqtt_rns_0", true},
		{"mesh_0", false},
		{"ax25_0", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isReticulumNativeIface(c.id); got != c.want {
			t.Errorf("isReticulumNativeIface(%q)=%v want %v", c.id, got, c.want)
		}
	}
}

// TestBuildBearersAllThroughRNS is the main contract: every bearer
// ends up calling rns.Send — even mesh_0 / ax25_0 — because that's
// the only path that preserves HeMB magic bytes on the wire. The
// gateway forwarder is only a fallback for ifaces not in the
// registry.
func TestBuildBearersAllThroughRNS(t *testing.T) {
	rns := &fakeRNS{}
	fwd := &fakeFwd{}
	ids := []string{"mesh_0", "ax25_0", "tcp_0", "ble_peer_1", "iridium_0"}
	bearers := BuildBearers(ids, fwd, rns)
	if len(bearers) != len(ids) {
		t.Fatalf("len: %d", len(bearers))
	}
	for i, b := range bearers {
		if b.HealthScore != 100 {
			t.Errorf("bearer[%d] HealthScore=%d want 100 (so bonder.sendMulti sees it as online)", i, b.HealthScore)
		}
		if b.InterfaceID != ids[i] {
			t.Errorf("bearer[%d] id=%q want %q", i, b.InterfaceID, ids[i])
		}
		if err := b.SendFn(context.Background(), []byte("x")); err != nil {
			t.Errorf("send %s: %v", b.InterfaceID, err)
		}
	}
	for _, id := range ids {
		if rns.got[id] != 1 {
			t.Errorf("rns.Send %s: got %d want 1", id, rns.got[id])
		}
	}
	if len(fwd.got) != 0 {
		t.Errorf("gateway forwarder should not have been touched: %v", fwd.got)
	}
}

// TestBuildBearersFallbackToForwarder: when rns.Send reports "unknown
// interface" (meaning the iface isn't in the registry), the SendFn
// must fall through to the dispatcher's gateway forwarder rather
// than dropping the frame.
func TestBuildBearersFallbackToForwarder(t *testing.T) {
	rns := &fakeRNS{unknown: map[string]bool{"custom_0": true}}
	fwd := &fakeFwd{}
	bearers := BuildBearers([]string{"custom_0"}, fwd, rns)
	if err := bearers[0].SendFn(context.Background(), []byte("y")); err != nil {
		t.Fatalf("send custom_0: %v", err)
	}
	if fwd.got["custom_0"] != 1 {
		t.Errorf("fallback to forwarder failed: %v", fwd.got)
	}
	if rns.got["custom_0"] != 0 {
		t.Errorf("rns should have rejected: %v", rns.got)
	}
}

func TestBuildBearersNilSenderErrors(t *testing.T) {
	b := BuildBearers([]string{"tcp_0"}, nil, nil)
	if len(b) != 1 {
		t.Fatalf("len: %d", len(b))
	}
	if err := b[0].SendFn(context.Background(), []byte("x")); err == nil {
		t.Fatal("expected error when both senders nil")
	}
}
