package routing

import (
	"bytes"
	"testing"
)

func TestSARSegment_SingleFrame(t *testing.T) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	var seq uint8
	segments := sarSegment(data, &seq)

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segments[0][0]&bleSARFlagMask != bleSARFlagSingle {
		t.Fatalf("expected single flag, got 0x%02x", segments[0][0])
	}
	if !bytes.Equal(segments[0][1:], data) {
		t.Fatal("payload mismatch")
	}
	if seq != 1 {
		t.Fatalf("expected seq=1, got %d", seq)
	}
}

func TestSARSegment_ExactMTU(t *testing.T) {
	data := make([]byte, bleSARMaxPayload)
	var seq uint8
	segments := sarSegment(data, &seq)

	if len(segments) != 1 {
		t.Fatalf("expected 1 segment for exact MTU, got %d", len(segments))
	}
	if segments[0][0]&bleSARFlagMask != bleSARFlagSingle {
		t.Fatal("expected single flag for exact MTU")
	}
}

func TestSARSegment_MultiFrame(t *testing.T) {
	data := make([]byte, 500) // Reticulum MTU
	for i := range data {
		data[i] = byte(i)
	}
	var seq uint8
	segments := sarSegment(data, &seq)

	// 500 / 243 = 2.058 -> 3 segments (243 + 243 + 14)
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}

	// Check flags.
	if segments[0][0]&bleSARFlagMask != bleSARFlagFirst {
		t.Fatalf("segment 0: expected first flag, got 0x%02x", segments[0][0])
	}
	if segments[1][0]&bleSARFlagMask != bleSARFlagCont {
		t.Fatalf("segment 1: expected continuation flag, got 0x%02x", segments[1][0])
	}
	if segments[2][0]&bleSARFlagMask != bleSARFlagLast {
		t.Fatalf("segment 2: expected last flag, got 0x%02x", segments[2][0])
	}

	// Verify sequence numbers increment.
	seq0 := segments[0][0] & bleSARSeqMask
	seq1 := segments[1][0] & bleSARSeqMask
	seq2 := segments[2][0] & bleSARSeqMask
	if seq1 != seq0+1 || seq2 != seq0+2 {
		t.Fatalf("sequence numbers not sequential: %d, %d, %d", seq0, seq1, seq2)
	}

	// Verify total payload matches original.
	var reassembled []byte
	for _, seg := range segments {
		reassembled = append(reassembled, seg[1:]...)
	}
	if !bytes.Equal(reassembled, data) {
		t.Fatal("reassembled payload mismatch")
	}
}

func TestSARSegment_SeqWraps(t *testing.T) {
	data := make([]byte, 10)
	seq := uint8(30) // near wrap point (mask is 0x1F = 31)
	segments := sarSegment(data, &seq)

	if len(segments) != 1 {
		t.Fatal("expected 1 segment")
	}
	got := segments[0][0] & bleSARSeqMask
	if got != 30 {
		t.Fatalf("expected seq=30, got %d", got)
	}
	if seq != 31 {
		t.Fatalf("expected seq to advance to 31, got %d", seq)
	}

	// One more should wrap to 0.
	sarSegment(data, &seq)
	if seq != 0 {
		t.Fatalf("expected seq to wrap to 0, got %d", seq)
	}
}

func TestSARReassemble_SingleFrame(t *testing.T) {
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	var seq uint8
	segments := sarSegment(data, &seq)

	buf := newSARBuffer()
	packet, complete := sarReassemble(&buf, segments[0])
	if !complete {
		t.Fatal("expected complete on single frame")
	}
	if !bytes.Equal(packet, data) {
		t.Fatalf("expected %x, got %x", data, packet)
	}
}

func TestSARReassemble_MultiFrame(t *testing.T) {
	data := make([]byte, 500)
	for i := range data {
		data[i] = byte(i)
	}
	var seq uint8
	segments := sarSegment(data, &seq)

	buf := newSARBuffer()

	// First segment: not complete.
	packet, complete := sarReassemble(&buf, segments[0])
	if complete || packet != nil {
		t.Fatal("first segment should not be complete")
	}

	// Continuation segment: not complete.
	packet, complete = sarReassemble(&buf, segments[1])
	if complete || packet != nil {
		t.Fatal("continuation segment should not be complete")
	}

	// Last segment: complete.
	packet, complete = sarReassemble(&buf, segments[2])
	if !complete {
		t.Fatal("last segment should complete reassembly")
	}
	if !bytes.Equal(packet, data) {
		t.Fatal("reassembled data mismatch")
	}
}

func TestSARReassemble_ContinuationWithoutFirst(t *testing.T) {
	// Continuation chunk received without a preceding First should be ignored.
	buf := newSARBuffer()
	chunk := []byte{bleSARFlagCont | 5, 0x01, 0x02}
	packet, complete := sarReassemble(&buf, chunk)
	if complete || packet != nil {
		t.Fatal("continuation without first should be ignored")
	}
}

func TestSARReassemble_LastWithoutFirst(t *testing.T) {
	buf := newSARBuffer()
	chunk := []byte{bleSARFlagLast | 5, 0x01, 0x02}
	packet, complete := sarReassemble(&buf, chunk)
	if complete || packet != nil {
		t.Fatal("last without first should be ignored")
	}
}

func TestSARReassemble_NewFirstResetsBuffer(t *testing.T) {
	// Receiving a new First while a previous reassembly is in progress
	// should reset the buffer and start a new packet.
	buf := newSARBuffer()

	// Start one packet.
	sarReassemble(&buf, []byte{bleSARFlagFirst | 0, 0xAA, 0xBB})

	// Start a new packet (interrupts the first).
	sarReassemble(&buf, []byte{bleSARFlagFirst | 1, 0xCC, 0xDD})

	// Complete the second packet.
	packet, complete := sarReassemble(&buf, []byte{bleSARFlagLast | 2, 0xEE, 0xFF})
	if !complete {
		t.Fatal("expected complete")
	}
	expected := []byte{0xCC, 0xDD, 0xEE, 0xFF}
	if !bytes.Equal(packet, expected) {
		t.Fatalf("expected %x, got %x", expected, packet)
	}
}

func TestSARReassemble_EmptyChunk(t *testing.T) {
	buf := newSARBuffer()
	packet, complete := sarReassemble(&buf, []byte{})
	if complete || packet != nil {
		t.Fatal("empty chunk should return nil")
	}
}

func TestSARRoundTrip_LargePacket(t *testing.T) {
	// Test a packet that requires many segments.
	data := make([]byte, 1500)
	for i := range data {
		data[i] = byte(i * 7)
	}
	var seq uint8
	segments := sarSegment(data, &seq)

	// 1500 / 243 = 6.17 -> 7 segments
	if len(segments) != 7 {
		t.Fatalf("expected 7 segments for 1500 bytes, got %d", len(segments))
	}

	buf := newSARBuffer()
	var result []byte
	var complete bool
	for _, seg := range segments {
		result, complete = sarReassemble(&buf, seg)
	}
	if !complete {
		t.Fatal("expected complete after all segments")
	}
	if !bytes.Equal(result, data) {
		t.Fatal("round-trip data mismatch")
	}
}

func TestBLEInterface_Defaults(t *testing.T) {
	b := NewBLEInterface(BLEInterfaceConfig{Name: "ble_0"}, nil)
	if b.config.AdapterID != "hci0" {
		t.Fatalf("expected default adapter hci0, got %s", b.config.AdapterID)
	}
	if b.config.DeviceName != "MeshSat-RNS" {
		t.Fatalf("expected default name MeshSat-RNS, got %s", b.config.DeviceName)
	}
	if b.IsOnline() {
		t.Fatal("new interface should be offline")
	}
}

func TestBLEInterface_SendOffline(t *testing.T) {
	b := NewBLEInterface(BLEInterfaceConfig{Name: "ble_0"}, nil)
	err := b.Send(t.Context(), []byte{0x01, 0x02})
	if err == nil {
		t.Fatal("expected error when sending on offline interface")
	}
}

func TestRegisterBLEInterface(t *testing.T) {
	called := false
	bleIface, ri := RegisterBLEInterface(BLEInterfaceConfig{
		Name:      "ble_0",
		AdapterID: "hci0",
	}, func(packet []byte) {
		called = true
	})

	if bleIface == nil || ri == nil {
		t.Fatal("RegisterBLEInterface returned nil")
	}
	if ri.ID() != "ble_0" {
		t.Fatalf("expected ID ble_0, got %s", ri.ID())
	}
	if ri.Type() != "ble" {
		t.Fatalf("expected type ble, got %s", ri.Type())
	}
	if ri.Cost() != 0 {
		t.Fatalf("expected cost 0, got %f", ri.Cost())
	}
	if ri.MTU() != 500 {
		t.Fatalf("expected MTU 500, got %d", ri.MTU())
	}
	if !ri.IsFloodable() {
		t.Fatal("BLE interface should be floodable (cost=0)")
	}
	_ = called
}
