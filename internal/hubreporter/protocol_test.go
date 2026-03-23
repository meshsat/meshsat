package hubreporter

import (
	"encoding/json"
	"testing"
	"time"
)

func TestProtocolVersion(t *testing.T) {
	if ProtocolVersion != "meshsat-uplink/v1" {
		t.Fatalf("unexpected protocol version: %s", ProtocolVersion)
	}
}

func TestTopicBuilders(t *testing.T) {
	tests := []struct {
		name   string
		fn     func() string
		expect string
	}{
		{"bridge birth", func() string { return TopicBridgeBirth("mule01") }, "meshsat/bridge/mule01/birth"},
		{"bridge death", func() string { return TopicBridgeDeath("mule01") }, "meshsat/bridge/mule01/death"},
		{"bridge health", func() string { return TopicBridgeHealth("mule01") }, "meshsat/bridge/mule01/health"},
		{"bridge cmd", func() string { return TopicBridgeCmd("mule01") }, "meshsat/bridge/mule01/cmd"},
		{"bridge cmd resp", func() string { return TopicBridgeCmdResp("mule01") }, "meshsat/bridge/mule01/cmd/response"},
		{"device birth", func() string { return TopicDeviceBirth("mule01", "!aabb") }, "meshsat/bridge/mule01/device/!aabb/birth"},
		{"device death", func() string { return TopicDeviceDeath("mule01", "!aabb") }, "meshsat/bridge/mule01/device/!aabb/death"},
		{"device position", func() string { return TopicDevicePosition("!aabb") }, "meshsat/!aabb/position"},
		{"device telemetry", func() string { return TopicDeviceTelemetry("!aabb") }, "meshsat/!aabb/telemetry"},
		{"device sos", func() string { return TopicDeviceSOS("!aabb") }, "meshsat/!aabb/sos"},
		{"device message", func() string { return TopicDeviceMessage("!aabb") }, "meshsat/!aabb/mo/decoded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn()
			if got != tt.expect {
				t.Errorf("got %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestBridgeBirthRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	birth := BridgeBirth{
		Protocol:     ProtocolVersion,
		BridgeID:     "mule01",
		Version:      "0.18.0",
		Hostname:     "nllei01mule01",
		Mode:         "direct",
		TenantID:     "default",
		Location:     &Location{Lat: 52.16, Lon: 4.51, Alt: 0, Source: "fixed"},
		Interfaces:   []InterfaceInfo{{Name: "mesh_0", Type: "meshtastic", Status: "online", Port: "/dev/ttyACM0"}},
		Capabilities: []string{"meshtastic", "iridium_sbd", "reticulum"},
		Reticulum:    &ReticulumInfo{IdentityHash: "abcd1234", PublicKey: "base64key", TransportEnabled: true},
		CoTType:      CoTBridge,
		CoTCallsign:  "MESHSAT-MULE01",
		UptimeSec:    86400,
		Timestamp:    now,
	}

	data, err := json.Marshal(birth)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded BridgeBirth
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Protocol != ProtocolVersion {
		t.Errorf("protocol: got %q, want %q", decoded.Protocol, ProtocolVersion)
	}
	if decoded.BridgeID != "mule01" {
		t.Errorf("bridge_id: got %q", decoded.BridgeID)
	}
	if decoded.Location.Lat != 52.16 {
		t.Errorf("lat: got %f", decoded.Location.Lat)
	}
	if decoded.CoTType != CoTBridge {
		t.Errorf("cot_type: got %q", decoded.CoTType)
	}
	if !decoded.Timestamp.Equal(now) {
		t.Errorf("timestamp: got %v, want %v", decoded.Timestamp, now)
	}
	if len(decoded.Interfaces) != 1 || decoded.Interfaces[0].Name != "mesh_0" {
		t.Errorf("interfaces: got %+v", decoded.Interfaces)
	}
	if decoded.Reticulum == nil || !decoded.Reticulum.TransportEnabled {
		t.Errorf("reticulum: got %+v", decoded.Reticulum)
	}
}

func TestDeviceBirthRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	birth := DeviceBirth{
		Protocol:     ProtocolVersion,
		DeviceID:     "!aabbccdd",
		BridgeID:     "mule01",
		Type:         DeviceMeshtastic,
		Label:        "T-Deck Alpha",
		Hardware:     "LILYGO_TDECK",
		Firmware:     "2.5.6",
		Position:     &Location{Lat: 52.17, Lon: 4.52, Alt: 5, Source: "gps"},
		CoTType:      CoTMeshNode,
		CoTCallsign:  "TDECK-ALPHA",
		Capabilities: []string{"position", "telemetry", "text"},
		Timestamp:    now,
	}

	data, err := json.Marshal(birth)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded DeviceBirth
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.DeviceID != "!aabbccdd" {
		t.Errorf("device_id: got %q", decoded.DeviceID)
	}
	if decoded.Type != DeviceMeshtastic {
		t.Errorf("type: got %q", decoded.Type)
	}
	if decoded.CoTType != CoTMeshNode {
		t.Errorf("cot_type: got %q", decoded.CoTType)
	}
}

func TestCommandRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	cmd := Command{
		Protocol:     ProtocolVersion,
		Cmd:          "send_text",
		RequestID:    "550e8400-e29b-41d4-a716-446655440000",
		TargetDevice: "!aabbccdd",
		Payload:      json.RawMessage(`{"text":"check in please"}`),
		Timestamp:    now,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Command
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Cmd != "send_text" {
		t.Errorf("cmd: got %q", decoded.Cmd)
	}
	if decoded.RequestID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("request_id: got %q", decoded.RequestID)
	}

	resp := CommandResponse{
		Protocol:  ProtocolVersion,
		RequestID: cmd.RequestID,
		Cmd:       cmd.Cmd,
		Status:    "ok",
		Timestamp: now,
	}

	data, err = json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}

	var decodedResp CommandResponse
	if err := json.Unmarshal(data, &decodedResp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}

	if decodedResp.Status != "ok" {
		t.Errorf("status: got %q", decodedResp.Status)
	}
}

func TestCoTTypeForDevice(t *testing.T) {
	tests := []struct {
		deviceType string
		want       string
	}{
		{DeviceMeshtastic, CoTMeshNode},
		{DeviceIridiumSBD, CoTSatModem},
		{DeviceIridiumIMT, CoTSatModem},
		{DeviceAstrocast, CoTSatModem},
		{DeviceCellular, CoTCellModem},
		{DeviceZigBee, CoTMeshNode},
		{DeviceAPRS, CoTMeshNode},
		{"unknown", CoTMeshNode},
	}
	for _, tt := range tests {
		t.Run(tt.deviceType, func(t *testing.T) {
			got := CoTTypeForDevice(tt.deviceType)
			if got != tt.want {
				t.Errorf("CoTTypeForDevice(%q): got %q, want %q", tt.deviceType, got, tt.want)
			}
		})
	}
}

func TestBridgeHealthRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	health := BridgeHealth{
		Protocol:  ProtocolVersion,
		BridgeID:  "bananapi01",
		UptimeSec: 3600,
		CPUPct:    12.5,
		MemPct:    45.2,
		DiskPct:   23.1,
		Interfaces: []InterfaceHealth{
			{Name: "imt_0", Status: "online", SignalBars: 3, MOCount: 42},
			{Name: "mesh_0", Status: "offline"},
		},
		BurstQueue: &BurstQueueInfo{Pending: 2},
		Reticulum:  &ReticulumStats{Routes: 5, Links: 1, AnnouncesRelayed: 12},
		Timestamp:  now,
	}

	data, err := json.Marshal(health)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded BridgeHealth
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.CPUPct != 12.5 {
		t.Errorf("cpu_pct: got %f", decoded.CPUPct)
	}
	if len(decoded.Interfaces) != 2 {
		t.Fatalf("interfaces count: got %d", len(decoded.Interfaces))
	}
	if decoded.Interfaces[0].SignalBars != 3 {
		t.Errorf("signal_bars: got %d", decoded.Interfaces[0].SignalBars)
	}
	if decoded.Reticulum.Routes != 5 {
		t.Errorf("routes: got %d", decoded.Reticulum.Routes)
	}
}
