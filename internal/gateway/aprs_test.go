package gateway

import (
	"math"
	"strings"
	"testing"
)

// --- KISS encode/decode tests ---

func TestKISSEncode(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03}
	frame := KISSEncode(payload)

	// Should start and end with FEND
	if frame[0] != kissFEND {
		t.Errorf("first byte: got 0x%02x, want 0x%02x", frame[0], kissFEND)
	}
	if frame[len(frame)-1] != kissFEND {
		t.Errorf("last byte: got 0x%02x, want 0x%02x", frame[len(frame)-1], kissFEND)
	}
	// Second byte should be command (0x00)
	if frame[1] != kissData {
		t.Errorf("command byte: got 0x%02x, want 0x00", frame[1])
	}
}

func TestKISSEncodeEscape(t *testing.T) {
	// Payload containing FEND and FESC characters
	payload := []byte{0x01, kissFEND, 0x02, kissFESC, 0x03}
	frame := KISSEncode(payload)

	// Should be: FEND + 0x00 + 0x01 + FESC TFEND + 0x02 + FESC TFESC + 0x03 + FEND
	expected := []byte{kissFEND, 0x00, 0x01, kissFESC, kissTFEND, 0x02, kissFESC, kissTFESC, 0x03, kissFEND}
	if len(frame) != len(expected) {
		t.Fatalf("frame length: got %d, want %d", len(frame), len(expected))
	}
	for i, b := range frame {
		if b != expected[i] {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x", i, b, expected[i])
		}
	}
}

func TestKISSDecode(t *testing.T) {
	// Command byte + unescaped data
	frame := []byte{0x00, 0x01, 0x02, 0x03}
	payload, err := KISSDecode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload) != 3 {
		t.Fatalf("payload length: got %d, want 3", len(payload))
	}
	if payload[0] != 0x01 || payload[1] != 0x02 || payload[2] != 0x03 {
		t.Errorf("payload: got %v", payload)
	}
}

func TestKISSDecodeEscape(t *testing.T) {
	// Command byte + escaped FEND and FESC
	frame := []byte{0x00, 0x01, kissFESC, kissTFEND, 0x02, kissFESC, kissTFESC, 0x03}
	payload, err := KISSDecode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	expected := []byte{0x01, kissFEND, 0x02, kissFESC, 0x03}
	if len(payload) != len(expected) {
		t.Fatalf("payload length: got %d, want %d", len(payload), len(expected))
	}
	for i, b := range payload {
		if b != expected[i] {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x", i, b, expected[i])
		}
	}
}

func TestKISSEncodeDecodeRoundtrip(t *testing.T) {
	original := []byte{0x00, kissFEND, kissFESC, 0xFF, 0x42}
	encoded := KISSEncode(original)

	// Strip outer FENDs and decode
	inner := encoded[1 : len(encoded)-1]
	decoded, err := KISSDecode(inner)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("length: got %d, want %d", len(decoded), len(original))
	}
	for i, b := range decoded {
		if b != original[i] {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x", i, b, original[i])
		}
	}
}

// --- AX.25 encode/decode tests ---

func TestAX25EncodeDecodeRoundtrip(t *testing.T) {
	src := AX25Address{Call: "PA3XYZ", SSID: 10}
	dst := AX25Address{Call: "APRS", SSID: 0}
	path := []AX25Address{{Call: "WIDE1", SSID: 1}}
	info := []byte("!5222.08N/00454.24E-MeshSat")

	encoded := EncodeAX25Frame(dst, src, path, info)
	decoded, err := DecodeAX25Frame(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.Src.Call != "PA3XYZ" {
		t.Errorf("src call: got %q", decoded.Src.Call)
	}
	if decoded.Src.SSID != 10 {
		t.Errorf("src ssid: got %d", decoded.Src.SSID)
	}
	if decoded.Dst.Call != "APRS" {
		t.Errorf("dst call: got %q", decoded.Dst.Call)
	}
	if len(decoded.Path) != 1 {
		t.Fatalf("path len: got %d", len(decoded.Path))
	}
	if decoded.Path[0].Call != "WIDE1" || decoded.Path[0].SSID != 1 {
		t.Errorf("path[0]: got %v", decoded.Path[0])
	}
	if string(decoded.Info) != "!5222.08N/00454.24E-MeshSat" {
		t.Errorf("info: got %q", string(decoded.Info))
	}
}

func TestFormatCallsign(t *testing.T) {
	tests := []struct {
		addr     AX25Address
		expected string
	}{
		{AX25Address{Call: "PA3XYZ", SSID: 0}, "PA3XYZ"},
		{AX25Address{Call: "PA3XYZ", SSID: 10}, "PA3XYZ-10"},
		{AX25Address{Call: "WIDE1", SSID: 1}, "WIDE1-1"},
	}
	for _, tt := range tests {
		got := FormatCallsign(tt.addr)
		if got != tt.expected {
			t.Errorf("FormatCallsign(%v): got %q, want %q", tt.addr, got, tt.expected)
		}
	}
}

// --- APRS position encode/decode tests ---

func TestEncodeAPRSPosition(t *testing.T) {
	pos := EncodeAPRSPosition(52.3676, 4.9041, '/', '-', "MeshSat Bridge")
	s := string(pos)

	if s[0] != '!' {
		t.Errorf("first byte: got %q, want '!'", string(s[0]))
	}
	if !strings.Contains(s, "N") {
		t.Errorf("missing N hemisphere: %s", s)
	}
	if !strings.Contains(s, "E") {
		t.Errorf("missing E hemisphere: %s", s)
	}
	if !strings.Contains(s, "MeshSat Bridge") {
		t.Errorf("missing comment: %s", s)
	}
}

func TestParseAPRSPosition(t *testing.T) {
	// Create a frame with a position
	info := []byte("!5222.06N/00454.25E-Test station")
	frame := &AX25Frame{
		Src:  AX25Address{Call: "PA3XYZ", SSID: 10},
		Dst:  AX25Address{Call: "APRS", SSID: 0},
		Info: info,
	}

	pkt, err := ParseAPRSPacket(frame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pkt.DataType != '!' {
		t.Errorf("data type: got %q", string(pkt.DataType))
	}

	// 52°22.06'N = 52 + 22.06/60 = 52.36767
	expectedLat := 52.0 + 22.06/60.0
	if math.Abs(pkt.Lat-expectedLat) > 0.001 {
		t.Errorf("lat: got %f, want ~%f", pkt.Lat, expectedLat)
	}

	// 004°54.25'E = 4 + 54.25/60 = 4.90417
	expectedLon := 4.0 + 54.25/60.0
	if math.Abs(pkt.Lon-expectedLon) > 0.001 {
		t.Errorf("lon: got %f, want ~%f", pkt.Lon, expectedLon)
	}

	if pkt.Comment != "Test station" {
		t.Errorf("comment: got %q", pkt.Comment)
	}
}

func TestParseAPRSMessage(t *testing.T) {
	info := []byte(":PA3ABC   :Hello from MeshSat{42")
	frame := &AX25Frame{
		Src:  AX25Address{Call: "PA3XYZ", SSID: 10},
		Dst:  AX25Address{Call: "APRS", SSID: 0},
		Info: info,
	}

	pkt, err := ParseAPRSPacket(frame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pkt.DataType != ':' {
		t.Errorf("data type: got %q", string(pkt.DataType))
	}
	if pkt.MsgTo != "PA3ABC" {
		t.Errorf("msg_to: got %q", pkt.MsgTo)
	}
	if pkt.Message != "Hello from MeshSat" {
		t.Errorf("message: got %q", pkt.Message)
	}
	if pkt.MsgID != "42" {
		t.Errorf("msg_id: got %q", pkt.MsgID)
	}
}

func TestEncodeAPRSMessage(t *testing.T) {
	msg := EncodeAPRSMessage("PA3ABC", "Hello", "123")
	s := string(msg)

	if !strings.HasPrefix(s, ":PA3ABC   :Hello{123") {
		t.Errorf("encoded message: got %q", s)
	}
}

// --- APRS config tests ---

func TestAPRSConfigValidate(t *testing.T) {
	// Missing callsign
	cfg := DefaultAPRSConfig()
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing callsign")
	}

	// Valid config
	cfg.Callsign = "PA3XYZ"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// APRS-IS without passcode
	cfg.APRSISEnable = true
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for APRS-IS without passcode")
	}

	// APRS-IS with passcode
	cfg.APRSISPass = "12345"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAPRSConfigParse(t *testing.T) {
	j := `{"callsign":"PA3XYZ","ssid":10,"kiss_host":"192.168.1.100","kiss_port":8001,"frequency_mhz":144.800}`
	cfg, err := ParseAPRSConfig(j)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Callsign != "PA3XYZ" {
		t.Errorf("callsign: got %q", cfg.Callsign)
	}
	if cfg.SSID != 10 {
		t.Errorf("ssid: got %d", cfg.SSID)
	}
	if cfg.KISSHost != "192.168.1.100" {
		t.Errorf("kiss_host: got %q", cfg.KISSHost)
	}
	if cfg.FrequencyMHz != 144.800 {
		t.Errorf("frequency: got %f", cfg.FrequencyMHz)
	}
}

func TestAPRSConfigRedacted(t *testing.T) {
	cfg := APRSConfig{
		Callsign:   "PA3XYZ",
		APRSISPass: "secret123",
	}
	redacted := cfg.Redacted()
	if redacted.APRSISPass != "****" {
		t.Errorf("passcode not redacted: %q", redacted.APRSISPass)
	}
	if redacted.Callsign != "PA3XYZ" {
		t.Errorf("callsign changed: %q", redacted.Callsign)
	}
}
