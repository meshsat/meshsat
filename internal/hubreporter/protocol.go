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
	"strings"
	"time"
)

// ProtocolVersion identifies this protocol revision. The Hub rejects unknown versions.
const ProtocolVersion = "meshsat-uplink/v1"

// MQTT topic patterns, below the tenant's root (Topics). Use the Topics
// methods, or the Topic* functions for the platform tenant.
const (
	topicBridgeBirth   = "%s/bridge/%s/birth"
	topicBridgeDeath   = "%s/bridge/%s/death"
	topicBridgeHealth  = "%s/bridge/%s/health"
	topicBridgeCmd     = "%s/bridge/%s/cmd"
	topicBridgeCmdResp = "%s/bridge/%s/cmd/response"
	topicDeviceBirth   = "%s/bridge/%s/device/%s/birth"
	topicDeviceDeath   = "%s/bridge/%s/device/%s/death"

	// Legacy device topics — existing Hub TAK subscriber listens on these.
	topicDevicePosition  = "%s/%s/position"
	topicDeviceTelemetry = "%s/%s/telemetry"
	topicDeviceSOS       = "%s/%s/sos"
	topicDeviceMessage   = "%s/%s/mo/decoded"

	// Spectrum alerts are per-bridge (the scan is a property of the kit's
	// RTL-SDR, not a paired device), so the topic is scoped to the bridge
	// ID and the band identifier is inside the payload.
	topicBridgeSpectrum = "%s/bridge/%s/spectrum"

	// CoT XML the TAK gateway exports to the Hub ("MQTT Export to Hub"), on
	// the bridge's OWN subtree. It was meshsat/<id>/tak/cot/out until
	// MESHSAT-1462, a topic no bridge credential was ever allowed to publish:
	// the broker grants each bridge meshsat/bridge/<its id>/# and nothing
	// device-shaped for TAK, so the export went nowhere. The Hub listens here
	// (meshsat-hub MESHSAT-1458) and forwards to the tenant's TAK servers.
	topicTAKCoTOut = "%s/bridge/%s/tak/cot/out"

	// What the Hub delivers back (meshsat-hub MESHSAT-1461): an event from one
	// of the tenant's TAK servers on this topic, and an event another bridge
	// of the tenant exported one level below it, with that bridge's id as the
	// last segment.
	topicTAKCoTIn = "%s/broadcast/tak/cot/in"
)

// DefaultTopicRoot is the platform tenant's topic root.
const DefaultTopicRoot = "meshsat"

// reservedTenantIDs are second topic segments that are not tenants on the Hub
// (meshsat-hub store.ReservedTenantIDs and hubmqtt reservedSecond).
var reservedTenantIDs = map[string]bool{
	"bridge": true, "hub": true, "broadcast": true, "reticulum": true, "federation": true, "routed": true, "relay": true,
}

// Topics builds every Hub topic under one tenant's root (MESHSAT-1467, HeMB-NG
// contract C13): "meshsat" for the platform tenant, "meshsat/{tenant}" for a
// customer tenant, as the Hub's provisioning bundle gives it in
// mqtt_topic_prefix. The broker confines a customer bridge's credential to its
// tenant's root, so a bridge on the wrong root can publish nothing at all.
type Topics struct{ root string }

// NewTopics checks a topic root from the Hub's bundle. Empty is the platform
// tenant. Anything but "meshsat" or "meshsat/{tenant}" with a plain tenant id
// is refused: a wildcard, a slash or a dot there would move the bridge onto
// other topics (a dot splits a NATS subject).
func NewTopics(prefix string) (Topics, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == DefaultTopicRoot {
		return Topics{root: DefaultTopicRoot}, nil
	}
	tenant, ok := strings.CutPrefix(prefix, DefaultTopicRoot+"/")
	if !ok || !validTenantSegment(tenant) {
		return Topics{}, fmt.Errorf("hub topic prefix %q: want %q or %q", prefix, DefaultTopicRoot, DefaultTopicRoot+"/<tenant>")
	}
	if reservedTenantIDs[tenant] {
		return Topics{}, fmt.Errorf("hub topic prefix %q: %q is not a tenant", prefix, tenant)
	}
	return Topics{root: prefix}, nil
}

func validTenantSegment(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Root is the tenant's topic root.
func (t Topics) Root() string {
	if t.root == "" {
		return DefaultTopicRoot
	}
	return t.root
}

func (t Topics) BridgeBirth(bridgeID string) string {
	return fmt.Sprintf(topicBridgeBirth, t.Root(), bridgeID)
}
func (t Topics) BridgeDeath(bridgeID string) string {
	return fmt.Sprintf(topicBridgeDeath, t.Root(), bridgeID)
}
func (t Topics) BridgeHealth(bridgeID string) string {
	return fmt.Sprintf(topicBridgeHealth, t.Root(), bridgeID)
}
func (t Topics) BridgeCmd(bridgeID string) string {
	return fmt.Sprintf(topicBridgeCmd, t.Root(), bridgeID)
}
func (t Topics) BridgeCmdResp(bridgeID string) string {
	return fmt.Sprintf(topicBridgeCmdResp, t.Root(), bridgeID)
}
func (t Topics) DeviceBirth(bridgeID, deviceID string) string {
	return fmt.Sprintf(topicDeviceBirth, t.Root(), bridgeID, deviceID)
}
func (t Topics) DeviceDeath(bridgeID, deviceID string) string {
	return fmt.Sprintf(topicDeviceDeath, t.Root(), bridgeID, deviceID)
}
func (t Topics) DevicePosition(deviceID string) string {
	return fmt.Sprintf(topicDevicePosition, t.Root(), deviceID)
}
func (t Topics) DeviceTelemetry(deviceID string) string {
	return fmt.Sprintf(topicDeviceTelemetry, t.Root(), deviceID)
}
func (t Topics) DeviceSOS(deviceID string) string {
	return fmt.Sprintf(topicDeviceSOS, t.Root(), deviceID)
}
func (t Topics) DeviceMessage(deviceID string) string {
	return fmt.Sprintf(topicDeviceMessage, t.Root(), deviceID)
}
func (t Topics) BridgeSpectrum(bridgeID string) string {
	return fmt.Sprintf(topicBridgeSpectrum, t.Root(), bridgeID)
}

// TAKCoTOut is where the TAK gateway's CoT XML goes:
// {root}/bridge/<bridge id>/tak/cot/out.
func (t Topics) TAKCoTOut(bridgeID string) string {
	return fmt.Sprintf(topicTAKCoTOut, t.Root(), topicSegment(bridgeID))
}

// Msg is one of a bridge's hub_msg topics, {root}/bridge/<bridge id>/msg/<kind>: "out" and
// "ack" the bridge publishes, "in" and "status" only the Hub may (MESHSAT-1584).
func (t Topics) Msg(bridgeID, kind string) string {
	return fmt.Sprintf("%s/bridge/%s/msg/%s", t.Root(), topicSegment(bridgeID), kind)
}

// SOS is one of a bridge's C10 SOS topics, {root}/bridge/<bridge id>/sos/<kind>: "event"
// the bridge publishes, "ack" only the Hub may (MESHSAT-1584).
func (t Topics) SOS(bridgeID, kind string) string {
	return fmt.Sprintf("%s/bridge/%s/sos/%s", t.Root(), topicSegment(bridgeID), kind)
}

// HubCaps is where the Hub keeps, retained, what it understands for this bridge:
// {root}/bridge/<bridge id>/hub/caps (only the Hub may publish it).
func (t Topics) HubCaps(bridgeID string) string {
	return fmt.Sprintf("%s/bridge/%s/hub/caps", t.Root(), topicSegment(bridgeID))
}

func (t Topics) takCoTIn() string { return fmt.Sprintf(topicTAKCoTIn, t.Root()) }

// TAKCoTInFilters are the two subscriptions that receive what the Hub
// delivers: the topic itself and one level below it.
func (t Topics) TAKCoTInFilters() []string {
	return []string{t.takCoTIn(), t.takCoTIn() + "/+"}
}

// TAKCoTSender returns the bridge that exported an event the Hub delivered, as
// the topic names it, or "" for an event that came from a TAK server (or for
// any other topic). The value is the topic segment, percent-encoded as the Hub
// writes it; compare it with topicSegment(own id).
func (t Topics) TAKCoTSender(topic string) string {
	rest, ok := strings.CutPrefix(topic, t.takCoTIn()+"/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return ""
	}
	return rest
}

// platformTopics are the platform tenant's, for the Topic* functions.
var platformTopics = Topics{root: DefaultTopicRoot}

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
	DeviceZigBee     = "zigbee"
	DeviceAPRS       = "aprs"
)

// --- Topic builders (the platform tenant's; a reporter uses its own Topics) ---

func TopicBridgeBirth(bridgeID string) string   { return platformTopics.BridgeBirth(bridgeID) }
func TopicBridgeDeath(bridgeID string) string   { return platformTopics.BridgeDeath(bridgeID) }
func TopicBridgeHealth(bridgeID string) string  { return platformTopics.BridgeHealth(bridgeID) }
func TopicBridgeCmd(bridgeID string) string     { return platformTopics.BridgeCmd(bridgeID) }
func TopicBridgeCmdResp(bridgeID string) string { return platformTopics.BridgeCmdResp(bridgeID) }

func TopicDeviceBirth(bridgeID, deviceID string) string {
	return platformTopics.DeviceBirth(bridgeID, deviceID)
}

func TopicDeviceDeath(bridgeID, deviceID string) string {
	return platformTopics.DeviceDeath(bridgeID, deviceID)
}

func TopicDevicePosition(deviceID string) string  { return platformTopics.DevicePosition(deviceID) }
func TopicDeviceTelemetry(deviceID string) string { return platformTopics.DeviceTelemetry(deviceID) }
func TopicDeviceSOS(deviceID string) string       { return platformTopics.DeviceSOS(deviceID) }
func TopicDeviceMessage(deviceID string) string   { return platformTopics.DeviceMessage(deviceID) }
func TopicBridgeSpectrum(bridgeID string) string  { return platformTopics.BridgeSpectrum(bridgeID) }

// TopicTAKCoTOut is where the TAK gateway's CoT XML goes:
// meshsat/bridge/<bridge id>/tak/cot/out.
func TopicTAKCoTOut(bridgeID string) string { return platformTopics.TAKCoTOut(bridgeID) }

// TAKCoTInFilters are the platform tenant's two TAK delivery subscriptions.
func TAKCoTInFilters() []string { return platformTopics.TAKCoTInFilters() }

// TAKCoTSender is Topics.TAKCoTSender for the platform tenant.
func TAKCoTSender(topic string) string { return platformTopics.TAKCoTSender(topic) }

// topicSegment writes an id as the Hub writes one into a topic: + # / and %
// percent-encoded (meshsat-hub hubmqtt.EncodeSegment). Ordinary bridge ids are
// unchanged by it.
func topicSegment(id string) string {
	return strings.NewReplacer("%", "%25", "+", "%2B", "#", "%23", "/", "%2F").Replace(id)
}

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
	Type   string `json:"type"`           // "meshtastic", "iridium_sbd", "iridium_imt", "cellular", "zigbee", "aprs", "tcp"
	Status string `json:"status"`         // "online", "offline", "error", "binding"
	Port   string `json:"port,omitempty"` // serial port path
	IMEI   string `json:"imei,omitempty"` // satellite/cellular modem IMEI
	IMSI   string `json:"imsi,omitempty"` // cellular SIM IMSI
}

// InterfaceHealth reports live metrics for a transport interface.
type InterfaceHealth struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	HealthScore   int    `json:"health_score,omitempty"`   // 0-100
	SignalBars    int    `json:"signal_bars,omitempty"`    // 0-5 (satellite)
	SignalDBm     int    `json:"signal_dbm,omitempty"`     // dBm (cellular)
	Operator      string `json:"operator,omitempty"`       // cellular operator name
	NodesSeen     int    `json:"nodes_seen,omitempty"`     // meshtastic peer count
	MOCount       int64  `json:"mo_count,omitempty"`       // satellite MO messages sent
	MTCount       int64  `json:"mt_count,omitempty"`       // satellite MT messages received
	SpectrumState string `json:"spectrum_state,omitempty"` // clear/jamming/interference/disabled (RTL-SDR)
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
	// DirectoryVersion is the Hub directory version the bridge applied and
	// DirectoryKeyID the id of the Hub key it trusts (C7.5, C7.6): the Hub
	// pushes a newer snapshot, and the rotation records before it.
	DirectoryVersion int64  `json:"directory_version,omitempty"`
	DirectoryKeyID   string `json:"directory_key_id,omitempty"`
}

// CapDirectoryV2 in the birth's capabilities: this bridge verifies v2
// directory envelopes and takes signed rotations (C7.2, C7.6).
const CapDirectoryV2 = "directory_v2"

// BridgeDeath is published when the bridge disconnects (explicit or LWT).
// Topic: meshsat/bridge/{bridge_id}/death (QoS 1)
type BridgeDeath struct {
	Protocol  string    `json:"protocol"`
	BridgeID  string    `json:"bridge_id"`
	Reason    string    `json:"reason"` // "shutdown", "lwt", "error"
	Timestamp time.Time `json:"timestamp"`
}

// SpectrumBandHealth reports per-band RTL-SDR spectrum monitoring state.
type SpectrumBandHealth struct {
	Band        string  `json:"band"`         // e.g. "lora_868", "aprs_144"
	InterfaceID string  `json:"interface_id"` // e.g. "mesh_0", "ax25_0"
	State       string  `json:"state"`        // clear/jamming/interference/calibrating/disabled
	PowerDB     float64 `json:"power_db"`
	FreqLow     int     `json:"freq_low"`
	FreqHigh    int     `json:"freq_high"`
}

// BridgeHealth is published periodically (default 30s).
// Topic: meshsat/bridge/{bridge_id}/health (QoS 0)
type BridgeHealth struct {
	Protocol   string               `json:"protocol"`
	BridgeID   string               `json:"bridge_id"`
	UptimeSec  int64                `json:"uptime_sec"`
	CPUPct     float64              `json:"cpu_pct"`
	MemPct     float64              `json:"mem_pct"`
	DiskPct    float64              `json:"disk_pct"`
	Interfaces []InterfaceHealth    `json:"interfaces"`
	Spectrum   []SpectrumBandHealth `json:"spectrum,omitempty"`
	BurstQueue *BurstQueueInfo      `json:"burst_queue,omitempty"`
	Reticulum  *ReticulumStats      `json:"reticulum,omitempty"`
	Outbox     *OutboxInfo          `json:"outbox,omitempty"`
	Timestamp  time.Time            `json:"timestamp"`
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

// DeviceMessage is published to meshsat/{device_id}/mo/decoded — the topic
// the Rock7 webhook already uses for satellite MO, now also carrying mesh
// text so the Hub sees a reply to something it relayed in. The topic names
// the mesh node and the BODY names the kit: DeviceBirth is published
// unretained, so the Hub cannot rely on its registry to resolve node ->
// bridge for a message that arrives after a reconnect. [MESHSAT-1178]
type DeviceMessage struct {
	Protocol  string    `json:"protocol"`
	DeviceID  string    `json:"device_id"`
	BridgeID  string    `json:"bridge_id"`
	Text      string    `json:"text"`
	Channel   int       `json:"channel,omitempty"`
	SNR       float64   `json:"snr,omitempty"`
	PacketID  uint32    `json:"packet_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
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

// SpectrumAlert is published to meshsat/bridge/{bridge_id}/spectrum when
// the RTL-SDR jamming monitor transitions a band into or out of a non-clear
// state. The hub uses these to correlate jamming events across bridges —
// a coordinated jamming campaign will light up the same band on multiple
// kits simultaneously.
type SpectrumAlert struct {
	BridgeID       string    `json:"bridge_id"`
	Band           string    `json:"band"`         // e.g. "lte_b20_dl"
	Label          string    `json:"label"`        // human-readable
	InterfaceID    string    `json:"interface_id"` // "mesh_0", "cellular_0", ...
	FreqLow        int       `json:"freq_low_hz"`
	FreqHigh       int       `json:"freq_high_hz"`
	State          string    `json:"state"` // "clear", "jamming", "interference"
	OldState       string    `json:"old_state"`
	PowerDB        float64   `json:"power_db"`     // average power in this scan
	MaxPowerDB     float64   `json:"max_power_db"` // peak bin
	BaselineMeanDB float64   `json:"baseline_mean_db"`
	BaselineStdDB  float64   `json:"baseline_std_db"`
	Lat            float64   `json:"lat,omitempty"`
	Lon            float64   `json:"lon,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
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

// MgmtRequest is the payload of the mgmt_* commands and of reboot: an OOB
// management command by name, executed locally through the same executor
// and audit path as a frame received over a bearer. [MESHSAT-756]
type MgmtRequest struct {
	Cmd     string `json:"cmd,omitempty"`     // PING, REBOOT, RESTART, RESET, BEARER, LOG, STATUS-NET
	Target  string `json:"target,omitempty"`  // RESET, BEARER target name
	Level   int    `json:"level,omitempty"`   // RESET level 1..3
	State   string `json:"state,omitempty"`   // BEARER on|off
	Unit    string `json:"unit,omitempty"`    // LOG unit name
	Lines   int    `json:"lines,omitempty"`   // LOG line count
	Delay   int    `json:"delay,omitempty"`   // REBOOT seconds
	Confirm bool   `json:"confirm,omitempty"` // required for reboot
}

// MgmtResult is the response payload of the mgmt_* commands.
type MgmtResult struct {
	Code   int    `json:"code"`
	Result string `json:"result"`
	Body   string `json:"body,omitempty"`
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
	case DeviceIridiumSBD, DeviceIridiumIMT:
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
