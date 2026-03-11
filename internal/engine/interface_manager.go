package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// InterfaceState represents the lifecycle state of an interface.
type InterfaceState int

const (
	StateUnbound InterfaceState = iota // no device_id configured
	StateOffline                       // device_id set but device not present
	StateBinding                       // device detected, transport initializing
	StateOnline                        // transport connected, ready
	StateError                         // transport error, awaiting retry
)

// String returns the state name for JSON and logging.
func (s InterfaceState) String() string {
	switch s {
	case StateUnbound:
		return "unbound"
	case StateOffline:
		return "offline"
	case StateBinding:
		return "binding"
	case StateOnline:
		return "online"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// MarshalJSON implements json.Marshaler.
func (s InterfaceState) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// InterfaceStatus is the runtime status of a single interface.
type InterfaceStatus struct {
	ID           string         `json:"id"`
	ChannelType  string         `json:"channel_type"`
	Label        string         `json:"label"`
	Enabled      bool           `json:"enabled"`
	State        InterfaceState `json:"state"`
	DeviceID     string         `json:"device_id,omitempty"`
	DevicePort   string         `json:"device_port,omitempty"`
	Error        string         `json:"error,omitempty"`
	LastActivity time.Time      `json:"last_activity,omitempty"`
}

// DetectedDevice represents a USB device found during hotplug scan.
type DetectedDevice struct {
	Port       string `json:"port"`
	VIDPID     string `json:"vid_pid"`
	DeviceID   string `json:"device_id"`
	DeviceType string `json:"device_type"` // meshtastic, iridium, cellular, astrocast, gps, unknown
	BoundTo    string `json:"bound_to"`    // interface ID if claimed, empty if unassigned
}

// interfaceRuntime holds mutable runtime state for one interface.
type interfaceRuntime struct {
	iface        database.Interface
	state        InterfaceState
	errorMsg     string
	lastActivity time.Time
}

// InterfaceManager manages the lifecycle and device binding for all interfaces.
type InterfaceManager struct {
	db       *database.DB
	mu       sync.RWMutex
	states   map[string]*interfaceRuntime // interface ID → runtime
	devices  []DetectedDevice             // last scan result
	cancelFn context.CancelFunc

	scanInterval  time.Duration
	onStateChange func(ifaceID, channelType string, newState InterfaceState)
}

// NewInterfaceManager creates a new interface manager.
func NewInterfaceManager(db *database.DB) *InterfaceManager {
	return &InterfaceManager{
		db:           db,
		states:       make(map[string]*interfaceRuntime),
		scanInterval: 5 * time.Second,
	}
}

// channelNeedsDevice returns true if the channel type requires a USB serial device binding.
func channelNeedsDevice(channelType string) bool {
	switch channelType {
	case "mqtt", "webhook":
		return false
	default:
		return true // mesh, iridium, cellular, astrocast, zigbee all need hardware
	}
}

// SetStateChangeCallback registers a callback that fires when an interface changes state.
func (m *InterfaceManager) SetStateChangeCallback(fn func(ifaceID, channelType string, newState InterfaceState)) {
	m.onStateChange = fn
}

// notifyStateChange fires the callback if registered. Must NOT be called with mu held.
func (m *InterfaceManager) notifyStateChange(ifaceID, channelType string, state InterfaceState) {
	if m.onStateChange != nil {
		m.onStateChange(ifaceID, channelType, state)
	}
}

// Start loads interfaces from DB and starts the device scan loop.
func (m *InterfaceManager) Start(ctx context.Context) error {
	ctx, m.cancelFn = context.WithCancel(ctx)

	ifaces, err := m.db.GetAllInterfaces()
	if err != nil {
		log.Warn().Err(err).Msg("ifacemgr: failed to load interfaces from DB")
		return nil
	}

	m.mu.Lock()
	for _, iface := range ifaces {
		rt := &interfaceRuntime{iface: iface}
		if !channelNeedsDevice(iface.ChannelType) && iface.Enabled {
			// Network-based interfaces (mqtt, webhook) — online when enabled
			rt.state = StateOnline
			rt.lastActivity = time.Now()
		} else if iface.DeviceID == "" {
			rt.state = StateUnbound
		} else {
			rt.state = StateOffline
		}
		m.states[iface.ID] = rt
	}
	m.mu.Unlock()

	log.Info().Int("count", len(ifaces)).Msg("ifacemgr: loaded interfaces from DB")

	// Initial device scan
	m.scanDevices()

	// Periodic scan loop
	go m.scanLoop(ctx)

	return nil
}

// Stop cancels the scan loop and sets all interfaces offline.
func (m *InterfaceManager) Stop() {
	if m.cancelFn != nil {
		m.cancelFn()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rt := range m.states {
		if rt.state == StateOnline || rt.state == StateBinding {
			rt.state = StateOffline
		}
	}
}

// GetStatus returns the runtime status of one interface.
func (m *InterfaceManager) GetStatus(id string) (*InterfaceStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rt, ok := m.states[id]
	if !ok {
		return nil, fmt.Errorf("interface %q not found", id)
	}
	return m.statusFromRuntime(rt), nil
}

// GetAllStatus returns the runtime status of all interfaces.
func (m *InterfaceManager) GetAllStatus() []InterfaceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]InterfaceStatus, 0, len(m.states))
	for _, rt := range m.states {
		result = append(result, *m.statusFromRuntime(rt))
	}
	return result
}

// GetDetectedDevices returns all USB devices found in the last scan.
func (m *InterfaceManager) GetDetectedDevices() []DetectedDevice {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]DetectedDevice, len(m.devices))
	copy(out, m.devices)
	return out
}

// GetUnassignedDevices returns detected devices not bound to any interface.
func (m *InterfaceManager) GetUnassignedDevices() []DetectedDevice {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []DetectedDevice
	for _, d := range m.devices {
		if d.BoundTo == "" {
			out = append(out, d)
		}
	}
	return out
}

// CreateInterface adds a new interface to the DB and runtime.
func (m *InterfaceManager) CreateInterface(iface database.Interface) error {
	if err := m.db.InsertInterface(&iface); err != nil {
		return err
	}

	m.mu.Lock()
	rt := &interfaceRuntime{iface: iface}
	if iface.DeviceID == "" {
		rt.state = StateUnbound
	} else {
		rt.state = StateOffline
	}
	m.states[iface.ID] = rt
	m.mu.Unlock()

	log.Info().Str("id", iface.ID).Str("type", iface.ChannelType).Msg("ifacemgr: interface created")
	return nil
}

// UpdateInterface updates an interface's config in DB and runtime.
func (m *InterfaceManager) UpdateInterface(iface database.Interface) error {
	if err := m.db.UpdateInterface(&iface); err != nil {
		return err
	}

	m.mu.Lock()
	if rt, ok := m.states[iface.ID]; ok {
		oldDeviceID := rt.iface.DeviceID
		rt.iface = iface
		// If device_id changed, reset state
		if iface.DeviceID != oldDeviceID {
			if iface.DeviceID == "" {
				rt.state = StateUnbound
			} else {
				rt.state = StateOffline
			}
		}
	}
	m.mu.Unlock()

	log.Info().Str("id", iface.ID).Msg("ifacemgr: interface updated")
	return nil
}

// DeleteInterface removes an interface from DB and runtime.
func (m *InterfaceManager) DeleteInterface(id string) error {
	if err := m.db.DeleteInterface(id); err != nil {
		return err
	}

	m.mu.Lock()
	delete(m.states, id)
	m.mu.Unlock()

	log.Info().Str("id", id).Msg("ifacemgr: interface deleted")
	return nil
}

// BindDevice associates a hardware device ID with an interface.
func (m *InterfaceManager) BindDevice(interfaceID, deviceID string) error {
	m.mu.Lock()
	rt, ok := m.states[interfaceID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("interface %q not found", interfaceID)
	}
	rt.iface.DeviceID = deviceID
	rt.state = StateOffline // will be promoted on next scan if device present
	m.mu.Unlock()

	return m.db.UpdateInterface(&rt.iface)
}

// UnbindDevice clears the hardware binding for an interface.
func (m *InterfaceManager) UnbindDevice(interfaceID string) error {
	m.mu.Lock()
	rt, ok := m.states[interfaceID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("interface %q not found", interfaceID)
	}
	rt.iface.DeviceID = ""
	rt.iface.DevicePort = ""
	rt.state = StateUnbound
	m.mu.Unlock()

	return m.db.UpdateInterface(&rt.iface)
}

// SetOnline marks an interface as ONLINE. Called by transport layer when connected.
func (m *InterfaceManager) SetOnline(id string) {
	m.mu.Lock()
	var channelType string
	changed := false
	if rt, ok := m.states[id]; ok {
		if rt.state != StateOnline {
			changed = true
			channelType = rt.iface.ChannelType
		}
		rt.state = StateOnline
		rt.lastActivity = time.Now()
		rt.errorMsg = ""
	}
	m.mu.Unlock()

	if changed {
		m.notifyStateChange(id, channelType, StateOnline)
	}
}

// SetError marks an interface as ERROR with a reason.
func (m *InterfaceManager) SetError(id, errMsg string) {
	m.mu.Lock()
	var channelType string
	changed := false
	if rt, ok := m.states[id]; ok {
		if rt.state != StateError {
			changed = true
			channelType = rt.iface.ChannelType
		}
		rt.state = StateError
		rt.errorMsg = errMsg
	}
	m.mu.Unlock()

	if changed {
		m.notifyStateChange(id, channelType, StateError)
	}
}

// scanLoop runs periodic device scanning.
func (m *InterfaceManager) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(m.scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scanDevices()
		}
	}
}

// scanDevices enumerates USB serial ports and matches them to interface bindings.
func (m *InterfaceManager) scanDevices() {
	var ports []string
	if matches, _ := filepath.Glob("/dev/ttyACM*"); matches != nil {
		ports = append(ports, matches...)
	}
	if matches, _ := filepath.Glob("/dev/ttyUSB*"); matches != nil {
		ports = append(ports, matches...)
	}

	detected := make([]DetectedDevice, 0, len(ports))
	for _, port := range ports {
		deviceID := transport.FindUSBDeviceID(port)
		vidpid := strings.SplitN(deviceID, "+", 2)[0] // VID:PID portion
		devType := transport.ClassifyDeviceWithProbe(vidpid, port)

		detected = append(detected, DetectedDevice{
			Port:       port,
			VIDPID:     vidpid,
			DeviceID:   deviceID,
			DeviceType: devType,
		})
	}

	type stateChange struct {
		ifaceID     string
		channelType string
		newState    InterfaceState
	}
	var changes []stateChange

	m.mu.Lock()

	// Build device_id → port lookup from detected devices
	devicePorts := make(map[string]string) // device_id → port
	for i := range detected {
		devicePorts[detected[i].DeviceID] = detected[i].Port
		// Also index by VID:PID only for fallback matching
		devicePorts[detected[i].VIDPID] = detected[i].Port
	}

	// Match detected devices to interface bindings
	for _, rt := range m.states {
		if rt.iface.DeviceID == "" {
			continue // unbound, skip
		}

		port, found := devicePorts[rt.iface.DeviceID]
		if !found {
			// Try VID:PID-only match (device_id might be just VID:PID)
			port, found = devicePorts[rt.iface.DeviceID]
		}

		if found {
			// Device present — promote to online (direct mode transports manage their own connections)
			if rt.state == StateOffline || rt.state == StateBinding {
				rt.state = StateOnline
				rt.iface.DevicePort = port
				rt.lastActivity = time.Now()
				log.Info().Str("id", rt.iface.ID).Str("port", port).Msg("ifacemgr: device detected, online")
				changes = append(changes, stateChange{rt.iface.ID, rt.iface.ChannelType, StateOnline})
			}
		} else {
			// Device gone
			if rt.state == StateOnline || rt.state == StateBinding {
				oldState := rt.state
				rt.state = StateOffline
				rt.iface.DevicePort = ""
				log.Warn().Str("id", rt.iface.ID).Str("old_state", oldState.String()).Msg("ifacemgr: device disconnected")
				changes = append(changes, stateChange{rt.iface.ID, rt.iface.ChannelType, StateOffline})
			}
		}
	}

	// Auto-bind: if an unbound interface's channel_type matches exactly one
	// unassigned detected device, bind them automatically.
	// Build set of already-bound device IDs
	boundDeviceIDs := make(map[string]bool)
	for _, rt := range m.states {
		if rt.iface.DeviceID != "" {
			boundDeviceIDs[rt.iface.DeviceID] = true
		}
	}

	for _, rt := range m.states {
		if rt.iface.DeviceID != "" || !rt.iface.Enabled {
			continue // already bound or disabled
		}
		// Find unassigned devices matching this channel type
		var candidates []DetectedDevice
		for _, d := range detected {
			if d.DeviceType == rt.iface.ChannelType && !boundDeviceIDs[d.DeviceID] {
				candidates = append(candidates, d)
			}
		}
		// Auto-bind only when exactly one candidate (unambiguous)
		if len(candidates) == 1 {
			dev := candidates[0]
			rt.iface.DeviceID = dev.DeviceID
			rt.iface.DevicePort = dev.Port
			rt.state = StateOnline
			rt.lastActivity = time.Now()
			boundDeviceIDs[dev.DeviceID] = true
			log.Info().Str("id", rt.iface.ID).Str("device", dev.DeviceID).Str("port", dev.Port).Msg("ifacemgr: auto-bound device, online")
			changes = append(changes, stateChange{rt.iface.ID, rt.iface.ChannelType, StateOnline})
			// Persist binding to DB (best-effort, don't block scan)
			if m.db != nil {
				_ = m.db.UpdateInterface(&rt.iface)
			}
		}
	}

	// Tag detected devices with their bound interface
	for i := range detected {
		for _, rt := range m.states {
			if rt.iface.DeviceID != "" &&
				(rt.iface.DeviceID == detected[i].DeviceID || rt.iface.DeviceID == detected[i].VIDPID) {
				detected[i].BoundTo = rt.iface.ID
				break
			}
		}
	}

	m.devices = detected
	m.mu.Unlock()

	// Fire state change callbacks outside the lock
	for _, c := range changes {
		m.notifyStateChange(c.ifaceID, c.channelType, c.newState)
	}
}

func (m *InterfaceManager) statusFromRuntime(rt *interfaceRuntime) *InterfaceStatus {
	return &InterfaceStatus{
		ID:           rt.iface.ID,
		ChannelType:  rt.iface.ChannelType,
		Label:        rt.iface.Label,
		Enabled:      rt.iface.Enabled,
		State:        rt.state,
		DeviceID:     rt.iface.DeviceID,
		DevicePort:   rt.iface.DevicePort,
		Error:        rt.errorMsg,
		LastActivity: rt.lastActivity,
	}
}
