package transport

import (
	"bytes"
	"testing"
)

func TestTryParseFrame(t *testing.T) {
	dt := &DirectAstrocastTransport{}

	// Valid ASCII hex frame
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
	if !bytes.Equal(frame.Payload, []byte{0x01, 0x00, 0x48, 0x69}) {
		t.Fatalf("Payload content mismatch")
	}

	// Incomplete frame — missing ETX
	partial := encoded[:len(encoded)-1]
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
	// Corrupt one of the CRC hex characters
	good[len(good)-2] = '0' // change last CRC hex char
	if good[len(good)-2] == good[len(good)-3] {
		good[len(good)-2] = '1' // make sure it's actually different
	}
	frame = dt.tryParseFrame(good)
	if frame != nil {
		t.Fatal("expected nil for corrupted CRC")
	}
}

func TestTryParseFrameASCIIHexFormat(t *testing.T) {
	dt := &DirectAstrocastTransport{}

	// Verify the frame format is ASCII hex between STX and ETX
	encoded := EncodeAstroFrame(AstroCmdPldER, []byte{0xDE, 0xAD})

	// Frame should be: STX + hex(opcode=25) + hex(payload=DEAD) + hex(CRC=4chars) + ETX
	// Total: 1 + 2 + 4 + 4 + 1 = 12 bytes
	if len(encoded) != 12 {
		t.Fatalf("frame length = %d, want 12", len(encoded))
	}

	// All bytes between STX and ETX should be ASCII hex chars
	for i := 1; i < len(encoded)-1; i++ {
		ch := encoded[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'F')) {
			t.Fatalf("byte %d = 0x%02x (%c), not valid hex ASCII", i, ch, ch)
		}
	}

	frame := dt.tryParseFrame(encoded)
	if frame == nil {
		t.Fatal("expected valid frame")
	}
	if frame.CommandID != AstroCmdPldER {
		t.Fatalf("CommandID = 0x%02x, want 0x%02x", frame.CommandID, AstroCmdPldER)
	}
	if !bytes.Equal(frame.Payload, []byte{0xDE, 0xAD}) {
		t.Fatalf("Payload = %x, want DEAD", frame.Payload)
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

func TestIsErrorResponseAndParseErrorCode(t *testing.T) {
	// Error response frame
	errPayload := []byte{0x01, 0x25} // 0x2501 buffer full in LE
	errFrame := &AstroFrame{CommandID: 0xFF, Payload: errPayload}
	if !IsErrorResponse(errFrame) {
		t.Fatal("expected IsErrorResponse=true for 0xFF")
	}
	code := ParseErrorCode(errFrame.Payload)
	if code != AstroErrBufferFull {
		t.Fatalf("error code = 0x%04x, want 0x%04x", code, AstroErrBufferFull)
	}

	// Normal response
	normalFrame := &AstroFrame{CommandID: 0xA5, Payload: nil}
	if IsErrorResponse(normalFrame) {
		t.Fatal("expected IsErrorResponse=false for 0xA5")
	}

	// Empty error payload
	emptyErr := &AstroFrame{CommandID: 0xFF, Payload: nil}
	code = ParseErrorCode(emptyErr.Payload)
	if code != 0 {
		t.Fatalf("empty error payload code = 0x%04x, want 0x0000", code)
	}
}
