package codec

import "testing"

func TestStripVersionByte_V1(t *testing.T) {
	payload := []byte{ProtoVersion1, 0xAA, 0xBB, 0xCC}
	ver, data := StripVersionByte(payload)
	if ver != ProtoVersion1 {
		t.Errorf("expected version %d, got %d", ProtoVersion1, ver)
	}
	if len(data) != 3 || data[0] != 0xAA {
		t.Errorf("expected payload [AA BB CC], got %x", data)
	}
}

func TestStripVersionByte_Legacy(t *testing.T) {
	payload := []byte{0xFF, 0xAA, 0xBB}
	ver, data := StripVersionByte(payload)
	if ver != 0 {
		t.Errorf("expected version 0 (legacy), got %d", ver)
	}
	if len(data) != 3 {
		t.Errorf("expected unchanged payload, got len %d", len(data))
	}
}

func TestStripVersionByte_MagicBytes(t *testing.T) {
	tests := []struct {
		name  string
		magic byte
	}{
		{"FullPosition", MagicGPSBridgeFull},
		{"DeltaPosition", MagicGPSBridgeDelta},
		{"CannedMessage", MagicCannedMessage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte{tt.magic, 0x01, 0x02}
			ver, data := StripVersionByte(payload)
			if ver != 0 {
				t.Errorf("magic byte 0x%02X should return version 0, got %d", tt.magic, ver)
			}
			if len(data) != 3 {
				t.Errorf("magic byte payload should be unchanged, got len %d", len(data))
			}
		})
	}
}

func TestStripVersionByte_Empty(t *testing.T) {
	ver, data := StripVersionByte([]byte{})
	if ver != 0 {
		t.Errorf("expected version 0, got %d", ver)
	}
	if len(data) != 0 {
		t.Errorf("expected empty payload")
	}
}

func TestPrependVersionByte(t *testing.T) {
	payload := []byte{0xAA, 0xBB}
	result := PrependVersionByte(payload)
	if len(result) != 3 {
		t.Fatalf("expected len 3, got %d", len(result))
	}
	if result[0] != ProtoVersion1 {
		t.Errorf("expected first byte %d, got %d", ProtoVersion1, result[0])
	}
	if result[1] != 0xAA || result[2] != 0xBB {
		t.Errorf("expected payload preserved, got %x", result[1:])
	}
}

func TestRoundTrip(t *testing.T) {
	original := []byte("hello world")
	versioned := PrependVersionByte(original)
	ver, data := StripVersionByte(versioned)
	if ver != ProtoVersion1 {
		t.Errorf("expected version %d, got %d", ProtoVersion1, ver)
	}
	if string(data) != "hello world" {
		t.Errorf("round-trip failed: got %q", string(data))
	}
}
