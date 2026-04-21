package hemb

import (
	"context"
	"errors"
	"testing"
)

type fakeRNS struct{ got map[string]int }

func (f *fakeRNS) Send(id string, _ []byte) error {
	if f.got == nil {
		f.got = map[string]int{}
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
	cases := []struct {
		id   string
		want bool
	}{
		{"tcp_0", true},
		{"tcp_5", true},
		{"ble_0", true},
		{"ble_peer_0", true},
		{"ble_peer_3", true},
		{"mqtt_rns_0", true},
		{"mesh_0", false},
		{"ax25_0", false},
		{"iridium_0", false},
		{"iridium_imt_0", false},
		{"sms_0", false},
		{"cellular_0", false},
		{"zigbee_0", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isReticulumNativeIface(c.id); got != c.want {
			t.Errorf("isReticulumNativeIface(%q)=%v want %v", c.id, got, c.want)
		}
	}
}

func TestBuildBearersRoutesSendFn(t *testing.T) {
	rns := &fakeRNS{}
	fwd := &fakeFwd{}
	ids := []string{"mesh_0", "ax25_0", "tcp_0", "ble_peer_1"}
	bearers := BuildBearers(ids, fwd, rns)
	if len(bearers) != 4 {
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
	// mesh/ax25 went through the gateway fwd; tcp/ble went through RNS.
	if fwd.got["mesh_0"] != 1 || fwd.got["ax25_0"] != 1 {
		t.Errorf("gateway fwd counts: %v", fwd.got)
	}
	if rns.got["tcp_0"] != 1 || rns.got["ble_peer_1"] != 1 {
		t.Errorf("rns send counts: %v", rns.got)
	}
	if fwd.got["tcp_0"] != 0 || rns.got["mesh_0"] != 0 {
		t.Errorf("cross-routing leak: fwd=%v rns=%v", fwd.got, rns.got)
	}
}

func TestBuildBearersNilSenderErrors(t *testing.T) {
	b := BuildBearers([]string{"tcp_0"}, nil, nil)
	if len(b) != 1 {
		t.Fatalf("len: %d", len(b))
	}
	err := b[0].SendFn(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("expected error when sender is nil")
	}
	if !errors.Is(err, err) { // placeholder — just confirm non-nil
		t.Fatal("error not propagated")
	}
}
