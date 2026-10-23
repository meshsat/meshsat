package engine

import (
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/hemb"
)

// hopRig is a hop set on a fake clock over a fixed list of links, with the
// audit entries and events it emits.
type hopRig struct {
	m      *HopSetManager
	now    time.Time
	jam    map[string]bool
	deaf   map[string]bool
	mu     sync.Mutex
	audits []string
	events []string
}

func (r *hopRig) JamVerdict(id string) (bool, bool) { return r.jam[id], true }
func (r *hopRig) ReceiveDeaf(id string) bool        { return r.deaf[id] }

func newHopRig(t *testing.T, links ...string) *hopRig {
	t.Helper()
	r := &hopRig{now: time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC), jam: map[string]bool{}, deaf: map[string]bool{}}
	var ifaces []database.Interface
	for _, l := range links { // "id:type"
		var id, ct string
		for i := range l {
			if l[i] == ':' {
				id, ct = l[:i], l[i+1:]
			}
		}
		ifaces = append(ifaces, database.Interface{ID: id, ChannelType: ct, Enabled: true})
	}
	r.m = NewHopSetManager(DefaultHopSetConfig(), func() ([]database.Interface, error) { return ifaces, nil }, true)
	r.m.clock = func() time.Time { return r.now }
	r.m.SetSpectrum(r)
	r.m.SetReceiveChecker(r)
	r.m.SetAudit(func(typ, id, _ string) { r.mu.Lock(); r.audits = append(r.audits, typ+" "+id); r.mu.Unlock() })
	r.m.SetPublisher(func(e hemb.Event) { r.mu.Lock(); r.events = append(r.events, string(e.Type)); r.mu.Unlock() })
	return r
}

func (r *hopRig) after(d time.Duration) { r.now = r.now.Add(d) }

func (r *hopRig) state(t *testing.T, id string) HopLinkStatus {
	t.Helper()
	for _, l := range r.m.Status().Links {
		if l.InterfaceID == id {
			return l
		}
	}
	t.Fatalf("no link %s", id)
	return HopLinkStatus{}
}

func (r *hopRig) excluded(id string) bool { ex, _ := r.m.RouteExcluded(id); return ex }

var threeFree = []string{"mesh_0:mesh", "aprs_0:aprs", "tetra_0:tetra", "cellular_0:cellular"}

// One sign makes a link suspect, never excluded; two failures to the same
// destination are one sign.
func TestHopSetOneSignIsSuspectOnly(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	r.after(15 * time.Second)
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	r.after(15 * time.Second)
	if s := r.state(t, "mesh_0"); s.State != HopSuspect || r.excluded("mesh_0") {
		t.Fatalf("one destination twice: %s excluded=%v, want suspect and tried", s.State, r.excluded("mesh_0"))
	}
}

// Two distinct signs exclude the link once it has been suspect for the
// drop dwell, not before; a jam verdict and a delivery failure are two.
func TestHopSetTwoSignsExcludeAfterTheDwell(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	r.after(2 * time.Second)
	r.m.Negative("mesh_0", "delivery", "!0000beef", "no_ack")
	if r.excluded("mesh_0") {
		t.Fatal("excluded 2 s after the first sign, before the drop dwell")
	}
	r.after(9 * time.Second)
	if !r.excluded("mesh_0") {
		t.Fatalf("two destinations, 11 s suspect: %s, want excluded", r.state(t, "mesh_0").State)
	}

	r2 := newHopRig(t, threeFree...)
	r2.jam["aprs_0"] = true
	r2.m.Negative("aprs_0", "delivery", "MSPRLX-10", "no_ack")
	r2.after(11 * time.Second)
	if !r2.excluded("aprs_0") {
		t.Fatalf("jam + delivery: %s, want excluded", r2.state(t, "aprs_0").State)
	}
}

// A lone jam verdict makes a link suspect only (the two-sign rule replaces
// the slice (a) gate's one-verdict exclusion when the hop set gates).
func TestHopSetJamAloneIsOneSign(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.jam["mesh_0"] = true
	r.after(time.Minute)
	if s := r.state(t, "mesh_0"); s.State != HopSuspect || r.excluded("mesh_0") {
		t.Fatalf("jam alone: %s excluded=%v, want suspect and tried", s.State, r.excluded("mesh_0"))
	}
}

// A positive sign after the negatives, or the negatives ageing out, clears
// a suspect link.
func TestHopSetSuspectClears(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	r.after(time.Second)
	r.m.Positive("mesh_0", "rx")
	if s := r.state(t, "mesh_0"); s.State != HopAdmitted {
		t.Fatalf("positive after the sign: %s, want admitted", s.State)
	}
	r.m.Negative("tetra_0", "radio", "not_submitted", "refused")
	r.after(121 * time.Second)
	if s := r.state(t, "tetra_0"); s.State != HopAdmitted {
		t.Fatalf("sign aged out: %s, want admitted", s.State)
	}
}

// excludeMesh drives mesh_0 to excluded.
func excludeMesh(t *testing.T, r *hopRig) {
	t.Helper()
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	r.m.Negative("mesh_0", "delivery", "!0000beef", "no_ack")
	r.after(11 * time.Second)
	if !r.excluded("mesh_0") {
		t.Fatalf("setup: mesh_0 %s, want excluded", r.state(t, "mesh_0").State)
	}
}

// An excluded link recovers only on a positive sign, never on time alone;
// it is admitted after the re-admit dwell, and a negative sign while
// recovering sends it back.
func TestHopSetReadmission(t *testing.T) {
	r := newHopRig(t, threeFree...)
	excludeMesh(t, r)
	r.after(time.Hour)
	if !r.excluded("mesh_0") {
		t.Fatal("re-admitted after an hour with no positive sign")
	}
	r.m.Positive("mesh_0", "rx")
	if s := r.state(t, "mesh_0"); s.State != HopRecovering || r.excluded("mesh_0") {
		t.Fatalf("after a positive sign: %s excluded=%v, want recovering and tried", s.State, r.excluded("mesh_0"))
	}
	r.after(2 * time.Minute)
	r.m.Negative("mesh_0", "radio", "duty_cycle", "DUTY_CYCLE_LIMIT")
	if !r.excluded("mesh_0") {
		t.Fatalf("negative while recovering: %s, want excluded", r.state(t, "mesh_0").State)
	}
	r.after(3 * time.Minute) // the duty-cycle sign ages out of the window at 120 s
	r.m.Positive("mesh_0", "rx")
	r.after(4 * time.Minute)
	if s := r.state(t, "mesh_0"); s.State != HopRecovering {
		t.Fatalf("4 min recovering: %s, want still recovering", s.State)
	}
	r.after(61 * time.Second)
	if s := r.state(t, "mesh_0"); s.State != HopAdmitted || r.excluded("mesh_0") {
		t.Fatalf("5 min recovering: %s, want admitted", s.State)
	}
}

// The floor: never fewer than two usable links, never the last free one.
// The link it keeps is the least bad (the fewest distinct signs), and a
// link it keeps never shows an exclusion it didn't have.
func TestHopSetFloor(t *testing.T) {
	// Three free links and one paid; tetra_0 has the fewest signs. mesh_0
	// and aprs_0 go, tetra_0 is kept as the last free link, still tried,
	// with a retained entry and no exclusion entry or event.
	r := newHopRig(t, threeFree...)
	for _, id := range []string{"mesh_0", "aprs_0"} {
		r.m.Negative(id, "delivery", "a", "no_ack")
		r.m.Negative(id, "delivery", "b", "no_ack")
		r.m.Negative(id, "radio", "x", "refused")
	}
	r.m.Negative("tetra_0", "delivery", "a", "no_ack")
	r.m.Negative("tetra_0", "delivery", "b", "no_ack")
	r.after(11 * time.Second)
	if !r.excluded("aprs_0") || !r.excluded("mesh_0") {
		t.Fatalf("aprs_0 %s, mesh_0 %s, want both excluded", r.state(t, "aprs_0").State, r.state(t, "mesh_0").State)
	}
	if s := r.state(t, "tetra_0"); r.excluded("tetra_0") || !s.Retained {
		t.Fatalf("tetra_0 %s retained=%v, want retained as the last free link", s.State, s.Retained)
	}
	r.after(30 * time.Second) // more evaluations: still no exclusion shown for tetra_0
	r.state(t, "tetra_0")
	r.mu.Lock()
	audits, events := append([]string{}, r.audits...), append([]string{}, r.events...)
	r.mu.Unlock()
	for _, a := range audits {
		if a == "hopset_excluded tetra_0" {
			t.Errorf("tetra_0 was shown excluded: %v", audits)
		}
	}
	n := 0
	for _, a := range audits {
		if a == "hopset_retained tetra_0" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("hopset_retained tetra_0 x%d, want x1 (%v)", n, audits)
	}
	deg := 0
	for _, e := range events {
		if e == string(hemb.EventBearerDegraded) {
			deg++
		}
	}
	if deg != 2 {
		t.Errorf("%d DEGRADED events, want 2 (mesh_0, aprs_0): %v", deg, events)
	}

	// Paid links count for the floor of two but not as free: with two
	// free links and three paid, both free ones may not go, while the
	// floor of two holds anyway.
	r3 := newHopRig(t, "mesh_0:mesh", "aprs_0:aprs", "cellular_0:cellular", "iridium_0:iridium", "iridium_imt_0:iridium_imt")
	r3.m.Negative("mesh_0", "delivery", "a", "no_ack")
	r3.m.Negative("mesh_0", "delivery", "b", "no_ack")
	r3.m.Negative("aprs_0", "delivery", "a", "no_ack")
	r3.m.Negative("aprs_0", "delivery", "b", "no_ack")
	r3.m.Negative("aprs_0", "radio", "x", "refused")
	r3.after(11 * time.Second)
	if s := r3.state(t, "mesh_0"); r3.excluded("mesh_0") || !s.Retained {
		t.Fatalf("mesh_0 %s retained=%v, want kept as the last free link (fewer signs than aprs_0)", s.State, s.Retained)
	}
	if !r3.excluded("aprs_0") {
		t.Fatalf("aprs_0 %s, want excluded", r3.state(t, "aprs_0").State)
	}

	// No free link at all (a kit on paid links only): the floor of two
	// still keeps the least-bad paid link.
	r4 := newHopRig(t, "cellular_0:cellular", "iridium_0:iridium", "iridium_imt_0:iridium_imt")
	r4.m.Negative("cellular_0", "delivery", "a", "no_ack")
	r4.m.Negative("cellular_0", "delivery", "b", "no_ack")
	r4.m.Negative("iridium_0", "delivery", "a", "no_ack")
	r4.m.Negative("iridium_0", "delivery", "b", "no_ack")
	r4.m.Negative("iridium_0", "radio", "x", "no service")
	r4.after(11 * time.Second)
	if s := r4.state(t, "cellular_0"); r4.excluded("cellular_0") || !s.Retained {
		t.Fatalf("paid only: cellular_0 %s retained=%v, want kept by the floor of two", s.State, s.Retained)
	}
	if !r4.excluded("iridium_0") {
		t.Fatalf("paid only: iridium_0 %s, want excluded", r4.state(t, "iridium_0").State)
	}

	// Two links only: neither can go.
	r2 := newHopRig(t, "mesh_0:mesh", "aprs_0:aprs")
	r2.m.Negative("mesh_0", "delivery", "a", "no_ack")
	r2.m.Negative("mesh_0", "delivery", "b", "no_ack")
	r2.after(11 * time.Second)
	if s := r2.state(t, "mesh_0"); r2.excluded("mesh_0") || !s.Retained {
		t.Fatalf("two links: mesh_0 %s retained=%v, want retained by the floor of two", s.State, s.Retained)
	}
}

// A deaf link is down: out of use whatever the floor says, not counted as
// usable, and when going down breaks the floor the least-bad excluded link
// comes back retained.
func TestHopSetDownAndFloorRepair(t *testing.T) {
	r := newHopRig(t, threeFree...)
	excludeMesh(t, r)
	r.m.Negative("aprs_0", "delivery", "a", "no_ack")
	r.m.Negative("aprs_0", "delivery", "b", "no_ack")
	r.m.Negative("aprs_0", "radio", "x", "refused")
	r.after(11 * time.Second)
	if !r.excluded("aprs_0") {
		t.Fatalf("setup: aprs_0 %s, want excluded", r.state(t, "aprs_0").State)
	}
	// tetra_0 goes deaf: free usable would be zero, so the least-bad
	// excluded free link (mesh_0: two signs against aprs_0's three) is
	// retained.
	r.deaf["tetra_0"] = true
	if ex, why := r.m.RouteExcluded("tetra_0"); !ex || why != "deaf" {
		t.Fatalf("deaf tetra_0: excluded=%v %q, want deaf", ex, why)
	}
	if s := r.state(t, "mesh_0"); r.excluded("mesh_0") || !s.Retained {
		t.Fatalf("mesh_0 %s retained=%v, want re-admitted by the floor", s.State, s.Retained)
	}
	if !r.excluded("aprs_0") {
		t.Error("aprs_0, the worse of the two, came back too")
	}
}

// Exclusions and re-admissions are audited and published as the HeMB
// bearer events; the alarm fires on a paid attempt while a free link is
// excluded, once per AlarmEvery.
func TestHopSetEventsAndAlarm(t *testing.T) {
	r := newHopRig(t, threeFree...)
	excludeMesh(t, r)
	r.m.PaidAttempt("cellular_0")
	r.m.PaidAttempt("cellular_0")
	r.m.PaidAttempt("aprs_0") // free: no alarm
	r.after(time.Second)
	r.m.Positive("mesh_0", "rx")
	r.after(5*time.Minute + time.Second)
	r.state(t, "mesh_0")
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[string]int{"hopset_excluded mesh_0": 1, "hopset_alarm mesh_0": 1, "hopset_recovering mesh_0": 1, "hopset_admitted mesh_0": 1}
	got := map[string]int{}
	for _, a := range r.audits {
		got[a]++
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("audit %q x%d, want x%d (all: %v)", k, got[k], n, r.audits)
		}
	}
	ev := map[string]int{}
	for _, e := range r.events {
		ev[e]++
	}
	if ev[string(hemb.EventBearerDegraded)] != 1 || ev[string(hemb.EventBearerRecovered)] != 1 || ev["HOPSET_ALARM"] != 1 {
		t.Errorf("events %v, want one DEGRADED, one RECOVERED, one HOPSET_ALARM", r.events)
	}
}

// A link outside the hop set (not a covered channel type) is gated by its
// deaf check only.
func TestHopSetOutsideLinkDeafOnly(t *testing.T) {
	r := newHopRig(t, threeFree...)
	if r.excluded("ax25_0") {
		t.Fatal("ax25_0 excluded with nothing against it")
	}
	r.deaf["ax25_0"] = true
	if ex, why := r.m.RouteExcluded("ax25_0"); !ex || why != "deaf" {
		t.Fatalf("deaf ax25_0: %v %q, want deaf", ex, why)
	}
}

// MESHSAT_HOPSET defaults to observing; true makes the hop set the gate.
func TestHopSetGatingFlag(t *testing.T) {
	t.Setenv("MESHSAT_HOPSET", "")
	if HopSetGating() {
		t.Error("gating with the flag unset")
	}
	t.Setenv("MESHSAT_HOPSET", "true")
	if !HopSetGating() {
		t.Error("not gating with MESHSAT_HOPSET=true")
	}
}
