package transport

import "testing"

// ZCL decoder tests for the new clusters added with the device manager
// [MESHSAT-509]. The wire frames here come from real Tuya / SONOFF /
// Xiaomi captures, plus synthetic frames for the "with cmd byte" vs
// "compact" layout variants the unified header walker has to handle.

func TestDecodeZCLInt16Report_Temperature_TuyaWithCmdByte(t *testing.T) {
	// Real Tuya temp/humidity sensor frame (from tesseract field-kit
	// capture): FCF=0x18, TSN=0x21, CMD=0x0a, AttrID=0x0000 (LE),
	// DataType=0x29 (int16), Value=0x09e6 (LE) = 2534 = 25.34°C
	frame := []byte{0x18, 0x21, 0x0a, 0x00, 0x00, 0x29, 0xe6, 0x09}
	attr, val, ok := decodeZCLInt16Report(frame)
	if !ok {
		t.Fatalf("expected decode ok, got false")
	}
	if attr != 0x0000 {
		t.Errorf("expected attr 0x0000, got 0x%04x", attr)
	}
	if val != 2534 {
		t.Errorf("expected 2534 (25.34°C), got %d", val)
	}
}

func TestDecodeZCLUint16Report_Humidity_TuyaWithCmdByte(t *testing.T) {
	// FCF=0x18, TSN=0x22, CMD=0x0a, AttrID=0x0000, DataType=0x21 (uint16),
	// Value=0x12c0 (LE) = 4800 = 48.00%
	frame := []byte{0x18, 0x22, 0x0a, 0x00, 0x00, 0x21, 0xc0, 0x12}
	attr, val, ok := decodeZCLUint16Report(frame)
	if !ok {
		t.Fatalf("expected decode ok, got false")
	}
	if attr != 0x0000 {
		t.Errorf("expected attr 0x0000, got 0x%04x", attr)
	}
	if val != 4800 {
		t.Errorf("expected 4800 (48.00%%), got %d", val)
	}
}

func TestDecodeZCLUint8Report_Battery(t *testing.T) {
	// PowerCfg cluster, BatteryPercentageRemaining (attr 0x0021), datatype
	// 0x20 (uint8). Value 200 = 100% in ZCL half-percent encoding (the
	// transport divides by 2 before exposing). Frame:
	//   FCF=0x18, TSN=0x33, CMD=0x0a, AttrID=0x0021, DataType=0x20, Value=200
	frame := []byte{0x18, 0x33, 0x0a, 0x21, 0x00, 0x20, 200}
	attr, val, ok := decodeZCLUint8Report(frame)
	if !ok {
		t.Fatalf("expected decode ok, got false")
	}
	if attr != ZCLAttrBatteryPercent {
		t.Errorf("expected attr 0x0021, got 0x%04x", attr)
	}
	if val != 200 {
		t.Errorf("expected 200 (100%% raw), got %d", val)
	}
}

func TestDecodeZCLUint8Report_OnOff(t *testing.T) {
	// OnOff cluster (0x0006), attr 0x0000 (OnOff), datatype 0x10 (boolean).
	// Frame for "ON": FCF=0x18, TSN=0x44, CMD=0x0a, AttrID=0x0000, DT=0x10, Val=0x01
	frameOn := []byte{0x18, 0x44, 0x0a, 0x00, 0x00, 0x10, 0x01}
	attr, val, ok := decodeZCLUint8Report(frameOn)
	if !ok {
		t.Fatalf("expected ON decode ok, got false")
	}
	if attr != ZCLAttrOnOffState {
		t.Errorf("expected attr 0x0000, got 0x%04x", attr)
	}
	if val != 1 {
		t.Errorf("expected ON (1), got %d", val)
	}

	frameOff := []byte{0x18, 0x45, 0x0a, 0x00, 0x00, 0x10, 0x00}
	_, val, ok = decodeZCLUint8Report(frameOff)
	if !ok || val != 0 {
		t.Errorf("OFF decode failed: ok=%v val=%d", ok, val)
	}
}

func TestDecodeZCLInt16Report_RejectsTooShort(t *testing.T) {
	if _, _, ok := decodeZCLInt16Report([]byte{0x18, 0x21}); ok {
		t.Error("expected short frame to reject, got ok=true")
	}
}

func TestDecodeZCLInt16Report_RejectsWrongDataType(t *testing.T) {
	// String datatype (0x42) instead of int16 (0x29) — must reject.
	frame := []byte{0x18, 0x21, 0x0a, 0x00, 0x00, 0x42, 0x05, 'h', 'e', 'l', 'l', 'o'}
	if _, _, ok := decodeZCLInt16Report(frame); ok {
		t.Error("expected wrong datatype to reject, got ok=true")
	}
}

func TestDecodeIASZoneStatus_NotificationCmd(t *testing.T) {
	// Zone Status Change Notification (cmd 0x00, cluster-specific FCF=0x09).
	// Status=0x0021 (Alarm1 + RestoreReports). ExtendedStatus=0, ZoneID=1, Delay=0.
	frame := []byte{0x09, 0x10, 0x00, 0x21, 0x00, 0x00, 0x01, 0x00, 0x00}
	zs, attr, ok := decodeIASZoneStatus(frame)
	if !ok {
		t.Fatalf("expected decode ok")
	}
	if attr != 0xFFFF {
		t.Errorf("expected sentinel attr 0xFFFF for cmd-0x00 path, got 0x%04x", attr)
	}
	if zs.Raw != 0x0021 {
		t.Errorf("expected raw 0x0021, got 0x%04x", zs.Raw)
	}
	if !zs.Alarm1 {
		t.Error("expected Alarm1 set")
	}
	if !zs.Triggered {
		t.Error("expected Triggered=true (Alarm1 is set)")
	}
	if zs.Tamper {
		t.Error("expected Tamper false")
	}
}

func TestDecodeIASZoneStatus_AttributeReport(t *testing.T) {
	// Some Xiaomi/Aqara devices echo zone status via attribute report on
	// attr 0x0002. FCF=0x18, TSN, CMD=0x0a, AttrID=0x0002, DT=0x21, Val=0x0008
	// (BatteryLow only — no alarm, not "Triggered").
	frame := []byte{0x18, 0x21, 0x0a, 0x02, 0x00, 0x21, 0x08, 0x00}
	zs, attr, ok := decodeIASZoneStatus(frame)
	if !ok {
		t.Fatalf("expected decode ok")
	}
	if attr != 0x0002 {
		t.Errorf("expected attr 0x0002, got 0x%04x", attr)
	}
	if !zs.BatteryLow {
		t.Error("expected BatteryLow set")
	}
	if zs.Triggered {
		t.Error("expected Triggered false (only BatteryLow, not an alarm)")
	}
}

func TestIASZoneText_AllClear(t *testing.T) {
	zs := decodeZoneStatus(0x0000)
	if got := iasZoneText(&zs); got != "clear" {
		t.Errorf("expected 'clear', got %q", got)
	}
}

func TestIASZoneText_MultipleFlags(t *testing.T) {
	zs := decodeZoneStatus(0x0001 | 0x0004 | 0x0008) // alarm1 + tamper + battery_low
	got := iasZoneText(&zs)
	want := "alarm1+tamper+battery_low"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestZCLReportHeader_FallbackToCompact(t *testing.T) {
	// Compact layout (no cmd byte, observed on some Xiaomi sensors):
	// FCF + TSN + AttrID(2) + DataType + Value
	frame := []byte{0x08, 0x10, 0x00, 0x00, 0x29, 0xe6, 0x09}
	off, attr, dt, ok := zclReportHeader(frame)
	if !ok {
		t.Fatalf("expected compact-layout decode ok")
	}
	if off != 5 {
		t.Errorf("expected value offset 5, got %d", off)
	}
	if attr != 0x0000 || dt != 0x29 {
		t.Errorf("expected attr=0 dt=0x29, got attr=0x%04x dt=0x%02x", attr, dt)
	}
}
