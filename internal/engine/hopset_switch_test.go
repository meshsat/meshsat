package engine

import (
	"context"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

// The media hopping switch (Settings > Media Hopping), off by default: off
// keeps nothing and routes as before media hopping, watch keeps the state
// without routing on it, on gates. [MESHSAT-1650]

// watchExcludeMesh gives mesh_0 two signs and lets the dwell pass; the state
// is read from the status, since only the switch on routes on it.
func watchExcludeMesh(t *testing.T, r *hopRig) {
	t.Helper()
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	r.m.Negative("mesh_0", "delivery", "!0000beef", "no_ack")
	r.after(11 * time.Second)
	if s := r.state(t, "mesh_0"); s.State != HopExcluded || s.Usable {
		t.Fatalf("setup: mesh_0 %s usable=%v, want excluded", s.State, s.Usable)
	}
}

// jamDeaf is a spectrum verdict and a deaf check over fixed links.
type jamDeaf struct{ jam, deaf map[string]bool }

func (j jamDeaf) JamVerdict(id string) (bool, bool) { return j.jam[id], true }
func (j jamDeaf) ReceiveDeaf(id string) bool        { return j.deaf[id] }

// switch_off_keeps_nothing: with the switch off no sign is kept, nothing is
// audited or published, the time-driven loop does nothing, the intents read
// no gate and a group skips a deaf receiver only.
func TestHopSwitchOffKeepsNothing(t *testing.T) {
	cfg := DefaultHopSetConfig()
	cfg.TickEvery = 5 * time.Millisecond
	r := newHopRigCfg(t, cfg, threeFree...)
	if _, err := r.m.SetMode(HopOff); err != nil {
		t.Fatal(err)
	}
	r.jam["mesh_0"] = true
	r.deaf["aprs_0"] = true
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	r.m.Negative("mesh_0", "radio", "not_submitted", "refused")
	r.after(11 * time.Second)
	r.m.Positive("tetra_0", "rx")
	r.m.PaidAttempt("cellular_0")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.m.Start(ctx); close(done) }()
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	st := r.m.Status()
	if st.Mode != HopOff || st.Gating || len(st.Links) != 0 {
		t.Fatalf("off: mode %q gating %v links %+v, want off, not gating, no links", st.Mode, st.Gating, st.Links)
	}
	r.mu.Lock()
	audits, events := len(r.audits), len(r.events)
	r.mu.Unlock()
	if audits != 0 || events != 0 {
		t.Fatalf("off: %d audits, %d events, want none", audits, events)
	}
	intents, group := r.m.IntentGate(), r.m.GroupGate()
	for _, id := range []string{"mesh_0", "aprs_0", "tetra_0", "cellular_0"} {
		if ex, why := intents.RouteExcluded(id); ex {
			t.Errorf("off: the intents' gate excludes %s (%s), want none", id, why)
		}
	}
	if ex, why := group.RouteExcluded("aprs_0"); !ex || why != "deaf" {
		t.Errorf("off: a group's gate on a deaf aprs_0: %v %q, want excluded as deaf", ex, why)
	}
	if ex, why := group.RouteExcluded("mesh_0"); ex {
		t.Errorf("off: a group's gate excludes the jammed mesh_0 (%s), want it tried as before", why)
	}
	if ex, _ := r.m.RouteExcluded("aprs_0"); ex {
		t.Error("off: the hop set's own decision excludes, want none")
	}
}

// switch_watch_records_never_gates: watch keeps the state, audits and
// publishes it, and routes as off does.
func TestHopSwitchWatchRecordsButNeverGates(t *testing.T) {
	r := newHopRig(t, threeFree...)
	if _, err := r.m.SetMode(HopWatch); err != nil {
		t.Fatal(err)
	}
	r.deaf["aprs_0"] = true
	watchExcludeMesh(t, r)
	st := r.m.Status()
	if st.Mode != HopWatch || st.Gating {
		t.Fatalf("watch: mode %q gating %v", st.Mode, st.Gating)
	}
	r.mu.Lock()
	audited := false
	for _, a := range r.audits {
		audited = audited || a == "hopset_excluded mesh_0"
	}
	r.mu.Unlock()
	if !audited {
		t.Errorf("watch: no hopset_excluded audit for mesh_0 (%v)", r.audits)
	}
	if ex, why := r.m.IntentGate().RouteExcluded("mesh_0"); ex {
		t.Errorf("watch: the intents' gate excludes mesh_0 (%s)", why)
	}
	if ex, why := r.m.GroupGate().RouteExcluded("mesh_0"); ex {
		t.Errorf("watch: a group's gate excludes mesh_0 (%s)", why)
	}
	if ex, why := r.m.GroupGate().RouteExcluded("aprs_0"); !ex || why != "deaf" {
		t.Errorf("watch: a group's gate on a deaf aprs_0: %v %q, want excluded as deaf", ex, why)
	}
}

// switch_moves_live: watch to on keeps what was watched and gates on it;
// off drops it; a bad mode changes nothing.
func TestHopSwitchMovesLive(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.m.SetMode(HopWatch)
	watchExcludeMesh(t, r)
	prev, err := r.m.SetMode(HopOn)
	if err != nil || prev != HopWatch {
		t.Fatalf("watch to on: prev %q err %v", prev, err)
	}
	if ex, _ := r.m.IntentGate().RouteExcluded("mesh_0"); !ex {
		t.Fatal("on after watch: mesh_0 not excluded, want what was watched to gate")
	}
	if ex, _ := r.m.GroupGate().RouteExcluded("mesh_0"); !ex {
		t.Fatal("on after watch: a group still uses mesh_0")
	}
	if _, err := r.m.SetMode("gate"); err == nil || r.m.Mode() != HopOn {
		t.Fatalf("bad mode: err %v, mode %q, want an error and on kept", err, r.m.Mode())
	}
	r.m.SetMode(HopOff)
	if ex, _ := r.m.IntentGate().RouteExcluded("mesh_0"); ex {
		t.Fatal("off: mesh_0 still excluded")
	}
	r.m.SetMode(HopWatch)
	if s := r.state(t, "mesh_0"); s.State != HopAdmitted || len(s.Signs) != 0 {
		t.Fatalf("watch after off: mesh_0 %s signs %+v, want admitted from nothing", s.State, s.Signs)
	}
}

// switch_off_routes_as_before: wired as main wires it, the switch off leaves
// round 1 on a link the hop set would exclude, and a group skips a deaf
// member but not a jammed one, as before media hopping. The same setup with
// the switch on moves round 1 (the positive control).
func TestHopSwitchOffRoutesAsBefore(t *testing.T) {
	for _, mode := range []HopMode{HopOff, HopWatch, HopOn} {
		h := newIntentHarness(t)
		now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
		m := NewHopSetManager(DefaultHopSetConfig(), h.db.GetAllInterfaces, mode)
		m.clock = func() time.Time { return now }
		m.SetSpectrum(jamDeaf{jam: map[string]bool{"mesh_0": true}})
		h.d.SetHopSigns(m)
		h.d.SetRouteGate(m.IntentGate())
		m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
		m.Negative("mesh_0", "delivery", "!0000beef", "no_ack")
		now = now.Add(11 * time.Second)
		want := "mesh_0"
		if mode == HopOn {
			want = "lxmf_0"
		}
		c := h.contact(meshAddr(1), lxmfAddr(2))
		if got := firstRowChannel(t, h, h.submit(c, "one", directory.StrategyOrderedFallback)); got != want {
			t.Errorf("%s: round 1 on %s, want %s", mode, got, want)
		}
	}

	fr, ifaceMgr, db := setupFailoverTest(t)
	db.InsertFailoverGroup(&database.FailoverGroup{ID: "sat", Label: "sat", Mode: "priority"})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat", InterfaceID: "iridium_0", Priority: 1})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat", InterfaceID: "iridium_1", Priority: 2})
	ifaceMgr.mu.Lock()
	ifaceMgr.states["iridium_0"].state = StateOnline
	ifaceMgr.states["iridium_1"].state = StateOnline
	ifaceMgr.mu.Unlock()
	m := NewHopSetManager(DefaultHopSetConfig(), db.GetAllInterfaces, HopOff)
	fr.SetRouteGate(m.GroupGate())
	m.SetSpectrum(jamDeaf{jam: map[string]bool{"iridium_0": true}})
	if got := fr.Resolve("sat"); got != "iridium_0" {
		t.Errorf("off, jammed primary: resolved %s, want iridium_0 as before", got)
	}
	m.SetReceiveChecker(jamDeaf{deaf: map[string]bool{"iridium_0": true}})
	if got := fr.Resolve("sat"); got != "iridium_1" {
		t.Errorf("off, deaf primary: resolved %s, want iridium_1 as before (MESHSAT-857)", got)
	}
}
