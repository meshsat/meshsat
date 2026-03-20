package reticulum

import (
	"errors"
	"fmt"
)

// Path discovery errors.
var (
	ErrPathRequestTooShort  = errors.New("reticulum: path request too short")
	ErrPathResponseTooShort = errors.New("reticulum: path response too short")
)

// PathRequest is a flooding request to discover a route to a destination.
// Wire format: [dest_hash:16] [tag:16]
// The tag is a random nonce for deduplication (prevents re-flooding the same request).
type PathRequest struct {
	DestHash [TruncatedHashLen]byte // Destination we're looking for
	Tag      [TruncatedHashLen]byte // Random dedup tag
}

// MarshalPathRequest encodes a path request for transmission.
func MarshalPathRequest(req *PathRequest) []byte {
	buf := make([]byte, TruncatedHashLen*2)
	copy(buf[:TruncatedHashLen], req.DestHash[:])
	copy(buf[TruncatedHashLen:], req.Tag[:])
	return buf
}

// UnmarshalPathRequest decodes a path request from wire format.
func UnmarshalPathRequest(data []byte) (*PathRequest, error) {
	if len(data) < TruncatedHashLen*2 {
		return nil, fmt.Errorf("%w: need %d bytes, got %d", ErrPathRequestTooShort, TruncatedHashLen*2, len(data))
	}
	req := &PathRequest{}
	copy(req.DestHash[:], data[:TruncatedHashLen])
	copy(req.Tag[:], data[TruncatedHashLen:TruncatedHashLen*2])
	return req, nil
}

// PathResponse is sent by a node that knows a route to the requested destination.
// It carries enough announce data for the requester to learn the path.
// Wire format: [dest_hash:16] [tag:16] [hops:1] [interface_type_len:1] [interface_type:N] [announce_data:...]
type PathResponse struct {
	DestHash      [TruncatedHashLen]byte // Destination this response is for
	Tag           [TruncatedHashLen]byte // Matches the request tag
	Hops          byte                   // Total hop count to the destination
	InterfaceType string                 // Interface type the route was learned from
	AnnounceData  []byte                 // Original announce payload (for verification)
}

// MarshalPathResponse encodes a path response for transmission.
func MarshalPathResponse(resp *PathResponse) []byte {
	ifaceBytes := []byte(resp.InterfaceType)
	if len(ifaceBytes) > 255 {
		ifaceBytes = ifaceBytes[:255]
	}
	size := TruncatedHashLen*2 + 1 + 1 + len(ifaceBytes) + len(resp.AnnounceData)
	buf := make([]byte, 0, size)
	buf = append(buf, resp.DestHash[:]...)
	buf = append(buf, resp.Tag[:]...)
	buf = append(buf, resp.Hops)
	buf = append(buf, byte(len(ifaceBytes)))
	buf = append(buf, ifaceBytes...)
	buf = append(buf, resp.AnnounceData...)
	return buf
}

// UnmarshalPathResponse decodes a path response from wire format.
func UnmarshalPathResponse(data []byte) (*PathResponse, error) {
	minLen := TruncatedHashLen*2 + 1 + 1 // dest_hash + tag + hops + iface_len
	if len(data) < minLen {
		return nil, fmt.Errorf("%w: need at least %d bytes, got %d", ErrPathResponseTooShort, minLen, len(data))
	}
	resp := &PathResponse{}
	pos := 0
	copy(resp.DestHash[:], data[pos:pos+TruncatedHashLen])
	pos += TruncatedHashLen
	copy(resp.Tag[:], data[pos:pos+TruncatedHashLen])
	pos += TruncatedHashLen
	resp.Hops = data[pos]
	pos++
	ifaceLen := int(data[pos])
	pos++
	if pos+ifaceLen > len(data) {
		return nil, fmt.Errorf("%w: interface type truncated", ErrPathResponseTooShort)
	}
	resp.InterfaceType = string(data[pos : pos+ifaceLen])
	pos += ifaceLen
	if pos < len(data) {
		resp.AnnounceData = make([]byte, len(data)-pos)
		copy(resp.AnnounceData, data[pos:])
	}
	return resp, nil
}

// BuildPathRequestPacket creates a full Reticulum packet for a path request.
// Uses HeaderType1, PacketData, DestPlain, ContextRequest.
func BuildPathRequestPacket(destHash [TruncatedHashLen]byte, req *PathRequest) []byte {
	hdr := Header{
		HeaderType: HeaderType1,
		PacketType: PacketData,
		DestType:   DestPlain,
		Context:    ContextRequest,
		DestHash:   destHash,
		Data:       MarshalPathRequest(req),
	}
	return hdr.Marshal()
}

// BuildPathResponsePacket creates a full Reticulum packet for a path response.
// Uses HeaderType1, PacketData, DestPlain, ContextPathResponse.
func BuildPathResponsePacket(destHash [TruncatedHashLen]byte, resp *PathResponse) []byte {
	hdr := Header{
		HeaderType: HeaderType1,
		PacketType: PacketData,
		DestType:   DestPlain,
		Context:    ContextPathResponse,
		DestHash:   destHash,
		Data:       MarshalPathResponse(resp),
	}
	return hdr.Marshal()
}
