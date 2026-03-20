package reticulum

import (
	"testing"
)

func TestPackFlags_Roundtrip(t *testing.T) {
	tests := []struct {
		name          string
		headerType    byte
		contextFlag   byte
		transportType byte
		destType      byte
		packetType    byte
	}{
		{"data broadcast", HeaderType1, 0, TransportBroadcast, DestSingle, PacketData},
		{"announce", HeaderType1, 0, TransportBroadcast, DestSingle, PacketAnnounce},
		{"announce with ratchet", HeaderType1, 1, TransportBroadcast, DestSingle, PacketAnnounce},
		{"link request", HeaderType1, 0, TransportBroadcast, DestSingle, PacketLinkRequest},
		{"proof", HeaderType1, 0, TransportBroadcast, DestLink, PacketProof},
		{"transport data", HeaderType2, 0, TransportTransport, DestSingle, PacketData},
		{"transport announce", HeaderType2, 1, TransportTransport, DestGroup, PacketAnnounce},
		{"all bits set", HeaderType2, 1, TransportTransport, DestLink, PacketProof},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Header{
				HeaderType:    tt.headerType,
				ContextFlag:   tt.contextFlag,
				TransportType: tt.transportType,
				DestType:      tt.destType,
				PacketType:    tt.packetType,
			}

			flags := h.PackFlags()
			h2 := &Header{}
			h2.UnpackFlags(flags)

			if h2.HeaderType != tt.headerType {
				t.Errorf("HeaderType: got %d, want %d", h2.HeaderType, tt.headerType)
			}
			if h2.ContextFlag != tt.contextFlag {
				t.Errorf("ContextFlag: got %d, want %d", h2.ContextFlag, tt.contextFlag)
			}
			if h2.TransportType != tt.transportType {
				t.Errorf("TransportType: got %d, want %d", h2.TransportType, tt.transportType)
			}
			if h2.DestType != tt.destType {
				t.Errorf("DestType: got %d, want %d", h2.DestType, tt.destType)
			}
			if h2.PacketType != tt.packetType {
				t.Errorf("PacketType: got %d, want %d", h2.PacketType, tt.packetType)
			}
		})
	}
}

func TestFlagsBitLayout(t *testing.T) {
	// Verify exact bit positions match the Reticulum spec:
	// [7:IFAC][6:HeaderType][5:ContextFlag][4:TransportType][3-2:DestType][1-0:PacketType]
	h := &Header{
		HeaderType:    HeaderType2,        // bit 6 = 1 → 0x40
		ContextFlag:   1,                  // bit 5 = 1 → 0x20
		TransportType: TransportTransport, // bit 4 = 1 → 0x10
		DestType:      DestLink,           // bits 3-2 = 11 → 0x0C
		PacketType:    PacketProof,        // bits 1-0 = 11 → 0x03
	}
	flags := h.PackFlags()
	expected := byte(0x40 | 0x20 | 0x10 | 0x0C | 0x03) // 0x7F
	if flags != expected {
		t.Errorf("flags: got 0x%02X, want 0x%02X", flags, expected)
	}
}

func TestHeaderType1_MarshalRoundtrip(t *testing.T) {
	h := &Header{
		HeaderType:    HeaderType1,
		TransportType: TransportBroadcast,
		DestType:      DestSingle,
		PacketType:    PacketAnnounce,
		Hops:          5,
		Context:       ContextNone,
		Data:          []byte("hello"),
	}
	copy(h.DestHash[:], []byte("0123456789abcdef"))

	raw := h.Marshal()
	if len(raw) != HeaderMinSize+5 {
		t.Fatalf("expected %d bytes, got %d", HeaderMinSize+5, len(raw))
	}

	h2, err := UnmarshalHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h2.HeaderType != HeaderType1 {
		t.Errorf("HeaderType: got %d, want %d", h2.HeaderType, HeaderType1)
	}
	if h2.Hops != 5 {
		t.Errorf("Hops: got %d, want 5", h2.Hops)
	}
	if h2.DestHash != h.DestHash {
		t.Error("DestHash mismatch")
	}
	if string(h2.Data) != "hello" {
		t.Errorf("Data: got %q, want %q", h2.Data, "hello")
	}
}

func TestHeaderType2_MarshalRoundtrip(t *testing.T) {
	h := &Header{
		HeaderType:    HeaderType2,
		TransportType: TransportTransport,
		DestType:      DestSingle,
		PacketType:    PacketData,
		Hops:          3,
		Context:       ContextNone,
		Data:          []byte("payload"),
	}
	copy(h.TransportID[:], []byte("transport_id_xxx"))
	copy(h.DestHash[:], []byte("destination_hash"))

	raw := h.Marshal()
	if len(raw) != HeaderMaxSize+7 {
		t.Fatalf("expected %d bytes, got %d", HeaderMaxSize+7, len(raw))
	}

	h2, err := UnmarshalHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h2.HeaderType != HeaderType2 {
		t.Errorf("HeaderType: got %d, want %d", h2.HeaderType, HeaderType2)
	}
	if h2.TransportID != h.TransportID {
		t.Error("TransportID mismatch")
	}
	if h2.DestHash != h.DestHash {
		t.Error("DestHash mismatch")
	}
	if string(h2.Data) != "payload" {
		t.Errorf("Data: got %q, want %q", h2.Data, "payload")
	}
}

func TestUnmarshalHeader_TooShort(t *testing.T) {
	_, err := UnmarshalHeader([]byte{0x01})
	if err == nil {
		t.Error("expected error for short packet")
	}
}

func TestUnmarshalHeader_Type2TooShort(t *testing.T) {
	// Create a valid type1-length packet but with type2 flag
	h := &Header{
		HeaderType: HeaderType2,
		PacketType: PacketData,
	}
	// Only write enough for type1 header
	raw := make([]byte, HeaderMinSize)
	raw[0] = h.PackFlags()

	_, err := UnmarshalHeader(raw)
	if err == nil {
		t.Error("expected error for type2 header with insufficient data")
	}
}

func TestIncrementHop(t *testing.T) {
	h := &Header{Hops: 0}
	for i := range PathfinderM {
		if !h.IncrementHop() {
			t.Fatalf("IncrementHop failed at hop %d", i)
		}
	}
	if h.IncrementHop() {
		t.Error("IncrementHop should fail at max hops")
	}
	if h.Hops != PathfinderM {
		t.Errorf("Hops: got %d, want %d", h.Hops, PathfinderM)
	}
}

func TestHeaderSize(t *testing.T) {
	h1 := &Header{HeaderType: HeaderType1}
	if h1.HeaderSize() != HeaderMinSize {
		t.Errorf("Type1 size: got %d, want %d", h1.HeaderSize(), HeaderMinSize)
	}

	h2 := &Header{HeaderType: HeaderType2}
	if h2.HeaderSize() != HeaderMaxSize {
		t.Errorf("Type2 size: got %d, want %d", h2.HeaderSize(), HeaderMaxSize)
	}
}

func TestPacketTypeString(t *testing.T) {
	if s := PacketTypeString(PacketData); s != "DATA" {
		t.Errorf("got %q", s)
	}
	if s := PacketTypeString(PacketAnnounce); s != "ANNOUNCE" {
		t.Errorf("got %q", s)
	}
	if s := PacketTypeString(PacketLinkRequest); s != "LINKREQUEST" {
		t.Errorf("got %q", s)
	}
	if s := PacketTypeString(PacketProof); s != "PROOF" {
		t.Errorf("got %q", s)
	}
	if s := PacketTypeString(0xFF); s == "" {
		t.Error("unknown type should return non-empty string")
	}
}
