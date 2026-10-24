package engine

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/hopwire"
)

// Media hopping part two: the peers' hop state (shown, never acted on), our
// compact form, the probe and the change hook. [MESHSAT-1650]

func allExcluded(ids ...string) hopwire.Report {
	var r hopwire.Report
	for _, id := range ids {
		r.Links = append(r.Links, hopwire.Link{Iface: id, State: hopwire.Excluded})
	}
	return r
}

// peer_report_never_a_sign: a peer that reports every link of ours excluded
// changes no state, no sign, no gate decision and audits nothing; the report
// is only shown.
func TestHopSetPeerReportNeverASign(t *testing.T) {
	for _, mode := range []HopMode{HopWatch, HopOn} {
		r := newHopRig(t, threeFree...)
		if _, err := r.m.SetMode(mode); err != nil {
			t.Fatal(err)
		}
		before := r.m.Status()
		r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack") // one sign: suspect
		mid := r.m.Status()
		audits := len(r.audits)
		r.m.NotePeer(7, "parallax", "mesh_0", "reply", allExcluded("mesh_0", "aprs_0", "tetra_0", "cellular_0"))
		r.after(30 * time.Second)
		after := r.m.Status()
		for i, l := range after.Links {
			m := mid.Links[i]
			if l.State != m.State || len(l.Signs) != len(m.Signs) || l.Usable != m.Usable {
				t.Errorf("%s: %s: %s/%d signs, before the report %s/%d", mode, l.InterfaceID, l.State, len(l.Signs), m.State, len(m.Signs))
			}
			if ex, _ := r.m.RouteExcluded(l.InterfaceID); ex {
				t.Errorf("%s: %s gated after a peer report", mode, l.InterfaceID)
			}
		}
		if len(r.audits) != audits {
			t.Errorf("%s: the report was audited: %v", mode, r.audits[audits:])
		}
		if len(before.Peers) != 0 || len(after.Peers) != 1 {
			t.Fatalf("%s: peers %v -> %v", mode, before.Peers, after.Peers)
		}
		p := after.Peers[0]
		if p.PeerID != 7 || p.Alias != "parallax" || p.Bearer != "mesh_0" || p.Via != "reply" || len(p.Links) != 4 || p.Links[0].State != "excluded" {
			t.Errorf("%s: peer %+v", mode, p)
		}
	}
}

// mode_off_keeps_no_peer_and_sends_no_form: with the switch off a report is
// not kept, our form is empty and a probe does nothing; switching off forgets
// the peers kept while watching.
func TestHopSetModeOffKeepsNoPeerAndSendsNoForm(t *testing.T) {
	r := newHopRig(t, threeFree...)
	if _, err := r.m.SetMode(HopOff); err != nil {
		t.Fatal(err)
	}
	r.m.NotePeer(7, "parallax", "mesh_0", "request", allExcluded("mesh_0"))
	r.m.Probe("mesh_0")
	st := r.m.Status()
	if len(st.Peers) != 0 || len(st.Links) != 0 {
		t.Fatalf("off: peers %v links %v", st.Peers, st.Links)
	}
	if f := r.m.Compact(100); f != "" {
		t.Fatalf("off: form %q", f)
	}
	if _, err := r.m.SetMode(HopWatch); err != nil {
		t.Fatal(err)
	}
	r.m.NotePeer(7, "parallax", "mesh_0", "request", allExcluded("mesh_0"))
	if len(r.m.Status().Peers) != 1 {
		t.Fatal("watch: report not kept")
	}
	if _, err := r.m.SetMode(HopOff); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetMode(HopWatch); err != nil {
		t.Fatal(err)
	}
	if n := len(r.m.Status().Peers); n != 0 {
		t.Fatalf("a peer survived the switch going off: %d", n)
	}
}

// compact_form_names_each_state: admitted A, suspect S, excluded X,
// recovering R, retained by the floor T, down D.
func TestHopSetCompactFormNamesEachState(t *testing.T) {
	r := newHopRig(t, "mesh_0:mesh", "aprs_0:aprs", "tetra_0:tetra", "zigbee_0:zigbee", "cell_0:cell", "iridium_0:iridium")
	excludeMesh(t, r)
	r.m.Positive("mesh_0", "rx") // recovering
	r.m.Negative("aprs_0", "delivery", "!1", "no_ack")
	r.m.Negative("aprs_0", "delivery", "!2", "no_ack")
	r.after(11 * time.Second)                             // aprs_0 excluded
	r.m.Negative("tetra_0", "radio", "not_submitted", "") // suspect
	r.deaf["zigbee_0"] = true                             // down
	if got := r.m.Compact(100); got != "H1:a0Xg0Ai0Am0Rt0Sz0D" {
		t.Fatalf("form %q", got)
	}

	// The floor: two free links and one excluded leave one usable free
	// link, so the floor keeps the excluded one, T.
	f := newHopRig(t, "mesh_0:mesh", "aprs_0:aprs")
	f.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	f.m.Negative("mesh_0", "delivery", "!0000beef", "no_ack")
	f.after(11 * time.Second)
	if st := f.state(t, "mesh_0"); st.State != HopExcluded || !st.Retained {
		t.Fatalf("setup: mesh_0 %s retained=%v", st.State, st.Retained)
	}
	if got := f.m.Compact(100); got != "H1:a0Am0T" {
		t.Fatalf("floor form %q", got)
	}
}

// probe_is_a_positive_sign_on_a_free_link: an answered request starts an
// excluded link's recovery and shows its time; it is no frame heard for the
// silence rule, and a paid link takes no probe.
func TestHopSetProbeIsAPositiveSignOnAFreeLink(t *testing.T) {
	r := newHopRig(t, threeFree...)
	excludeMesh(t, r)
	r.after(time.Second)
	r.m.Probe("mesh_0")
	st := r.state(t, "mesh_0")
	if st.State != HopRecovering || st.LastProbe == nil || !st.LastProbe.Equal(r.now) {
		t.Fatalf("after a probe: %s, last probe %v", st.State, st.LastProbe)
	}

	// Silence: a busy link that hears nothing for 10 min has a silence sign
	// however many probes are answered meanwhile.
	s := newHopRig(t, threeFree...)
	hearEvery(s, "aprs_0", time.Minute, 70*time.Minute)
	for i := 0; i < 12; i++ {
		s.after(time.Minute)
		s.m.Probe("aprs_0")
	}
	found := false
	for _, sg := range s.state(t, "aprs_0").Signs {
		if sg.Source == "silence" {
			found = true
		}
	}
	if !found {
		t.Fatalf("probes ended the silence sign: %+v", s.state(t, "aprs_0").Signs)
	}

	// A paid link takes no probe.
	p := newHopRig(t, threeFree...)
	p.m.Negative("cellular_0", "delivery", "+31", "no_ack")
	p.m.Probe("cellular_0")
	st = p.state(t, "cellular_0")
	if st.LastProbe != nil || st.LastPositive != nil || st.State != HopSuspect {
		t.Fatalf("paid link after a probe: %s, positive %v, probe %v", st.State, st.LastPositive, st.LastProbe)
	}
	// A link outside the hop set takes none either, and nothing panics.
	p.m.Probe("tcp_0")
}

// on_change_once_per_evaluation_with_a_use_change: the hook runs when a link
// goes out of use or back into use, once per evaluation, never with the
// switch off.
func TestHopSetOnChange(t *testing.T) {
	r := newHopRig(t, threeFree...)
	var n atomic.Int32
	r.m.SetOnChange(func() { n.Add(1) })
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	if n.Load() != 0 {
		t.Fatalf("suspect (still in use) ran the hook %d times", n.Load())
	}
	excludeMesh(t, r)
	if n.Load() != 1 {
		t.Fatalf("exclusion ran the hook %d times, want 1", n.Load())
	}
	r.m.Positive("mesh_0", "rx")
	r.after(readmit + time.Second)
	r.m.Status()
	if n.Load() != 2 {
		t.Fatalf("re-admission ran the hook %d times, want 2 in all", n.Load())
	}
	if _, err := r.m.SetMode(HopOff); err != nil {
		t.Fatal(err)
	}
	r.m.Negative("aprs_0", "delivery", "!1", "no_ack")
	r.m.Negative("aprs_0", "delivery", "!2", "no_ack")
	r.after(time.Minute)
	r.m.Status()
	if n.Load() != 2 {
		t.Fatalf("off ran the hook")
	}
}

// api_peers_shape: GET /api/hopset carries peers (an empty list, never null)
// and a link's last probe.
func TestHopSetStatusPeersShape(t *testing.T) {
	r := newHopRig(t, threeFree...)
	b, _ := json.Marshal(r.m.Status())
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["peers"]) != "[]" {
		t.Fatalf("peers %s, want []", raw["peers"])
	}
	r.m.Probe("aprs_0")
	r.m.NotePeer(3, "tesseract", "aprs_0", "request", hopwire.Report{Cut: true, Links: []hopwire.Link{{Iface: "mesh_0", State: hopwire.Down}}})
	b, _ = json.Marshal(r.m.Status())
	var st struct {
		Links []map[string]any `json:"links"`
		Peers []map[string]any `json:"peers"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Peers) != 1 {
		t.Fatalf("peers %v", st.Peers)
	}
	p := st.Peers[0]
	for _, k := range []string{"peer_id", "alias", "bearer", "via", "heard_at", "cut", "links"} {
		if _, ok := p[k]; !ok {
			t.Errorf("peer has no %q: %v", k, p)
		}
	}
	links := p["links"].([]any)
	if l := links[0].(map[string]any); l["interface_id"] != "mesh_0" || l["state"] != "down" || p["cut"] != true {
		t.Errorf("peer links %v cut %v", links, p["cut"])
	}
	var probed bool
	for _, l := range st.Links {
		if l["interface_id"] == "aprs_0" {
			_, probed = l["last_probe"]
		} else if _, ok := l["last_probe"]; ok {
			t.Errorf("%v has a probe it never took", l["interface_id"])
		}
	}
	if !probed {
		t.Error("aprs_0 shows no last_probe")
	}
}
