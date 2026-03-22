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
//  5. Astronode probe (~500ms)
//  6. ZNP probe for ZigBee (~500ms)
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
	OnPortFound func(port string) // called when a port is identified for this role
	OnPortLost  func(port string) // called when a claimed port vanishes
}

// DeviceSupervisor periodically scans serial ports and manages identification,
// claiming, and driver reconnection.
type DeviceSupervisor struct {
	registry *DeviceRegistry

	// Driver callbacks keyed by role
	callbacksMu sync.RWMutex
	callbacks   map[DeviceRole]*DriverCallbacks

	// Explicit port overrides (from env vars) — skip auto-detect for these roles
	explicitPorts map[DeviceRole]string

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
		registry:      NewDeviceRegistry(),
		callbacks:     make(map[DeviceRole]*DriverCallbacks),
		explicitPorts: make(map[DeviceRole]string),
		stopCh:        make(chan struct{}),
		scanNowCh:     make(chan struct{}, 1),
		eventClients:  make(map[uint64]chan DeviceEvent),
	}
}

// Registry returns the underlying device registry.
func (s *DeviceSupervisor) Registry() *DeviceRegistry {
	return s.registry
}

// SetCallbacks registers driver callbacks for a role.
func (s *DeviceSupervisor) SetCallbacks(role DeviceRole, cb *DriverCallbacks) {
	s.callbacksMu.Lock()
	defer s.callbacksMu.Unlock()
	s.callbacks[role] = cb
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

func (s *DeviceSupervisor) run() {
	// Register explicit ports first
	s.registerExplicitPorts()

	// Initial scan
	s.recoverMissingTTY()
	s.scanSerialPorts()
	s.reconcileSerialDevices()

	portTicker := time.NewTicker(30 * time.Second)
	serialTicker := time.NewTicker(15 * time.Second)
	defer portTicker.Stop()
	defer serialTicker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-portTicker.C:
			s.recoverMissingTTY()
			s.scanSerialPorts()
		case <-serialTicker.C:
			s.reconcileSerialDevices()
		case <-s.scanNowCh:
			s.recoverMissingTTY()
			s.scanSerialPorts()
			s.reconcileSerialDevices()
		}
	}
}

// registerExplicitPorts claims ports that were explicitly configured via env vars.
func (s *DeviceSupervisor) registerExplicitPorts() {
	for role, port := range s.explicitPorts {
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
				s.registry.Remove(port)
				// Fall through to register as new
			} else {
				// Same device, update LastSeen
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

	// Reconnect: check for roles that lost their port but a matching port reappeared
	s.reconnectDisconnected()
}

// identifyAndClaimPort runs the identification cascade on an unclaimed port.
// Order: VID:PID match → JSPR (9704) → AT (9603) → Cellular AT → Astronode → ZNP
func (s *DeviceSupervisor) identifyAndClaimPort(port string) {
	vidpid := findUSBVIDPID(port)

	// Step 1: VID:PID match (~1ms)
	if vidpid != "" {
		role := s.classifyByVIDPID(vidpid, port)
		if role != RoleNone {
			if s.registry.ClaimPort(port, role) {
				s.registry.SetRole(port, role)
				s.registry.SetState(port, StateReady)
				log.Info().Str("port", port).Str("vidpid", vidpid).Str("role", string(role)).Msg("device-supervisor: identified by VID:PID")
				s.notifyPortFound(role, port)
				s.emitEvent(DeviceEvent{
					Type:   "device_connected",
					Device: s.registryEntry(port),
				})
			}
			return
		}
	}

	// Step 2: JSPR probe for 9704 (~500ms)
	if probeJSPR(port) {
		role := RoleIridium9704
		if s.registry.ClaimPort(port, role) {
			s.registry.SetRole(port, role)
			s.registry.SetState(port, StateReady)
			log.Info().Str("port", port).Msg("device-supervisor: identified as 9704 by JSPR probe")
			s.notifyPortFound(role, port)
			s.emitEvent(DeviceEvent{
				Type:   "device_connected",
				Device: s.registryEntry(port),
			})
		}
		return
	}

	// Step 3: AT probe for 9603 (~500ms)
	// Skip ports with known non-Iridium VID:PIDs
	if vidpid == "" || (!knownMeshtasticVIDPIDs[vidpid] && !gpsVIDPIDs[vidpid] &&
		!knownCellularVIDPIDs[vidpid] && !knownAstrocastVIDPIDs[vidpid] &&
		!knownZigBeeOnlyVIDPIDs[vidpid]) {
		if probeAT(port) {
			role := RoleIridium9603
			if s.registry.ClaimPort(port, role) {
				s.registry.SetRole(port, role)
				s.registry.SetState(port, StateReady)
				log.Info().Str("port", port).Msg("device-supervisor: identified as 9603 by AT probe")
				s.notifyPortFound(role, port)
				s.emitEvent(DeviceEvent{
					Type:   "device_connected",
					Device: s.registryEntry(port),
				})
			}
			return
		}
	}

	// Step 4: Cellular AT probe (~500ms)
	// Only try on ports with known cellular VID:PIDs or truly unknown ports
	if vidpid == "" || knownCellularVIDPIDs[vidpid] {
		// Check multi-interface modem (Huawei E220: skip iface 0)
		if iface, ok := cellularATInterface[vidpid]; ok {
			if findUSBInterfaceNum(port) != iface {
				return // wrong interface for AT commands
			}
		}
		if probeCellularAT(port) {
			role := RoleCellular
			if s.registry.ClaimPort(port, role) {
				s.registry.SetRole(port, role)
				s.registry.SetState(port, StateReady)
				log.Info().Str("port", port).Msg("device-supervisor: identified as cellular by AT probe")
				s.notifyPortFound(role, port)
				s.emitEvent(DeviceEvent{
					Type:   "device_connected",
					Device: s.registryEntry(port),
				})
			}
			return
		}
	}

	// Step 5: Astronode probe (~500ms)
	// Only try on FTDI or CP210x ports that aren't already identified
	if vidpid == "" || knownAstrocastVIDPIDs[vidpid] {
		if probeAstronode(port) {
			role := RoleAstrocast
			if s.registry.ClaimPort(port, role) {
				s.registry.SetRole(port, role)
				s.registry.SetState(port, StateReady)
				log.Info().Str("port", port).Msg("device-supervisor: identified as astronode by probe")
				s.notifyPortFound(role, port)
				s.emitEvent(DeviceEvent{
					Type:   "device_connected",
					Device: s.registryEntry(port),
				})
			}
			return
		}
	}

	// Step 6: ZNP probe for ZigBee (~500ms)
	// Only try on ambiguous VID:PIDs (CP210x, CH343) or ZigBee-only VID:PIDs
	if knownZigBeeOnlyVIDPIDs[vidpid] || ambiguousZigBeeVIDPIDs[vidpid] {
		if ProbeZNP(port) {
			role := RoleZigBee
			if s.registry.ClaimPort(port, role) {
				s.registry.SetRole(port, role)
				s.registry.SetState(port, StateReady)
				log.Info().Str("port", port).Msg("device-supervisor: identified as zigbee by ZNP probe")
				s.notifyPortFound(role, port)
				s.emitEvent(DeviceEvent{
					Type:   "device_connected",
					Device: s.registryEntry(port),
				})
			}
			return
		}
	}

	// Unknown — will retry next cycle
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
		// For multi-interface modems, only claim the AT port
		if iface, ok := cellularATInterface[vidpid]; ok {
			if findUSBInterfaceNum(port) != iface {
				return RoleNone // wrong interface
			}
		}
		return RoleCellular
	}

	// Ambiguous Meshtastic/ZigBee — needs protocol probe, handled in cascade
	if ambiguousZigBeeVIDPIDs[vidpid] {
		// Try ZNP first (ZigBee is rarer, so if it responds it's definitely ZigBee)
		if ProbeZNP(port) {
			return RoleZigBee
		}
		return RoleMeshtastic // default for ambiguous
	}

	// Meshtastic-only (ESP32-S3 native USB, etc.)
	if knownMeshtasticVIDPIDs[vidpid] {
		return RoleMeshtastic
	}

	// FTDI VID:PIDs could be Iridium 9603, 9704, or Astrocast — needs protocol probe
	if knownIridiumVIDPIDs[vidpid] || knownAstrocastVIDPIDs[vidpid] {
		return RoleNone // fall through to protocol probes
	}

	return RoleNone
}

// reconnectDisconnected checks if any role lost its port but the port reappeared.
// IMPORTANT: Re-verifies VID:PID before reconnecting — a different device may have
// been plugged into the same port. If VID:PID changed, the old entry is removed
// and the port will be re-identified in the next reconcile cycle.
func (s *DeviceSupervisor) reconnectDisconnected() {
	for _, entry := range s.registry.ListAll() {
		if entry.State != StateDisconnected {
			continue
		}
		// Check if the port is back
		found := false
		for _, pattern := range []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/ttyAMA*"} {
			matches, _ := filepath.Glob(pattern)
			for _, m := range matches {
				if m == entry.DevPath {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
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

	if cb, ok := s.callbacks[role]; ok && cb.OnPortFound != nil {
		cb.OnPortFound(port)
	}
}

func (s *DeviceSupervisor) notifyPortLost(role DeviceRole, port string) {
	s.callbacksMu.RLock()
	defer s.callbacksMu.RUnlock()

	if cb, ok := s.callbacks[role]; ok && cb.OnPortLost != nil {
		cb.OnPortLost(port)
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
			!knownIMTVIDPIDs[vidpid] && !knownCellularVIDPIDs[vidpid] &&
			!knownAstrocastVIDPIDs[vidpid] {
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
