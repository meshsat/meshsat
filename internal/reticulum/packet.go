package reticulum

import (
	"errors"
	"fmt"
)

// Common errors.
var (
	ErrTooShort    = errors.New("reticulum: packet too short")
	ErrWrongType   = errors.New("reticulum: wrong packet type")
	ErrInvalidFlag = errors.New("reticulum: invalid flag combination")
	ErrMaxHops     = errors.New("reticulum: max hops exceeded")
)

// Header represents a parsed Reticulum packet header.
type Header struct {
	// Flags byte fields (packed into byte 0).
	HeaderType    byte // 0=Type1 (single addr), 1=Type2 (transport)
	ContextFlag   byte // 0=unset, 1=set (e.g. ratchet present in announce)
	TransportType byte // 0=broadcast, 1=transport
	DestType      byte // 0=single, 1=group, 2=plain, 3=link
	PacketType    byte // 0=data, 1=announce, 2=linkrequest, 3=proof

	Hops byte // Hop count (byte 1)

	// Addresses — TransportID is only present for HeaderType2.
	TransportID [TruncatedHashLen]byte // Only if HeaderType == 1
	DestHash    [TruncatedHashLen]byte // Always present

	Context byte // Context byte after addresses

	Data []byte // Remaining payload after header
}

// PackFlags packs the header fields into the flags byte.
// Bit layout: [7:IFAC][6:HeaderType][5:ContextFlag][4:TransportType][3-2:DestType][1-0:PacketType]
// Note: bit 7 (IFAC) is handled at the interface layer, not here.
func (h *Header) PackFlags() byte {
	return (h.HeaderType << 6) | (h.ContextFlag << 5) | (h.TransportType << 4) | (h.DestType << 2) | h.PacketType
}

// UnpackFlags extracts header fields from the flags byte.
func (h *Header) UnpackFlags(flags byte) {
	h.HeaderType = (flags & 0x40) >> 6
	h.ContextFlag = (flags & 0x20) >> 5
	h.TransportType = (flags & 0x10) >> 4
	h.DestType = (flags & 0x0C) >> 2
	h.PacketType = flags & 0x03
}

// HeaderSize returns the header size based on the header type.
func (h *Header) HeaderSize() int {
	if h.HeaderType == HeaderType2 {
		return HeaderMaxSize
	}
	return HeaderMinSize
}

// MarshalHeader serializes the header to wire format.
func (h *Header) MarshalHeader() []byte {
	size := h.HeaderSize()
	buf := make([]byte, 0, size+len(h.Data))

	buf = append(buf, h.PackFlags())
	buf = append(buf, h.Hops)

	if h.HeaderType == HeaderType2 {
		buf = append(buf, h.TransportID[:]...)
	}
	buf = append(buf, h.DestHash[:]...)
	buf = append(buf, h.Context)

	if len(h.Data) > 0 {
		buf = append(buf, h.Data...)
	}
	return buf
}

// Marshal serializes the full packet (header + data) to wire format.
func (h *Header) Marshal() []byte {
	return h.MarshalHeader()
}

// UnmarshalHeader parses a Reticulum packet header from wire format.
// Returns the parsed header and any remaining data.
func UnmarshalHeader(data []byte) (*Header, error) {
	if len(data) < HeaderMinSize {
		return nil, ErrTooShort
	}

	h := &Header{}
	h.UnpackFlags(data[0])
	h.Hops = data[1]

	pos := 2
	if h.HeaderType == HeaderType2 {
		if len(data) < HeaderMaxSize {
			return nil, fmt.Errorf("%w: need %d bytes for type 2 header, got %d", ErrTooShort, HeaderMaxSize, len(data))
		}
		copy(h.TransportID[:], data[pos:pos+TruncatedHashLen])
		pos += TruncatedHashLen
	}

	copy(h.DestHash[:], data[pos:pos+TruncatedHashLen])
	pos += TruncatedHashLen

	h.Context = data[pos]
	pos++

	if pos < len(data) {
		h.Data = make([]byte, len(data)-pos)
		copy(h.Data, data[pos:])
	}

	return h, nil
}

// IncrementHop increments the hop count. Returns false if max hops exceeded.
func (h *Header) IncrementHop() bool {
	if int(h.Hops) >= PathfinderM {
		return false
	}
	h.Hops++
	return true
}

// PacketTypeString returns a human-readable name for the packet type.
func PacketTypeString(pt byte) string {
	switch pt {
	case PacketData:
		return "DATA"
	case PacketAnnounce:
		return "ANNOUNCE"
	case PacketLinkRequest:
		return "LINKREQUEST"
	case PacketProof:
		return "PROOF"
	default:
		return fmt.Sprintf("UNKNOWN(%02x)", pt)
	}
}

// DestTypeString returns a human-readable name for the destination type.
func DestTypeString(dt byte) string {
	switch dt {
	case DestSingle:
		return "SINGLE"
	case DestGroup:
		return "GROUP"
	case DestPlain:
		return "PLAIN"
	case DestLink:
		return "LINK"
	default:
		return fmt.Sprintf("UNKNOWN(%02x)", dt)
	}
}
