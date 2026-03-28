// Package hubreporter implements the bridge-to-hub uplink protocol.
//
// Protocol: meshsat-uplink/v1 (Sparkplug B inspired, CoT-native)
//
// The bridge connects to the Hub MQTT broker and publishes lifecycle events
// (birth/death/health) plus device telemetry. The Hub uses these to build
// a fleet-wide situational awareness picture with TAK/CoT projection.
package hubreporter

import (
	"encoding/json"
	"fmt"
	"time"
)

// ProtocolVersion identifies this protocol revision. The Hub rejects unknown versions.
const ProtocolVersion = "meshsat-uplink/v1"

// MQTT topic patterns. Use the Topic* functions for safe formatting.
const (
	topicBridgeBirth   = "meshsat/bridge/%s/birth"
	topicBridgeDeath   = "meshsat/bridge/%s/death"
	topicBridgeHealth  = "meshsat/bridge/%s/health"
	topicBridgeCmd     = "meshsat/bridge/%s/cmd"
	topicBridgeCmdResp = "meshsat/bridge/%s/cmd/response"
	topicDeviceBirth   = "meshsat/bridge/%s/device/%s/birth"
	topicDeviceDeath   = "meshsat/bridge/%s/device/%s/death"

	// Legacy device topics — existing Hub TAK subscriber listens on these.
	topicDevicePosition  = "meshsat/%s/position"
	topicDeviceTelemetry = "meshsat/%s/telemetry"
	topicDeviceSOS       = "meshsat/%s/sos"
	topicDeviceMessage   = "meshsat/%s/mo/decoded"
)

// CoT type constants (MIL-STD-2525 symbology).
const (
	CoTBridge    = "a-f-G-U-C-I" // Friendly Ground Unit — Infrastructure
	CoTMeshNode  = "a-f-G-U-C"   // Friendly Ground Unit
	CoTSatModem  = "a-f-G-E-S"   // Friendly Ground Equipment — Sensor
	CoTCellModem = "a-f-G-E-C"   // Friendly Ground Equipment — Comms
	CoTMobile    = "a-f-G-U-C"   // Friendly Ground Unit (mobile)
	CoTHub       = "a-f-G-I-H"   // Friendly Ground Installation — HQ
	CoTEmergency = "b-a"         // Alarm/Emergency
	CoTChat      = "b-t-f"       // GeoChat
)

// Device type identifiers.
const (
	DeviceMeshtastic = "meshtastic_node"
	DeviceIridiumSBD = "iridium_sbd"
	DeviceIridiumIMT = "iridium_imt"
	DeviceCellular   = "cellular"
	DeviceAstrocast  = "astrocast"
	DeviceZigBee     = "zigbee"
	DeviceAPRS       = "aprs"
)

// --- Topic builders ---

func TopicBridgeBirth(bridgeID string) string   { return fmt.Sprintf(topicBridgeBirth, bridgeID) }
func TopicBridgeDeath(bridgeID string) string   { return fmt.Sprintf(topicBridgeDeath, bridgeID) }
func TopicBridgeHealth(bridgeID string) string  { return fmt.Sprintf(topicBridgeHealth, bridgeID) }
func TopicBridgeCmd(bridgeID string) string     { return fmt.Sprintf(topicBridgeCmd, bridgeID) }
func TopicBridgeCmdResp(bridgeID string) string { return fmt.Sprintf(topicBridgeCmdResp, bridgeID) }

func TopicDeviceBirth(bridgeID, deviceID string) string {
	return fmt.Sprintf(topicDeviceBirth, bridgeID, deviceID)
}

func TopicDeviceDeath(bridgeID, deviceID string) string {
	return fmt.Sprintf(topicDeviceDeath, bridgeID, deviceID)
}

func TopicDevicePosition(deviceID string) string  { return fmt.Sprintf(topicDevicePosition, deviceID) }
func TopicDeviceTelemetry(deviceID string) string { return fmt.Sprintf(topicDeviceTelemetry, deviceID) }
func TopicDeviceSOS(deviceID string) string       { return fmt.Sprintf(topicDeviceSOS, deviceID) }
func TopicDeviceMessage(deviceID string) string   { return fmt.Sprintf(topicDeviceMessage, deviceID) }

// --- Shared types ---

// Location represents a geographic position with source attribution.
type Location struct {
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Alt    float64 `json:"alt"`
	Source string  `json:"source"` // "gps", "fixed", "iridium_cep", "cell_tower"
}

// InterfaceInfo describes a transport interface on the bridge.
type InterfaceInfo struct {
	Name   string `json:"name"`           // e.g. "mesh_0", "iridium_0"
	Type   string `json:"type"`           // "meshtastic", "iridium_sbd", "iridium_imt", "cellular", "astrocast", "zigbee", "aprs", "tcp"
	Status string `json:"status"`         // "online", "offline", "error", "binding"
	Port   string `json:"port,omitempty"` // serial port path
	IMEI   string `json:"imei,omitempty"` // satellite/cellular modem IMEI
	IMSI   string `json:"imsi,omitempty"` // cellular SIM IMSI
}

// InterfaceHealth reports live metrics for a transport interface.
type InterfaceHealth struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	HealthScore int    `json:"health_score,omitempty"` // 0-100
	SignalBars  int    `json:"signal_bars,omitempty"`  // 0-5 (satellite)
	SignalDBm   int    `json:"signal_dbm,omitempty"`   // dBm (cellular)
	Operator    string `json:"operator,omitempty"`     // cellular operator name
	NodesSeen   int    `json:"nodes_seen,omitempty"`   // meshtastic peer count
	MOCount     int64  `json:"mo_count,omitempty"`     // satellite MO messages sent
	MTCount     int64  `json:"mt_count,omitempty"`     // satellite MT messages received
}

// ReticulumInfo describes the bridge's Reticulum identity.
type ReticulumInfo struct {
	IdentityHash     string `json:"identity_hash"`
	PublicKey        string `json:"public_key"`
	TransportEnabled bool   `json:"transport_enabled"`
}

// ReticulumStats reports live Reticulum routing metrics.
type ReticulumStats struct {
	Routes           int   `json:"routes"`
	Links            int   `json:"links"`
	AnnouncesRelayed int64 `json:"announces_relayed"`
}

// BurstQueueInfo reports satellite burst queue status.
type BurstQueueInfo struct {
	Pending    int        `json:"pending"`
	NextWindow *time.Time `json:"next_window,omitempty"`
}

// OutboxInfo reports offline message queue status.
type OutboxInfo struct {
	Pending  int        `json:"pending"`
	Oldest   *time.Time `json:"oldest,omitempty"`
	Replayed int64      `json:"replayed"`
}

// --- Bridge lifecycle messages ---

// BridgeBirth is published when the bridge connects to the Hub.
// Topic: meshsat/bridge/{bridge_id}/birth (retained, QoS 1)
type BridgeBirth struct {
	Protocol     string          `json:"protocol"`
	BridgeID     string          `json:"bridge_id"`
	Version      string          `json:"version"`
	Hostname     string          `json:"hostname"`
	Mode         string          `json:"mode"` // "direct" or "cubeos"
	TenantID     string          `json:"tenant_id"`
	Location     *Location       `json:"location,omitempty"`
	Interfaces   []InterfaceInfo `json:"interfaces"`
	Capabilities []string        `json:"capabilities"`
	Reticulum    *ReticulumInfo  `json:"reticulum,omitempty"`
	CoTType      string          `json:"cot_type"`
	CoTCallsign  string          `json:"cot_callsign"`
	UptimeSec    int64           `json:"uptime_sec"`
	Timestamp    time.Time       `json:"timestamp"`
	Certificate  string          `json:"certificate,omitempty"` // base64 PEM of bridge TLS cert
	Signature    string          `json:"signature,omitempty"`   // base64 ECDSA-P256-SHA256 signature
}

// BridgeDeath is published when the bridge disconnects (explicit or LWT).
// Topic: meshsat/bridge/{bridge_id}/death (QoS 1)
type BridgeDeath struct {
	Protocol  string    `json:"protocol"`
	BridgeID  string    `json:"bridge_id"`
	Reason    string    `json:"reason"` // "shutdown", "lwt", "error"
	Timestamp time.Time `json:"timestamp"`
}

// BridgeHealth is published periodically (default 30s).
// Topic: meshsat/bridge/{bridge_id}/health (QoS 0)
type BridgeHealth struct {
	Protocol   string            `json:"protocol"`
	BridgeID   string            `json:"bridge_id"`
	UptimeSec  int64             `json:"uptime_sec"`
	CPUPct     float64           `json:"cpu_pct"`
	MemPct     float64           `json:"mem_pct"`
	DiskPct    float64           `json:"disk_pct"`
	Interfaces []InterfaceHealth `json:"interfaces"`
	BurstQueue *BurstQueueInfo   `json:"burst_queue,omitempty"`
	Reticulum  *ReticulumStats   `json:"reticulum,omitempty"`
	Outbox     *OutboxInfo       `json:"outbox,omitempty"`
	Timestamp  time.Time         `json:"timestamp"`
}

// --- Device lifecycle messages ---

// DeviceBirth is published when a device comes online under this bridge.
// Topic: meshsat/bridge/{bridge_id}/device/{device_id}/birth (QoS 1)
type DeviceBirth struct {
	Protocol     string    `json:"protocol"`
	DeviceID     string    `json:"device_id"`
	BridgeID     string    `json:"bridge_id"`
	Type         string    `json:"type"` // DeviceMeshtastic, DeviceIridiumSBD, etc.
	Label        string    `json:"label"`
	Hardware     string    `json:"hardware,omitempty"`
	Firmware     string    `json:"firmware,omitempty"`
	IMEI         string    `json:"imei,omitempty"`
	Position     *Location `json:"position,omitempty"`
	CoTType      string    `json:"cot_type"`
	CoTCallsign  string    `json:"cot_callsign"`
	Capabilities []string  `json:"capabilities"`
	Timestamp    time.Time `json:"timestamp"`
}

// DeviceDeath is published when a device goes offline.
// Topic: meshsat/bridge/{bridge_id}/device/{device_id}/death (QoS 1)
type DeviceDeath struct {
	Protocol  string    `json:"protocol"`
	DeviceID  string    `json:"device_id"`
	BridgeID  string    `json:"bridge_id"`
	Reason    string    `json:"reason"` // "offline", "removed", "bridge_shutdown"
	Timestamp time.Time `json:"timestamp"`
}

// --- Device telemetry messages (published to legacy topics) ---

// DevicePosition is published to meshsat/{device_id}/position.
type DevicePosition struct {
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	Alt       float64   `json:"alt,omitempty"`
	Speed     float64   `json:"speed,omitempty"`     // m/s
	Course    float64   `json:"course,omitempty"`    // degrees
	Source    string    `json:"source"`              // "gps", "iridium_cep", "cell_tower", "fixed"
	CEP       float64   `json:"cep,omitempty"`       // circular error probable (meters)
	BridgeID  string    `json:"bridge_id,omitempty"` // originating bridge
	Timestamp time.Time `json:"timestamp"`
}

// DeviceTelemetry is published to meshsat/{device_id}/telemetry.
type DeviceTelemetry struct {
	BatteryLevel float64   `json:"battery_level,omitempty"` // 0-100
	Voltage      float64   `json:"voltage,omitempty"`       // volts
	Temperature  float64   `json:"temperature,omitempty"`   // celsius
	Humidity     float64   `json:"humidity,omitempty"`      // percent
	Pressure     float64   `json:"pressure,omitempty"`      // hPa
	ChannelUtil  float64   `json:"channel_util,omitempty"`  // percent (meshtastic)
	AirUtilTx    float64   `json:"air_util_tx,omitempty"`   // percent (meshtastic)
	UptimeSec    int64     `json:"uptime_sec,omitempty"`
	BridgeID     string    `json:"bridge_id,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
}

// DeviceSOS is published to meshsat/{device_id}/sos.
type DeviceSOS struct {
	DeviceID  string    `json:"device_id"`
	BridgeID  string    `json:"bridge_id"`
	Type      string    `json:"type"` // "triggered", "cancelled"
	Message   string    `json:"message,omitempty"`
	Lat       float64   `json:"lat,omitempty"`
	Lon       float64   `json:"lon,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// --- Command channel ---

// Command is published by the Hub to meshsat/bridge/{bridge_id}/cmd.
type Command struct {
	Protocol     string          `json:"protocol"`
	Cmd          string          `json:"cmd"` // "send_mt", "send_text", "config_update", "flush_burst", "ping", "reboot"
	RequestID    string          `json:"request_id"`
	TargetDevice string          `json:"target_device,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Timestamp    time.Time       `json:"timestamp"`
}

// CommandResponse is published by the bridge to meshsat/bridge/{bridge_id}/cmd/response.
type CommandResponse struct {
	Protocol  string          `json:"protocol"`
	RequestID string          `json:"request_id"`
	Cmd       string          `json:"cmd"`
	Status    string          `json:"status"` // "ok", "error"
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}

// --- CoT type helpers ---

// CoTTypeForDevice returns the appropriate CoT type for a device type.
func CoTTypeForDevice(deviceType string) string {
	switch deviceType {
	case DeviceMeshtastic:
		return CoTMeshNode
	case DeviceIridiumSBD, DeviceIridiumIMT, DeviceAstrocast:
		return CoTSatModem
	case DeviceCellular:
		return CoTCellModem
	case DeviceZigBee:
		return CoTMeshNode
	case DeviceAPRS:
		return CoTMeshNode
	default:
		return CoTMeshNode
	}
}
