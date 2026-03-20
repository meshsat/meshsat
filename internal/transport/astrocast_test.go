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

func TestNewCommandConstants(t *testing.T) {
	// Verify all new command opcodes match the Astronode S binary protocol spec
	cmds := map[string]uint8{
		"NCO_RR": AstroCmdNcoRR,
		"GEO_WR": AstroCmdGeoWR,
		"SAK_RR": AstroCmdSakRR,
		"SAK_CR": AstroCmdSakCR,
		"CMD_RR": AstroCmdCmdRR,
		"CMD_CR": AstroCmdCmdCR,
		"PER_RR": AstroCmdPerRR,
		"MST_RR": AstroCmdMstRR,
		"LCD_RR": AstroCmdLcdRR,
		"END_RR": AstroCmdEndRR,
	}
	expected := map[string]uint8{
		"NCO_RR": 0x18,
		"GEO_WR": 0x35,
		"SAK_RR": 0x45,
		"SAK_CR": 0x46,
		"CMD_RR": 0x47,
		"CMD_CR": 0x48,
		"PER_RR": 0x67,
		"MST_RR": 0x69,
		"LCD_RR": 0x6A,
		"END_RR": 0x6B,
	}
	for name, got := range cmds {
		want := expected[name]
		if got != want {
			t.Errorf("%s = 0x%02x, want 0x%02x", name, got, want)
		}
	}
}

func TestEncodeDecodeNewCommands(t *testing.T) {
	// Verify round-trip for each new command
	newCmds := []struct {
		name    string
		cmd     uint8
		payload []byte
	}{
		{"SAK_RR", AstroCmdSakRR, nil},
		{"SAK_CR", AstroCmdSakCR, nil},
		{"CMD_RR", AstroCmdCmdRR, nil},
		{"CMD_CR", AstroCmdCmdCR, nil},
		{"GEO_WR", AstroCmdGeoWR, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}},
		{"NCO_RR", AstroCmdNcoRR, nil},
		{"MST_RR", AstroCmdMstRR, nil},
		{"LCD_RR", AstroCmdLcdRR, nil},
		{"END_RR", AstroCmdEndRR, nil},
		{"PER_RR", AstroCmdPerRR, nil},
	}

	for _, tt := range newCmds {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeAstroFrame(tt.cmd, tt.payload)
			if encoded[0] != 0x02 || encoded[len(encoded)-1] != 0x03 {
				t.Fatal("missing STX/ETX")
			}

			frame, err := DecodeAstroFrame(encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if frame.CommandID != tt.cmd {
				t.Fatalf("CommandID = 0x%02x, want 0x%02x", frame.CommandID, tt.cmd)
			}
			if !bytes.Equal(frame.Payload, tt.payload) {
				t.Fatalf("Payload mismatch")
			}
		})
	}
}

func TestGeoWRPayloadEncoding(t *testing.T) {
	// Verify geolocation payload layout: [lat:4 LE] [lon:4 LE]
	lat := int32(485000000)   // 48.5 degrees * 1e7
	lon := int32(-1234000000) // -123.4 degrees * 1e7 (negative = west)

	payload := make([]byte, 8)
	payload[0] = byte(lat)
	payload[1] = byte(lat >> 8)
	payload[2] = byte(lat >> 16)
	payload[3] = byte(lat >> 24)
	payload[4] = byte(lon)
	payload[5] = byte(lon >> 8)
	payload[6] = byte(lon >> 16)
	payload[7] = byte(lon >> 24)

	// Decode back
	gotLat := int32(uint32(payload[0]) | uint32(payload[1])<<8 | uint32(payload[2])<<16 | uint32(payload[3])<<24)
	gotLon := int32(uint32(payload[4]) | uint32(payload[5])<<8 | uint32(payload[6])<<16 | uint32(payload[7])<<24)

	if gotLat != lat {
		t.Fatalf("latitude roundtrip: got %d, want %d", gotLat, lat)
	}
	if gotLon != lon {
		t.Fatalf("longitude roundtrip: got %d, want %d", gotLon, lon)
	}

	// Encode as full frame
	frame := EncodeAstroFrame(AstroCmdGeoWR, payload)
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.CommandID != AstroCmdGeoWR {
		t.Fatalf("CommandID = 0x%02x, want 0x%02x", decoded.CommandID, AstroCmdGeoWR)
	}
	if len(decoded.Payload) != 8 {
		t.Fatalf("Payload len = %d, want 8", len(decoded.Payload))
	}
}

func TestMSTResponseParsing(t *testing.T) {
	// Simulate MST_RR response: [uplink:1][downlink:1][uptime:4 LE][reset_reason:1]
	payload := []byte{
		3,                      // uplink_pending
		1,                      // downlink_pending
		0x58, 0x02, 0x00, 0x00, // uptime = 600 seconds
		2, // last_reset_reason = watchdog
	}

	frame := EncodeAstroFrame(AstroCmdMstRR, payload)
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if decoded.Payload[0] != 3 {
		t.Fatalf("uplink_pending = %d, want 3", decoded.Payload[0])
	}
	if decoded.Payload[1] != 1 {
		t.Fatalf("downlink_pending = %d, want 1", decoded.Payload[1])
	}
	uptime := uint32(decoded.Payload[2]) | uint32(decoded.Payload[3])<<8 |
		uint32(decoded.Payload[4])<<16 | uint32(decoded.Payload[5])<<24
	if uptime != 600 {
		t.Fatalf("uptime = %d, want 600", uptime)
	}
	if decoded.Payload[6] != 2 {
		t.Fatalf("last_reset_reason = %d, want 2", decoded.Payload[6])
	}
}

func TestLCDResponseParsing(t *testing.T) {
	// Simulate LCD_RR response: [time_since:4 LE][peak_rssi:2 LE signed][time_peak:4 LE]
	payload := make([]byte, 10)
	// time_since_last = 120 seconds
	payload[0] = 0x78
	payload[1] = 0x00
	payload[2] = 0x00
	payload[3] = 0x00
	// peak_rssi = -85 dBm (signed int16)
	rssi := int16(-85)
	payload[4] = byte(uint16(rssi))
	payload[5] = byte(uint16(rssi) >> 8)
	// time_peak_rssi = 60 seconds
	payload[6] = 0x3C
	payload[7] = 0x00
	payload[8] = 0x00
	payload[9] = 0x00

	frame := EncodeAstroFrame(AstroCmdLcdRR, payload)
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	gotRSSI := int16(uint16(decoded.Payload[4]) | uint16(decoded.Payload[5])<<8)
	if gotRSSI != -85 {
		t.Fatalf("peak_rssi = %d, want -85", gotRSSI)
	}
}

func TestPERResponseParsing(t *testing.T) {
	// Simulate PER_RR response: [sent:4 LE][ackd:4 LE][resets:4 LE]
	payload := make([]byte, 12)
	// fragments_sent = 1000
	payload[0] = 0xE8
	payload[1] = 0x03
	payload[2] = 0x00
	payload[3] = 0x00
	// fragments_ackd = 950
	payload[4] = 0xB6
	payload[5] = 0x03
	payload[6] = 0x00
	payload[7] = 0x00
	// reset_count = 5
	payload[8] = 0x05
	payload[9] = 0x00
	payload[10] = 0x00
	payload[11] = 0x00

	frame := EncodeAstroFrame(AstroCmdPerRR, payload)
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	readU32 := func(b []byte) uint32 {
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	}

	sent := readU32(decoded.Payload[0:4])
	ackd := readU32(decoded.Payload[4:8])
	resets := readU32(decoded.Payload[8:12])

	if sent != 1000 {
		t.Fatalf("fragments_sent = %d, want 1000", sent)
	}
	if ackd != 950 {
		t.Fatalf("fragments_ackd = %d, want 950", ackd)
	}
	if resets != 5 {
		t.Fatalf("reset_count = %d, want 5", resets)
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
