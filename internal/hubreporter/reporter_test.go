package hubreporter

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReporterConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ReporterConfig
		wantErr bool
	}{
		{
			name:    "empty URL means disabled",
			cfg:     ReporterConfig{HubURL: "", BridgeID: "test"},
			wantErr: true,
		},
		{
			name:    "empty bridge ID",
			cfg:     ReporterConfig{HubURL: "tcp://hub:1883", BridgeID: ""},
			wantErr: true,
		},
		{
			name:    "valid minimal config",
			cfg:     ReporterConfig{HubURL: "tcp://hub:1883", BridgeID: "mule01", HealthInterval: 30 * time.Second},
			wantErr: false,
		},
		{
			name:    "valid with auth",
			cfg:     ReporterConfig{HubURL: "ssl://hub:8883", BridgeID: "mule01", Username: "bridge", Password: "secret", HealthInterval: 30 * time.Second},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewHubReporter(t *testing.T) {
	cfg := ReporterConfig{
		HubURL:         "tcp://hub:1883",
		BridgeID:       "mule01",
		HealthInterval: 30 * time.Second,
	}
	birthFn := func() BridgeBirth {
		return BridgeBirth{Version: "0.18.0"}
	}
	healthFn := func() BridgeHealth {
		return BridgeHealth{UptimeSec: 100}
	}

	r := NewHubReporter(cfg, birthFn, healthFn)
	if r == nil {
		t.Fatal("NewHubReporter returned nil")
	}
	if r.cfg.BridgeID != "mule01" {
		t.Errorf("bridge_id: got %q", r.cfg.BridgeID)
	}
	if r.IsConnected() {
		t.Error("should not be connected before Start")
	}
	if r.stopCh == nil {
		t.Error("stopCh should be initialized")
	}
}

func TestBirthCallbackBuilding(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	birthFn := func() BridgeBirth {
		return BridgeBirth{
			Version:  "0.18.0",
			Hostname: "nllei01mule01",
			Mode:     "direct",
			TenantID: "default",
			Location: &Location{Lat: 52.16, Lon: 4.51, Alt: 0, Source: "fixed"},
			Interfaces: []InterfaceInfo{
				{Name: "mesh_0", Type: "meshtastic", Status: "online", Port: "/dev/ttyACM0"},
				{Name: "imt_0", Type: "iridium_imt", Status: "online", IMEI: "300258060902280"},
			},
			Capabilities: []string{"meshtastic", "iridium_imt", "reticulum"},
			Reticulum:    &ReticulumInfo{IdentityHash: "abcd1234", PublicKey: "base64key", TransportEnabled: true},
			CoTType:      CoTBridge,
			CoTCallsign:  "MESHSAT-MULE01",
			UptimeSec:    3600,
			Timestamp:    now,
		}
	}

	birth := birthFn()
	// Simulate what the reporter does before publishing
	birth.Protocol = ProtocolVersion
	birth.BridgeID = "mule01"

	if birth.Protocol != ProtocolVersion {
		t.Errorf("protocol: got %q", birth.Protocol)
	}
	if birth.BridgeID != "mule01" {
		t.Errorf("bridge_id: got %q", birth.BridgeID)
	}
	if birth.Version != "0.18.0" {
		t.Errorf("version: got %q", birth.Version)
	}
	if birth.Hostname != "nllei01mule01" {
		t.Errorf("hostname: got %q", birth.Hostname)
	}
	if birth.Mode != "direct" {
		t.Errorf("mode: got %q", birth.Mode)
	}
	if len(birth.Interfaces) != 2 {
		t.Fatalf("interfaces: got %d, want 2", len(birth.Interfaces))
	}
	if birth.Interfaces[1].IMEI != "300258060902280" {
		t.Errorf("imt imei: got %q", birth.Interfaces[1].IMEI)
	}
	if len(birth.Capabilities) != 3 {
		t.Errorf("capabilities: got %d, want 3", len(birth.Capabilities))
	}
	if birth.Reticulum == nil || !birth.Reticulum.TransportEnabled {
		t.Errorf("reticulum: got %+v", birth.Reticulum)
	}
	if birth.CoTType != CoTBridge {
		t.Errorf("cot_type: got %q", birth.CoTType)
	}
}

func TestHealthCallbackBuilding(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	healthFn := func() BridgeHealth {
		return BridgeHealth{
			UptimeSec: 7200,
			CPUPct:    15.3,
			MemPct:    42.1,
			DiskPct:   28.7,
			Interfaces: []InterfaceHealth{
				{Name: "mesh_0", Status: "online", HealthScore: 95, NodesSeen: 8},
				{Name: "imt_0", Status: "online", SignalBars: 4, MOCount: 15, MTCount: 3},
			},
			BurstQueue: &BurstQueueInfo{Pending: 1},
			Reticulum:  &ReticulumStats{Routes: 3, Links: 1, AnnouncesRelayed: 7},
			Timestamp:  now,
		}
	}

	health := healthFn()
	// Simulate what the reporter does
	health.Protocol = ProtocolVersion
	health.BridgeID = "mule01"

	if health.Protocol != ProtocolVersion {
		t.Errorf("protocol: got %q", health.Protocol)
	}
	if health.UptimeSec != 7200 {
		t.Errorf("uptime: got %d", health.UptimeSec)
	}
	if health.CPUPct != 15.3 {
		t.Errorf("cpu: got %f", health.CPUPct)
	}
	if len(health.Interfaces) != 2 {
		t.Fatalf("interfaces: got %d", len(health.Interfaces))
	}
	if health.Interfaces[0].NodesSeen != 8 {
		t.Errorf("nodes_seen: got %d", health.Interfaces[0].NodesSeen)
	}
	if health.Interfaces[1].SignalBars != 4 {
		t.Errorf("signal_bars: got %d", health.Interfaces[1].SignalBars)
	}
	if health.BurstQueue.Pending != 1 {
		t.Errorf("burst pending: got %d", health.BurstQueue.Pending)
	}
	if health.Reticulum.Routes != 3 {
		t.Errorf("routes: got %d", health.Reticulum.Routes)
	}
}

func TestBirthSerializationRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	birth := BridgeBirth{
		Protocol:     ProtocolVersion,
		BridgeID:     "bananapi01",
		Version:      "0.18.0",
		Hostname:     "bananapi01",
		Mode:         "direct",
		TenantID:     "default",
		Location:     &Location{Lat: 52.37, Lon: 4.89, Alt: 10, Source: "gps"},
		Interfaces:   []InterfaceInfo{{Name: "mesh_0", Type: "meshtastic", Status: "online"}},
		Capabilities: []string{"meshtastic"},
		CoTType:      CoTBridge,
		CoTCallsign:  "MESHSAT-BANANA",
		UptimeSec:    600,
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
		t.Errorf("protocol: got %q", decoded.Protocol)
	}
	if decoded.BridgeID != "bananapi01" {
		t.Errorf("bridge_id: got %q", decoded.BridgeID)
	}
	if decoded.Version != "0.18.0" {
		t.Errorf("version: got %q", decoded.Version)
	}
	if decoded.Location == nil || decoded.Location.Source != "gps" {
		t.Errorf("location: got %+v", decoded.Location)
	}
	if !decoded.Timestamp.Equal(now) {
		t.Errorf("timestamp: got %v, want %v", decoded.Timestamp, now)
	}
}

func TestHealthSerializationRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	nextWindow := now.Add(15 * time.Minute)
	health := BridgeHealth{
		Protocol:  ProtocolVersion,
		BridgeID:  "mule01",
		UptimeSec: 86400,
		CPUPct:    5.2,
		MemPct:    33.8,
		DiskPct:   12.4,
		Interfaces: []InterfaceHealth{
			{Name: "mesh_0", Status: "online", HealthScore: 100, NodesSeen: 12},
			{Name: "imt_0", Status: "online", SignalBars: 5, MOCount: 200, MTCount: 50},
			{Name: "cell_0", Status: "offline", SignalDBm: -85, Operator: "KPN"},
		},
		BurstQueue: &BurstQueueInfo{Pending: 3, NextWindow: &nextWindow},
		Reticulum:  &ReticulumStats{Routes: 10, Links: 2, AnnouncesRelayed: 100},
		Outbox:     &OutboxInfo{Pending: 5, Replayed: 42},
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

	if decoded.UptimeSec != 86400 {
		t.Errorf("uptime: got %d", decoded.UptimeSec)
	}
	if len(decoded.Interfaces) != 3 {
		t.Fatalf("interfaces: got %d", len(decoded.Interfaces))
	}
	if decoded.Interfaces[2].Operator != "KPN" {
		t.Errorf("operator: got %q", decoded.Interfaces[2].Operator)
	}
	if decoded.BurstQueue == nil || decoded.BurstQueue.Pending != 3 {
		t.Errorf("burst_queue: got %+v", decoded.BurstQueue)
	}
	if decoded.BurstQueue.NextWindow == nil || !decoded.BurstQueue.NextWindow.Equal(nextWindow) {
		t.Errorf("next_window: got %v, want %v", decoded.BurstQueue.NextWindow, nextWindow)
	}
	if decoded.Outbox == nil || decoded.Outbox.Replayed != 42 {
		t.Errorf("outbox: got %+v", decoded.Outbox)
	}
	if !decoded.Timestamp.Equal(now) {
		t.Errorf("timestamp: got %v, want %v", decoded.Timestamp, now)
	}
}

func TestDeathSerializationRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, reason := range []string{"shutdown", "lwt", "error"} {
		t.Run(reason, func(t *testing.T) {
			death := BridgeDeath{
				Protocol:  ProtocolVersion,
				BridgeID:  "mule01",
				Reason:    reason,
				Timestamp: now,
			}

			data, err := json.Marshal(death)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var decoded BridgeDeath
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if decoded.Reason != reason {
				t.Errorf("reason: got %q, want %q", decoded.Reason, reason)
			}
			if decoded.BridgeID != "mule01" {
				t.Errorf("bridge_id: got %q", decoded.BridgeID)
			}
		})
	}
}

func TestDevicePublishPayloads(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	t.Run("device position", func(t *testing.T) {
		pos := DevicePosition{
			Lat:       52.37,
			Lon:       4.89,
			Alt:       15.0,
			Speed:     1.2,
			Source:    "gps",
			BridgeID:  "mule01",
			Timestamp: now,
		}
		data, err := json.Marshal(pos)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded DevicePosition
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.Lat != 52.37 {
			t.Errorf("lat: got %f", decoded.Lat)
		}
		if decoded.Source != "gps" {
			t.Errorf("source: got %q", decoded.Source)
		}
	})

	t.Run("device telemetry", func(t *testing.T) {
		tel := DeviceTelemetry{
			BatteryLevel: 85.5,
			Voltage:      3.7,
			Temperature:  22.3,
			ChannelUtil:  15.2,
			BridgeID:     "mule01",
			Timestamp:    now,
		}
		data, err := json.Marshal(tel)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded DeviceTelemetry
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.BatteryLevel != 85.5 {
			t.Errorf("battery: got %f", decoded.BatteryLevel)
		}
	})

	t.Run("device SOS", func(t *testing.T) {
		sos := DeviceSOS{
			DeviceID:  "!aabbccdd",
			BridgeID:  "mule01",
			Type:      "triggered",
			Message:   "Help needed",
			Lat:       52.37,
			Lon:       4.89,
			Timestamp: now,
		}
		data, err := json.Marshal(sos)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded DeviceSOS
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.Type != "triggered" {
			t.Errorf("type: got %q", decoded.Type)
		}
		if decoded.Message != "Help needed" {
			t.Errorf("message: got %q", decoded.Message)
		}
	})
}

func TestIsConnectedDefault(t *testing.T) {
	r := NewHubReporter(
		ReporterConfig{HubURL: "tcp://hub:1883", BridgeID: "test"},
		func() BridgeBirth { return BridgeBirth{} },
		func() BridgeHealth { return BridgeHealth{} },
	)
	if r.IsConnected() {
		t.Error("new reporter should not be connected")
	}
}
