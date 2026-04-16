package transport

// DeviceSupervisor — USB inventory + serial reconciliation engine.
// Ported from HAL's DeviceSupervisor, adapted for Bridge's full transport set.
//
// Two tiers:
//   - Tier 1 (30s): Serial port inventory via /dev/ttyUSB* + /dev/ttyACM* + /dev/ttyAMA*
//   - Tier 2 (15s): Identify unclaimed ports, reconnect disconnected drivers
//
// Identification cascade (fast to slow):
//  1. VID:PID match (~1ms)
//  2. JSPR probe for 9704 (~500ms)
//  3. AT probe for 9603 (~500ms)
//  4. Cellular AT probe (~500ms)
//  5. ZNP probe for ZigBee (~500ms)
//  7. GPS NMEA probe (~2s) — not implemented, GPS uses VID:PID only

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// DeviceEvent is emitted when a device state changes.
type DeviceEvent struct {
	Type   string             `json:"type"`
	Device *SerialDeviceEntry `json:"device"`
	Time   string             `json:"time"`
}

// DriverCallbacks holds functions the supervisor calls when ports are discovered or lost.
type DriverCallbacks struct {
	InstanceID  string            // unique identifier for multi-instance (e.g. "iridium_0")
	OnPortFound func(port string) // called when a port is identified for this role
	OnPortLost  func(port string) // called when a claimed port vanishes
	HasPort     func() bool       // returns true if this instance already has a port assigned
}

// DeviceSupervisor periodically scans serial ports and manages identification,
// claiming, and driver reconnection.
type DeviceSupervisor struct {
	registry *DeviceRegistry

	// Driver callbacks keyed by role — multiple callbacks per role for multi-instance
	callbacksMu sync.RWMutex
	callbacks   map[DeviceRole][]*DriverCallbacks

	// Port-to-instance tracking: which instance ID claimed each port
	portInstances   map[string]string // devPath → instanceID
	portInstancesMu sync.RWMutex

	// scanMu serializes scan and reconcile cycles — prevents concurrent
	// registry modification from overlapping 30s scan and 15s reconcile tickers.
	// [MESHSAT-444]
	scanMu sync.Mutex

	// Probe serialization: prevents concurrent probes on the same port and
	// prevents re-probing ports that were already identified as wrong-interface
	// (e.g., Huawei E220 interface 0). [MESHSAT-444]
	probingMu sync.Mutex
	probing   map[string]bool // ports currently being probed
	skipPorts map[string]bool // ports to skip permanently (wrong interface, etc.)

	// probeMu serializes all serial port probes globally — only one probe
	// may open a serial port at a time to prevent USB bus contention and
	// kernel driver confusion on shared hubs. [MESHSAT-444]
	probeMu sync.Mutex

	// Explicit port overrides (from env vars) — skip auto-detect for these roles
	explicitPorts map[DeviceRole]string

	// initialScanDone is closed after the first scan+identify cycle completes.
	// ReconcileWithHardware should wait for this before disabling gateways,
	// otherwise it races with identification of ports that require probing. [MESHSAT-403]
	initialScanDone chan struct{}

	stopCh    chan struct{}
	scanNowCh chan struct{}

	// SSE subscribers
	eventMu      sync.RWMutex
	eventClients map[uint64]chan DeviceEvent
	nextClientID uint64
}

// NewDeviceSupervisor creates a new supervisor.
func NewDeviceSupervisor() *DeviceSupervisor {
	return &DeviceSupervisor{
		registry:        NewDeviceRegistry(),
		callbacks:       make(map[DeviceRole][]*DriverCallbacks),
		portInstances:   make(map[string]string),
		probing:         make(map[string]bool),
		skipPorts:       make(map[string]bool),
		explicitPorts:   make(map[DeviceRole]string),
		initialScanDone: make(chan struct{}),
		stopCh:          make(chan struct{}),
		scanNowCh:       make(chan struct{}, 1),
		eventClients:    make(map[uint64]chan DeviceEvent),
	}
}

// Registry returns the underlying device registry.
func (s *DeviceSupervisor) Registry() *DeviceRegistry {
	return s.registry
}

// SetCallbacks registers driver callbacks for a role (legacy — single instance).
// For multi-instance, use AddCallbacks instead.
func (s *DeviceSupervisor) SetCallbacks(role DeviceRole, cb *DriverCallbacks) {
	s.callbacksMu.Lock()
	defer s.callbacksMu.Unlock()
	if cb.InstanceID == "" {
		cb.InstanceID = string(role) + "_0"
	}
	// Legacy: replace all callbacks for this role with a single one
	s.callbacks[role] = []*DriverCallbacks{cb}
}

// AddCallbacks registers additional driver callbacks for a role (multi-instance).
// Multiple callbacks for the same role enables multiple modems of the same type.
func (s *DeviceSupervisor) AddCallbacks(role DeviceRole, cb *DriverCallbacks) {
	s.callbacksMu.Lock()
	defer s.callbacksMu.Unlock()
	s.callbacks[role] = append(s.callbacks[role], cb)
}

// RemoveCallbacks removes driver callbacks for a specific instance.
func (s *DeviceSupervisor) RemoveCallbacks(role DeviceRole, instanceID string) {
	s.callbacksMu.Lock()
	defer s.callbacksMu.Unlock()
	cbs := s.callbacks[role]
	for i, cb := range cbs {
		if cb.InstanceID == instanceID {
			s.callbacks[role] = append(cbs[:i], cbs[i+1:]...)
			return
		}
	}
}

// GetPortInstance returns the instance ID that claimed a port, or "".
func (s *DeviceSupervisor) GetPortInstance(port string) string {
	s.portInstancesMu.RLock()
	defer s.portInstancesMu.RUnlock()
	return s.portInstances[port]
}

// SetExplicitPort sets an explicit port for a role (from env var).
// When set, the supervisor claims this port immediately without probing.
func (s *DeviceSupervisor) SetExplicitPort(role DeviceRole, port string) {
	if port != "" && port != "auto" {
		s.explicitPorts[role] = port
	}
}

// Start launches the background scan goroutine.
func (s *DeviceSupervisor) Start() {
	go s.run()
	log.Info().Msg("device-supervisor: started")
}

// Stop signals the supervisor to shut down.
func (s *DeviceSupervisor) Stop() {
	close(s.stopCh)
	log.Info().Msg("device-supervisor: stopped")
}

// TriggerScan forces an immediate scan (non-blocking).
func (s *DeviceSupervisor) TriggerScan() {
	select {
	case s.scanNowCh <- struct{}{}:
	default:
	}
}

// WaitForInitialScan blocks until the supervisor's first scan+identify cycle
// completes. Call this before ReconcileWithHardware to avoid a race where
// gateways are disabled for "missing" hardware that hasn't been identified yet.
// Returns immediately if the scan already completed. Times out after 30s
// to prevent deadlock if the supervisor never starts. [MESHSAT-403]
func (s *DeviceSupervisor) WaitForInitialScan() {
	select {
	case <-s.initialScanDone:
	case <-time.After(30 * time.Second):
		log.Warn().Msg("device-supervisor: WaitForInitialScan timed out after 30s")
	}
}

func (s *DeviceSupervisor) run() {
	// Register explicit ports first
	s.registerExplicitPorts()

	// Initial scan — serialized under scanMu [MESHSAT-444]
	s.scanMu.Lock()
	s.recoverMissingTTY()
	s.scanSerialPorts()
	s.reconcileSerialDevices()
	s.scanMu.Unlock()

	// Signal that the initial scan+identify cycle is complete.
	// ReconcileWithHardware waits for this before disabling gateways. [MESHSAT-403]
	close(s.initialScanDone)

	portTicker := time.NewTicker(30 * time.Second)
	serialTicker := time.NewTicker(15 * time.Second)
	defer portTicker.Stop()
	defer serialTicker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-portTicker.C:
			s.scanMu.Lock()
			s.recoverMissingTTY()
			s.scanSerialPorts()
			s.scanMu.Unlock()
		case <-serialTicker.C:
			s.scanMu.Lock()
			s.reconcileSerialDevices()
			s.scanMu.Unlock()
		case <-s.scanNowCh:
			s.scanMu.Lock()
			s.recoverMissingTTY()
			s.scanSerialPorts()
			s.reconcileSerialDevices()
			s.scanMu.Unlock()
		}
	}
}

// registerExplicitPorts claims ports that were explicitly configured via env vars.
// Skips ports that don't exist on the filesystem — stale env vars should not
// prevent auto-detection from working.
func (s *DeviceSupervisor) registerExplicitPorts() {
	for role, port := range s.explicitPorts {
		// Verify the port actually exists before registering
		if _, err := os.Stat(port); err != nil {
			log.Warn().Str("port", port).Str("role", string(role)).
				Msg("device-supervisor: explicit port does not exist, skipping (will auto-detect)")
			continue
		}

		now := time.Now()
		vidpid := findUSBVIDPID(port)
		usbSerial := FindUSBSerial(port)

		entry := SerialDeviceEntry{
			DevPath:   port,
			VIDPID:    vidpid,
			USBSerial: usbSerial,
			Role:      role,
			State:     StateReady,
			FirstSeen: now,
			LastSeen:  now,
		}

		s.registry.Upsert(port, entry)
		s.registry.ClaimPort(port, role)
		s.registry.SetRole(port, role)
		s.registry.SetState(port, StateReady)

		log.Info().Str("port", port).Str("role", string(role)).Msg("device-supervisor: explicit port registered")

		// Notify driver
		s.notifyPortFound(role, port)
	}
}

// scanSerialPorts discovers all serial ports and updates the registry.
func (s *DeviceSupervisor) scanSerialPorts() {
	var activePorts []string
	for _, pattern := range []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/ttyAMA*"} {
		matches, _ := filepath.Glob(pattern)
		activePorts = append(activePorts, matches...)
	}

	now := time.Now()
	activeSet := make(map[string]bool, len(activePorts))

	for _, port := range activePorts {
		activeSet[port] = true

		vidpid := findUSBVIDPID(port)
		usbSerial := FindUSBSerial(port)

		// Check if port already exists in registry
		existing := s.registry.FindByPort(port)
		if existing != nil {
			// Port exists — check if the device changed (VID:PID swap).
			// If the VID:PID changed, a different device was plugged into the same port.
			// Release the old claim and re-identify.
			if existing.VIDPID != "" && vidpid != "" && existing.VIDPID != vidpid {
				log.Info().Str("port", port).Str("old_vidpid", existing.VIDPID).Str("new_vidpid", vidpid).
					Str("old_role", string(existing.Role)).Msg("device-supervisor: device swapped on port, re-identifying")
				if existing.Role != RoleNone {
					s.notifyPortLost(existing.Role, port)
				}
				s.emitEvent(DeviceEvent{
					Type:   "device_disconnected",
					Device: existing,
				})
				s.registry.Remove(port)
				// Fall through to register as new
			} else {
				// Same device — actually update LastSeen [MESHSAT-444]
				s.registry.UpdateLastSeen(port, now)
				continue
			}
		}

		entry := SerialDeviceEntry{
			DevPath:   port,
			VIDPID:    vidpid,
			USBSerial: usbSerial,
			State:     StateDetected,
			FirstSeen: now,
			LastSeen:  now,
		}

		isNew := s.registry.Upsert(port, entry)
		if isNew {
			s.emitEvent(DeviceEvent{
				Type:   "device_added",
				Device: &entry,
			})
			log.Debug().Str("port", port).Str("vidpid", vidpid).Msg("device-supervisor: new serial port detected")
		}
	}

	// Reconcile — remove ports no longer present
	removed := s.registry.Reconcile(activeSet)
	for _, entry := range removed {
		s.emitEvent(DeviceEvent{
			Type:   "device_removed",
			Device: entry,
		})

		// Clean up skip list when device is unplugged — the same /dev path
		// may be reassigned to a different device on re-plug. [MESHSAT-444]
		s.probingMu.Lock()
		delete(s.skipPorts, entry.DevPath)
		s.probingMu.Unlock()

		// Notify driver that port is gone
		if entry.Role != RoleNone {
			s.notifyPortLost(entry.Role, entry.DevPath)
			log.Info().Str("port", entry.DevPath).Str("role", string(entry.Role)).Msg("device-supervisor: port vanished")
		}
	}
}

// reconcileSerialDevices identifies unclaimed ports and reconnects disconnected ones.
func (s *DeviceSupervisor) reconcileSerialDevices() {
	// Scan current ports
	var activePorts []string
	for _, pattern := range []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/ttyAMA*"} {
		matches, _ := filepath.Glob(pattern)
		activePorts = append(activePorts, matches...)
	}

	activeSet := make(map[string]bool, len(activePorts))
	for _, p := range activePorts {
		activeSet[p] = true
	}

	// Prune: check claimed ports that vanished — notify driver and remove from registry.
	// Full removal (not just state change) ensures a different device on the same port
	// will be identified fresh instead of inheriting the old role.
	for _, entry := range s.registry.ListAll() {
		if entry.DevPath != "" && !activeSet[entry.DevPath] {
			if entry.Role != RoleNone {
				s.notifyPortLost(entry.Role, entry.DevPath)
				log.Info().Str("port", entry.DevPath).Str("role", string(entry.Role)).Msg("device-supervisor: serial port vanished")
			}
			s.registry.Remove(entry.DevPath)
			// Clean up skip list — port may be reassigned on re-plug [MESHSAT-444]
			s.probingMu.Lock()
			delete(s.skipPorts, entry.DevPath)
			s.probingMu.Unlock()
			s.emitEvent(DeviceEvent{
				Type:   "device_disconnected",
				Device: entry,
			})
		}
	}

	// Identify unclaimed ports
	for _, port := range activePorts {
		if role := s.registry.GetPortRole(port); role != RoleNone {
			continue // already claimed
		}
		s.identifyAndClaimPort(port)
	}

	// Reconnect: check for roles that lost their port but a matching port reappeared.
	// Pass activeSet to avoid duplicate glob scan. [MESHSAT-444]
	s.reconnectDisconnected(activeSet)
}

// identifyAndClaimPort runs the identification cascade on an unclaimed port.
// Order: VID:PID match → JSPR (9704) → AT (9603) → Cellular AT → ZNP
//
// [MESHSAT-444] Probe serialization: acquires a per-port lock to prevent
// concurrent probes on the same port. Skips ports that are permanently
// marked (wrong interface on multi-port devices like Huawei E220).
func (s *DeviceSupervisor) identifyAndClaimPort(port string) {
	// Skip ports permanently marked as wrong-interface or non-AT data ports.
	s.probingMu.Lock()
	if s.skipPorts[port] {
		s.probingMu.Unlock()
		return
	}
	// Serialize: if another goroutine is already probing this port, skip.
	if s.probing[port] {
		s.probingMu.Unlock()
		return
	}
	// Double-check claim under probe lock to prevent race with parallel reconcile.
	if role := s.registry.GetPortRole(port); role != RoleNone {
		s.probingMu.Unlock()
		return
	}
	s.probing[port] = true
	s.probingMu.Unlock()

	defer func() {
		s.probingMu.Lock()
		delete(s.probing, port)
		s.probingMu.Unlock()
	}()

	vidpid := findUSBVIDPID(port)

	// Step 1: VID:PID match (~1ms)
	if vidpid != "" {
		role := s.classifyByVIDPID(vidpid, port)
		if role != RoleNone {
			s.claimAndNotify(port, vidpid, role, "VID:PID")
			return
		}
	}

	// Mark port as identifying while probes run [MESHSAT-444]
	s.registry.SetState(port, StateIdentifying)

	// Step 1b: Meshtastic probe for unknown VID:PID ACM devices (~2s)
	// ESP32-S3 native USB may not expose VID:PID in sysfs. Without this check,
	// the cascade sends JSPR (230400), AT (19200), and Cellular AT (115200) to
	// the Meshtastic radio — all at wrong baud rates — corrupting its serial
	// buffer and leaving it in a state where it never responds. [MESHSAT-403]
	if vidpid == "" && strings.HasPrefix(filepath.Base(port), "ttyACM") {
		s.probeMu.Lock()
		meshResult := ProbeMeshtastic(port)
		s.probeMu.Unlock()
		if meshResult {
			s.claimAndNotify(port, vidpid, RoleMeshtastic, "Meshtastic probe (unknown VID:PID)")
			return
		}
	}

	// Step 2: JSPR probe for 9704 (~500ms)
	// The 9704 uses FTDI chips (0403:6015, 0403:6001) exclusively, which create
	// ttyUSB* devices. CDC-ACM devices (ttyACM*) are never FTDI — probing them
	// with JSPR at 230400 baud wastes ~9s per device and corrupts their serial
	// state (e.g., AIOC audio interfaces, GPS receivers). [MESHSAT-403]
	// Skip on: ttyACM*, ttyAMA*, known cellular/Meshtastic/ZigBee VID:PIDs.
	portBase := filepath.Base(port)
	if strings.HasPrefix(portBase, "ttyACM") {
		// CDC-ACM device — never FTDI, skip JSPR entirely [MESHSAT-403]
	} else if strings.HasPrefix(portBase, "ttyAMA") {
		// Platform UART — use Go serial library probe instead of raw fd+select
		// which doesn't work on PL011/RP1 UARTs. This enables auto-detection
		// of the 9704 on Pi 5 UART2 (/dev/ttyAMA2) without requiring
		// MESHSAT_IMT_PORT to be set explicitly. [MESHSAT-403]
		s.probeMu.Lock()
		jsprResult := probeJSPRSerial(port)
		s.probeMu.Unlock()
		if jsprResult {
			s.claimAndNotify(port, vidpid, RoleIridium9704, "JSPR probe (platform UART)")
			return
		}
	} else if knownCellularVIDPIDs[vidpid] {
		// Known cellular modem — skip JSPR probe (would hang for 9s) [MESHSAT-444]
	} else if knownMeshtasticVIDPIDs[vidpid] || ambiguousZigBeeVIDPIDs[vidpid] || knownZigBeeOnlyVIDPIDs[vidpid] {
		// Known Meshtastic/ZigBee VID:PID — JSPR at wrong baud rate corrupts device state [MESHSAT-403]
	} else {
		s.probeMu.Lock()
		jsprResult := probeJSPR(port)
		s.probeMu.Unlock()
		if jsprResult {
			s.claimAndNotify(port, vidpid, RoleIridium9704, "JSPR probe")
			return
		}
	}

	// Step 3: AT probe for 9603 (~500ms)
	// Iridium 9603 uses FTDI (ttyUSB*), never CDC-ACM (ttyACM*). Also skip
	// known non-Iridium VID:PIDs (Meshtastic, ZigBee, cellular, GPS). [MESHSAT-403]
	if !strings.HasPrefix(portBase, "ttyACM") &&
		(vidpid == "" || (!knownMeshtasticVIDPIDs[vidpid] && !gpsVIDPIDs[vidpid] &&
			!knownCellularVIDPIDs[vidpid] &&
			!knownZigBeeOnlyVIDPIDs[vidpid] &&
			!ambiguousZigBeeVIDPIDs[vidpid])) {
		s.probeMu.Lock()
		atResult := probeAT(port)
		s.probeMu.Unlock()
		if atResult {
			s.claimAndNotify(port, vidpid, RoleIridium9603, "AT probe")
			return
		}
	}

	// Step 4: Cellular AT probe (~500ms)
	// Only try on ports with known cellular VID:PIDs or truly unknown ports
	if vidpid == "" || knownCellularVIDPIDs[vidpid] {
		// Check multi-interface modem (Huawei E220: skip iface 0).
		// Mark wrong-interface ports permanently so they are never re-probed. [MESHSAT-444]
		if iface, ok := cellularATInterface[vidpid]; ok {
			if findUSBInterfaceNum(port) != iface {
				s.probingMu.Lock()
				s.skipPorts[port] = true
				s.probingMu.Unlock()
				log.Debug().Str("port", port).Str("vidpid", vidpid).
					Str("got_iface", findUSBInterfaceNum(port)).Str("want_iface", iface).
					Msg("device-supervisor: skipping wrong interface on multi-port modem")
				return
			}
		}
		s.probeMu.Lock()
		cellResult := probeCellularAT(port)
		s.probeMu.Unlock()
		if cellResult {
			s.claimAndNotify(port, vidpid, RoleCellular, "cellular AT probe")
			return
		}
	}

	// Step 5: ZNP probe for ZigBee (~500ms)
	// Only try on ambiguous VID:PIDs (CP210x, CH343) or ZigBee-only VID:PIDs.
	// Serialized under probeMu to prevent USB bus contention. [MESHSAT-444]
	if knownZigBeeOnlyVIDPIDs[vidpid] || ambiguousZigBeeVIDPIDs[vidpid] {
		s.probeMu.Lock()
		znpResult := ProbeZNP(port)
		s.probeMu.Unlock()
		if znpResult {
			s.claimAndNotify(port, vidpid, RoleZigBee, "ZNP probe")
			return
		}
		// Ambiguous VID:PID + ZNP probe failed → default to Meshtastic [MESHSAT-444]
		if ambiguousZigBeeVIDPIDs[vidpid] {
			s.claimAndNotify(port, vidpid, RoleMeshtastic, "ambiguous VID:PID default")
			return
		}
	}

	// Unknown — will retry next cycle
}

// claimAndNotify claims a port for a role and fires callbacks.
// Logs a warning if the claim fails (port was claimed by another goroutine). [MESHSAT-444]
func (s *DeviceSupervisor) claimAndNotify(port, vidpid string, role DeviceRole, method string) {
	if !s.registry.ClaimPort(port, role) {
		log.Warn().Str("port", port).Str("role", string(role)).Str("method", method).
			Msg("device-supervisor: probe succeeded but claim failed (port claimed by another goroutine)")
		return
	}
	s.registry.SetRole(port, role)
	s.registry.SetState(port, StateReady)
	log.Info().Str("port", port).Str("vidpid", vidpid).Str("role", string(role)).
		Msgf("device-supervisor: identified by %s", method)
	s.notifyPortFound(role, port)
	s.emitEvent(DeviceEvent{
		Type:   "device_connected",
		Device: s.registryEntry(port),
	})
}

// classifyByVIDPID returns the role for a known VID:PID, or RoleNone for unknown/ambiguous.
func (s *DeviceSupervisor) classifyByVIDPID(vidpid, port string) DeviceRole {
	// Check for unambiguous Meshtastic (not shared with ZigBee or cellular)
	if knownMeshtasticVIDPIDs[vidpid] && !ambiguousZigBeeVIDPIDs[vidpid] && !knownCellularVIDPIDs[vidpid] {
		return RoleMeshtastic
	}

	// GPS VID:PIDs are unambiguous
	if gpsVIDPIDs[vidpid] {
		return RoleGPS
	}

	// ZigBee-only VID:PIDs
	if knownZigBeeOnlyVIDPIDs[vidpid] {
		return RoleZigBee
	}

	// Cellular VID:PIDs — check before ambiguous handler since CH9102F is in both
	if knownCellularVIDPIDs[vidpid] {
		// For multi-interface modems, only claim the AT port.
		// Mark wrong-interface ports permanently so they're never re-probed. [MESHSAT-444]
		if iface, ok := cellularATInterface[vidpid]; ok {
			if findUSBInterfaceNum(port) != iface {
				s.probingMu.Lock()
				s.skipPorts[port] = true
				s.probingMu.Unlock()
				return RoleNone // wrong interface
			}
		}
		return RoleCellular
	}

	// Ambiguous Meshtastic/ZigBee — return RoleNone to let the identification
	// cascade (Step 6) handle it with proper probe serialization. [MESHSAT-444]
	// ProbeZNP was previously called here (serial I/O in a "fast" VID:PID path)
	// which ran unserialized and could interfere with other probes.
	if ambiguousZigBeeVIDPIDs[vidpid] {
		return RoleNone
	}

	// Meshtastic-only (ESP32-S3 native USB, etc.)
	if knownMeshtasticVIDPIDs[vidpid] {
		return RoleMeshtastic
	}

	// FTDI VID:PIDs — disambiguate by USB product string (sysfs, no serial open).
	// probeJSPR uses O_NONBLOCK + select() which doesn't work on FTDI USB-serial
	// on ARM64: select() always times out because the USB subsystem needs blocking
	// reads to trigger URB submission. The USB product string reliably distinguishes:
	//   FT230X/FT234X → RockBLOCK 9704 (IMT/JSPR at 230400)
	//   FT232R        → Iridium 9603N (SBD/AT at 19200)
	if knownIMTVIDPIDs[vidpid] {
		product := findUSBProduct(port)
		if strings.Contains(product, "FT230X") || strings.Contains(product, "FT234X") {
			log.Info().Str("port", port).Str("product", product).
				Msg("device-supervisor: identified as 9704 by USB product string (skipping JSPR probe)")
			return RoleIridium9704
		}
	}
	if knownIridiumVIDPIDs[vidpid] {
		return RoleNone // fall through to protocol probes
	}

	return RoleNone
}

// reconnectDisconnected checks if any role lost its port but the port reappeared.
// IMPORTANT: Re-verifies VID:PID before reconnecting — a different device may have
// been plugged into the same port. If VID:PID changed, the old entry is removed
// and the port will be re-identified in the next reconcile cycle.
// [MESHSAT-444] Takes activeSet from caller to avoid duplicate glob scan.
func (s *DeviceSupervisor) reconnectDisconnected(activeSet map[string]bool) {
	for _, entry := range s.registry.ListAll() {
		if entry.State != StateDisconnected {
			continue
		}
		// Check if the port is back using the caller's active set
		if !activeSet[entry.DevPath] {
			continue
		}

		// Port reappeared — verify it's the same device by checking VID:PID.
		// If the VID:PID changed (different device on same port), remove the
		// stale entry and let the identify cascade handle it fresh.
		currentVIDPID := findUSBVIDPID(entry.DevPath)
		if entry.VIDPID != "" && currentVIDPID != "" && entry.VIDPID != currentVIDPID {
			log.Info().Str("port", entry.DevPath).
				Str("old_vidpid", entry.VIDPID).Str("new_vidpid", currentVIDPID).
				Str("old_role", string(entry.Role)).
				Msg("device-supervisor: port reappeared with different device, re-identifying")
			s.registry.Remove(entry.DevPath)
			continue // will be picked up as new device in next scan
		}

		// Same device (or platform UART with no VID:PID) — reconnect
		if entry.Role != RoleNone {
			s.registry.SetState(entry.DevPath, StateReady)
			log.Info().Str("port", entry.DevPath).Str("role", string(entry.Role)).Msg("device-supervisor: port reappeared, reconnecting")
			s.notifyPortFound(entry.Role, entry.DevPath)
			s.emitEvent(DeviceEvent{
				Type:   "device_connected",
				Device: entry,
			})
		}
	}
}

func (s *DeviceSupervisor) registryEntry(port string) *SerialDeviceEntry {
	for _, e := range s.registry.ListAll() {
		if e.DevPath == port {
			return e
		}
	}
	return nil
}

func (s *DeviceSupervisor) notifyPortFound(role DeviceRole, port string) {
	s.callbacksMu.RLock()
	defer s.callbacksMu.RUnlock()

	cbs := s.callbacks[role]
	if len(cbs) == 0 {
		return
	}

	// Multi-instance: find the first callback that doesn't already have a port.
	// If all have ports, notify the first one (legacy behavior).
	for _, cb := range cbs {
		if cb.HasPort != nil && !cb.HasPort() && cb.OnPortFound != nil {
			cb.OnPortFound(port)
			s.portInstancesMu.Lock()
			s.portInstances[port] = cb.InstanceID
			s.portInstancesMu.Unlock()
			return
		}
	}
	// Fallback: all instances have ports or no HasPort func — notify first
	if cbs[0].OnPortFound != nil {
		cbs[0].OnPortFound(port)
		s.portInstancesMu.Lock()
		s.portInstances[port] = cbs[0].InstanceID
		s.portInstancesMu.Unlock()
	}
}

func (s *DeviceSupervisor) notifyPortLost(role DeviceRole, port string) {
	s.callbacksMu.RLock()
	defer s.callbacksMu.RUnlock()

	// Find the instance that owns this port and notify it
	s.portInstancesMu.RLock()
	instanceID := s.portInstances[port]
	s.portInstancesMu.RUnlock()

	cbs := s.callbacks[role]
	for _, cb := range cbs {
		if cb.InstanceID == instanceID && cb.OnPortLost != nil {
			cb.OnPortLost(port)
			s.portInstancesMu.Lock()
			delete(s.portInstances, port)
			s.portInstancesMu.Unlock()
			return
		}
	}

	// Fallback: no instance match (legacy) — notify first callback
	if len(cbs) > 0 && cbs[0].OnPortLost != nil {
		cbs[0].OnPortLost(port)
		s.portInstancesMu.Lock()
		delete(s.portInstances, port)
		s.portInstancesMu.Unlock()
	}
}

// ============================================================================
// SSE Event System
// ============================================================================

// SubscribeEvents registers a new SSE client.
func (s *DeviceSupervisor) SubscribeEvents() (<-chan DeviceEvent, func()) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()

	id := s.nextClientID
	s.nextClientID++
	ch := make(chan DeviceEvent, 16)
	s.eventClients[id] = ch

	unsub := func() {
		s.eventMu.Lock()
		defer s.eventMu.Unlock()
		delete(s.eventClients, id)
		close(ch)
	}
	return ch, unsub
}

func (s *DeviceSupervisor) emitEvent(event DeviceEvent) {
	if event.Time == "" {
		event.Time = time.Now().UTC().Format(time.RFC3339)
	}

	s.eventMu.RLock()
	defer s.eventMu.RUnlock()

	for _, ch := range s.eventClients {
		select {
		case ch <- event:
		default:
			// Drop if subscriber is slow
		}
	}
}

// MarkConnected updates a device's state to Connected (called by transport after successful connect).
func (s *DeviceSupervisor) MarkConnected(port string) {
	s.registry.SetState(port, StateConnected)
}

// MarkDisconnected updates a device's state to Disconnected (called by transport on serial error).
func (s *DeviceSupervisor) MarkDisconnected(port string) {
	s.registry.SetState(port, StateDisconnected)
}

// ============================================================================
// USB TTY Recovery
// ============================================================================

// recoverMissingTTY checks for USB devices that are enumerated in sysfs but
// have no /dev/tty* entry. This happens when a container holds a stale fd
// during USB replug — the kernel can't rebind cdc_acm. Recovery: unbind the
// USB device from its driver and rebind it, forcing the kernel to create a
// fresh tty. This is a capability neither HAL nor any standard Linux device
// manager provides.
func (s *DeviceSupervisor) recoverMissingTTY() {
	entries, err := os.ReadDir("/sys/bus/usb/devices")
	if err != nil {
		return
	}

	for _, e := range entries {
		name := e.Name()
		// Skip interfaces (contain ':') and root hubs
		if strings.Contains(name, ":") || strings.HasPrefix(name, "usb") {
			continue
		}

		devDir := filepath.Join("/sys/bus/usb/devices", name)
		vid := readSysfsFile(filepath.Join(devDir, "idVendor"))
		pid := readSysfsFile(filepath.Join(devDir, "idProduct"))
		if vid == "" || pid == "" {
			continue
		}

		vidpid := vid + ":" + pid

		// Only recover devices we care about
		if !knownMeshtasticVIDPIDs[vidpid] && !knownIridiumVIDPIDs[vidpid] &&
			!knownIMTVIDPIDs[vidpid] && !knownCellularVIDPIDs[vidpid] {
			continue
		}

		// Check if this USB device has a tty associated
		if hasTTYDevice(devDir) {
			continue // tty exists, no recovery needed
		}

		// USB device is enumerated but no tty — try rebind
		log.Warn().Str("device", name).Str("vidpid", vidpid).Msg("device-supervisor: USB device has no tty, attempting rebind")

		driverPath := filepath.Join(devDir, "driver")
		driverLink, err := os.Readlink(driverPath)
		if err != nil {
			log.Debug().Str("device", name).Msg("device-supervisor: no driver bound, skipping rebind")
			continue
		}
		driverName := filepath.Base(driverLink)

		unbindPath := filepath.Join("/sys/bus/usb/drivers", driverName, "unbind")
		bindPath := filepath.Join("/sys/bus/usb/drivers", driverName, "bind")

		// Unbind
		if err := os.WriteFile(unbindPath, []byte(name), 0200); err != nil {
			log.Warn().Err(err).Str("device", name).Msg("device-supervisor: unbind failed")
			continue
		}
		time.Sleep(500 * time.Millisecond)

		// Rebind
		if err := os.WriteFile(bindPath, []byte(name), 0200); err != nil {
			log.Warn().Err(err).Str("device", name).Msg("device-supervisor: rebind failed")
			continue
		}
		time.Sleep(1 * time.Second)

		if hasTTYDevice(devDir) {
			log.Info().Str("device", name).Str("vidpid", vidpid).Msg("device-supervisor: USB rebind recovered tty device")
		} else {
			log.Warn().Str("device", name).Str("vidpid", vidpid).Msg("device-supervisor: USB rebind did not recover tty")
		}
	}
}

// hasTTYDevice checks if a USB device directory has an associated tty.
func hasTTYDevice(usbDevDir string) bool {
	// Walk interface subdirs looking for tty/ttyXXX
	entries, err := os.ReadDir(usbDevDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !strings.Contains(e.Name(), ":") {
			continue // not an interface dir
		}
		ttyDir := filepath.Join(usbDevDir, e.Name(), "tty")
		if info, err := os.Stat(ttyDir); err == nil && info.IsDir() {
			return true
		}
		// Also check for ttyUSB (FTDI-style: interface/ttyUSBX)
		ifaceEntries, err := os.ReadDir(filepath.Join(usbDevDir, e.Name()))
		if err != nil {
			continue
		}
		for _, ie := range ifaceEntries {
			if strings.HasPrefix(ie.Name(), "ttyUSB") || strings.HasPrefix(ie.Name(), "ttyACM") {
				return true
			}
		}
	}
	return false
}

// readSysfsFile reads a sysfs attribute file and returns trimmed content.
func readSysfsFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
