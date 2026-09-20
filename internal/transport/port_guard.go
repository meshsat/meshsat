package transport

// Serial port ownership guard [MESHSAT-1265].
//
// A serial port that belongs to one device must never be opened by the
// detector or the probe of another, whatever else is present on the bus.
// Pulling a device is exactly what breaks that: the DeviceSupervisor probes
// an unclaimed port once and owns the result, but five package-level
// scanners survive beside it (FindZigBeePort, autoDetectMeshtastic,
// autoDetectIridium, autoDetectCellular, autoDetectGPS) and glob /dev
// directly, so an absent device sends them hunting across their neighbours'
// ports. The live case was FindZigBeePort, reached from ZigBeeGateway.Start
// with a config of "auto": it keeps every port whose VID:PID is in
// knownZigBeeVIDPIDs, and the PicoAPRS V4 KISS TNC is a CP2102N on
// 10c4:ea60, the SONOFF dongle's own pair. ProbeZNP then opens it and the
// CP210x driver asserts DTR+RTS on open.
//
// TIOCEXCL does not save a held port here: the container has CAP_SYS_ADMIN,
// so a second open() succeeds on a port a running transport owns (the
// MESHSAT-815 note in internal/engine/interface_manager.go). The guard has
// to be ours.
//
// Owners are the DeviceRole the registry claimed the port for, or
// PortOwnerExcluded for a port the supervisor excluded outright. Claims are
// reserved and released by DeviceRegistry so the guard can never drift from
// the claim it mirrors.

import (
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// PortOwnerExcluded marks a port that belongs to a driver outside the
// registry's claim system — today the APRS gateway's hardware KISS TNC.
// It is available to nobody. [MESHSAT-821]
const PortOwnerExcluded = "excluded"

var portGuard = struct {
	mu     sync.RWMutex
	owners map[string]string // path as reserved → owner
}{owners: make(map[string]string)}

// portGuardBlocks counts probes the guard refused. Tests assert on it: a
// probe that stops consulting the guard stops incrementing it.
var portGuardBlocks atomic.Int64

// ReservePort records that a port belongs to owner. The path is stored as
// given and compared against its resolved target on every lookup, so a
// /dev/serial/by-id symlink still matches the /dev/ttyUSB* name a scan sees
// even when the device is plugged in later.
func ReservePort(path, owner string) {
	if path == "" || owner == "" {
		return
	}
	portGuard.mu.Lock()
	defer portGuard.mu.Unlock()
	portGuard.owners[path] = owner
}

// UnreservePort drops a reservation. Called when a claim is released or the
// port vanishes, so a replacement device can be identified freely.
func UnreservePort(path string) {
	if path == "" {
		return
	}
	portGuard.mu.Lock()
	defer portGuard.mu.Unlock()
	delete(portGuard.owners, path)
}

// PortOwner returns the owner of a port, or "" when it belongs to nobody.
func PortOwner(path string) string {
	if path == "" {
		return ""
	}
	portGuard.mu.RLock()
	defer portGuard.mu.RUnlock()
	if len(portGuard.owners) == 0 {
		return ""
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	for p, owner := range portGuard.owners {
		if p == path || p == real {
			return owner
		}
		if r, err := filepath.EvalSymlinks(p); err == nil && (r == path || r == real) {
			return owner
		}
	}
	return ""
}

// PortAvailableTo reports whether owner may open a port: it is free, or it
// is already theirs.
func PortAvailableTo(path, owner string) bool {
	o := PortOwner(path)
	return o == "" || o == owner
}

// FilterUnownedPorts drops every port that belongs to a device from a
// scanner's candidate list. Scanners call this before looking at VID:PIDs so
// a foreign port is never a candidate in the first place.
func FilterUnownedPorts(ports []string) []string {
	portGuard.mu.RLock()
	n := len(portGuard.owners)
	portGuard.mu.RUnlock()
	if n == 0 {
		return ports
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		if owner := PortOwner(p); owner != "" {
			log.Debug().Str("port", p).Str("owner", owner).
				Msg("port guard: candidate skipped, the port belongs to another device")
			continue
		}
		out = append(out, p)
	}
	return out
}

// guardProbe is the backstop every serial probe calls before it opens a
// port. It returns true when the probe must not run. A scanner that
// filtered its candidates never reaches this, so a Warn here means a caller
// went around FilterUnownedPorts.
func guardProbe(port, probe string) bool {
	owner := PortOwner(port)
	if owner == "" {
		return false
	}
	portGuardBlocks.Add(1)
	log.Warn().Str("port", port).Str("owner", owner).Str("probe", probe).
		Msg("port guard: probe refused, the port belongs to another device")
	return true
}

// clearPortGuard drops every reservation. Tests only: reservations are
// process-wide and outlive the DeviceSupervisor that made them.
func clearPortGuard() {
	portGuard.mu.Lock()
	defer portGuard.mu.Unlock()
	portGuard.owners = make(map[string]string)
	portGuardBlocks.Store(0)
}
