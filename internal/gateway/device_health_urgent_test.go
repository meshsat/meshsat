package gateway

import (
	"testing"
	"time"
)

// A radio silent since its port opened cannot be reached by the soft rungs
// (its USB stack is wedged). An urgent miss starts the ladder at once, and
// with the soft rungs skipped the first rung to run is the hub-port cut.
// [MESHSAT-850]
func TestDeviceHealth_UrgentMissGoesStraightToHardRung(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{})
	silent := func() bool { return true }
	h.dh.Register(h.target("mesh", []string{"mesh_0"},
		h.step("mesh", HealLevelSoft, "reconnect", 75*time.Second, silent),
		h.step("mesh", HealLevelSoft, "dtr reboot", 75*time.Second, silent),
		h.step("mesh", HealLevelDevice, "admin reboot", 60*time.Second, silent),
		h.step("mesh", HealLevelHard, "power cycle", 150*time.Second, nil),
	))
	h.set("mesh", ProbeResult{Detail: "radio silent", Urgent: true})
	h.tick(t)

	if n := h.count("mesh", HealLevelHard, "power cycle"); n != 1 {
		t.Fatalf("hard rung ran %d times after one urgent miss, want 1", n)
	}
	for _, s := range []struct {
		level byte
		name  string
	}{{HealLevelSoft, "reconnect"}, {HealLevelSoft, "dtr reboot"}, {HealLevelDevice, "admin reboot"}} {
		if n := h.count("mesh", s.level, s.name); n != 0 {
			t.Fatalf("skipped rung %q ran %d times", s.name, n)
		}
	}
	if got := h.state("mesh"); got != HealthStateHealing {
		t.Fatalf("state %q, want healing", got)
	}
}

func TestDeviceHealth_PlainMissStillWaitsForMisses(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{})
	h.dh.Register(h.target("mesh", []string{"mesh_0"}, meshLadder(h)...))
	h.miss("mesh")
	h.tick(t)
	h.advance(30 * time.Second)
	h.tick(t)
	for _, name := range []string{"reconnect", "admin reboot", "power cycle"} {
		for _, level := range []byte{HealLevelSoft, HealLevelDevice, HealLevelHard} {
			if n := h.count("mesh", level, name); n != 0 {
				t.Fatalf("rung %q ran after two plain misses", name)
			}
		}
	}
}

// Urgent does not bypass the hard-reset budget: with the budget spent the
// target fails instead of cutting again.
func TestDeviceHealth_UrgentMissRespectsHardBudget(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{})
	tg := h.target("mesh", []string{"mesh_0"},
		h.step("mesh", HealLevelSoft, "reconnect", 75*time.Second, func() bool { return true }),
		h.step("mesh", HealLevelHard, "power cycle", 150*time.Second, nil),
	)
	tg.HardBudget = 1
	h.dh.Register(tg)

	h.set("mesh", ProbeResult{Detail: "radio silent", Urgent: true})
	h.tick(t)
	h.ok("mesh")
	h.advance(160 * time.Second)
	h.tick(t)
	h.set("mesh", ProbeResult{Detail: "radio silent", Urgent: true})
	h.advance(60 * time.Second)
	h.tick(t)

	if n := h.count("mesh", HealLevelHard, "power cycle"); n != 1 {
		t.Fatalf("hard rung ran %d times with a budget of 1, want 1", n)
	}
	if got := h.state("mesh"); got != HealthStateFailed {
		t.Fatalf("state %q, want failed", got)
	}
}

// InterfaceHealth maps a gateway instance to its device target, for status
// APIs that show health beside the link flag. [MESHSAT-1064]
func TestDeviceHealth_InterfaceHealth(t *testing.T) {
	h := newDHHarness(DeviceHealthConfig{})
	h.dh.Register(h.target("cellular", []string{"cellular_0", "sms_0"},
		h.step("cellular", HealLevelSoft, "reconnect", 60*time.Second, nil)))
	h.dh.RegisterExternal("aprs", []string{"aprs_0", "ax25_0"}, func() (string, string) { return HealthStateOK, "receive ok" })

	h.ok("cellular")
	h.tick(t)
	if state, _, ok := h.dh.InterfaceHealth("sms_0"); !ok || state != HealthStateOK {
		t.Fatalf("sms_0: state %q ok %v", state, ok)
	}
	if state, detail, ok := h.dh.InterfaceHealth("ax25_0"); !ok || state != HealthStateOK || detail != "receive ok" {
		t.Fatalf("external target: %q %q %v", state, detail, ok)
	}
	if _, _, ok := h.dh.InterfaceHealth("zigbee_0"); ok {
		t.Fatal("uncovered interface reported as covered")
	}

	h.miss("cellular")
	for range 3 {
		h.tick(t)
		h.advance(30 * time.Second)
	}
	if state, detail, _ := h.dh.InterfaceHealth("cellular_0"); state != HealthStateHealing || detail == "" {
		t.Fatalf("cellular_0 after three misses: %q %q, want healing with a detail", state, detail)
	}
}
