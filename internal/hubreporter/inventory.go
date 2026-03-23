package hubreporter

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// DeviceInventory tracks which devices the bridge manages and publishes
// births/deaths to the HubReporter. It deduplicates — calling RegisterDevice
// with the same device_id and unchanged data does NOT re-publish.
type DeviceInventory struct {
	reporter *HubReporter
	bridgeID string
	mu       sync.Mutex
	devices  map[string]DeviceBirth // device_id -> last birth cert
}

// NewDeviceInventory creates a DeviceInventory that publishes device
// lifecycle events through the given HubReporter.
func NewDeviceInventory(reporter *HubReporter, bridgeID string) *DeviceInventory {
	return &DeviceInventory{
		reporter: reporter,
		bridgeID: bridgeID,
		devices:  make(map[string]DeviceBirth),
	}
}

// RegisterDevice publishes a DeviceBirth if the device is new or its
// metadata has changed since the last registration. Returns true if a
// birth was published.
func (inv *DeviceInventory) RegisterDevice(device DeviceBirth) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	existing, ok := inv.devices[device.DeviceID]
	if ok && deviceUnchanged(existing, device) {
		return false
	}

	device.Timestamp = time.Now().UTC()
	if device.CoTType == "" {
		device.CoTType = CoTTypeForDevice(device.Type)
	}
	if device.CoTCallsign == "" {
		device.CoTCallsign = device.DeviceID
	}

	inv.devices[device.DeviceID] = device

	if inv.reporter != nil {
		if err := inv.reporter.PublishDeviceBirth(device); err != nil {
			log.Warn().Err(err).Str("device_id", device.DeviceID).Msg("hubreporter: failed to publish device birth")
		} else {
			log.Info().Str("device_id", device.DeviceID).Str("type", device.Type).Msg("hubreporter: device birth published")
		}
	}
	return true
}

// UnregisterDevice publishes a DeviceDeath for the given device and
// removes it from the inventory.
func (inv *DeviceInventory) UnregisterDevice(deviceID, reason string) {
	inv.mu.Lock()
	_, ok := inv.devices[deviceID]
	if !ok {
		inv.mu.Unlock()
		return
	}
	delete(inv.devices, deviceID)
	inv.mu.Unlock()

	death := DeviceDeath{
		DeviceID:  deviceID,
		Reason:    reason,
		Timestamp: time.Now().UTC(),
	}
	if inv.reporter != nil {
		if err := inv.reporter.PublishDeviceDeath(death); err != nil {
			log.Warn().Err(err).Str("device_id", deviceID).Msg("hubreporter: failed to publish device death")
		} else {
			log.Info().Str("device_id", deviceID).Str("reason", reason).Msg("hubreporter: device death published")
		}
	}
}

// UnregisterAll publishes a death for every tracked device. Used on shutdown.
func (inv *DeviceInventory) UnregisterAll(reason string) {
	inv.mu.Lock()
	ids := make([]string, 0, len(inv.devices))
	for id := range inv.devices {
		ids = append(ids, id)
	}
	inv.mu.Unlock()

	for _, id := range ids {
		inv.UnregisterDevice(id, reason)
	}
}

// IsRegistered returns whether a device is currently tracked.
func (inv *DeviceInventory) IsRegistered(deviceID string) bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	_, ok := inv.devices[deviceID]
	return ok
}

// Count returns the number of tracked devices.
func (inv *DeviceInventory) Count() int {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	return len(inv.devices)
}

// deviceUnchanged returns true if the two birth certs describe the same
// device with the same metadata. Timestamp is intentionally excluded.
func deviceUnchanged(a, b DeviceBirth) bool {
	return a.DeviceID == b.DeviceID &&
		a.Type == b.Type &&
		a.Label == b.Label &&
		a.Hardware == b.Hardware &&
		a.Firmware == b.Firmware &&
		a.IMEI == b.IMEI
}
