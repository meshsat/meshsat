package reticulum

import (
	"bytes"
	"testing"
)

func TestHDLCEscape_NoSpecialBytes(t *testing.T) {
	input := []byte{0x01, 0x02, 0x03, 0x04}
	escaped := HDLCEscape(input)
	if !bytes.Equal(escaped, input) {
		t.Fatalf("expected no change, got %x", escaped)
	}
}

func TestHDLCEscape_FlagByte(t *testing.T) {
	input := []byte{0x00, HDLCFlag, 0xFF}
	escaped := HDLCEscape(input)
	expected := []byte{0x00, HDLCEsc, HDLCFlag ^ HDLCEscMask, 0xFF}
	if !bytes.Equal(escaped, expected) {
		t.Fatalf("got %x, want %x", escaped, expected)
	}
}

func TestHDLCEscape_EscByte(t *testing.T) {
	input := []byte{HDLCEsc}
	escaped := HDLCEscape(input)
	expected := []byte{HDLCEsc, HDLCEsc ^ HDLCEscMask}
	if !bytes.Equal(escaped, expected) {
		t.Fatalf("got %x, want %x", escaped, expected)
	}
}

func TestHDLCEscape_BothSpecialBytes(t *testing.T) {
	input := []byte{HDLCFlag, HDLCEsc, HDLCFlag}
	escaped := HDLCEscape(input)
	expected := []byte{
		HDLCEsc, HDLCFlag ^ HDLCEscMask,
		HDLCEsc, HDLCEsc ^ HDLCEscMask,
		HDLCEsc, HDLCFlag ^ HDLCEscMask,
	}
	if !bytes.Equal(escaped, expected) {
		t.Fatalf("got %x, want %x", escaped, expected)
	}
}

func TestHDLCUnescape_Roundtrip(t *testing.T) {
	inputs := [][]byte{
		{0x01, 0x02, 0x03},
		{HDLCFlag, HDLCEsc, 0x00, 0xFF},
		{HDLCEsc, HDLCEsc, HDLCFlag, HDLCFlag},
		bytes.Repeat([]byte{HDLCFlag}, 10),
		{}, // empty
	}

	for i, input := range inputs {
		escaped := HDLCEscape(input)
		unescaped := HDLCUnescape(escaped)
		if !bytes.Equal(unescaped, input) {
			t.Fatalf("case %d: roundtrip failed: input %x → escaped %x → unescaped %x", i, input, escaped, unescaped)
		}
	}
}

func TestHDLCFrame(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	frame := HDLCFrame(data)

	if frame[0] != HDLCFlag {
		t.Fatalf("first byte = 0x%02x, want FLAG (0x%02x)", frame[0], HDLCFlag)
	}
	if frame[len(frame)-1] != HDLCFlag {
		t.Fatalf("last byte = 0x%02x, want FLAG (0x%02x)", frame[len(frame)-1], HDLCFlag)
	}

	// Inner content should be escaped data (no special bytes in this case)
	inner := frame[1 : len(frame)-1]
	if !bytes.Equal(inner, data) {
		t.Fatalf("inner = %x, want %x", inner, data)
	}
}

func TestHDLCFrame_WithSpecialBytes(t *testing.T) {
	data := []byte{HDLCFlag, 0x01, HDLCEsc}
	frame := HDLCFrame(data)

	// Should not contain bare FLAG or ESC bytes inside
	inner := frame[1 : len(frame)-1]
	for i, b := range inner {
		if b == HDLCFlag {
			t.Fatalf("bare FLAG at inner position %d", i)
		}
	}
}

func TestHDLCFrameReader_SingleFrame(t *testing.T) {
	reader := NewHDLCFrameReader()

	// Build a valid Reticulum packet (at least HeaderMinSize bytes)
	payload := make([]byte, HeaderMinSize+5)
	for i := range payload {
		payload[i] = byte(i)
	}

	frame := HDLCFrame(payload)
	frames := reader.Feed(frame)

	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if !bytes.Equal(frames[0], payload) {
		t.Fatalf("frame content mismatch")
	}
}

func TestHDLCFrameReader_MultipleFrames(t *testing.T) {
	reader := NewHDLCFrameReader()

	p1 := make([]byte, HeaderMinSize)
	p1[0] = 0xAA
	p2 := make([]byte, HeaderMinSize)
	p2[0] = 0xBB

	// Concatenate two frames
	data := append(HDLCFrame(p1), HDLCFrame(p2)...)
	frames := reader.Feed(data)

	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}
	if frames[0][0] != 0xAA {
		t.Fatalf("frame 0 first byte = 0x%02x, want 0xAA", frames[0][0])
	}
	if frames[1][0] != 0xBB {
		t.Fatalf("frame 1 first byte = 0x%02x, want 0xBB", frames[1][0])
	}
}

func TestHDLCFrameReader_PartialFrame(t *testing.T) {
	reader := NewHDLCFrameReader()

	payload := make([]byte, HeaderMinSize+10)
	frame := HDLCFrame(payload)

	// Feed first half
	mid := len(frame) / 2
	frames := reader.Feed(frame[:mid])
	if len(frames) != 0 {
		t.Fatalf("expected 0 frames from partial, got %d", len(frames))
	}

	// Feed second half
	frames = reader.Feed(frame[mid:])
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame after completion, got %d", len(frames))
	}
	if !bytes.Equal(frames[0], payload) {
		t.Fatal("payload mismatch after reassembly")
	}
}

func TestHDLCFrameReader_TooShort(t *testing.T) {
	reader := NewHDLCFrameReader()

	// Frame with data shorter than HeaderMinSize should be discarded
	short := []byte{0x01, 0x02}
	frame := HDLCFrame(short)
	frames := reader.Feed(frame)

	if len(frames) != 0 {
		t.Fatalf("expected 0 frames for short payload, got %d", len(frames))
	}
}

func TestHDLCFrameReader_GarbageBeforeFrame(t *testing.T) {
	reader := NewHDLCFrameReader()

	payload := make([]byte, HeaderMinSize)
	payload[0] = 0xCC
	frame := HDLCFrame(payload)

	// Prepend garbage
	garbage := []byte{0xFF, 0xFE, 0xFD, 0xFC}
	data := append(garbage, frame...)
	frames := reader.Feed(data)

	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if frames[0][0] != 0xCC {
		t.Fatalf("frame first byte = 0x%02x, want 0xCC", frames[0][0])
	}
}

func TestHDLCFrameReader_SpecialBytesInPayload(t *testing.T) {
	reader := NewHDLCFrameReader()

	// Payload containing FLAG and ESC bytes
	payload := make([]byte, HeaderMinSize+2)
	payload[0] = HDLCFlag
	payload[1] = HDLCEsc
	payload[2] = 0x42

	frame := HDLCFrame(payload)
	frames := reader.Feed(frame)

	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if !bytes.Equal(frames[0], payload) {
		t.Fatalf("payload mismatch: got %x, want %x", frames[0], payload)
	}
}
