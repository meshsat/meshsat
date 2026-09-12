package gateway

import (
	"context"
	"testing"
	"time"
)

// A probe that never returns must not take the target's health with it.
//
// parallax, 8 September 2026: the cellular probe parked on an unbounded send to
// a wedged I/O loop. tick() skips a target while it is still probing, and the
// ladder is only ever evaluated from inside the probe's own completion path, so
// nothing counted a miss, nothing escalated, and the level 2 and level 3 rungs
// that exist precisely for a wedged modem never ran. The dashboard went on
// reporting the modem connected while both SMS lanes were dead. [MESHSAT-986]
func TestDeviceHealth_StuckProbeDoesNotFreezeTheLadder(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Tick: time.Second, Misses: 1})

	release := make(chan struct{})
	defer close(release)

	stuck := HealthTarget{
		Name:     "cellular",
		IfaceIDs: []string{"cellular_0"},
		// Ignores its context, exactly as DirectCellTransport.Probe used to,
		// and blocks for longer than any test would wait.
		Probe: func(ctx context.Context) ProbeResult {
			<-release
			return ProbeResult{OK: true}
		},
		ProbeTimeout: 100 * time.Millisecond,
		Steps: []HealStep{
			h.step("cellular", 1, "serial reconnect", 0, nil),
			h.step("cellular", 2, "AT+CFUN=1,1", 0, nil),
		},
	}
	h.dh.Register(stuck)

	// Two evaluations: the first probe overruns and must be recorded as a miss
	// rather than leaving the target probing for ever.
	h.tick(t)
	h.advance(time.Minute)
	h.tick(t)

	h.dh.mu.Lock()
	ts := h.dh.targets["cellular"]
	probing := ts.probing
	misses := ts.misses
	h.dh.mu.Unlock()

	if probing {
		t.Error("target still marked probing after its probe overran, so tick() will skip it for ever")
	}
	if misses == 0 {
		t.Error("a probe that never answered was not counted as a miss")
	}
	if h.count("cellular", 1, "serial reconnect") == 0 {
		t.Error("the ladder never advanced past the stuck probe; that is the freeze this fixes")
	}
}

// The ordinary path must keep working: a probe that answers inside its timeout
// is unaffected by the timeout machinery around it.
func TestDeviceHealth_FastProbeStillReportsNormally(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{Tick: time.Second, Misses: 2})

	tgt := h.target("mesh", []string{"mesh_0"}, h.step("mesh", 1, "reconnect", 0, nil))
	tgt.ProbeTimeout = 2 * time.Second
	h.dh.Register(tgt)

	h.ok("mesh")
	h.tick(t)

	h.dh.mu.Lock()
	ts := h.dh.targets["mesh"]
	state, misses := ts.state, ts.misses
	h.dh.mu.Unlock()

	if misses != 0 {
		t.Errorf("misses = %d, want 0 for a probe that answered", misses)
	}
	if state != "ok" {
		t.Errorf("state = %q, want ok", state)
	}
}
