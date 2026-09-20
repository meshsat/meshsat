package transport

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPortGuard_ReserveAndRelease covers the plain reservation lifecycle
// and the symlink resolution a /dev/serial/by-id path needs. [MESHSAT-1265]
func TestPortGuard_ReserveAndRelease(t *testing.T) {
	clearPortGuard()
	defer clearPortGuard()

	dir := t.TempDir()
	real := filepath.Join(dir, "ttyUSB0")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "usb-Silicon_Labs_CP2102N-if00-port0")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if owner := PortOwner(real); owner != "" {
		t.Fatalf("a free port has an owner: %q", owner)
	}
	if !PortAvailableTo(real, "zigbee") {
		t.Fatal("a free port is not available")
	}

	// Reserved by its symlink, found by its real name.
	ReservePort(link, PortOwnerExcluded)
	if owner := PortOwner(real); owner != PortOwnerExcluded {
		t.Fatalf("real path owner = %q, want %q", owner, PortOwnerExcluded)
	}
	if PortAvailableTo(real, "zigbee") {
		t.Fatal("an excluded port is available to zigbee")
	}
	UnreservePort(link)

	// Reserved by its real name, found by its symlink.
	ReservePort(real, string(RoleMeshtastic))
	if owner := PortOwner(link); owner != string(RoleMeshtastic) {
		t.Fatalf("symlink owner = %q, want %q", owner, RoleMeshtastic)
	}
	if !PortAvailableTo(real, string(RoleMeshtastic)) {
		t.Fatal("the owner may not open its own port")
	}
	if PortAvailableTo(real, string(RoleZigBee)) {
		t.Fatal("another role may open a claimed port")
	}

	UnreservePort(real)
	if owner := PortOwner(real); owner != "" {
		t.Fatalf("owner after release = %q, want none", owner)
	}
}

// TestPortGuard_FilterUnownedPorts: a scanner's candidate list loses the
// ports that belong to somebody and keeps the rest. [MESHSAT-1265]
func TestPortGuard_FilterUnownedPorts(t *testing.T) {
	clearPortGuard()
	defer clearPortGuard()

	ports := []string{"/dev/ttyUSB0", "/dev/ttyUSB1", "/dev/ttyACM0"}
	if got := FilterUnownedPorts(ports); len(got) != 3 {
		t.Fatalf("no reservations, got %d ports, want 3", len(got))
	}

	ReservePort("/dev/ttyUSB1", PortOwnerExcluded)
	ReservePort("/dev/ttyACM0", string(RoleMeshtastic))
	got := FilterUnownedPorts(ports)
	if len(got) != 1 || got[0] != "/dev/ttyUSB0" {
		t.Fatalf("filtered = %v, want [/dev/ttyUSB0]", got)
	}
}

// TestSerialProbes_RefuseAForeignPort: every serial probe consults the
// guard before it opens anything. The counter is what makes this a real
// assertion — a probe that stops calling guardProbe stops incrementing it,
// while the boolean would stay false either way on a path that cannot be
// opened. [MESHSAT-1265]
func TestSerialProbes_RefuseAForeignPort(t *testing.T) {
	clearPortGuard()
	defer clearPortGuard()

	const port = "/dev/ttyUSB-owned-by-aprs"
	ReservePort(port, PortOwnerExcluded)

	probes := []struct {
		name string
		fn   func(string) bool
	}{
		{"ProbeZNP", ProbeZNP},
		{"ProbeMeshtastic", ProbeMeshtastic},
		{"probeAT", probeAT},
		{"probeJSPR", probeJSPR},
		{"probeJSPRSerial", probeJSPRSerial},
		{"probeCellularAT", probeCellularAT},
	}
	for i, p := range probes {
		if p.fn(port) {
			t.Errorf("%s claimed a port that belongs to another device", p.name)
		}
		if got, want := portGuardBlocks.Load(), int64(i+1); got != want {
			t.Errorf("%s did not consult the port guard: blocks = %d, want %d", p.name, got, want)
		}
	}
}

// TestFindZigBeePort_NeverProbesAnotherDevicesPort is the regression test
// for the bug this guard exists for: a ZigBee coordinator shares
// 10c4:ea60 with the PicoAPRS V4's CP2102N, so once the dongle is off the
// bus the only candidate left is the APRS TNC, and ProbeZNP opens what it
// is handed. [MESHSAT-1265]
func TestFindZigBeePort_NeverProbesAnotherDevicesPort(t *testing.T) {
	clearPortGuard()
	defer clearPortGuard()

	const (
		tnc    = "/dev/ttyUSB0" // PicoAPRS V4, the APRS gateway's alone
		dongle = "/dev/ttyUSB1" // SONOFF ZBDongle-P, same VID:PID
	)
	vidpidOf := func(string) string { return "10c4:ea60" }

	// The TNC is excluded the way main.go excludes it at startup.
	ReservePort(tnc, PortOwnerExcluded)

	var probed []string
	probe := func(port string) bool {
		probed = append(probed, port)
		return port == dongle // only the real coordinator answers ZNP
	}

	// Dongle present: it is found, and the TNC is never opened.
	if got := findZigBeePort([]string{tnc, dongle}, vidpidOf, probe, nil); got != dongle {
		t.Fatalf("coordinator = %q, want %q", got, dongle)
	}
	if len(probed) != 1 || probed[0] != dongle {
		t.Fatalf("probed %v, want only [%s]", probed, dongle)
	}

	// Dongle pulled: no candidate is left, and the TNC is still not opened.
	probed = nil
	if got := findZigBeePort([]string{tnc}, vidpidOf, probe, nil); got != "" {
		t.Fatalf("coordinator = %q with no dongle on the bus, want none", got)
	}
	if len(probed) != 0 {
		t.Fatalf("probed %v with no dongle on the bus, want nothing", probed)
	}
}

// TestDeviceRegistry_ClaimReservesPort: a claim and its reservation are
// written and dropped together, so the guard cannot drift from the
// registry it mirrors. [MESHSAT-1265]
func TestDeviceRegistry_ClaimReservesPort(t *testing.T) {
	clearPortGuard()
	defer clearPortGuard()

	r := NewDeviceRegistry()
	const port = "/dev/ttyACM2"

	if !r.ClaimPort(port, RoleMeshtastic) {
		t.Fatal("first claim refused")
	}
	if owner := PortOwner(port); owner != string(RoleMeshtastic) {
		t.Fatalf("owner after claim = %q, want %q", owner, RoleMeshtastic)
	}

	r.ReleasePort(port)
	if owner := PortOwner(port); owner != "" {
		t.Fatalf("owner after release = %q, want none", owner)
	}

	// Remove drops it too.
	r.Upsert(port, SerialDeviceEntry{DevPath: port})
	r.ClaimPort(port, RoleZigBee)
	r.Remove(port)
	if owner := PortOwner(port); owner != "" {
		t.Fatalf("owner after remove = %q, want none", owner)
	}

	// So does the reconcile that runs when a port vanishes from /dev,
	// which is what a pulled device looks like.
	r.Upsert(port, SerialDeviceEntry{DevPath: port})
	r.ClaimPort(port, RoleZigBee)
	if removed := r.Reconcile(map[string]bool{}); len(removed) != 1 {
		t.Fatalf("reconcile removed %d entries, want 1", len(removed))
	}
	if owner := PortOwner(port); owner != "" {
		t.Fatalf("owner after reconcile = %q, want none", owner)
	}
}

// TestDeviceSupervisor_ExcludePortReserves: the MESHSAT-821 exclusion now
// reaches the package-level scanners too. [MESHSAT-1265]
func TestDeviceSupervisor_ExcludePortReserves(t *testing.T) {
	clearPortGuard()
	defer clearPortGuard()

	s := NewDeviceSupervisor()
	const tnc = "/dev/ttyUSB-picoaprs"
	s.ExcludePort(tnc)

	if owner := PortOwner(tnc); owner != PortOwnerExcluded {
		t.Fatalf("owner = %q, want %q", owner, PortOwnerExcluded)
	}
	if PortAvailableTo(tnc, string(RoleZigBee)) {
		t.Fatal("an excluded port is available to the zigbee gateway")
	}
}
