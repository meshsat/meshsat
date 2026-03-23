package hubreporter

import (
	"testing"
	"time"
)

func TestEncodeSatPosition_RoundTrip(t *testing.T) {
	bridgeID := "mule01"
	lat := 51.5074
	lon := -0.1278
	alt := float32(42)
	source := byte(1)
	ts := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)

	data := EncodeSatPosition(bridgeID, lat, lon, alt, source, ts)

	header, payload, err := DecodeSatUplink(data)
	if err != nil {
		t.Fatalf("DecodeSatUplink: %v", err)
	}
	if header.MsgType != SatMsgPosition {
		t.Fatalf("expected type %d, got %d", SatMsgPosition, header.MsgType)
	}
	if header.Version != 1 {
		t.Fatalf("expected version 1, got %d", header.Version)
	}

	gotBridge, gotLat, gotLon, gotAlt, gotSource, gotTS, err := DecodeSatPosition(payload)
	if err != nil {
		t.Fatalf("DecodeSatPosition: %v", err)
	}
	if gotBridge != bridgeID {
		t.Errorf("bridgeID: got %q, want %q", gotBridge, bridgeID)
	}
	// float32 precision: compare with tolerance
	if diff := gotLat - lat; diff > 0.001 || diff < -0.001 {
		t.Errorf("lat: got %f, want %f", gotLat, lat)
	}
	if diff := gotLon - lon; diff > 0.001 || diff < -0.001 {
		t.Errorf("lon: got %f, want %f", gotLon, lon)
	}
	if gotAlt != alt {
		t.Errorf("alt: got %f, want %f", gotAlt, alt)
	}
	if gotSource != source {
		t.Errorf("source: got %d, want %d", gotSource, source)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("timestamp: got %v, want %v", gotTS, ts)
	}
}

func TestEncodeSatSOS_RoundTrip(t *testing.T) {
	bridgeID := "mule01"
	deviceID := "!abcd1234"
	lat := 40.7128
	lon := -74.0060
	message := "Emergency at base camp"
	ts := time.Date(2026, 3, 23, 14, 30, 0, 0, time.UTC)

	data := EncodeSatSOS(bridgeID, deviceID, lat, lon, message, ts)

	header, payload, err := DecodeSatUplink(data)
	if err != nil {
		t.Fatalf("DecodeSatUplink: %v", err)
	}
	if header.MsgType != SatMsgSOS {
		t.Fatalf("expected type %d, got %d", SatMsgSOS, header.MsgType)
	}

	gotBridge, gotDevice, gotLat, gotLon, gotMsg, gotTS, err := DecodeSatSOS(payload)
	if err != nil {
		t.Fatalf("DecodeSatSOS: %v", err)
	}
	if gotBridge != bridgeID {
		t.Errorf("bridgeID: got %q, want %q", gotBridge, bridgeID)
	}
	if gotDevice != deviceID {
		t.Errorf("deviceID: got %q, want %q", gotDevice, deviceID)
	}
	if diff := gotLat - lat; diff > 0.001 || diff < -0.001 {
		t.Errorf("lat: got %f, want %f", gotLat, lat)
	}
	if diff := gotLon - lon; diff > 0.001 || diff < -0.001 {
		t.Errorf("lon: got %f, want %f", gotLon, lon)
	}
	if gotMsg != message {
		t.Errorf("message: got %q, want %q", gotMsg, message)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("timestamp: got %v, want %v", gotTS, ts)
	}
}

func TestEncodeSatHealth_RoundTrip(t *testing.T) {
	bridgeID := "mule01"
	uptimeSec := uint32(86400)
	cpuPct := byte(45)
	memPct := byte(72)
	diskPct := byte(33)
	ifaces := []SatIfaceStatus{
		{Name: "mesh_0", Online: true, Signal: 0},
		{Name: "iridium_0", Online: true, Signal: 4},
		{Name: "cellular_0", Online: false, Signal: 0},
	}
	ts := time.Date(2026, 3, 23, 16, 0, 0, 0, time.UTC)

	data := EncodeSatHealth(bridgeID, uptimeSec, cpuPct, memPct, diskPct, ifaces, ts)

	header, payload, err := DecodeSatUplink(data)
	if err != nil {
		t.Fatalf("DecodeSatUplink: %v", err)
	}
	if header.MsgType != SatMsgHealthSummary {
		t.Fatalf("expected type %d, got %d", SatMsgHealthSummary, header.MsgType)
	}

	gotBridge, gotUptime, gotCPU, gotMem, gotDisk, gotIfaces, gotTS, err := DecodeSatHealth(payload)
	if err != nil {
		t.Fatalf("DecodeSatHealth: %v", err)
	}
	if gotBridge != bridgeID {
		t.Errorf("bridgeID: got %q, want %q", gotBridge, bridgeID)
	}
	if gotUptime != uptimeSec {
		t.Errorf("uptime: got %d, want %d", gotUptime, uptimeSec)
	}
	if gotCPU != cpuPct {
		t.Errorf("cpu: got %d, want %d", gotCPU, cpuPct)
	}
	if gotMem != memPct {
		t.Errorf("mem: got %d, want %d", gotMem, memPct)
	}
	if gotDisk != diskPct {
		t.Errorf("disk: got %d, want %d", gotDisk, diskPct)
	}
	if len(gotIfaces) != len(ifaces) {
		t.Fatalf("iface count: got %d, want %d", len(gotIfaces), len(ifaces))
	}
	for i, want := range ifaces {
		got := gotIfaces[i]
		if got.Name != want.Name {
			t.Errorf("iface[%d].Name: got %q, want %q", i, got.Name, want.Name)
		}
		if got.Online != want.Online {
			t.Errorf("iface[%d].Online: got %v, want %v", i, got.Online, want.Online)
		}
		if got.Signal != want.Signal {
			t.Errorf("iface[%d].Signal: got %d, want %d", i, got.Signal, want.Signal)
		}
	}
	if !gotTS.Equal(ts) {
		t.Errorf("timestamp: got %v, want %v", gotTS, ts)
	}
}

func TestSatPosition_FitsInSBD(t *testing.T) {
	// Worst case: max-length bridge ID
	data := EncodeSatPosition("bridge0123456789", 90.0, 180.0, 8848, 1, time.Now())
	if len(data) > 340 {
		t.Errorf("position payload %d bytes exceeds SBD limit of 340", len(data))
	}
	t.Logf("position payload: %d bytes", len(data))
}

func TestSatSOS_FitsInSBD(t *testing.T) {
	// Worst case: max-length everything
	longMsg := "EMERGENCY: This is a distress signal from the field operations team"
	if len(longMsg) > 64 {
		longMsg = longMsg[:64]
	}
	data := EncodeSatSOS("bridge0123456789", "device0123456789", 90.0, 180.0, longMsg, time.Now())
	if len(data) > 340 {
		t.Errorf("SOS payload %d bytes exceeds SBD limit of 340", len(data))
	}
	t.Logf("SOS payload: %d bytes", len(data))
}

func TestSatHealth_FitsInSBD(t *testing.T) {
	// Worst case: max interfaces with long names
	ifaces := make([]SatIfaceStatus, 10)
	for i := range ifaces {
		ifaces[i] = SatIfaceStatus{Name: "interface_name_1", Online: true, Signal: 5}
	}
	data := EncodeSatHealth("bridge0123456789", 999999, 99, 99, 99, ifaces, time.Now())
	if len(data) > 340 {
		t.Errorf("health payload %d bytes exceeds SBD limit of 340", len(data))
	}
	t.Logf("health payload: %d bytes (with %d interfaces)", len(data), len(ifaces))
}

func TestIsSatUplink(t *testing.T) {
	data := EncodeSatPosition("test", 0, 0, 0, 0, time.Now())
	if !IsSatUplink(data) {
		t.Error("IsSatUplink should return true for valid data")
	}
	if IsSatUplink([]byte{0x00, 0x00, 0x00, 0x00}) {
		t.Error("IsSatUplink should return false for invalid magic")
	}
	if IsSatUplink([]byte{0x4D}) {
		t.Error("IsSatUplink should return false for short data")
	}
	if IsSatUplink(nil) {
		t.Error("IsSatUplink should return false for nil")
	}
}

func TestDecodeSatUplink_InvalidData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		err  error
	}{
		{"too short", []byte{0x4D}, ErrTooShort},
		{"wrong magic", []byte{0x00, 0x00, 0x01, 0x01}, ErrBadMagic},
		{"bad version", []byte{0x4D, 0x53, 0xFF, 0x01}, ErrBadVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DecodeSatUplink(tt.data)
			if err != tt.err {
				t.Errorf("got error %v, want %v", err, tt.err)
			}
		})
	}
}

func TestDecodeSatPosition_Truncated(t *testing.T) {
	// Valid header but truncated payload
	data := []byte{0x4D, 0x53, 0x01, SatMsgPosition, 0x04, 't', 'e', 's', 't'}
	_, payload, err := DecodeSatUplink(data)
	if err != nil {
		t.Fatalf("DecodeSatUplink: %v", err)
	}
	_, _, _, _, _, _, err = DecodeSatPosition(payload)
	if err != ErrTruncated {
		t.Errorf("expected ErrTruncated, got %v", err)
	}
}

func TestEncodeSatSOS_LongMessageTruncated(t *testing.T) {
	longMsg := "This is a very long SOS message that exceeds the sixty-four character limit and should be truncated to fit"
	data := EncodeSatSOS("test", "dev", 0, 0, longMsg, time.Now())

	_, payload, _ := DecodeSatUplink(data)
	_, _, _, _, gotMsg, _, err := DecodeSatSOS(payload)
	if err != nil {
		t.Fatalf("DecodeSatSOS: %v", err)
	}
	if len(gotMsg) > maxSOSMessageLen {
		t.Errorf("message should be truncated to %d chars, got %d", maxSOSMessageLen, len(gotMsg))
	}
}

func TestEncodeSatHealth_NoInterfaces(t *testing.T) {
	data := EncodeSatHealth("test", 100, 10, 20, 30, nil, time.Now())

	_, payload, err := DecodeSatUplink(data)
	if err != nil {
		t.Fatalf("DecodeSatUplink: %v", err)
	}
	gotBridge, gotUptime, gotCPU, gotMem, gotDisk, gotIfaces, _, err := DecodeSatHealth(payload)
	if err != nil {
		t.Fatalf("DecodeSatHealth: %v", err)
	}
	if gotBridge != "test" {
		t.Errorf("bridgeID: got %q, want %q", gotBridge, "test")
	}
	if gotUptime != 100 {
		t.Errorf("uptime: got %d, want 100", gotUptime)
	}
	if gotCPU != 10 || gotMem != 20 || gotDisk != 30 {
		t.Errorf("metrics: got %d/%d/%d, want 10/20/30", gotCPU, gotMem, gotDisk)
	}
	if len(gotIfaces) != 0 {
		t.Errorf("expected 0 interfaces, got %d", len(gotIfaces))
	}
}
