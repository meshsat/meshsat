package api

import (
	"strings"
	"testing"
)

// TestMeshSatBLEUUIDDetection guards against silent drift between the
// UUID advertised by internal/routing/ble_interface.go and the one
// this package uses to tag remote MeshSat kits. [MESHSAT-629]
func TestMeshSatBLEUUIDDetection(t *testing.T) {
	infoWithUUID := `Device 00:11:22:33:44:55 MeshSat-RNS
	Name: MeshSat-RNS
	Alias: MeshSat-RNS
	Paired: yes
	Connected: no
	Trusted: yes
	UUID: Vendor specific           (7e57c0de-0001-4000-8000-000000000001)
	RSSI: -58`

	if !infoHasMeshSatUUID(infoWithUUID) {
		t.Fatalf("expected detection of MeshSat UUID in bluetoothctl info output")
	}

	// BlueZ sometimes emits the trailing hex block mixed-case.
	mixed := strings.Replace(infoWithUUID, "7e57c0de", "7E57C0DE", 1)
	if !infoHasMeshSatUUID(mixed) {
		t.Fatalf("expected case-insensitive match")
	}

	infoWithoutUUID := `Device AA:BB:CC:DD:EE:FF Speaker
	Name: Speaker
	UUID: Audio Sink               (0000110b-0000-1000-8000-00805f9b34fb)
	RSSI: -70`
	if infoHasMeshSatUUID(infoWithoutUUID) {
		t.Fatalf("must not flag non-MeshSat devices")
	}
}
