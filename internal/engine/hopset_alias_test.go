package engine

import "testing"

// probe_on_ax25_is_aprs (R9): the time-sync answer on the routing interface
// ax25_N is the probe of hop link aprs_N, the same PicoAPRS radio.
func TestHopSetProbeOnAx25IsAprs(t *testing.T) {
	r := newHopRig(t, "mesh_0:mesh", "aprs_0:aprs", "aprs_1:aprs", "tetra_0:tetra")
	probeNow(r.m, "ax25_0")
	if st := r.state(t, "aprs_0"); st.LastProbe == nil {
		t.Fatal("ax25_0's answer is no probe of aprs_0")
	}
	if st := r.state(t, "aprs_1"); st.LastProbe != nil {
		t.Fatal("ax25_0's answer counted for aprs_1")
	}
	probeNow(r.m, "ax25_1")
	if st := r.state(t, "aprs_1"); st.LastProbe == nil {
		t.Fatal("ax25_1's answer is no probe of aprs_1")
	}
	probeNow(r.m, "mesh_0")
	if st := r.state(t, "mesh_0"); st.LastProbe == nil {
		t.Fatal("mesh_0 lost its own probe")
	}
}
