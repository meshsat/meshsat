package transport

// DeviceRegistry — Central USB device inventory with claim-based port management.
// Ported from HAL's DeviceRegistry, adapted for MeshSat Bridge's richer role set.

import (
	"sync"
	"time"
)

// DeviceRole sub-classifies serial devices by their protocol/function.
type DeviceRole string

const (
	RoleMeshtastic  DeviceRole = "meshtastic"
	RoleIridium9603 DeviceRole = "iridium_9603"
	RoleIridium9704 DeviceRole = "iridium_9704"
	RoleCellular    DeviceRole = "cellular"
	RoleGPS         DeviceRole = "gps"
	RoleZigBee      DeviceRole = "zigbee"
	RoleAstrocast   DeviceRole = "astrocast"
	RoleNone        DeviceRole = ""
)

// DeviceState tracks a device's lifecycle from detection through connection.
type DeviceState string

const (
	StateDetected     DeviceState = "detected"
	StateIdentifying  DeviceState = "identifying"
	StateReady        DeviceState = "ready"
	StateConnected    DeviceState = "connected"
	StateDisconnected DeviceState = "disconnected"
	StateRemoved      DeviceState = "removed"
)

// SerialDeviceEntry holds metadata for a discovered serial device.
type SerialDeviceEntry struct {
	DevPath   string      `json:"dev_path"`             // /dev/ttyUSB0
	VIDPID    string      `json:"vid_pid"`              // "0403:6015"
	USBSerial string      `json:"usb_serial,omitempty"` // USB serial number
	Role      DeviceRole  `json:"role,omitempty"`
	State     DeviceState `json:"state"`
	Error     string      `json:"error,omitempty"`
	FirstSeen time.Time   `json:"first_seen"`
	LastSeen  time.Time   `json:"last_seen"`
}

// DeviceRegistry is a thread-safe registry of serial devices with claim-based port management.
type DeviceRegistry struct {
	mu      sync.RWMutex
	devices map[string]*SerialDeviceEntry // keyed by dev path (/dev/ttyUSB0)

	claimsMu sync.Mutex
	claims   map[string]DeviceRole // devPath → role
}

// NewDeviceRegistry creates a new empty registry.
func NewDeviceRegistry() *DeviceRegistry {
	return &DeviceRegistry{
		devices: make(map[string]*SerialDeviceEntry),
		claims:  make(map[string]DeviceRole),
	}
}

// Upsert adds or updates a device in the registry.
// Returns true if the device was newly added.
func (r *DeviceRegistry) Upsert(devPath string, entry SerialDeviceEntry) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, exists := r.devices[devPath]
	if exists {
		existing.LastSeen = entry.LastSeen
		if entry.VIDPID != "" {
			existing.VIDPID = entry.VIDPID
		}
		if entry.USBSerial != "" {
			existing.USBSerial = entry.USBSerial
		}
		return false
	}

	e := entry
	e.DevPath = devPath
	r.devices[devPath] = &e
	return true
}

// Remove removes a device and releases its claim.
func (r *DeviceRegistry) Remove(devPath string) *SerialDeviceEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.devices[devPath]
	if !exists {
		return nil
	}

	r.claimsMu.Lock()
	delete(r.claims, devPath)
	r.claimsMu.Unlock()

	entry.State = StateRemoved
	delete(r.devices, devPath)
	return entry
}

// ClaimPort atomically claims a serial port for a role.
// Returns true if the claim succeeded (port was unclaimed).
func (r *DeviceRegistry) ClaimPort(devPath string, role DeviceRole) bool {
	r.claimsMu.Lock()
	defer r.claimsMu.Unlock()

	if _, claimed := r.claims[devPath]; claimed {
		return false
	}
	r.claims[devPath] = role
	return true
}

// ReleasePort releases a serial port claim.
func (r *DeviceRegistry) ReleasePort(devPath string) {
	r.claimsMu.Lock()
	defer r.claimsMu.Unlock()
	delete(r.claims, devPath)
}

// GetPortRole returns the role that has claimed a port, or RoleNone.
func (r *DeviceRegistry) GetPortRole(devPath string) DeviceRole {
	r.claimsMu.Lock()
	defer r.claimsMu.Unlock()
	return r.claims[devPath]
}

// FindByPort returns the device entry for a specific port path, or nil.
func (r *DeviceRegistry) FindByPort(devPath string) *SerialDeviceEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if entry, exists := r.devices[devPath]; exists {
		e := *entry
		return &e
	}
	return nil
}

// FindByRole returns the first device with a given role, or nil.
func (r *DeviceRegistry) FindByRole(role DeviceRole) *SerialDeviceEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, entry := range r.devices {
		if entry.Role == role {
			e := *entry
			return &e
		}
	}
	return nil
}

// FindAllByRole returns all devices with a given role.
func (r *DeviceRegistry) FindAllByRole(role DeviceRole) []*SerialDeviceEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*SerialDeviceEntry
	for _, entry := range r.devices {
		if entry.Role == role {
			e := *entry
			result = append(result, &e)
		}
	}
	return result
}

// ListAll returns a snapshot of all devices.
func (r *DeviceRegistry) ListAll() []*SerialDeviceEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*SerialDeviceEntry, 0, len(r.devices))
	for _, entry := range r.devices {
		e := *entry
		result = append(result, &e)
	}
	return result
}

// SetState updates the state of a device.
func (r *DeviceRegistry) SetState(devPath string, state DeviceState) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, exists := r.devices[devPath]; exists {
		entry.State = state
	}
}

// UpdateLastSeen updates the LastSeen timestamp for a device. [MESHSAT-444]
func (r *DeviceRegistry) UpdateLastSeen(devPath string, t time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, exists := r.devices[devPath]; exists {
		entry.LastSeen = t
	}
}

// SetRole updates the role of a device.
func (r *DeviceRegistry) SetRole(devPath string, role DeviceRole) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, exists := r.devices[devPath]; exists {
		entry.Role = role
	}
}

// Reconcile removes entries for ports that no longer exist.
// Returns entries that were removed.
func (r *DeviceRegistry) Reconcile(activePorts map[string]bool) []*SerialDeviceEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	var removed []*SerialDeviceEntry
	for devPath, entry := range r.devices {
		if !activePorts[devPath] {
			entry.State = StateRemoved
			removed = append(removed, entry)

			r.claimsMu.Lock()
			delete(r.claims, devPath)
			r.claimsMu.Unlock()

			delete(r.devices, devPath)
		}
	}
	return removed
}
