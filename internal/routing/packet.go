package routing

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Reticulum wire protocol constants.
// Reference: https://reticulum.network/manual/understanding.html
// Source: https://github.com/markqvist/Reticulum (MIT license)
const (
	// MTU is the maximum transmission unit for Reticulum packets (bytes).
	MTU = 500
	// MDU is the maximum data unit (payload) after the largest header (HEADER_2).
	MDU = MTU - Header2Size // 465
	// EncryptedMDU is the max payload after encryption overhead (IV+HMAC+padding).
	EncryptedMDU = 383
	// TruncatedHashLen is the truncated hash length in bytes (128 bits).
	TruncatedHashLen = 16
	// FullHashLen is the full SHA-256 hash length in bytes.
	FullHashLen = 32
	// KeySize is the combined public key size: X25519(32) + Ed25519(32).
	KeySize = 64
	// SigLen is the Ed25519 signature length in bytes.
	SigLen = 64
	// NameHashLen is the truncated name hash length in bytes (80 bits).
	NameHashLen = 10
	// RandomHashLen is the random hash length in bytes (80 bits).
	RandomHashLen = 10
	// PathfinderM is the maximum hop count for path discovery.
	PathfinderM = 128

	// Header sizes.
	// Header1Size: flags(1) + hops(1) + dest_hash(16) + context(1) = 19.
	Header1Size = 1 + 1 + TruncatedHashLen + 1
	// Header2Size: flags(1) + hops(1) + transport_id(16) + dest_hash(16) + context(1) = 35.
	Header2Size = 1 + 1 + TruncatedHashLen + TruncatedHashLen + 1
)

// Header types (2 bits, bits 7-6 of flags byte).
const (
	HeaderType1 byte = 0x00 // Normal header: flags + hops + dest_hash + context
	HeaderType2 byte = 0x01 // Transport header: flags + hops + transport_id + dest_hash + context
)

// Packet types (2 bits, bits 1-0 of flags byte).
const (
	PacketTypeData        byte = 0x00 // Data packet
	PacketTypeAnnounce    byte = 0x01 // Announce
	PacketTypeLinkRequest byte = 0x02 // Link request
	PacketTypeProof       byte = 0x03 // Proof (link proof or delivery proof)
)

// Propagation types (2 bits, bits 5-4 of flags byte).
const (
	PropBroadcast byte = 0x00 // Flood to all reachable
	PropTransport byte = 0x01 // Directed via transport node
)

// Destination types (2 bits, bits 3-2 of flags byte).
const (
	DestSingle byte = 0x00 // Single destination (one identity)
	DestGroup  byte = 0x01 // Group destination (shared key)
	DestPlain  byte = 0x02 // Plain/unencrypted destination
	DestLink   byte = 0x03 // Link destination (established link)
)

// Context byte values (carried as the last byte of the header).
const (
	CtxNone         byte = 0x00
	CtxResource     byte = 0x01
	CtxResourceAdv  byte = 0x02
	CtxResourceReq  byte = 0x03
	CtxResourceHMU  byte = 0x04
	CtxResourcePRF  byte = 0x05
	CtxResourceICL  byte = 0x06
	CtxResourceRCL  byte = 0x07
	CtxCacheRequest byte = 0x08
	CtxRequest      byte = 0x09
	CtxResponse     byte = 0x0A
	CtxPathResponse byte = 0x0B
	CtxCommand      byte = 0x0C
	CtxCommandStatus byte = 0x0D
	CtxKeepalive    byte = 0xFA
	CtxLinkIdentify byte = 0xFB
	CtxLinkClose    byte = 0xFC
	CtxLinkProof    byte = 0xFD
	CtxLinkRTT      byte = 0xFE
	CtxLinkRTTProof byte = 0xFF
)

// PacketHeader represents a decoded Reticulum packet header.
type PacketHeader struct {
	HeaderType  byte // 0 = HEADER_1, 1 = HEADER_2
	PropType    byte // 0 = BROADCAST, 1 = TRANSPORT
	DestType    byte // 0 = SINGLE, 1 = GROUP, 2 = PLAIN, 3 = LINK
	PacketType  byte // 0 = DATA, 1 = ANNOUNCE, 2 = LINKREQUEST, 3 = PROOF
	Hops        byte
	TransportID [TruncatedHashLen]byte // only for HEADER_2
	DestHash    [TruncatedHashLen]byte
	Context     byte
}

// EncodeFlags packs the header fields into the flags byte.
//
//	Bit layout: [header_type:2][prop_type:2][dest_type:2][packet_type:2]
//	Bits 7-6: header_type
//	Bits 5-4: propagation_type
//	Bits 3-2: destination_type
//	Bits 1-0: packet_type
func (h *PacketHeader) EncodeFlags() byte {
	return (h.HeaderType << 6) | (h.PropType << 4) | (h.DestType << 2) | h.PacketType
}

// DecodeFlags unpacks the flags byte into header fields.
func (h *PacketHeader) DecodeFlags(flags byte) {
	h.HeaderType = (flags >> 6) & 0x03
	h.PropType = (flags >> 4) & 0x03
	h.DestType = (flags >> 2) & 0x03
	h.PacketType = flags & 0x03
}

// Size returns the total header size in bytes.
func (h *PacketHeader) Size() int {
	if h.HeaderType == HeaderType2 {
		return Header2Size
	}
	return Header1Size
}

// Marshal serializes the packet header to wire format.
func (h *PacketHeader) Marshal() []byte {
	buf := make([]byte, h.Size())
	buf[0] = h.EncodeFlags()
	buf[1] = h.Hops
	pos := 2

	if h.HeaderType == HeaderType2 {
		copy(buf[pos:pos+TruncatedHashLen], h.TransportID[:])
		pos += TruncatedHashLen
	}

	copy(buf[pos:pos+TruncatedHashLen], h.DestHash[:])
	pos += TruncatedHashLen

	buf[pos] = h.Context
	return buf
}

// UnmarshalPacketHeader parses a Reticulum packet header from wire data.
// Returns the header and the number of bytes consumed.
func UnmarshalPacketHeader(data []byte) (*PacketHeader, int, error) {
	if len(data) < 2 {
		return nil, 0, errors.New("packet too short for header")
	}

	h := &PacketHeader{}
	h.DecodeFlags(data[0])
	h.Hops = data[1]

	expectedSize := Header1Size
	if h.HeaderType == HeaderType2 {
		expectedSize = Header2Size
	}
	if len(data) < expectedSize {
		return nil, 0, fmt.Errorf("packet too short: need %d bytes, have %d", expectedSize, len(data))
	}

	pos := 2
	if h.HeaderType == HeaderType2 {
		copy(h.TransportID[:], data[pos:pos+TruncatedHashLen])
		pos += TruncatedHashLen
	}

	copy(h.DestHash[:], data[pos:pos+TruncatedHashLen])
	pos += TruncatedHashLen

	h.Context = data[pos]
	pos++

	return h, pos, nil
}

// Packet represents a full Reticulum packet: header + payload.
type Packet struct {
	Header  PacketHeader
	Payload []byte
}

// Marshal serializes a full packet (header + payload) to wire format.
func (p *Packet) Marshal() ([]byte, error) {
	hdr := p.Header.Marshal()
	total := len(hdr) + len(p.Payload)
	if total > MTU {
		return nil, fmt.Errorf("packet exceeds MTU: %d > %d", total, MTU)
	}
	buf := make([]byte, total)
	copy(buf, hdr)
	copy(buf[len(hdr):], p.Payload)
	return buf, nil
}

// UnmarshalPacket parses a full Reticulum packet from wire data.
func UnmarshalPacket(data []byte) (*Packet, error) {
	h, consumed, err := UnmarshalPacketHeader(data)
	if err != nil {
		return nil, err
	}
	p := &Packet{
		Header:  *h,
		Payload: make([]byte, len(data)-consumed),
	}
	copy(p.Payload, data[consumed:])
	return p, nil
}

// NewHeader1 creates a HEADER_1 packet header with the given fields.
func NewHeader1(propType, destType, packetType byte, destHash [TruncatedHashLen]byte, context byte) *PacketHeader {
	return &PacketHeader{
		HeaderType: HeaderType1,
		PropType:   propType,
		DestType:   destType,
		PacketType: packetType,
		Hops:       0,
		DestHash:   destHash,
		Context:    context,
	}
}

// NewHeader2 creates a HEADER_2 (transport) packet header.
func NewHeader2(propType, destType, packetType byte, transportID, destHash [TruncatedHashLen]byte, context byte) *PacketHeader {
	return &PacketHeader{
		HeaderType:  HeaderType2,
		PropType:    propType,
		DestType:    destType,
		PacketType:  packetType,
		Hops:        0,
		TransportID: transportID,
		DestHash:    destHash,
		Context:     context,
	}
}

// IncrementHops increments the hop count. Returns false if max hops exceeded.
func (h *PacketHeader) IncrementHops() bool {
	if int(h.Hops) >= PathfinderM {
		return false
	}
	h.Hops++
	return true
}

// IsAnnounce returns true if this is an announce packet.
func (h *PacketHeader) IsAnnounce() bool {
	return h.PacketType == PacketTypeAnnounce
}

// IsLinkRequest returns true if this is a link request packet.
func (h *PacketHeader) IsLinkRequest() bool {
	return h.PacketType == PacketTypeLinkRequest
}

// IsProof returns true if this is a proof packet.
func (h *PacketHeader) IsProof() bool {
	return h.PacketType == PacketTypeProof
}

// IsTransport returns true if this is a HEADER_2 (transport-relayed) packet.
func (h *PacketHeader) IsTransport() bool {
	return h.HeaderType == HeaderType2
}

// MaxPayloadSize returns the maximum payload size for this header type.
func (h *PacketHeader) MaxPayloadSize() int {
	return MTU - h.Size()
}

// --- Helper: ifac_flag encoding for interface authentication (future) ---

// IFACFlag extracts the IFAC flag from the flags byte (bit 7 of propagation).
// In Reticulum, the high bit of propagation_type field can indicate IFAC.
// This is reserved for future use.
func IFACFlag(flags byte) bool {
	return (flags>>5)&0x01 != 0
}

// --- Wire format utilities ---

// PutUint16BE writes a uint16 in big-endian to buf.
func PutUint16BE(buf []byte, v uint16) {
	binary.BigEndian.PutUint16(buf, v)
}

// Uint16BE reads a big-endian uint16 from buf.
func Uint16BE(buf []byte) uint16 {
	return binary.BigEndian.Uint16(buf)
}
