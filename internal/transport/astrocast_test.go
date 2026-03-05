package transport

import (
	"bytes"
	"testing"
)

func TestCRC16CCITT(t *testing.T) {
	// Known test vector: "123456789" -> 0x29B1
	crc := CRC16CCITT([]byte("123456789"))
	if crc != 0x29B1 {
		t.Fatalf("CRC16CCITT(\"123456789\") = 0x%04X, want 0x29B1", crc)
	}

	// Empty input
	crc = CRC16CCITT([]byte{})
	if crc != 0xFFFF {
		t.Fatalf("CRC16CCITT(\"\") = 0x%04X, want 0xFFFF", crc)
	}
}

func TestEncodeDecodeAstroFrame(t *testing.T) {
	tests := []struct {
		name    string
		cmd     uint8
		payload []byte
	}{
		{"empty payload", AstroCmdEvtRR, nil},
		{"single byte", AstroCmdPldER, []byte{0x42}},
		{"multi byte", AstroCmdPldER, []byte("hello astrocast")},
		{"max uplink", AstroCmdPldER, bytes.Repeat([]byte{0xAA}, 160)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeAstroFrame(tt.cmd, tt.payload)

			// Check STX/ETX
			if encoded[0] != 0x02 {
				t.Fatalf("STX = 0x%02x, want 0x02", encoded[0])
			}
			if encoded[len(encoded)-1] != 0x03 {
				t.Fatalf("ETX = 0x%02x, want 0x03", encoded[len(encoded)-1])
			}

			frame, err := DecodeAstroFrame(encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if frame.CommandID != tt.cmd {
				t.Fatalf("CommandID = 0x%02x, want 0x%02x", frame.CommandID, tt.cmd)
			}
			if !bytes.Equal(frame.Payload, tt.payload) {
				t.Fatalf("Payload mismatch: got %d bytes, want %d", len(frame.Payload), len(tt.payload))
			}
		})
	}
}

func TestDecodeAstroFrameErrors(t *testing.T) {
	// Too short
	if _, err := DecodeAstroFrame([]byte{0x02, 0x03}); err == nil {
		t.Fatal("expected error for short frame")
	}

	// Bad STX
	if _, err := DecodeAstroFrame([]byte{0xFF, 0x01, 0x00, 0x65, 0x00, 0x00, 0x03}); err == nil {
		t.Fatal("expected error for bad STX")
	}

	// CRC corruption
	good := EncodeAstroFrame(AstroCmdEvtRR, nil)
	good[len(good)-2] ^= 0xFF // flip CRC byte
	if _, err := DecodeAstroFrame(good); err == nil {
		t.Fatal("expected error for bad CRC")
	}
}

func TestFragmentHeader(t *testing.T) {
	tests := []struct {
		msgID, fragNum, fragTotal uint8
	}{
		{0, 0, 1},
		{5, 0, 2},
		{15, 2, 3},
		{7, 3, 4},
	}

	for _, tt := range tests {
		b := EncodeFragmentHeader(tt.msgID, tt.fragNum, tt.fragTotal)
		gotID, gotNum, gotTotal := DecodeFragmentHeader(b)
		if gotID != tt.msgID || gotNum != tt.fragNum || gotTotal != tt.fragTotal {
			t.Fatalf("roundtrip(%d,%d,%d) = (%d,%d,%d)", tt.msgID, tt.fragNum, tt.fragTotal, gotID, gotNum, gotTotal)
		}
	}
}

func TestFragmentMessage(t *testing.T) {
	// Small message — no fragmentation
	small := bytes.Repeat([]byte{0x41}, 100)
	frags := FragmentMessage(1, small)
	if frags != nil {
		t.Fatal("expected nil for small message")
	}

	// Exactly 160 bytes — no fragmentation
	exact := bytes.Repeat([]byte{0x42}, 160)
	frags = FragmentMessage(1, exact)
	if frags != nil {
		t.Fatal("expected nil for 160-byte message")
	}

	// 340 bytes — should split into 3 fragments (159 + 159 + 22)
	data := bytes.Repeat([]byte{0x43}, 340)
	frags = FragmentMessage(5, data)
	if len(frags) != 3 {
		t.Fatalf("expected 3 fragments, got %d", len(frags))
	}

	// Each fragment <= 160 bytes
	for i, f := range frags {
		if len(f) > AstroMaxUplink {
			t.Fatalf("fragment %d is %d bytes, exceeds %d", i, len(f), AstroMaxUplink)
		}
		// First byte is header
		msgID, fragNum, fragTotal := DecodeFragmentHeader(f[0])
		if msgID != 5 {
			t.Fatalf("fragment %d msgID = %d, want 5", i, msgID)
		}
		if fragNum != uint8(i) {
			t.Fatalf("fragment %d fragNum = %d, want %d", i, fragNum, i)
		}
		if fragTotal != 3 {
			t.Fatalf("fragment %d fragTotal = %d, want 3", i, fragTotal)
		}
	}

	// Verify reassembly recovers original data
	rb := NewReassemblyBuffer()
	now := int64(1000)
	for _, f := range frags {
		msgID, fragNum, fragTotal := DecodeFragmentHeader(f[0])
		result := rb.AddFragment(AstroFragment{
			MsgID:     msgID,
			FragNum:   fragNum,
			FragTotal: fragTotal,
			Payload:   f[1:],
		}, now)
		if fragNum < fragTotal-1 && result != nil {
			t.Fatal("got result before all fragments")
		}
		if fragNum == fragTotal-1 {
			if result == nil {
				t.Fatal("expected reassembled result")
			}
			if !bytes.Equal(result, data) {
				t.Fatalf("reassembled data mismatch: got %d bytes, want %d", len(result), len(data))
			}
		}
	}
}

func TestReassemblyExpire(t *testing.T) {
	rb := NewReassemblyBuffer()

	// Add a fragment at t=100
	rb.AddFragment(AstroFragment{MsgID: 1, FragNum: 0, FragTotal: 2, Payload: []byte("a")}, 100)
	if len(rb.pending) != 1 {
		t.Fatal("expected 1 pending entry")
	}

	// Expire at t=200 with 60s timeout — should NOT expire (age=100)
	rb.Expire(200, 300)
	if len(rb.pending) != 1 {
		t.Fatal("should not have expired yet")
	}

	// Expire at t=500 with 300s timeout — SHOULD expire (age=400)
	rb.Expire(500, 300)
	if len(rb.pending) != 0 {
		t.Fatal("should have expired")
	}
}

func TestFragmentMessageMaxFragments(t *testing.T) {
	// 700 bytes — exceeds 4*159=636, should be truncated to 4 fragments
	data := bytes.Repeat([]byte{0x44}, 700)
	frags := FragmentMessage(0, data)
	if len(frags) != 4 {
		t.Fatalf("expected 4 fragments, got %d", len(frags))
	}

	// Total payload should be 636 bytes (4 * 159)
	total := 0
	for _, f := range frags {
		total += len(f) - 1 // subtract header byte
	}
	if total != 4*AstroFragPayload {
		t.Fatalf("total payload = %d, want %d", total, 4*AstroFragPayload)
	}
}
