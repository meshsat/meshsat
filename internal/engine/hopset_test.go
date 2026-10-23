package engine

import (
	"fmt"
	"strings"
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
	return newHopRigCfg(t, DefaultHopSetConfig(), links...)
}

// readmit and window are the defaults' re-admission dwell and sign window.
var (
	readmit = DefaultHopSetConfig().ReadmitDwell
	window  = DefaultHopSetConfig().TriggerWindow
)

func newHopRigCfg(t *testing.T, cfg HopSetConfig, links ...string) *hopRig {
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
	r.m = NewHopSetManager(cfg, func() ([]database.Interface, error) { return ifaces, nil }, HopOn)
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
	r.after(window + time.Second)
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
// recovering sends it back. recovery_dwell_gates_ordinary_traffic: a
// recovering link carries no ordinary traffic until it is admitted.
func TestHopSetReadmission(t *testing.T) {
	r := newHopRig(t, threeFree...)
	excludeMesh(t, r)        // exclusion 1
	for i := 0; i < 3; i++ { // an hour, in three evaluations
		r.after(20 * time.Minute)
		if s := r.state(t, "mesh_0"); s.State != HopExcluded || !r.excluded("mesh_0") {
			t.Fatalf("%d min with no positive sign: %s excluded=%v, want still excluded", 20*(i+1), s.State, r.excluded("mesh_0"))
		}
	}
	r.m.Positive("mesh_0", "rx")
	if s := r.state(t, "mesh_0"); s.State != HopRecovering || !r.excluded("mesh_0") {
		t.Fatalf("after a positive sign: %s excluded=%v, want recovering and not yet tried", s.State, r.excluded("mesh_0"))
	}
	r.after(2 * time.Minute)
	r.m.Negative("mesh_0", "radio", "duty_cycle", "DUTY_CYCLE_LIMIT") // exclusion 2, over an hour after 1
	if s := r.state(t, "mesh_0"); !r.excluded("mesh_0") || s.ReadmitAfter != readmit.String() {
		t.Fatalf("negative while recovering: %s readmit %s, want excluded with the base dwell %v", s.State, s.ReadmitAfter, readmit)
	}
	r.after(time.Second)
	r.m.Positive("mesh_0", "rx")
	r.after(4 * time.Minute)
	if !r.excluded("mesh_0") {
		t.Fatal("tried 4 min into recovering, before the re-admit dwell")
	}
	r.after(readmit - 4*time.Minute + time.Second)
	if s := r.state(t, "mesh_0"); s.State != HopAdmitted || r.excluded("mesh_0") {
		t.Fatalf("%v recovering: %s, want admitted", readmit, s.State)
	}
	// Exclusion 3, within the hour of 2: the next recovery takes twice as
	// long (D-10).
	r.m.Negative("mesh_0", "delivery", "c", "no_ack")
	r.m.Negative("mesh_0", "delivery", "d", "no_ack")
	r.after(11 * time.Second)
	if s := r.state(t, "mesh_0"); s.State != HopExcluded || s.ReadmitAfter != (2*readmit).String() || s.Flaps != 1 {
		t.Fatalf("repeat within the hour: %s readmit %s flaps %d, want excluded, %v, 1", s.State, s.ReadmitAfter, s.Flaps, 2*readmit)
	}
	r.after(time.Second)
	r.m.Positive("mesh_0", "rx")
	r.after(readmit + time.Minute)
	if s := r.state(t, "mesh_0"); s.State != HopRecovering || !r.excluded("mesh_0") {
		t.Fatalf("%v recovering after a repeat: %s, want still recovering (dwell doubled)", readmit+time.Minute, s.State)
	}
	r.after(readmit)
	if s := r.state(t, "mesh_0"); s.State != HopAdmitted || r.excluded("mesh_0") {
		t.Fatalf("%v recovering after a repeat: %s, want admitted", 2*readmit+time.Minute, s.State)
	}
}

// The re-admission dwell doubles for each exclusion within an hour of the
// one before, up to the cap, and starts again from the base once an hour
// passes between exclusions (as route flap damping does, D-10).
func TestHopSetReadmitDoublesUpToTheCapThenResets(t *testing.T) {
	r := newHopRig(t, threeFree...)
	var dwells []string
	for i := 0; i < 5; i++ {
		r.m.Negative("mesh_0", "delivery", fmt.Sprintf("a%d", i), "no_ack")
		r.m.Negative("mesh_0", "delivery", fmt.Sprintf("b%d", i), "no_ack")
		r.after(11 * time.Second)
		s := r.state(t, "mesh_0")
		if s.State != HopExcluded {
			t.Fatalf("cycle %d: %s, want excluded", i, s.State)
		}
		dwells = append(dwells, s.ReadmitAfter)
		r.after(time.Second)
		r.m.Positive("mesh_0", "rx")
		d, _ := time.ParseDuration(s.ReadmitAfter)
		r.after(d + time.Second)
		if s := r.state(t, "mesh_0"); s.State != HopAdmitted {
			t.Fatalf("cycle %d: %s after its %v dwell, want admitted", i, s.State, d)
		}
	}
	want := []string{"10m0s", "20m0s", "40m0s", "1h0m0s", "10m0s"}
	if strings.Join(dwells, ",") != strings.Join(want, ",") {
		t.Fatalf("dwells %v, want %v", dwells, want)
	}
}

// The radio's own refusals at least a minute apart are separate signs; two
// in quick succession are one (D-10).
func TestHopSetRepeatedRadioRefusalsCount(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.m.Negative("tetra_0", "radio", "not_submitted", "refused")
	r.after(30 * time.Second)
	r.m.Negative("tetra_0", "radio", "not_submitted", "refused")
	r.after(11 * time.Second)
	if s := r.state(t, "tetra_0"); s.State != HopSuspect {
		t.Fatalf("two refusals 30 s apart: %s, want suspect (one sign)", s.State)
	}
	r.after(20 * time.Second) // the third refusal comes 61 s after the first
	r.m.Negative("tetra_0", "radio", "not_submitted", "refused")
	r.after(11 * time.Second)
	if s := r.state(t, "tetra_0"); s.State != HopExcluded {
		t.Fatalf("refusals 61 s apart: %s, want excluded (two signs)", s.State)
	}
}

// hearEvery feeds the link a frame every gap for span; the clock ends at
// the last frame.
func hearEvery(r *hopRig, id string, gap, span time.Duration) {
	for d := time.Duration(0); d < span; d += gap {
		if d > 0 {
			r.after(gap)
		}
		r.m.Positive(id, "rx")
	}
}

// A link that usually hears traffic every minute and hears nothing for 10
// minutes has a silence sign: one sign, suspect; with a delivery failure,
// two, excluded. The next frame heard ends it. A short burst followed by
// the link's usual quiet is not silence (D-10).
func TestHopSetSilenceSign(t *testing.T) {
	r := newHopRig(t, threeFree...)
	hearEvery(r, "mesh_0", time.Minute, 70*time.Minute)
	r.after(9 * time.Minute)
	if s := r.state(t, "mesh_0"); len(s.Signs) != 0 {
		t.Fatalf("9 min quiet: signs %+v, want none yet", s.Signs)
	}
	r.after(time.Minute)
	s := r.state(t, "mesh_0")
	if s.State != HopSuspect || len(s.Signs) != 1 || s.Signs[0].Source != "silence" {
		t.Fatalf("10 min quiet: %s signs %+v, want suspect on a silence sign", s.State, s.Signs)
	}
	r.m.Negative("mesh_0", "delivery", "!0000abcd", "no_ack")
	r.after(11 * time.Second)
	if s := r.state(t, "mesh_0"); s.State != HopExcluded {
		t.Fatalf("silence + a delivery failure: %s, want excluded", s.State)
	}
	r.m.Positive("mesh_0", "rx")
	if s := r.state(t, "mesh_0"); s.State != HopRecovering || len(s.Signs) != 0 {
		t.Fatalf("a frame heard: %s signs %+v, want recovering with no signs", s.State, s.Signs)
	}

	// A TETRA test burst: 20 frames in 10 minutes, then the usual quiet.
	r2 := newHopRig(t, threeFree...)
	hearEvery(r2, "tetra_0", 30*time.Second, 10*time.Minute)
	r2.after(30 * time.Minute)
	if s := r2.state(t, "tetra_0"); len(s.Signs) != 0 {
		t.Fatalf("quiet after a burst: signs %+v, want none", s.Signs)
	}
}

// The floor exception to the dwell: when the floor needs a link, a
// silence_history_survives_receive_volume: a link that hears 20 frames a
// minute, three of them in one second, keeps its whole hour (R48-S1), so
// ten quiet minutes are still a silence sign; the history is one mark per
// second, bounded by time and counting every frame.
func TestHopSetSilenceHistorySurvivesReceiveVolume(t *testing.T) {
	r := newHopRig(t, threeFree...)
	for i := 0; i < 70; i++ {
		start := r.now
		for k := 0; k < 3; k++ {
			r.m.Positive("mesh_0", "rx")
		}
		for j := 0; j < 17; j++ {
			r.after(3 * time.Second)
			r.m.Positive("mesh_0", "rx")
		}
		r.now = start.Add(time.Minute)
	}
	r.m.mu.Lock()
	l := r.m.links["mesh_0"]
	marks, frames := len(l.rx), 0
	for _, m := range l.rx {
		frames += m.n
	}
	first := l.rx[0].at
	r.m.mu.Unlock()
	if marks != 70*18 || frames != 70*20 || r.now.Sub(first) < 69*time.Minute {
		t.Fatalf("history: %d marks, %d frames, from %s before now; want 1260, 1400, the whole 70 min", marks, frames, r.now.Sub(first))
	}
	r.after(9 * time.Minute)
	if s := r.state(t, "mesh_0"); len(s.Signs) != 0 {
		t.Fatalf("9 min quiet: signs %+v, want none yet", s.Signs)
	}
	r.after(time.Minute)
	if s := r.state(t, "mesh_0"); s.State != HopSuspect || len(s.Signs) != 1 || s.Signs[0].Source != "silence" {
		t.Fatalf("10 min quiet after a busy hour: %s signs %+v, want suspect on a silence sign", s.State, s.Signs)
	}
	// Another 100 minutes of the same traffic: the history stays within 90
	// minutes, one mark per second at most.
	for i := 0; i < 100; i++ {
		start := r.now
		for j := 0; j < 20; j++ {
			r.m.Positive("mesh_0", "rx")
			r.after(time.Second)
		}
		r.now = start.Add(time.Minute)
	}
	r.m.mu.Lock()
	n, span := len(r.m.links["mesh_0"].rx), r.m.links["mesh_0"].lastRx().Sub(r.m.links["mesh_0"].rx[0].at)
	r.m.mu.Unlock()
	if n > 90*60 || span > 90*time.Minute {
		t.Fatalf("history after 170 min: %d marks over %s, want at most 5400 over 90 min", n, span)
	}

	// Ten bursts of three frames, each in one second, six minutes apart:
	// 30 frames in six stretches make a busy link though they are ten marks.
	r3 := newHopRig(t, threeFree...)
	for i := 0; i < 10; i++ {
		for k := 0; k < 3; k++ {
			r3.m.Positive("tetra_0", "rx")
		}
		r3.after(6 * time.Minute)
	}
	r3.after(4 * time.Minute)
	if s := r3.state(t, "tetra_0"); len(s.Signs) != 1 || s.Signs[0].Source != "silence" {
		t.Fatalf("30 frames in bursts, then 10 min quiet: signs %+v, want a silence sign", s.Signs)
	}
}

// recovering one is kept before an excluded one with the same evidence.
func TestHopSetRecoveringKeptByTheFloor(t *testing.T) {
	r := newHopRig(t, "mesh_0:mesh", "aprs_0:aprs", "cellular_0:cellular")
	excludeMesh(t, r)
	r.after(time.Second)
	r.m.Positive("mesh_0", "rx") // mesh_0 recovering, signs cleared
	r.m.Negative("aprs_0", "delivery", "a", "no_ack")
	r.m.Negative("aprs_0", "delivery", "b", "no_ack")
	r.after(11 * time.Second)
	// aprs_0 would be excluded and leave no free link usable: the floor
	// keeps one, and the recovering mesh_0 (no signs) is the least bad.
	if s := r.state(t, "mesh_0"); s.State != HopRecovering || !s.Retained || r.excluded("mesh_0") {
		t.Fatalf("mesh_0 %s retained=%v excluded=%v, want recovering, kept by the floor, tried", s.State, s.Retained, r.excluded("mesh_0"))
	}
	if !r.excluded("aprs_0") {
		t.Fatalf("aprs_0 %s, want excluded", r.state(t, "aprs_0").State)
	}
}

// cleared_signs_stay_cleared: signs a positive sign answered never count
// again, not toward a later exclusion, not in the summary, not in the
// floor's ranking.
func TestHopSetClearedSignsStayCleared(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.m.Negative("mesh_0", "delivery", "a", "no_ack")
	r.m.Negative("mesh_0", "delivery", "b", "no_ack")
	r.m.Positive("mesh_0", "rx") // answers both
	r.m.Negative("mesh_0", "delivery", "c", "no_ack")
	r.after(30 * time.Second)
	s := r.state(t, "mesh_0")
	if s.State != HopSuspect || r.excluded("mesh_0") || len(s.Signs) != 1 || s.Signs[0].Key != "c" {
		t.Fatalf("mesh_0 %s excluded=%v signs %+v, want suspect on the one unanswered sign", s.State, r.excluded("mesh_0"), s.Signs)
	}
}

// absent_device_does_not_support_floor: a device known to be absent is
// down, out of use and not counted for the floor, so the last free link
// that is actually there is kept. An unknown answer is not absence.
func TestHopSetAbsentDeviceDoesNotSupportFloor(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.m.SetAvailability(func(id string) (bool, bool) {
		switch id {
		case "tetra_0":
			return false, true // the radio is known to be gone
		case "aprs_0":
			return false, false // nothing known
		}
		return true, true
	})
	r.m.Negative("mesh_0", "delivery", "a", "no_ack")
	r.m.Negative("mesh_0", "delivery", "b", "no_ack")
	r.m.Negative("aprs_0", "delivery", "a", "no_ack")
	r.m.Negative("aprs_0", "delivery", "b", "no_ack")
	r.m.Negative("aprs_0", "radio", "x", "refused")
	r.after(11 * time.Second)
	if ex, why := r.m.RouteExcluded("tetra_0"); !ex || why != "absent" {
		t.Fatalf("tetra_0: excluded=%v %q, want absent", ex, why)
	}
	if s := r.state(t, "aprs_0"); s.Absent {
		t.Fatal("aprs_0 absent on an unknown answer")
	}
	// A device-less link is never absent, whatever the answer.
	r2 := newHopRig(t, "lxmf_0:lxmf", "hub:hub", "mesh_0:mesh")
	r2.m.SetAvailability(func(string) (bool, bool) { return false, true })
	if r2.state(t, "lxmf_0").Absent || r2.state(t, "hub").Absent || !r2.state(t, "mesh_0").Absent {
		t.Fatal("device-less links marked absent, or the mesh not")
	}
	if s := r.state(t, "mesh_0"); r.excluded("mesh_0") || !s.Retained {
		t.Fatalf("mesh_0 %s retained=%v, want kept as the last free link actually there", s.State, s.Retained)
	}
	if !r.excluded("aprs_0") {
		t.Fatalf("aprs_0 %s, want excluded (more signs than mesh_0)", r.state(t, "aprs_0").State)
	}
}

// every_net_state_change_is_audited: suspicion and clearance too, once
// each, after the floor.
func TestHopSetEveryNetStateChangeIsAudited(t *testing.T) {
	r := newHopRig(t, threeFree...)
	r.m.Negative("mesh_0", "delivery", "a", "no_ack") // suspect
	r.m.Positive("mesh_0", "rx")                      // admitted
	excludeMesh(t, r)                                 // suspect, excluded
	r.after(time.Second)
	r.m.Positive("mesh_0", "rx") // recovering
	r.after(readmit + time.Second)
	r.state(t, "mesh_0") // admitted
	r.mu.Lock()
	defer r.mu.Unlock()
	var got []string
	for _, a := range r.audits {
		if strings.HasSuffix(a, " mesh_0") {
			got = append(got, strings.TrimSuffix(a, " mesh_0"))
		}
	}
	want := []string{"hopset_suspect", "hopset_admitted", "hopset_suspect", "hopset_excluded", "hopset_recovering", "hopset_admitted"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audits %v, want %v", got, want)
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
	r.after(readmit + time.Second)
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

// The switch reads off, watch and on in any case; anything else is refused,
// and a bad mode at construction is off.
func TestHopSetModeParse(t *testing.T) {
	for in, want := range map[string]HopMode{"off": HopOff, "watch": HopWatch, "on": HopOn, " On ": HopOn, "WATCH": HopWatch} {
		if m, ok := ParseHopMode(in); !ok || m != want {
			t.Errorf("ParseHopMode(%q) = %q %v, want %q", in, m, ok, want)
		}
	}
	for _, in := range []string{"", "true", "gate", "1"} {
		if m, ok := ParseHopMode(in); ok || m != HopOff {
			t.Errorf("ParseHopMode(%q) = %q %v, want off, false", in, m, ok)
		}
	}
	if m := NewHopSetManager(DefaultHopSetConfig(), nil, "bogus").Mode(); m != HopOff {
		t.Errorf("a bad mode at construction: %q, want off", m)
	}
}

// Between two links out of use with the same evidence, the floor keeps the
// recovering one (a positive sign since its exclusion) before the excluded
// one, whatever their positive signs' order.
func TestHopSetFloorPrefersRecoveringOnATie(t *testing.T) {
	cfg := DefaultHopSetConfig()
	cfg.TriggerWindow, cfg.ReadmitDwell = 2*time.Minute, 5*time.Minute // a tie needs the signs to age out first
	r := newHopRigCfg(t, cfg, "mesh_0:mesh", "aprs_0:aprs", "tetra_0:tetra")
	excludeMesh(t, r) // t=11: mesh excluded (aprs, tetra usable)
	r.after(time.Second)
	r.m.Positive("mesh_0", "rx") // mesh recovering, no signs
	r.after(time.Second)
	r.m.Positive("tetra_0", "rx") // tetra's positive is the newer
	r.after(time.Second)
	r.m.Negative("tetra_0", "delivery", "a", "no_ack")
	r.m.Negative("tetra_0", "delivery", "b", "no_ack")
	r.after(11 * time.Second) // tetra excluded; the floor keeps mesh (no signs)
	if s := r.state(t, "mesh_0"); !s.Retained {
		t.Fatalf("setup: mesh_0 %s retained=%v, want kept", s.State, s.Retained)
	}
	r.after(2 * time.Minute) // tetra's signs age out: a tie, no signs either side
	if s := r.state(t, "tetra_0"); s.State != HopExcluded || len(s.Signs) != 0 {
		t.Fatalf("setup: tetra_0 %s signs %+v, want excluded with none", s.State, s.Signs)
	}
	if m, tt := r.state(t, "mesh_0"), r.state(t, "tetra_0"); !m.Retained || tt.Retained {
		t.Fatalf("on a tie: mesh_0 (recovering) retained=%v, tetra_0 (excluded, newer positive) retained=%v; want the recovering one kept", m.Retained, tt.Retained)
	}
}

// A change of retention or of down alone, the state unchanged, is audited.
func TestHopSetRetentionAndDownChangesAreAudited(t *testing.T) {
	r := newHopRig(t, "mesh_0:mesh", "aprs_0:aprs", "tetra_0:tetra")
	excludeMesh(t, r)
	r.deaf["aprs_0"] = true // aprs down: tetra alone usable, so the floor keeps mesh
	r.state(t, "mesh_0")
	r.mu.Lock()
	defer r.mu.Unlock()
	got := map[string]bool{}
	for _, a := range r.audits {
		got[a] = true
	}
	if !got["hopset_down aprs_0"] || !got["hopset_retained mesh_0"] {
		t.Fatalf("audits %v, want hopset_down aprs_0 and hopset_retained mesh_0", r.audits)
	}
}

// The defaults are the owner's ruling (design D-10, 23 Oct 2026).
func TestHopSetDefaultsAreTheRuling(t *testing.T) {
	c := DefaultHopSetConfig()
	got := []time.Duration{c.TriggerWindow, c.DropDwell, c.ReadmitDwell, c.ReadmitMax, c.FlapMemory, c.RadioRepeat, c.SilenceAfter, c.AlarmEvery}
	want := []time.Duration{15 * time.Minute, 10 * time.Second, 10 * time.Minute, time.Hour, time.Hour, time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("setting %d = %v, want %v", i, got[i], want[i])
		}
	}
	if c.MinSet != 2 {
		t.Errorf("MinSet = %d, want 2", c.MinSet)
	}
}
