package routing

import (
	"bytes"
	"testing"
)

func TestConstants(t *testing.T) {
	// Verify header sizes match documented layout.
	if Header1Size != 19 {
		t.Errorf("Header1Size = %d, want 19", Header1Size)
	}
	if Header2Size != 35 {
		t.Errorf("Header2Size = %d, want 35", Header2Size)
	}
	if MTU != 500 {
		t.Errorf("MTU = %d, want 500", MTU)
	}
	if MDU != 465 {
		t.Errorf("MDU = %d, want 465", MDU)
	}
	if PathfinderM != 128 {
		t.Errorf("PathfinderM = %d, want 128", PathfinderM)
	}
}

func TestEncodeFlagsRoundtrip(t *testing.T) {
	tests := []struct {
		name       string
		headerType byte
		propType   byte
		destType   byte
		packetType byte
		wantFlags  byte
	}{
		{
			name:       "all zeros",
			headerType: HeaderType1, propType: PropBroadcast,
			destType: DestSingle, packetType: PacketTypeData,
			wantFlags: 0x00,
		},
		{
			name:       "type2 transport single announce",
			headerType: HeaderType2, propType: PropTransport,
			destType: DestSingle, packetType: PacketTypeAnnounce,
			wantFlags: 0x51, // 01_01_00_01
		},
		{
			name:       "type1 broadcast link linkrequest",
			headerType: HeaderType1, propType: PropBroadcast,
			destType: DestLink, packetType: PacketTypeLinkRequest,
			wantFlags: 0x0E, // 00_00_11_10
		},
		{
			name:       "type1 broadcast group proof",
			headerType: HeaderType1, propType: PropBroadcast,
			destType: DestGroup, packetType: PacketTypeProof,
			wantFlags: 0x07, // 00_00_01_11
		},
		{
			name:       "type2 broadcast plain data",
			headerType: HeaderType2, propType: PropBroadcast,
			destType: DestPlain, packetType: PacketTypeData,
			wantFlags: 0x48, // 01_00_10_00
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &PacketHeader{
				HeaderType: tt.headerType,
				PropType:   tt.propType,
				DestType:   tt.destType,
				PacketType: tt.packetType,
			}
			flags := h.EncodeFlags()
			if flags != tt.wantFlags {
				t.Errorf("EncodeFlags() = 0x%02X, want 0x%02X", flags, tt.wantFlags)
			}

			// Decode and verify roundtrip.
			var h2 PacketHeader
			h2.DecodeFlags(flags)
			if h2.HeaderType != tt.headerType {
				t.Errorf("HeaderType = %d, want %d", h2.HeaderType, tt.headerType)
			}
			if h2.PropType != tt.propType {
				t.Errorf("PropType = %d, want %d", h2.PropType, tt.propType)
			}
			if h2.DestType != tt.destType {
				t.Errorf("DestType = %d, want %d", h2.DestType, tt.destType)
			}
			if h2.PacketType != tt.packetType {
				t.Errorf("PacketType = %d, want %d", h2.PacketType, tt.packetType)
			}
		})
	}
}

func testDestHash() [TruncatedHashLen]byte {
	var h [TruncatedHashLen]byte
	for i := range h {
		h[i] = byte(i + 1)
	}
	return h
}

func testTransportID() [TruncatedHashLen]byte {
	var h [TruncatedHashLen]byte
	for i := range h {
		h[i] = byte(0xA0 + i)
	}
	return h
}

func TestHeaderMarshalUnmarshalType1(t *testing.T) {
	dest := testDestHash()
	h := NewHeader1(PropBroadcast, DestSingle, PacketTypeAnnounce, dest, CtxNone)
	h.Hops = 5

	data := h.Marshal()
	if len(data) != Header1Size {
		t.Fatalf("Marshal len = %d, want %d", len(data), Header1Size)
	}

	// Verify flags byte.
	wantFlags := byte(0x01) // 00_00_00_01
	if data[0] != wantFlags {
		t.Errorf("flags = 0x%02X, want 0x%02X", data[0], wantFlags)
	}
	if data[1] != 5 {
		t.Errorf("hops = %d, want 5", data[1])
	}

	// Unmarshal.
	h2, consumed, err := UnmarshalPacketHeader(data)
	if err != nil {
		t.Fatalf("UnmarshalPacketHeader: %v", err)
	}
	if consumed != Header1Size {
		t.Errorf("consumed = %d, want %d", consumed, Header1Size)
	}
	if h2.HeaderType != HeaderType1 {
		t.Errorf("HeaderType = %d, want %d", h2.HeaderType, HeaderType1)
	}
	if h2.PacketType != PacketTypeAnnounce {
		t.Errorf("PacketType = %d, want %d", h2.PacketType, PacketTypeAnnounce)
	}
	if h2.Hops != 5 {
		t.Errorf("Hops = %d, want 5", h2.Hops)
	}
	if h2.DestHash != dest {
		t.Errorf("DestHash mismatch")
	}
	if h2.Context != CtxNone {
		t.Errorf("Context = 0x%02X, want 0x%02X", h2.Context, CtxNone)
	}
}

func TestHeaderMarshalUnmarshalType2(t *testing.T) {
	dest := testDestHash()
	transport := testTransportID()
	h := NewHeader2(PropTransport, DestSingle, PacketTypeData, transport, dest, CtxRequest)
	h.Hops = 3

	data := h.Marshal()
	if len(data) != Header2Size {
		t.Fatalf("Marshal len = %d, want %d", len(data), Header2Size)
	}

	h2, consumed, err := UnmarshalPacketHeader(data)
	if err != nil {
		t.Fatalf("UnmarshalPacketHeader: %v", err)
	}
	if consumed != Header2Size {
		t.Errorf("consumed = %d, want %d", consumed, Header2Size)
	}
	if h2.HeaderType != HeaderType2 {
		t.Errorf("HeaderType = %d, want Type2", h2.HeaderType)
	}
	if h2.PropType != PropTransport {
		t.Errorf("PropType = %d, want PropTransport", h2.PropType)
	}
	if h2.Hops != 3 {
		t.Errorf("Hops = %d, want 3", h2.Hops)
	}
	if h2.TransportID != transport {
		t.Errorf("TransportID mismatch")
	}
	if h2.DestHash != dest {
		t.Errorf("DestHash mismatch")
	}
	if h2.Context != CtxRequest {
		t.Errorf("Context = 0x%02X, want 0x%02X", h2.Context, CtxRequest)
	}
}

func TestPacketMarshalUnmarshalRoundtrip(t *testing.T) {
	dest := testDestHash()
	payload := []byte("hello reticulum")

	pkt := &Packet{
		Header:  *NewHeader1(PropBroadcast, DestSingle, PacketTypeData, dest, CtxNone),
		Payload: payload,
	}

	data, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) != Header1Size+len(payload) {
		t.Errorf("total len = %d, want %d", len(data), Header1Size+len(payload))
	}

	pkt2, err := UnmarshalPacket(data)
	if err != nil {
		t.Fatalf("UnmarshalPacket: %v", err)
	}
	if pkt2.Header.PacketType != PacketTypeData {
		t.Errorf("PacketType = %d, want Data", pkt2.Header.PacketType)
	}
	if pkt2.Header.DestHash != dest {
		t.Errorf("DestHash mismatch")
	}
	if !bytes.Equal(pkt2.Payload, payload) {
		t.Errorf("Payload = %q, want %q", pkt2.Payload, payload)
	}
}

func TestPacketMarshalExceedsMTU(t *testing.T) {
	dest := testDestHash()
	oversized := make([]byte, MTU) // header + MTU bytes > MTU

	pkt := &Packet{
		Header:  *NewHeader1(PropBroadcast, DestSingle, PacketTypeData, dest, CtxNone),
		Payload: oversized,
	}

	_, err := pkt.Marshal()
	if err == nil {
		t.Error("expected MTU error, got nil")
	}
}

func TestPacketEmptyPayload(t *testing.T) {
	dest := testDestHash()
	pkt := &Packet{
		Header: *NewHeader1(PropBroadcast, DestSingle, PacketTypeProof, dest, CtxLinkProof),
	}

	data, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	pkt2, err := UnmarshalPacket(data)
	if err != nil {
		t.Fatalf("UnmarshalPacket: %v", err)
	}
	if len(pkt2.Payload) != 0 {
		t.Errorf("Payload len = %d, want 0", len(pkt2.Payload))
	}
	if pkt2.Header.Context != CtxLinkProof {
		t.Errorf("Context = 0x%02X, want 0x%02X", pkt2.Header.Context, CtxLinkProof)
	}
}

func TestUnmarshalHeaderTooShort(t *testing.T) {
	// Empty.
	_, _, err := UnmarshalPacketHeader(nil)
	if err == nil {
		t.Error("expected error for nil data")
	}

	// 1 byte.
	_, _, err = UnmarshalPacketHeader([]byte{0x00})
	if err == nil {
		t.Error("expected error for 1-byte data")
	}

	// Type2 header but only 19 bytes (needs 35).
	buf := make([]byte, Header1Size)
	buf[0] = HeaderType2 << 6 // set header type 2
	_, _, err = UnmarshalPacketHeader(buf)
	if err == nil {
		t.Error("expected error for truncated Type2 header")
	}
}

func TestIncrementHops(t *testing.T) {
	h := &PacketHeader{Hops: 0}

	// Should succeed up to PathfinderM.
	for i := 0; i < PathfinderM; i++ {
		if !h.IncrementHops() {
			t.Fatalf("IncrementHops failed at hop %d", i)
		}
	}
	if h.Hops != PathfinderM {
		t.Errorf("Hops = %d, want %d", h.Hops, PathfinderM)
	}

	// Next increment should fail.
	if h.IncrementHops() {
		t.Error("IncrementHops should fail at max hops")
	}
}

func TestHeaderSize(t *testing.T) {
	h1 := &PacketHeader{HeaderType: HeaderType1}
	if h1.Size() != Header1Size {
		t.Errorf("Type1 Size = %d, want %d", h1.Size(), Header1Size)
	}

	h2 := &PacketHeader{HeaderType: HeaderType2}
	if h2.Size() != Header2Size {
		t.Errorf("Type2 Size = %d, want %d", h2.Size(), Header2Size)
	}
}

func TestMaxPayloadSize(t *testing.T) {
	h1 := &PacketHeader{HeaderType: HeaderType1}
	if h1.MaxPayloadSize() != MTU-Header1Size {
		t.Errorf("Type1 MaxPayloadSize = %d, want %d", h1.MaxPayloadSize(), MTU-Header1Size)
	}

	h2 := &PacketHeader{HeaderType: HeaderType2}
	if h2.MaxPayloadSize() != MTU-Header2Size {
		t.Errorf("Type2 MaxPayloadSize = %d, want %d", h2.MaxPayloadSize(), MTU-Header2Size)
	}
}

func TestTypePredicates(t *testing.T) {
	tests := []struct {
		name        string
		packetType  byte
		headerType  byte
		isAnnounce  bool
		isLink      bool
		isProof     bool
		isTransport bool
	}{
		{"data_type1", PacketTypeData, HeaderType1, false, false, false, false},
		{"announce_type1", PacketTypeAnnounce, HeaderType1, true, false, false, false},
		{"linkreq_type2", PacketTypeLinkRequest, HeaderType2, false, true, false, true},
		{"proof_type1", PacketTypeProof, HeaderType1, false, false, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &PacketHeader{PacketType: tt.packetType, HeaderType: tt.headerType}
			if h.IsAnnounce() != tt.isAnnounce {
				t.Errorf("IsAnnounce = %v", h.IsAnnounce())
			}
			if h.IsLinkRequest() != tt.isLink {
				t.Errorf("IsLinkRequest = %v", h.IsLinkRequest())
			}
			if h.IsProof() != tt.isProof {
				t.Errorf("IsProof = %v", h.IsProof())
			}
			if h.IsTransport() != tt.isTransport {
				t.Errorf("IsTransport = %v", h.IsTransport())
			}
		})
	}
}

func TestWireUtilities(t *testing.T) {
	buf := make([]byte, 2)
	PutUint16BE(buf, 0x1234)
	if Uint16BE(buf) != 0x1234 {
		t.Errorf("Uint16BE roundtrip failed: got 0x%04X", Uint16BE(buf))
	}
}

func TestType2PacketRoundtrip(t *testing.T) {
	dest := testDestHash()
	transport := testTransportID()
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	pkt := &Packet{
		Header:  *NewHeader2(PropTransport, DestLink, PacketTypeData, transport, dest, CtxKeepalive),
		Payload: payload,
	}

	data, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) != Header2Size+len(payload) {
		t.Errorf("total len = %d, want %d", len(data), Header2Size+len(payload))
	}

	pkt2, err := UnmarshalPacket(data)
	if err != nil {
		t.Fatalf("UnmarshalPacket: %v", err)
	}
	if !pkt2.Header.IsTransport() {
		t.Error("expected IsTransport = true")
	}
	if pkt2.Header.TransportID != transport {
		t.Error("TransportID mismatch")
	}
	if pkt2.Header.DestHash != dest {
		t.Error("DestHash mismatch")
	}
	if pkt2.Header.Context != CtxKeepalive {
		t.Errorf("Context = 0x%02X, want 0x%02X", pkt2.Header.Context, CtxKeepalive)
	}
	if !bytes.Equal(pkt2.Payload, payload) {
		t.Errorf("Payload mismatch")
	}
}
