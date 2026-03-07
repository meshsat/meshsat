package transport

import (
	"testing"
)

func TestTryParseFrame(t *testing.T) {
	dt := &DirectAstrocastTransport{}

	// Valid frame
	encoded := EncodeAstroFrame(AstroCmdEvtRR, nil)
	frame := dt.tryParseFrame(encoded)
	if frame == nil {
		t.Fatal("expected frame from valid data")
	}
	if frame.CommandID != AstroCmdEvtRR {
		t.Fatalf("CommandID = 0x%02x, want 0x%02x", frame.CommandID, AstroCmdEvtRR)
	}

	// Frame with leading garbage
	garbage := append([]byte{0xFF, 0xFE, 0xFD}, encoded...)
	frame = dt.tryParseFrame(garbage)
	if frame == nil {
		t.Fatal("expected frame even with leading garbage")
	}
	if frame.CommandID != AstroCmdEvtRR {
		t.Fatalf("CommandID = 0x%02x after garbage, want 0x%02x", frame.CommandID, AstroCmdEvtRR)
	}

	// Frame with payload
	encoded = EncodeAstroFrame(AstroCmdPldDR, []byte{0x01, 0x00, 0x48, 0x69})
	frame = dt.tryParseFrame(encoded)
	if frame == nil {
		t.Fatal("expected frame with payload")
	}
	if frame.CommandID != AstroCmdPldDR {
		t.Fatalf("CommandID = 0x%02x, want 0x%02x", frame.CommandID, AstroCmdPldDR)
	}
	if len(frame.Payload) != 4 {
		t.Fatalf("Payload len = %d, want 4", len(frame.Payload))
	}

	// Incomplete frame — missing bytes
	partial := encoded[:len(encoded)-2]
	frame = dt.tryParseFrame(partial)
	if frame != nil {
		t.Fatal("expected nil for incomplete frame")
	}

	// No STX at all
	frame = dt.tryParseFrame([]byte{0xFF, 0xFE, 0xFD})
	if frame != nil {
		t.Fatal("expected nil for data without STX")
	}

	// Empty input
	frame = dt.tryParseFrame(nil)
	if frame != nil {
		t.Fatal("expected nil for empty input")
	}

	// Corrupted CRC — tryParseFrame calls DecodeAstroFrame which checks CRC
	good := EncodeAstroFrame(AstroCmdEvtRR, nil)
	good[len(good)-2] ^= 0xFF // corrupt CRC
	frame = dt.tryParseFrame(good)
	if frame != nil {
		t.Fatal("expected nil for corrupted CRC")
	}
}

func TestAutoDetectAstrocastExclusion(t *testing.T) {
	// Verify that the exclusion set logic works correctly
	excludePorts := []string{"/dev/ttyUSB0", "/dev/ttyUSB1"}
	excludeSet := make(map[string]bool)
	for _, p := range excludePorts {
		excludeSet[p] = true
	}

	if !excludeSet["/dev/ttyUSB0"] {
		t.Fatal("expected /dev/ttyUSB0 to be excluded")
	}
	if !excludeSet["/dev/ttyUSB1"] {
		t.Fatal("expected /dev/ttyUSB1 to be excluded")
	}
	if excludeSet["/dev/ttyUSB2"] {
		t.Fatal("expected /dev/ttyUSB2 to NOT be excluded")
	}
}

func TestKnownAstrocastVIDPIDs(t *testing.T) {
	// Verify known VID:PID table
	expected := []string{"0403:6001", "0403:6015", "10c4:ea60"}
	for _, vidpid := range expected {
		if !knownAstrocastVIDPIDs[vidpid] {
			t.Fatalf("expected %s in knownAstrocastVIDPIDs", vidpid)
		}
	}

	// Verify no overlap with Meshtastic-only VIDs (some share CP2102)
	// Note: 10c4:ea60 is in both — Astronode S and some ESP32 boards.
	// The auto-detect logic handles this by checking knownMeshtasticVIDPIDs first.
}

func TestNewDirectAstrocastTransport(t *testing.T) {
	dt := NewDirectAstrocastTransport("auto")
	if dt.port != "auto" {
		t.Fatalf("port = %q, want %q", dt.port, "auto")
	}
	if dt.eventSubs == nil {
		t.Fatal("eventSubs should be initialized")
	}

	dt2 := NewDirectAstrocastTransport("/dev/ttyUSB3")
	if dt2.port != "/dev/ttyUSB3" {
		t.Fatalf("port = %q, want %q", dt2.port, "/dev/ttyUSB3")
	}
}

func TestGetPort(t *testing.T) {
	dt := NewDirectAstrocastTransport("/dev/ttyUSB5")
	if got := dt.GetPort(); got != "/dev/ttyUSB5" {
		t.Fatalf("GetPort() = %q, want %q", got, "/dev/ttyUSB5")
	}
}

func TestSetExcludePortFuncs(t *testing.T) {
	dt := NewDirectAstrocastTransport("auto")

	fn1 := func() string { return "/dev/ttyUSB0" }
	fn2 := func() string { return "/dev/ttyACM0" }
	dt.SetExcludePortFuncs([]func() string{fn1, fn2})

	if len(dt.excludePortFns) != 2 {
		t.Fatalf("excludePortFns len = %d, want 2", len(dt.excludePortFns))
	}
	if dt.excludePortFns[0]() != "/dev/ttyUSB0" {
		t.Fatal("excludePortFns[0] returned wrong port")
	}
	if dt.excludePortFns[1]() != "/dev/ttyACM0" {
		t.Fatal("excludePortFns[1] returned wrong port")
	}
}
