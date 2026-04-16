package transport

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeZNP_RoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		frame ZNPFrame
	}{
		{"SysPing", BuildSysPing()},
		{"SysVersion", BuildSysVersion()},
		{"ZDOStartup", BuildZDOStartup()},
		{"AFRegister", BuildAFRegister(1, 0x0104, 0x0100, []uint16{0x0000, 0x0006}, []uint16{0x0000})},
		{"AFDataReq", BuildAFDataReq(0x1234, 1, 1, 0x0006, 42, []byte("hello zigbee"))},
		{"EmptyData", ZNPFrame{Cmd: CmdSysPing}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := EncodeZNP(tc.frame)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			// Verify SOF
			if encoded[0] != znpSOF {
				t.Errorf("first byte should be SOF 0xFE, got 0x%02x", encoded[0])
			}

			decoded, consumed, err := DecodeZNP(encoded)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if consumed != len(encoded) {
				t.Errorf("consumed %d, expected %d", consumed, len(encoded))
			}
			if decoded.Cmd != tc.frame.Cmd {
				t.Errorf("cmd mismatch: got %v, want %v", decoded.Cmd, tc.frame.Cmd)
			}
			if !bytes.Equal(decoded.Data, tc.frame.Data) {
				t.Errorf("data mismatch: got %x, want %x", decoded.Data, tc.frame.Data)
			}
		})
	}
}

func TestDecodeZNP_FCSValidation(t *testing.T) {
	frame := BuildSysPing()
	encoded, _ := EncodeZNP(frame)

	// Corrupt FCS
	corrupted := make([]byte, len(encoded))
	copy(corrupted, encoded)
	corrupted[len(corrupted)-1] ^= 0xFF

	_, _, err := DecodeZNP(corrupted)
	if err == nil {
		t.Error("expected FCS error for corrupted frame")
	}
}

func TestDecodeZNP_NoSOF(t *testing.T) {
	_, _, err := DecodeZNP([]byte{0x00, 0x01, 0x02, 0x03, 0x04})
	if err == nil {
		t.Error("expected error for missing SOF")
	}
}

func TestDecodeZNP_TooShort(t *testing.T) {
	_, _, err := DecodeZNP([]byte{0xFE, 0x00})
	if err == nil {
		t.Error("expected error for short buffer")
	}
}

func TestDecodeZNP_GarbageBeforeSOF(t *testing.T) {
	frame := BuildSysVersion()
	encoded, _ := EncodeZNP(frame)

	// Prepend garbage
	withGarbage := append([]byte{0xAA, 0xBB, 0xCC}, encoded...)
	decoded, consumed, err := DecodeZNP(withGarbage)
	if err != nil {
		t.Fatalf("decode with garbage prefix: %v", err)
	}
	if consumed != len(withGarbage) {
		t.Errorf("consumed %d, expected %d", consumed, len(withGarbage))
	}
	if decoded.Cmd != frame.Cmd {
		t.Errorf("cmd mismatch after garbage skip")
	}
}

func TestEncodeZNP_DataTooLong(t *testing.T) {
	f := ZNPFrame{
		Cmd:  CmdAFDataReq,
		Data: make([]byte, 251), // exceeds 250 max
	}
	_, err := EncodeZNP(f)
	if err == nil {
		t.Error("expected error for data > 250 bytes")
	}
}

func TestZNPFrame_TypeAndSubsystem(t *testing.T) {
	f := ZNPFrame{Cmd: [2]byte{ZNPTypeSREQ | ZNPSubAF, 0x01}}
	if f.Type() != ZNPTypeSREQ {
		t.Errorf("expected SREQ, got 0x%02x", f.Type())
	}
	if f.Subsystem() != ZNPSubAF {
		t.Errorf("expected AF subsystem, got 0x%02x", f.Subsystem())
	}
}

func TestZNPFrame_IsCmd(t *testing.T) {
	f := ZNPFrame{Cmd: CmdAFIncomingMsg}
	if !f.IsCmd(CmdAFIncomingMsg) {
		t.Error("should match CmdAFIncomingMsg")
	}
	if f.IsCmd(CmdSysPing) {
		t.Error("should not match CmdSysPing")
	}
}

func TestParseSysVersionRsp(t *testing.T) {
	data := []byte{0x02, 0x01, 0x03, 0x00, 0x01} // transport=2, product=1, v3.0.1
	info, err := ParseSysVersionRsp(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.MajorRel != 3 || info.MinorRel != 0 || info.MaintRel != 1 {
		t.Errorf("version mismatch: got %d.%d.%d", info.MajorRel, info.MinorRel, info.MaintRel)
	}
}

func TestZNPStatusString(t *testing.T) {
	cases := map[byte]string{
		ZStatusSuccess:           "success",
		ZStatusNwkInvalidRequest: "network not ready (coordinator not in operational state)",
		ZStatusNwkInvalidParam:   "network invalid parameter",
		ZStatusApsDuplicateEntry: "APS duplicate entry",
		0xAB:                     "status 0xab",
	}
	for code, want := range cases {
		got := ZNPStatusString(code)
		if got != want {
			t.Errorf("status 0x%02x: got %q, want %q", code, got, want)
		}
	}
}

func TestZNPDevStateName(t *testing.T) {
	cases := map[byte]string{
		ZNPDevStateHold:          "hold",
		ZNPDevStateInit:          "init",
		ZNPDevStateCoordStarting: "coord-starting",
		ZNPDevStateCoord:         "coord-ready",
		0xAA:                     "0xaa",
	}
	for state, want := range cases {
		if got := ZNPDevStateName(state); got != want {
			t.Errorf("state 0x%02x: got %q, want %q", state, got, want)
		}
	}
}

func TestZNPResetReasonName(t *testing.T) {
	cases := map[byte]string{
		ZNPResetReasonPowerUp:  "power-up",
		ZNPResetReasonWatchdog: "watchdog",
		ZNPResetReasonHardFlt:  "hard-fault",
		0x77:                   "0x77",
	}
	for r, want := range cases {
		if got := ZNPResetReasonName(r); got != want {
			t.Errorf("reason 0x%02x: got %q, want %q", r, got, want)
		}
	}
}

func TestParseSysResetInd(t *testing.T) {
	data := []byte{
		ZNPResetReasonWatchdog, // reason
		0x02,                   // transport rev
		0x01,                   // product
		0x02,                   // major
		0x07,                   // minor
		0x01,                   // hwrev
	}
	info, err := ParseSysResetInd(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Reason != ZNPResetReasonWatchdog || info.MinorRel != 7 {
		t.Errorf("bad parse: %+v", info)
	}
	if _, err := ParseSysResetInd([]byte{0x00}); err == nil {
		t.Error("expected error for short data")
	}
}

func TestBuildSysResetReq(t *testing.T) {
	f := BuildSysResetReq(ZNPResetTypeSoft)
	if f.Cmd != CmdSysResetReq {
		t.Errorf("wrong cmd: %v", f.Cmd)
	}
	if len(f.Data) != 1 || f.Data[0] != ZNPResetTypeSoft {
		t.Errorf("wrong data: %v", f.Data)
	}
}

func TestBuildZDOStartup_StartDelay(t *testing.T) {
	// zigbee-herdsman sends startdelay=100ms as uint16 LE. This prevents
	// stack-not-settled races on Z-Stack 3.0.x.
	f := BuildZDOStartup()
	if len(f.Data) != 2 || f.Data[0] != 0x64 || f.Data[1] != 0x00 {
		t.Errorf("expected startdelay=100ms LE (0x64 0x00), got %v", f.Data)
	}
}

func TestBuildMgmtPermitJoinReq_Format(t *testing.T) {
	// Cross-check against zigbee-herdsman: addrMode=0x0F (ADDR_BROADCAST),
	// dstAddr=0xFFFC (BroadcastAddress.DEFAULT), duration, tcSig=0x01.
	f := BuildMgmtPermitJoinReq(120)
	want := []byte{0x0F, 0xFC, 0xFF, 120, 0x01}
	if len(f.Data) != len(want) {
		t.Fatalf("len: got %d, want %d", len(f.Data), len(want))
	}
	for i, b := range want {
		if f.Data[i] != b {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x", i, f.Data[i], b)
		}
	}
}

func TestParseAFIncomingMsg(t *testing.T) {
	// Build a synthetic AF_INCOMING_MSG payload
	data := make([]byte, 22)
	data[0] = 0x00  // GroupID low
	data[1] = 0x00  // GroupID high
	data[2] = 0x06  // ClusterID low (on/off)
	data[3] = 0x00  // ClusterID high
	data[4] = 0x34  // SrcAddr low
	data[5] = 0x12  // SrcAddr high
	data[6] = 0x01  // SrcEP
	data[7] = 0x01  // DstEP
	data[8] = 0x00  // WasBcast
	data[9] = 0xFF  // LQI
	data[10] = 0x00 // SecUse
	data[11] = 0x00 // Timestamp (4 bytes)
	data[12] = 0x00
	data[13] = 0x00
	data[14] = 0x00
	data[15] = 0x01 // TransSeq
	data[16] = 0x05 // DataLen
	data[17] = 'h'
	data[18] = 'e'
	data[19] = 'l'
	data[20] = 'l'
	data[21] = 'o'

	msg, err := ParseAFIncomingMsg(data)
	if err != nil {
		t.Fatal(err)
	}
	if msg.SrcAddr != 0x1234 {
		t.Errorf("SrcAddr: got 0x%04x, want 0x1234", msg.SrcAddr)
	}
	if msg.ClusterID != 0x0006 {
		t.Errorf("ClusterID: got 0x%04x, want 0x0006", msg.ClusterID)
	}
	if msg.LQI != 0xFF {
		t.Errorf("LQI: got %d, want 255", msg.LQI)
	}
	if string(msg.Data) != "hello" {
		t.Errorf("data: got %q, want %q", msg.Data, "hello")
	}
}
