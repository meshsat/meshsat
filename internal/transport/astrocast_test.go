package transport

import (
	"bytes"
	"encoding/hex"
	"strings"
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

func TestEncodeAstroFrameASCIIHex(t *testing.T) {
	// Encode EVT_RR (0x65) with no payload
	frame := EncodeAstroFrame(AstroCmdEvtRR, nil)

	// Must start with STX and end with ETX
	if frame[0] != 0x02 {
		t.Fatalf("STX = 0x%02x, want 0x02", frame[0])
	}
	if frame[len(frame)-1] != 0x03 {
		t.Fatalf("ETX = 0x%02x, want 0x03", frame[len(frame)-1])
	}

	// Content between STX and ETX should be all uppercase hex ASCII
	hexContent := string(frame[1 : len(frame)-1])
	for _, ch := range hexContent {
		if !((ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'F')) {
			t.Fatalf("non-hex char %q in frame content %q", string(ch), hexContent)
		}
	}

	// Opcode 0x65 should encode as "65"
	if hexContent[:2] != "65" {
		t.Fatalf("opcode hex = %q, want \"65\"", hexContent[:2])
	}

	// Frame should be: STX + "65" + CRC(4 chars) + ETX = 8 bytes total
	if len(frame) != 8 {
		t.Fatalf("frame length = %d, want 8 for no-payload command", len(frame))
	}
}

func TestEncodeDecodeAstroFrameRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		cmd     uint8
		payload []byte
	}{
		{"empty payload", AstroCmdEvtRR, nil},
		{"single byte", AstroCmdPldER, []byte{0x42}},
		{"multi byte", AstroCmdPldER, []byte("hello astrocast")},
		{"max uplink", AstroCmdPldER, bytes.Repeat([]byte{0xAA}, 160)},
		{"binary data", AstroCmdGeoWR, []byte{0x00, 0xFF, 0x80, 0x7F, 0x01, 0xFE, 0x55, 0xAA}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeAstroFrame(tt.cmd, tt.payload)

			// Verify STX/ETX
			if encoded[0] != 0x02 {
				t.Fatalf("STX = 0x%02x, want 0x02", encoded[0])
			}
			if encoded[len(encoded)-1] != 0x03 {
				t.Fatalf("ETX = 0x%02x, want 0x03", encoded[len(encoded)-1])
			}

			// Decode and verify round-trip
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
	if _, err := DecodeAstroFrame([]byte{0xFF, 0x36, 0x35, 0x41, 0x42, 0x43, 0x44, 0x03}); err == nil {
		t.Fatal("expected error for bad STX")
	}

	// Bad ETX
	if _, err := DecodeAstroFrame([]byte{0x02, 0x36, 0x35, 0x41, 0x42, 0x43, 0x44, 0xFF}); err == nil {
		t.Fatal("expected error for bad ETX")
	}

	// Odd hex length
	if _, err := DecodeAstroFrame([]byte{0x02, 0x36, 0x35, 0x41, 0x42, 0x43, 0x03}); err == nil {
		t.Fatal("expected error for odd hex length")
	}

	// Invalid hex characters
	if _, err := DecodeAstroFrame([]byte{0x02, 'Z', 'Z', 'A', 'B', 'C', 'D', 0x03}); err == nil {
		t.Fatal("expected error for invalid hex")
	}

	// CRC corruption
	good := EncodeAstroFrame(AstroCmdEvtRR, nil)
	// Corrupt the CRC (last hex chars before ETX)
	good[len(good)-2] ^= 0x01
	if _, err := DecodeAstroFrame(good); err == nil {
		t.Fatal("expected error for bad CRC")
	}
}

func TestCRCByteSwap(t *testing.T) {
	// Verify that CRC is byte-swapped in the frame.
	// Encode a known command and check the CRC bytes manually.
	cmd := AstroCmdEvtRR // 0x65
	rawData := []byte{cmd}
	crc := CRC16CCITT(rawData)

	frame := EncodeAstroFrame(cmd, nil)
	// Frame: STX + "65" + CRC(4 hex chars) + ETX
	hexContent := string(frame[1 : len(frame)-1])
	// CRC hex chars are the last 4 characters
	crcHex := hexContent[2:]

	// Decode the CRC hex back to bytes
	crcBytes, err := hex.DecodeString(crcHex)
	if err != nil {
		t.Fatalf("hex decode CRC: %v", err)
	}
	if len(crcBytes) != 2 {
		t.Fatalf("CRC bytes length = %d, want 2", len(crcBytes))
	}

	// CRC should be byte-swapped: [lo, hi]
	expectedLo := byte(crc & 0xFF)
	expectedHi := byte(crc >> 8)
	if crcBytes[0] != expectedLo || crcBytes[1] != expectedHi {
		t.Fatalf("CRC byte-swap: got [0x%02x, 0x%02x], want [0x%02x, 0x%02x]",
			crcBytes[0], crcBytes[1], expectedLo, expectedHi)
	}
}

func TestAnswerOpcodeIs80OR(t *testing.T) {
	// Answer opcode = request opcode | 0x80
	// Encode a "response" frame and verify decoding
	reqCmd := AstroCmdPldER       // 0x25
	ansCmd := reqCmd | 0x80       // 0xA5
	payload := []byte{0x01, 0x00} // payload_id

	frame := EncodeAstroFrame(ansCmd, payload)
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode answer frame: %v", err)
	}
	if decoded.CommandID != 0xA5 {
		t.Fatalf("answer CommandID = 0x%02x, want 0xA5", decoded.CommandID)
	}
}

func TestErrorResponseFrame(t *testing.T) {
	// Error response: opcode 0xFF with 2-byte LE error code
	errCode := AstroErrBufferFull // 0x2501
	payload := []byte{byte(errCode & 0xFF), byte(errCode >> 8)}

	frame := EncodeAstroFrame(0xFF, payload)
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode error frame: %v", err)
	}

	if !IsErrorResponse(decoded) {
		t.Fatal("expected IsErrorResponse to return true")
	}

	gotCode := ParseErrorCode(decoded.Payload)
	if gotCode != AstroErrBufferFull {
		t.Fatalf("error code = 0x%04x, want 0x%04x", gotCode, AstroErrBufferFull)
	}
}

func TestParseTLV(t *testing.T) {
	// Build a TLV payload matching MST_RR format
	// tag=0x41 len=1 val=3  (MsgQueued)
	// tag=0x42 len=1 val=1  (AckMsgQueued)
	// tag=0x43 len=1 val=2  (LastResetReason)
	// tag=0x44 len=4 val=600 (Uptime as LE uint32)
	data := []byte{
		0x41, 0x01, 0x03,
		0x42, 0x01, 0x01,
		0x43, 0x01, 0x02,
		0x44, 0x04, 0x58, 0x02, 0x00, 0x00, // 600 = 0x258
	}

	tlv, err := parseTLV(data)
	if err != nil {
		t.Fatalf("parseTLV: %v", err)
	}

	if got := tlvUint8(tlv, 0x41); got != 3 {
		t.Fatalf("tag 0x41 = %d, want 3", got)
	}
	if got := tlvUint8(tlv, 0x42); got != 1 {
		t.Fatalf("tag 0x42 = %d, want 1", got)
	}
	if got := tlvUint8(tlv, 0x43); got != 2 {
		t.Fatalf("tag 0x43 = %d, want 2", got)
	}
	if got := tlvUint32LE(tlv, 0x44); got != 600 {
		t.Fatalf("tag 0x44 = %d, want 600", got)
	}

	// Missing tag returns 0
	if got := tlvUint8(tlv, 0x99); got != 0 {
		t.Fatalf("missing tag = %d, want 0", got)
	}
}

func TestParseTLVEmpty(t *testing.T) {
	tlv, err := parseTLV([]byte{})
	if err != nil {
		t.Fatalf("parseTLV empty: %v", err)
	}
	if len(tlv) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(tlv))
	}
}

func TestParseTLVTruncated(t *testing.T) {
	// Tag present but length extends beyond data
	data := []byte{0x41, 0x04, 0x01} // says 4 bytes but only 1
	_, err := parseTLV(data)
	if err == nil {
		t.Fatal("expected error for truncated TLV")
	}
}

func TestCommandConstants(t *testing.T) {
	// Verify all command opcodes match the official Astronode S protocol spec
	cmds := map[string]uint8{
		"CFG_WR": AstroCmdCfgWR,
		"WIF_WR": AstroCmdWifWR,
		"SSC_WR": AstroCmdSscWR,
		"CFG_SR": AstroCmdCfgSR,
		"CFG_FR": AstroCmdCfgFR,
		"CFG_RR": AstroCmdCfgRR,
		"RTC_RR": AstroCmdRtcRR,
		"NCO_RR": AstroCmdNcoRR,
		"MGI_RR": AstroCmdMgiRR,
		"MSN_RR": AstroCmdMsnRR,
		"MPN_RR": AstroCmdMpnRR,
		"PLD_ER": AstroCmdPldER,
		"PLD_DR": AstroCmdPldDR,
		"PLD_FR": AstroCmdPldFR,
		"GEO_WR": AstroCmdGeoWR,
		"SAK_RR": AstroCmdSakRR,
		"SAK_CR": AstroCmdSakCR,
		"CMD_RR": AstroCmdCmdRR,
		"CMD_CR": AstroCmdCmdCR,
		"RES_CR": AstroCmdResetR,
		"GPO_SR": AstroCmdGpoSR,
		"GPI_RR": AstroCmdGpiRR,
		"EVT_RR": AstroCmdEvtRR,
		"CTX_SR": AstroCmdCtxSR,
		"PER_RR": AstroCmdPerRR,
		"PER_CR": AstroCmdPerCR,
		"MST_RR": AstroCmdMstRR,
		"LCD_RR": AstroCmdLcdRR,
		"END_RR": AstroCmdEndRR,
	}
	expected := map[string]uint8{
		"CFG_WR": 0x05,
		"WIF_WR": 0x06,
		"SSC_WR": 0x07,
		"CFG_SR": 0x10,
		"CFG_FR": 0x11,
		"CFG_RR": 0x15,
		"RTC_RR": 0x17,
		"NCO_RR": 0x18,
		"MGI_RR": 0x19,
		"MSN_RR": 0x1A,
		"MPN_RR": 0x1B,
		"PLD_ER": 0x25,
		"PLD_DR": 0x26,
		"PLD_FR": 0x27,
		"GEO_WR": 0x35,
		"SAK_RR": 0x45,
		"SAK_CR": 0x46,
		"CMD_RR": 0x47,
		"CMD_CR": 0x48,
		"RES_CR": 0x55,
		"GPO_SR": 0x62,
		"GPI_RR": 0x63,
		"EVT_RR": 0x65,
		"CTX_SR": 0x66,
		"PER_RR": 0x67,
		"PER_CR": 0x68,
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

func TestEventRegisterBits(t *testing.T) {
	// Verify event register bit assignments match official docs
	if AstroEvtSAKAvail != 0x01 {
		t.Errorf("AstroEvtSAKAvail = 0x%02x, want 0x01", AstroEvtSAKAvail)
	}
	if AstroEvtReset != 0x02 {
		t.Errorf("AstroEvtReset = 0x%02x, want 0x02", AstroEvtReset)
	}
	if AstroEvtCmdAvail != 0x04 {
		t.Errorf("AstroEvtCmdAvail = 0x%02x, want 0x04", AstroEvtCmdAvail)
	}
	if AstroEvtBusy != 0x08 {
		t.Errorf("AstroEvtBusy = 0x%02x, want 0x08", AstroEvtBusy)
	}
}

func TestErrorCodeConstants(t *testing.T) {
	codes := map[string]uint16{
		"CRC":     AstroErrCRCNotValid,
		"Length":  AstroErrLengthNotValid,
		"Opcode":  AstroErrOpcodeNotValid,
		"Format":  AstroErrFormatNotValid,
		"Flash":   AstroErrFlashWriteFail,
		"Full":    AstroErrBufferFull,
		"DupID":   AstroErrDuplicateID,
		"Empty":   AstroErrBufferEmpty,
		"InvPos":  AstroErrInvalidPosition,
		"NoACK":   AstroErrNoACK,
		"NoClear": AstroErrNothingToClear,
	}
	expected := map[string]uint16{
		"CRC":     0x0001,
		"Length":  0x0011,
		"Opcode":  0x0121,
		"Format":  0x0601,
		"Flash":   0x0611,
		"Full":    0x2501,
		"DupID":   0x2511,
		"Empty":   0x2601,
		"InvPos":  0x3501,
		"NoACK":   0x4501,
		"NoClear": 0x4601,
	}
	for name, got := range codes {
		want := expected[name]
		if got != want {
			t.Errorf("%s = 0x%04x, want 0x%04x", name, got, want)
		}
	}
}

func TestEncodeDecodeNewCommands(t *testing.T) {
	// Verify round-trip for each command
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
		{"CFG_RR", AstroCmdCfgRR, nil},
		{"CFG_SR", AstroCmdCfgSR, nil},
		{"CFG_FR", AstroCmdCfgFR, nil},
		{"RTC_RR", AstroCmdRtcRR, nil},
		{"MGI_RR", AstroCmdMgiRR, nil},
		{"MSN_RR", AstroCmdMsnRR, nil},
		{"MPN_RR", AstroCmdMpnRR, nil},
		{"CTX_SR", AstroCmdCtxSR, nil},
		{"PER_CR", AstroCmdPerCR, nil},
	}

	for _, tt := range newCmds {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeAstroFrame(tt.cmd, tt.payload)
			if encoded[0] != 0x02 || encoded[len(encoded)-1] != 0x03 {
				t.Fatal("missing STX/ETX")
			}

			// Content between STX and ETX must be valid hex
			hexContent := string(encoded[1 : len(encoded)-1])
			if _, err := hex.DecodeString(hexContent); err != nil {
				t.Fatalf("invalid hex content: %v", err)
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

	// Encode as full ASCII hex frame
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

func TestMSTResponseTLVParsing(t *testing.T) {
	// Build a TLV-encoded MST_RR response payload
	payload := []byte{
		AstroTLVMsgQueued, 0x01, 0x03, // MsgQueued = 3
		AstroTLVAckMsgQueued, 0x01, 0x01, // AckMsgQueued = 1
		AstroTLVLastResetReason, 0x01, 0x02, // LastResetReason = 2 (watchdog)
		AstroTLVUptime, 0x04, 0x58, 0x02, 0x00, 0x00, // Uptime = 600 seconds
	}

	// Wrap in a frame and decode
	frame := EncodeAstroFrame(AstroCmdMstRR|0x80, payload) // answer opcode
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	tlv, err := parseTLV(decoded.Payload)
	if err != nil {
		t.Fatalf("parseTLV: %v", err)
	}

	if got := tlvUint8(tlv, AstroTLVMsgQueued); got != 3 {
		t.Fatalf("MsgQueued = %d, want 3", got)
	}
	if got := tlvUint8(tlv, AstroTLVAckMsgQueued); got != 1 {
		t.Fatalf("AckMsgQueued = %d, want 1", got)
	}
	if got := tlvUint8(tlv, AstroTLVLastResetReason); got != 2 {
		t.Fatalf("LastResetReason = %d, want 2", got)
	}
	if got := tlvUint32LE(tlv, AstroTLVUptime); got != 600 {
		t.Fatalf("Uptime = %d, want 600", got)
	}
}

func TestLCDResponseTLVParsing(t *testing.T) {
	// Build a TLV-encoded LCD_RR response
	payload := []byte{
		AstroTLVStartTime, 0x04, 0x78, 0x00, 0x00, 0x00, // StartTime = 120
		AstroTLVEndTime, 0x04, 0xF0, 0x00, 0x00, 0x00, // EndTime = 240
		AstroTLVPeakRSSI, 0x01, 0xAB, // PeakRSSI = 171 (unsigned)
		AstroTLVPeakTime, 0x04, 0x3C, 0x00, 0x00, 0x00, // PeakTime = 60
	}

	frame := EncodeAstroFrame(AstroCmdLcdRR|0x80, payload)
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	tlv, err := parseTLV(decoded.Payload)
	if err != nil {
		t.Fatalf("parseTLV: %v", err)
	}

	if got := tlvUint32LE(tlv, AstroTLVStartTime); got != 120 {
		t.Fatalf("StartTime = %d, want 120", got)
	}
	if got := tlvUint32LE(tlv, AstroTLVEndTime); got != 240 {
		t.Fatalf("EndTime = %d, want 240", got)
	}
	if got := tlvUint8(tlv, AstroTLVPeakRSSI); got != 0xAB {
		t.Fatalf("PeakRSSI = %d, want 171", got)
	}
	if got := tlvUint32LE(tlv, AstroTLVPeakTime); got != 60 {
		t.Fatalf("PeakTime = %d, want 60", got)
	}
}

func TestPERResponseTLVParsing(t *testing.T) {
	// Build a TLV-encoded PER_RR response with a couple of counters
	payload := []byte{
		0x01, 0x04, 0xE8, 0x03, 0x00, 0x00, // SatSearchPhasesCnt = 1000
		0x0B, 0x04, 0xB6, 0x03, 0x00, 0x00, // AckReceived = 950
		0x0C, 0x04, 0x05, 0x00, 0x00, 0x00, // MsgTransmitted = 5
	}

	frame := EncodeAstroFrame(AstroCmdPerRR|0x80, payload)
	decoded, err := DecodeAstroFrame(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	tlv, err := parseTLV(decoded.Payload)
	if err != nil {
		t.Fatalf("parseTLV: %v", err)
	}

	if got := tlvUint32LE(tlv, 0x01); got != 1000 {
		t.Fatalf("SatSearchPhasesCnt = %d, want 1000", got)
	}
	if got := tlvUint32LE(tlv, 0x0B); got != 950 {
		t.Fatalf("AckReceived = %d, want 950", got)
	}
	if got := tlvUint32LE(tlv, 0x0C); got != 5 {
		t.Fatalf("MsgTransmitted = %d, want 5", got)
	}
}

func TestHexByteUppercase(t *testing.T) {
	// Verify hex encoding is uppercase
	for b := 0; b < 256; b++ {
		h := hexByte(byte(b))
		s := string(h)
		if s != strings.ToUpper(s) {
			t.Fatalf("hexByte(0x%02x) = %q, not uppercase", b, s)
		}
		// Verify it matches expected hex
		expected := strings.ToUpper(hex.EncodeToString([]byte{byte(b)}))
		if s != expected {
			t.Fatalf("hexByte(0x%02x) = %q, want %q", b, s, expected)
		}
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

	// Expire at t=200 with 300s timeout — should NOT expire (age=100)
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
