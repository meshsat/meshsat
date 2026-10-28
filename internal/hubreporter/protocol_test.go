package hubreporter

import (
	"encoding/json"
	"strings"
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

// The export rides the bridge's own subtree, which is the only place a bridge
// credential may publish TAK. [MESHSAT-1462]
func TestTopicTAKCoTOut(t *testing.T) {
	for id, want := range map[string]string{
		"mule01":        "meshsat/bridge/mule01/tak/cot/out",
		"android-phone": "meshsat/bridge/android-phone/tak/cot/out",
		// An id is written the way the Hub writes one into a topic.
		"kit+1/2": "meshsat/bridge/kit%2B1%2F2/tak/cot/out",
	} {
		if got := TopicTAKCoTOut(id); got != want {
			t.Errorf("TopicTAKCoTOut(%q) = %q, want %q", id, got, want)
		}
	}
}

// A chat's outcome rides the bridge's own subtree too, in both tenant shapes,
// and its JSON carries exactly what the Hub records. [MESHSAT-1746]
func TestTopicTAKChatStatus(t *testing.T) {
	for prefix, want := range map[string]string{
		"":              "meshsat/bridge/kit%2B1/tak/chat/status",
		"meshsat":       "meshsat/bridge/kit%2B1/tak/chat/status",
		"meshsat/t_x":   "meshsat/t_x/bridge/kit%2B1/tak/chat/status",
		"meshsat/acme1": "meshsat/acme1/bridge/kit%2B1/tak/chat/status",
	} {
		tp, err := NewTopics(prefix)
		if err != nil {
			t.Fatalf("NewTopics(%q): %v", prefix, err)
		}
		if got := tp.TAKChatStatus("kit+1"); got != want {
			t.Errorf("Topics(%q).TAKChatStatus = %q, want %q", prefix, got, want)
		}
	}
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	data, err := json.Marshal(TAKChatStatus{MessageID: "m1", CrewUID: "meshsat-a1b3c3a4", Status: "undelivered",
		Reason: "not_heard", Detail: "this kit has not heard MSPB in the last day", At: at})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"message_id":"m1","crew_uid":"meshsat-a1b3c3a4","status":"undelivered","reason":"not_heard",` +
		`"detail":"this kit has not heard MSPB in the last day","at":"2026-10-28T12:00:00Z"}`
	if string(data) != want {
		t.Errorf("wire JSON\n got %s\nwant %s", data, want)
	}
	// A queued status carries no reason and no detail keys when both are empty.
	data, _ = json.Marshal(TAKChatStatus{MessageID: "m1", CrewUID: "x", Status: "queued", At: at})
	if strings.Contains(string(data), `"reason"`) || strings.Contains(string(data), `"detail"`) {
		t.Errorf("empty reason or detail written: %s", data)
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

// What the Hub delivers back arrives on two topics; the second names the bridge
// that exported the event. [MESHSAT-1462]
func TestTAKCoTInTopics(t *testing.T) {
	want := []string{"meshsat/broadcast/tak/cot/in", "meshsat/broadcast/tak/cot/in/+"}
	got := TAKCoTInFilters()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("TAKCoTInFilters() = %v, want %v", got, want)
	}
	for topic, sender := range map[string]string{
		"meshsat/broadcast/tak/cot/in":             "", // from a TAK server
		"meshsat/broadcast/tak/cot/in/mule01":      "mule01",
		"meshsat/broadcast/tak/cot/in/kit%2B1":     "kit%2B1",
		"meshsat/broadcast/tak/cot/in/":            "",
		"meshsat/broadcast/tak/cot/in/mule01/more": "",
		"meshsat/bridge/mule01/tak/cot/out":        "",
		"meshsat/broadcast/tak/cot/inmule01":       "",
	} {
		if got := TAKCoTSender(topic); got != sender {
			t.Errorf("TAKCoTSender(%q) = %q, want %q", topic, got, sender)
		}
	}
}
