package hubreporter

import (
	"encoding/json"
	"testing"

	"meshsat/internal/transport"
)

// --- DeviceInventory tests ---

func TestInventoryRegisterNew(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")

	birth := DeviceBirth{
		DeviceID: "!aabbccdd",
		Type:     DeviceMeshtastic,
		Label:    "Node A",
	}
	published := inv.RegisterDevice(birth)
	if !published {
		t.Fatal("expected birth to be published for new device")
	}
	if inv.Count() != 1 {
		t.Fatalf("expected count 1, got %d", inv.Count())
	}
	if !inv.IsRegistered("!aabbccdd") {
		t.Fatal("device should be registered")
	}
}

func TestInventoryDeduplication(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")

	birth := DeviceBirth{
		DeviceID: "!aabbccdd",
		Type:     DeviceMeshtastic,
		Label:    "Node A",
		Hardware: "TBEAM",
	}

	// First registration should publish
	if !inv.RegisterDevice(birth) {
		t.Fatal("first registration should publish")
	}

	// Same data — should NOT publish
	if inv.RegisterDevice(birth) {
		t.Fatal("identical registration should be deduplicated")
	}

	// Changed label — should publish
	birth.Label = "Node A Updated"
	if !inv.RegisterDevice(birth) {
		t.Fatal("changed label should trigger re-publish")
	}

	// Still only 1 device
	if inv.Count() != 1 {
		t.Fatalf("expected count 1, got %d", inv.Count())
	}
}

func TestInventoryUnregister(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")

	inv.RegisterDevice(DeviceBirth{
		DeviceID: "!11111111",
		Type:     DeviceMeshtastic,
		Label:    "Node 1",
	})
	inv.RegisterDevice(DeviceBirth{
		DeviceID: "!22222222",
		Type:     DeviceMeshtastic,
		Label:    "Node 2",
	})

	if inv.Count() != 2 {
		t.Fatalf("expected 2 devices, got %d", inv.Count())
	}

	inv.UnregisterDevice("!11111111", "offline")

	if inv.Count() != 1 {
		t.Fatalf("expected 1 device after unregister, got %d", inv.Count())
	}
	if inv.IsRegistered("!11111111") {
		t.Fatal("device should no longer be registered")
	}
	if !inv.IsRegistered("!22222222") {
		t.Fatal("other device should still be registered")
	}
}

func TestInventoryUnregisterNonexistent(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")

	// Should not panic
	inv.UnregisterDevice("!99999999", "offline")
	if inv.Count() != 0 {
		t.Fatalf("expected 0 devices, got %d", inv.Count())
	}
}

func TestInventoryUnregisterAll(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")

	for i := 0; i < 5; i++ {
		inv.RegisterDevice(DeviceBirth{
			DeviceID: string(rune('A' + i)),
			Type:     DeviceMeshtastic,
		})
	}

	if inv.Count() != 5 {
		t.Fatalf("expected 5 devices, got %d", inv.Count())
	}

	inv.UnregisterAll("bridge_shutdown")

	if inv.Count() != 0 {
		t.Fatalf("expected 0 devices after UnregisterAll, got %d", inv.Count())
	}
}

func TestInventoryCoTTypeDefault(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")

	inv.RegisterDevice(DeviceBirth{
		DeviceID: "!aabbccdd",
		Type:     DeviceMeshtastic,
	})

	inv.mu.Lock()
	stored := inv.devices["!aabbccdd"]
	inv.mu.Unlock()

	if stored.CoTType != CoTMeshNode {
		t.Fatalf("expected CoT type %q, got %q", CoTMeshNode, stored.CoTType)
	}
	if stored.CoTCallsign != "!aabbccdd" {
		t.Fatalf("expected CoT callsign %q, got %q", "!aabbccdd", stored.CoTCallsign)
	}
}

// --- EventTap tests ---

func TestEventTapNodeUpdate(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")
	tap := NewEventTap(nil, inv, "test-bridge")

	node := transport.MeshNode{
		Num:          0xaabbccdd,
		UserID:       "!aabbccdd",
		LongName:     "Test Node",
		HWModelName:  "TBEAM",
		Latitude:     37.7749,
		Longitude:    -122.4194,
		Altitude:     10,
		BatteryLevel: 85,
		Voltage:      3.7,
	}
	data, _ := json.Marshal(node)

	event := transport.MeshEvent{
		Type: "node_update",
		Data: data,
	}

	tap.HandleMeshEvent(event)

	if !inv.IsRegistered("!aabbccdd") {
		t.Fatal("device should be registered after node_update")
	}
	if inv.Count() != 1 {
		t.Fatalf("expected 1 device, got %d", inv.Count())
	}

	inv.mu.Lock()
	stored := inv.devices["!aabbccdd"]
	inv.mu.Unlock()

	if stored.Label != "Test Node" {
		t.Fatalf("expected label 'Test Node', got %q", stored.Label)
	}
	if stored.Hardware != "TBEAM" {
		t.Fatalf("expected hardware 'TBEAM', got %q", stored.Hardware)
	}
}

func TestEventTapPosition(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")
	tap := NewEventTap(nil, inv, "test-bridge")

	pos := struct {
		NodeID    string  `json:"node_id"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Altitude  int     `json:"altitude"`
	}{
		NodeID:    "!11223344",
		Latitude:  51.5074,
		Longitude: -0.1278,
		Altitude:  20,
	}
	data, _ := json.Marshal(pos)

	event := transport.MeshEvent{
		Type: "position",
		Data: data,
	}

	// Position events don't register devices — they just forward position
	tap.HandleMeshEvent(event)

	// Device is not registered by position alone (requires node_update or nodeinfo)
	if inv.IsRegistered("!11223344") {
		t.Fatal("position event alone should not register device")
	}
}

func TestEventTapTelemetryNodeUpdate(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")
	tap := NewEventTap(nil, inv, "test-bridge")

	temp := float32(22.5)
	humidity := float32(65.0)
	node := transport.MeshNode{
		Num:          0xdeadbeef,
		UserID:       "!deadbeef",
		LongName:     "Sensor Node",
		BatteryLevel: 42,
		Voltage:      3.3,
		Temperature:  &temp,
		Humidity:     &humidity,
		ChannelUtil:  12.5,
		AirUtilTx:    3.2,
	}
	data, _ := json.Marshal(node)

	tap.HandleMeshEvent(transport.MeshEvent{
		Type: "node_update",
		Data: data,
	})

	if !inv.IsRegistered("!deadbeef") {
		t.Fatal("device should be registered from node_update with telemetry")
	}
}

func TestEventTapSkipsTextMessages(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")
	tap := NewEventTap(nil, inv, "test-bridge")

	msg := transport.MeshMessage{
		From:        0xaabbccdd,
		PortNum:     1, // TEXT_MESSAGE_APP
		DecodedText: "Hello world",
	}
	data, _ := json.Marshal(msg)

	tap.HandleMeshEvent(transport.MeshEvent{
		Type: "message",
		Data: data,
	})

	// TEXT_MESSAGE_APP should NOT register devices or do anything
	if inv.IsRegistered("!aabbccdd") {
		t.Fatal("text messages should not register devices")
	}
}

func TestEventTapNodeInfoMessage(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")
	tap := NewEventTap(nil, inv, "test-bridge")

	msg := transport.MeshMessage{
		From:        0x12345678,
		PortNum:     4, // NODEINFO_APP
		DecodedText: "My Node Name",
	}
	data, _ := json.Marshal(msg)

	tap.HandleMeshEvent(transport.MeshEvent{
		Type: "message",
		Data: data,
	})

	if !inv.IsRegistered("!12345678") {
		t.Fatal("NODEINFO_APP message should register device")
	}
}

func TestEventTapNilReporter(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")
	tap := NewEventTap(nil, inv, "test-bridge")

	// Should not panic with nil reporter
	node := transport.MeshNode{
		Num:    0xaabbccdd,
		UserID: "!aabbccdd",
	}
	data, _ := json.Marshal(node)

	tap.HandleMeshEvent(transport.MeshEvent{
		Type: "node_update",
		Data: data,
	})

	if !inv.IsRegistered("!aabbccdd") {
		t.Fatal("should work with nil reporter (inventory-only mode)")
	}
}

func TestEventTapNodeUpdateWithoutUserID(t *testing.T) {
	inv := NewDeviceInventory(nil, "test-bridge")
	tap := NewEventTap(nil, inv, "test-bridge")

	// Node without UserID — should fall back to hex-formatted Num
	node := transport.MeshNode{
		Num:      0x00ff00ff,
		LongName: "No User ID",
	}
	data, _ := json.Marshal(node)

	tap.HandleMeshEvent(transport.MeshEvent{
		Type: "node_update",
		Data: data,
	})

	if !inv.IsRegistered("!00ff00ff") {
		t.Fatal("should register with hex-formatted node num when UserID is empty")
	}
}
