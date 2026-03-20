package reticulum

import (
	"bytes"
	"testing"
)

func TestPathRequest_MarshalUnmarshal(t *testing.T) {
	req := &PathRequest{}
	for i := range req.DestHash {
		req.DestHash[i] = byte(i)
	}
	for i := range req.Tag {
		req.Tag[i] = byte(0xA0 + i)
	}

	data := MarshalPathRequest(req)
	if len(data) != TruncatedHashLen*2 {
		t.Fatalf("marshaled length = %d, want %d", len(data), TruncatedHashLen*2)
	}

	got, err := UnmarshalPathRequest(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DestHash != req.DestHash {
		t.Fatal("DestHash mismatch")
	}
	if got.Tag != req.Tag {
		t.Fatal("Tag mismatch")
	}
}

func TestPathRequest_UnmarshalTooShort(t *testing.T) {
	_, err := UnmarshalPathRequest(make([]byte, 10))
	if err == nil {
		t.Fatal("expected error for short data")
	}
}

func TestPathResponse_MarshalUnmarshal(t *testing.T) {
	resp := &PathResponse{
		Hops:          3,
		InterfaceType: "iridium",
		AnnounceData:  []byte("announce-payload-here"),
	}
	for i := range resp.DestHash {
		resp.DestHash[i] = byte(i + 0x10)
	}
	for i := range resp.Tag {
		resp.Tag[i] = byte(i + 0xB0)
	}

	data := MarshalPathResponse(resp)
	got, err := UnmarshalPathResponse(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.DestHash != resp.DestHash {
		t.Fatal("DestHash mismatch")
	}
	if got.Tag != resp.Tag {
		t.Fatal("Tag mismatch")
	}
	if got.Hops != 3 {
		t.Fatalf("Hops = %d, want 3", got.Hops)
	}
	if got.InterfaceType != "iridium" {
		t.Fatalf("InterfaceType = %q, want %q", got.InterfaceType, "iridium")
	}
	if !bytes.Equal(got.AnnounceData, resp.AnnounceData) {
		t.Fatal("AnnounceData mismatch")
	}
}

func TestPathResponse_NoAnnounceData(t *testing.T) {
	resp := &PathResponse{
		Hops:          0,
		InterfaceType: "local",
	}

	data := MarshalPathResponse(resp)
	got, err := UnmarshalPathResponse(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Hops != 0 {
		t.Fatalf("Hops = %d, want 0", got.Hops)
	}
	if got.InterfaceType != "local" {
		t.Fatalf("InterfaceType = %q, want %q", got.InterfaceType, "local")
	}
	if len(got.AnnounceData) != 0 {
		t.Fatalf("AnnounceData should be empty, got %d bytes", len(got.AnnounceData))
	}
}

func TestPathResponse_UnmarshalTooShort(t *testing.T) {
	_, err := UnmarshalPathResponse(make([]byte, 10))
	if err == nil {
		t.Fatal("expected error for short data")
	}
}

func TestBuildPathRequestPacket(t *testing.T) {
	var dest [TruncatedHashLen]byte
	req := &PathRequest{DestHash: dest}
	for i := range req.Tag {
		req.Tag[i] = byte(i)
	}

	packet := BuildPathRequestPacket(dest, req)
	if len(packet) < HeaderMinSize {
		t.Fatalf("packet too short: %d bytes", len(packet))
	}

	hdr, err := UnmarshalHeader(packet)
	if err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if hdr.PacketType != PacketData {
		t.Fatalf("PacketType = %d, want DATA", hdr.PacketType)
	}
	if hdr.Context != ContextRequest {
		t.Fatalf("Context = 0x%02x, want ContextRequest (0x%02x)", hdr.Context, ContextRequest)
	}
	if hdr.DestType != DestPlain {
		t.Fatalf("DestType = %d, want PLAIN", hdr.DestType)
	}

	// Verify we can parse the path request from the data
	_, err = UnmarshalPathRequest(hdr.Data)
	if err != nil {
		t.Fatalf("unmarshal path request from packet: %v", err)
	}
}

func TestBuildPathResponsePacket(t *testing.T) {
	var dest [TruncatedHashLen]byte
	dest[0] = 0xAA
	resp := &PathResponse{
		DestHash:      dest,
		Hops:          2,
		InterfaceType: "mesh",
		AnnounceData:  []byte("test"),
	}

	packet := BuildPathResponsePacket(dest, resp)
	hdr, err := UnmarshalHeader(packet)
	if err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if hdr.Context != ContextPathResponse {
		t.Fatalf("Context = 0x%02x, want ContextPathResponse (0x%02x)", hdr.Context, ContextPathResponse)
	}

	got, err := UnmarshalPathResponse(hdr.Data)
	if err != nil {
		t.Fatalf("unmarshal path response from packet: %v", err)
	}
	if got.Hops != 2 {
		t.Fatalf("Hops = %d, want 2", got.Hops)
	}
}
