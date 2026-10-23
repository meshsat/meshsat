package engine

import (
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

// fakeGate excludes fixed links with a reason (stand-in for the health
// scorer's jam and deaf verdicts). [MESHSAT-1650]
type fakeGate map[string]string

func (g fakeGate) RouteExcluded(id string) (bool, string) { return g[id] != "", g[id] }

// jamChecker marks a fixed set of interfaces jammed.
type jamChecker map[string]bool

func (j jamChecker) IsJammed(id string) bool { return j[id] }

func firstRowChannel(t *testing.T, h *intentHarness, id string) string {
	t.Helper()
	s := h.snap(id)
	if len(s.Rows) != 1 {
		t.Fatalf("round 1 has %d rows, want 1", len(s.Rows))
	}
	return s.Rows[0].Channel
}

// A jammed or deaf primary is not tried first: round 1 goes to the next
// admitted route. With every admitted route excluded the first is still
// tried, since a verdict can be wrong and a message must still leave.
func TestIntentFirstRoundSkipsAnExcludedRoute(t *testing.T) {
	for _, why := range []string{"jammed", "deaf"} {
		t.Run(why, func(t *testing.T) {
			h := newIntentHarness(t)
			c := h.contact(meshAddr(1), lxmfAddr(2))
			h.d.SetRouteGate(fakeGate{"mesh_0": why})
			if got := firstRowChannel(t, h, h.submit(c, "one", directory.StrategyOrderedFallback)); got != "lxmf_0" {
				t.Errorf("%s primary: round 1 on %s, want lxmf_0", why, got)
			}
			h.d.SetRouteGate(fakeGate{"mesh_0": why, "lxmf_0": why})
			if got := firstRowChannel(t, h, h.submit(c, "two", directory.StrategyOrderedFallback)); got != "mesh_0" {
				t.Errorf("all %s: round 1 on %s, want mesh_0 (the first admitted)", why, got)
			}
			h.d.SetRouteGate(nil)
			if got := firstRowChannel(t, h, h.submit(c, "three", directory.StrategyOrderedFallback)); got != "mesh_0" {
				t.Errorf("no gate: round 1 on %s, want mesh_0", got)
			}
		})
	}
}

// ALL_BEARERS sends on every admitted route the gate lets through, or on
// all of them when it lets none through; PRIMARY_ONLY is the operator's
// choice of route and sends there whatever the gate says.
func TestIntentFirstRoundGateByStrategy(t *testing.T) {
	h := newIntentHarness(t)
	c := h.contact(meshAddr(1), lxmfAddr(2))
	channels := func(id string) []string {
		var out []string
		for _, r := range h.snap(id).Rows {
			out = append(out, r.Channel)
		}
		return out
	}
	h.d.SetRouteGate(fakeGate{"mesh_0": "jammed"})
	if got := channels(h.submit(c, "a", directory.StrategyAllBearers)); len(got) != 1 || got[0] != "lxmf_0" {
		t.Errorf("ALL_BEARERS, mesh jammed: round 1 on %v, want [lxmf_0]", got)
	}
	if got := channels(h.submit(c, "b", directory.StrategyPrimaryOnly)); len(got) != 1 || got[0] != "mesh_0" {
		t.Errorf("PRIMARY_ONLY, mesh jammed: round 1 on %v, want [mesh_0]", got)
	}
	h.d.SetRouteGate(fakeGate{"mesh_0": "jammed", "lxmf_0": "deaf"})
	if got := channels(h.submit(c, "c", directory.StrategyAllBearers)); len(got) != 2 {
		t.Errorf("ALL_BEARERS, all excluded: round 1 on %v, want both routes", got)
	}
}

// The walk's next route is read through usable, so an excluded link is
// not the next one either.
func TestIntentEligibilityReadsTheGate(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(1), lxmfAddr(2)), "hi", directory.StrategyOrderedFallback)
	in := h.snap(id).Intent
	idx := func(iface string) int {
		for _, p := range in.RoutePlan {
			if p.InterfaceID == iface {
				return p.RouteIndex
			}
		}
		t.Fatalf("no route on %s", iface)
		return 0
	}
	if !h.esc.eligibility(&in)[idx("lxmf_0")] {
		t.Fatal("lxmf_0 not eligible without a gate")
	}
	h.d.SetRouteGate(fakeGate{"lxmf_0": "jammed"})
	if h.esc.eligibility(&in)[idx("lxmf_0")] {
		t.Error("a jammed lxmf_0 is still eligible")
	}
}

// The health scorer is the gate: a fresh jam verdict or a deaf receiver
// excludes a link, nothing else does (never the composite score).
func TestHealthScorerRouteExcluded(t *testing.T) {
	h := NewHealthScorer(newHealthDB(t))
	if ex, _ := h.RouteExcluded("mesh_0"); ex {
		t.Fatal("excluded with no checkers")
	}
	h.SetSpectrumChecker(jamChecker{"mesh_0": true})
	h.SetReceiveChecker(deafChecker{"aprs_0": true})
	for id, want := range map[string]string{"mesh_0": "jammed", "aprs_0": "deaf", "tetra_0": ""} {
		ex, why := h.RouteExcluded(id)
		if ex != (want != "") || why != want {
			t.Errorf("%s: excluded=%v %q, want %q", id, ex, why, want)
		}
	}
}

// A failover group skips a jammed member as it skips a deaf one.
func TestFailoverResolver_JammedPrimarySkipped(t *testing.T) {
	fr, ifaceMgr, db := setupFailoverTest(t)
	db.InsertFailoverGroup(&database.FailoverGroup{ID: "sat", Label: "sat", Mode: "priority"})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat", InterfaceID: "iridium_0", Priority: 1})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat", InterfaceID: "iridium_1", Priority: 2})
	ifaceMgr.mu.Lock()
	ifaceMgr.states["iridium_0"].state = StateOnline
	ifaceMgr.states["iridium_1"].state = StateOnline
	ifaceMgr.mu.Unlock()
	fr.SetSpectrumChecker(jamChecker{"iridium_0": true})
	if got := fr.Resolve("sat"); got != "iridium_1" {
		t.Errorf("jammed primary: resolved %s, want iridium_1", got)
	}
	fr.SetSpectrumChecker(jamChecker{})
	if got := fr.Resolve("sat"); got != "iridium_0" {
		t.Errorf("jam over: resolved %s, want iridium_0", got)
	}
}
