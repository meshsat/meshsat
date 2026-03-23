package hubreporter

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// EventTap hooks into the processor's event stream and forwards relevant
// events (positions, telemetry, node info) to the HubReporter. Text messages
// are intentionally excluded to avoid excessive volume and privacy concerns.
//
// Note on MQTT topic namespaces: The HubReporter publishes device data to
// Hub topics (meshsat/{device_id}/position, meshsat/{device_id}/telemetry).
// The bridge's MQTT gateway publishes to mesh topics
// (msh/cubeos/{channel}/{node}). These are different topic namespaces and
// do not conflict.
type EventTap struct {
	reporter  *HubReporter
	inventory *DeviceInventory
	bridgeID  string
}

// NewEventTap creates an EventTap that forwards mesh events to the Hub.
func NewEventTap(reporter *HubReporter, inventory *DeviceInventory, bridgeID string) *EventTap {
	return &EventTap{
		reporter:  reporter,
		inventory: inventory,
		bridgeID:  bridgeID,
	}
}

// HandleMeshEvent processes a single mesh event, extracting position,
// telemetry, or node info data and publishing it to the Hub. This method
// is safe for concurrent use and designed to be called from the processor's
// event broadcast loop. When the reporter is nil, the tap still updates the
// inventory but skips MQTT publishing.
func (t *EventTap) HandleMeshEvent(event transport.MeshEvent) {
	switch event.Type {
	case "node_update":
		t.handleNodeUpdate(event)
	case "position":
		t.handlePosition(event)
	case "message":
		t.handleMessage(event)
	}
}

// handleNodeUpdate processes node_update events which carry full MeshNode
// data including identity, position, and telemetry.
func (t *EventTap) handleNodeUpdate(event transport.MeshEvent) {
	var node transport.MeshNode
	if err := json.Unmarshal(event.Data, &node); err != nil {
		return
	}

	deviceID := node.UserID
	if deviceID == "" {
		deviceID = fmt.Sprintf("!%08x", node.Num)
	}

	// Register/update device in inventory (auto-deduplicates)
	birth := DeviceBirth{
		DeviceID: deviceID,
		Type:     DeviceMeshtastic,
		Label:    node.LongName,
		Hardware: node.HWModelName,
	}
	if node.ShortName != "" && birth.Label == "" {
		birth.Label = node.ShortName
	}
	if node.Latitude != 0 || node.Longitude != 0 {
		birth.Position = &Location{
			Lat:    node.Latitude,
			Lon:    node.Longitude,
			Alt:    float64(node.Altitude),
			Source: "gps",
		}
	}
	t.inventory.RegisterDevice(birth)

	// Publish position if present (skip if reporter is nil)
	if t.reporter != nil && (node.Latitude != 0 || node.Longitude != 0) {
		pos := DevicePosition{
			Lat:       node.Latitude,
			Lon:       node.Longitude,
			Alt:       float64(node.Altitude),
			Source:    "gps",
			Timestamp: time.Now().UTC(),
		}
		if err := t.reporter.PublishDevicePosition(deviceID, pos); err != nil {
			log.Debug().Err(err).Str("device_id", deviceID).Msg("hubreporter: failed to publish position")
		}
	}

	// Publish telemetry if any metrics are present (skip if reporter is nil)
	hasTelemetry := node.BatteryLevel > 0 || node.Voltage > 0 ||
		node.ChannelUtil > 0 || node.AirUtilTx > 0 ||
		node.Temperature != nil || node.Humidity != nil || node.Pressure != nil ||
		node.UptimeSeconds > 0

	if t.reporter != nil && hasTelemetry {
		tel := DeviceTelemetry{
			BatteryLevel: float64(node.BatteryLevel),
			Voltage:      float64(node.Voltage),
			ChannelUtil:  float64(node.ChannelUtil),
			AirUtilTx:    float64(node.AirUtilTx),
			UptimeSec:    int64(node.UptimeSeconds),
			Timestamp:    time.Now().UTC(),
		}
		if node.Temperature != nil {
			tel.Temperature = float64(*node.Temperature)
		}
		if node.Humidity != nil {
			tel.Humidity = float64(*node.Humidity)
		}
		if node.Pressure != nil {
			tel.Pressure = float64(*node.Pressure)
		}
		if err := t.reporter.PublishDeviceTelemetry(deviceID, tel); err != nil {
			log.Debug().Err(err).Str("device_id", deviceID).Msg("hubreporter: failed to publish telemetry")
		}
	}
}

// handlePosition processes standalone position events.
func (t *EventTap) handlePosition(event transport.MeshEvent) {
	var pos struct {
		NodeID    string  `json:"node_id"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Altitude  int     `json:"altitude"`
	}
	if err := json.Unmarshal(event.Data, &pos); err != nil {
		return
	}

	if pos.Latitude == 0 && pos.Longitude == 0 {
		return
	}

	deviceID := pos.NodeID
	if deviceID == "" {
		return
	}

	if t.reporter != nil {
		dp := DevicePosition{
			Lat:       pos.Latitude,
			Lon:       pos.Longitude,
			Alt:       float64(pos.Altitude),
			Source:    "gps",
			Timestamp: time.Now().UTC(),
		}
		if err := t.reporter.PublishDevicePosition(deviceID, dp); err != nil {
			log.Debug().Err(err).Str("device_id", deviceID).Msg("hubreporter: failed to publish position")
		}
	}
}

// handleMessage processes message events. Only NODEINFO_APP (portnum 4) and
// POSITION_APP (portnum 3) and TELEMETRY_APP (portnum 67) are extracted.
// Text messages (portnum 1) are intentionally skipped.
func (t *EventTap) handleMessage(event transport.MeshEvent) {
	var msg struct {
		From    uint32          `json:"from"`
		PortNum int             `json:"portnum"`
		Data    json.RawMessage `json:"data,omitempty"`
	}
	// First unmarshal to get portnum
	if err := json.Unmarshal(event.Data, &msg); err != nil {
		return
	}

	switch msg.PortNum {
	case 3: // POSITION_APP
		t.handlePositionPortnum(event.Data, msg.From)
	case 4: // NODEINFO_APP
		t.handleNodeInfoPortnum(event.Data, msg.From)
	case 67: // TELEMETRY_APP
		t.handleTelemetryPortnum(event.Data, msg.From)
	}
}

// handlePositionPortnum extracts position from a POSITION_APP message.
func (t *EventTap) handlePositionPortnum(data json.RawMessage, from uint32) {
	var msg transport.MeshMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	// Position data is typically in the decoded text as JSON
	// but the processor handles it via node_update events, so
	// position messages with raw payload are less common here.
	// We primarily rely on node_update and standalone position events.
	deviceID := fmt.Sprintf("!%08x", from)
	_ = deviceID // position extraction from message portnum is best-effort
}

// handleNodeInfoPortnum registers a device from a NODEINFO_APP message.
func (t *EventTap) handleNodeInfoPortnum(data json.RawMessage, from uint32) {
	deviceID := fmt.Sprintf("!%08x", from)

	// Try to extract long_name/short_name from decoded text
	var msg transport.MeshMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	birth := DeviceBirth{
		DeviceID: deviceID,
		Type:     DeviceMeshtastic,
		Label:    msg.DecodedText, // NODEINFO_APP decoded text is typically the long name
	}
	t.inventory.RegisterDevice(birth)
}

// handleTelemetryPortnum extracts telemetry from a TELEMETRY_APP message.
func (t *EventTap) handleTelemetryPortnum(data json.RawMessage, from uint32) {
	deviceID := fmt.Sprintf("!%08x", from)

	var msg transport.MeshMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	// Telemetry from TELEMETRY_APP messages is typically available via
	// node_update events which carry full MeshNode data including
	// battery, voltage, channel util, etc. We handle it there primarily.
	// This handler is a fallback — if we get a standalone telemetry
	// message, we register the device at minimum.
	if !t.inventory.IsRegistered(deviceID) {
		t.inventory.RegisterDevice(DeviceBirth{
			DeviceID: deviceID,
			Type:     DeviceMeshtastic,
		})
	}
}
