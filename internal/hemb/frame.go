package hemb

import (
	"errors"
	"fmt"
)

// Frame format constants.
const (
	CompactHeaderLen  = 8
	ExtendedHeaderLen = 16
	MagicByte0        = 0x48 // 'H'
	MagicByte1        = 0x4D // 'M'
	VersionV1         = 0x00 // 2-bit version field
)

// Frame flags (2 bits).
const (
	FlagData   = 0x00
	FlagRepair = 0x01
	FlagAck    = 0x02
	FlagCtrl   = 0x03
)

// Header modes for BearerProfile.
const (
	HeaderModeCompact  = "compact"
	HeaderModeExtended = "extended"
	HeaderModeImplicit = "implicit" // IPoUGRS: no header, session context pre-negotiated
)

var (
	ErrFrameTooShort = errors.New("hemb: frame too short")
	ErrBadCRC        = errors.New("hemb: CRC-8 mismatch")
	ErrBadMagic      = errors.New("hemb: bad magic bytes")
)

// CompactHeader is the 8-byte HeMB header for constrained bearers (MTU <= 340).
// Stream ID is scoped per (bearer_source_address, stream_id) — NOT globally unique.
type CompactHeader struct {
	Version      uint8  // 2 bits (0 = v1)
	StreamID     uint8  // 4 bits (0-15)
	Flags        uint8  // 2 bits (data/repair/ack/ctrl)
	Sequence     uint16 // 12 bits (0-4095)
	K            uint8  // source symbols in generation (1-255)
	N            uint8  // total coded symbols (1-255)
	BearerIndex  uint8  // 4 bits (0-15)
	GenerationID uint16 // 10 bits (0-1023)
	TTL          uint8  // 6 bits, units of 30 seconds (0-63, max 31.5 min)
}

// ExtendedHeader is the 16-byte HeMB header for unconstrained bearers or relay.
type ExtendedHeader struct {
	Version          uint8
	StreamID         uint8  // 8 bits (0-255)
	Flags            uint8  // 2 bits
	Sequence         uint16 // 16 bits
	K                uint8
	N                uint8
	BearerIndex      uint8  // 8 bits
	GenerationID     uint16 // 16 bits
	TotalPayloadSize uint16 // original payload size before splitting
	TTL              uint8  // units of 10 seconds (0-255, max 42.5 min)
	FlagsExtended    uint8  // bit0=has_fec_meta, bit1=systematic, bit2=priority
}

// crc8 computes CRC-8 with ITU-T polynomial 0x07.
func crc8(data []byte) byte {
	var crc byte
	for _, b := range data {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = (crc << 1) ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// MarshalCompact encodes a CompactHeader into 8 bytes.
func MarshalCompact(h CompactHeader) [CompactHeaderLen]byte {
	var b [CompactHeaderLen]byte
	b[0] = (h.Version&0x03)<<6 | (h.StreamID&0x0F)<<2 | (h.Flags & 0x03)
	b[1] = byte(h.Sequence & 0xFF)
	b[2] = h.K
	b[3] = h.N
	b[4] = (h.BearerIndex&0x0F)<<4 | byte((h.Sequence>>8)&0x0F)
	b[5] = byte(h.GenerationID & 0xFF)
	b[6] = byte((h.GenerationID>>8)&0x03)<<6 | (h.TTL & 0x3F)
	b[7] = crc8(b[:7])
	return b
}

// UnmarshalCompact decodes 8 bytes into a CompactHeader.
func UnmarshalCompact(b [CompactHeaderLen]byte) (CompactHeader, error) {
	if crc8(b[:7]) != b[7] {
		return CompactHeader{}, ErrBadCRC
	}
	return CompactHeader{
		Version:      (b[0] >> 6) & 0x03,
		StreamID:     (b[0] >> 2) & 0x0F,
		Flags:        b[0] & 0x03,
		Sequence:     uint16(b[1]) | uint16(b[4]&0x0F)<<8,
		K:            b[2],
		N:            b[3],
		BearerIndex:  (b[4] >> 4) & 0x0F,
		GenerationID: uint16(b[5]) | uint16((b[6]>>6)&0x03)<<8,
		TTL:          b[6] & 0x3F,
	}, nil
}

// MarshalExtended encodes an ExtendedHeader into 16 bytes.
func MarshalExtended(h ExtendedHeader) [ExtendedHeaderLen]byte {
	var b [ExtendedHeaderLen]byte
	b[0] = MagicByte0
	b[1] = MagicByte1
	b[2] = (h.Version&0x03)<<6 | (h.StreamID&0x0F)<<2 | (h.Flags & 0x03)
	b[3] = h.StreamID
	b[4] = byte(h.Sequence)
	b[5] = byte(h.Sequence >> 8)
	b[6] = h.K
	b[7] = h.N
	b[8] = h.BearerIndex
	b[9] = byte(h.GenerationID)
	b[10] = byte(h.GenerationID >> 8)
	b[11] = byte(h.TotalPayloadSize)
	b[12] = byte(h.TotalPayloadSize >> 8)
	b[13] = h.TTL
	b[14] = h.FlagsExtended
	b[15] = crc8(b[:15])
	return b
}

// UnmarshalExtended decodes 16 bytes into an ExtendedHeader.
func UnmarshalExtended(b [ExtendedHeaderLen]byte) (ExtendedHeader, error) {
	if b[0] != MagicByte0 || b[1] != MagicByte1 {
		return ExtendedHeader{}, ErrBadMagic
	}
	if crc8(b[:15]) != b[15] {
		return ExtendedHeader{}, ErrBadCRC
	}
	return ExtendedHeader{
		Version:          (b[2] >> 6) & 0x03,
		StreamID:         b[3],
		Flags:            b[2] & 0x03,
		Sequence:         uint16(b[4]) | uint16(b[5])<<8,
		K:                b[6],
		N:                b[7],
		BearerIndex:      b[8],
		GenerationID:     uint16(b[9]) | uint16(b[10])<<8,
		TotalPayloadSize: uint16(b[11]) | uint16(b[12])<<8,
		TTL:              b[13],
		FlagsExtended:    b[14],
	}, nil
}

// IsHeMBFrame returns true if data starts with a valid HeMB frame header.
func IsHeMBFrame(data []byte) bool {
	if len(data) >= ExtendedHeaderLen && data[0] == MagicByte0 && data[1] == MagicByte1 {
		var b [ExtendedHeaderLen]byte
		copy(b[:], data[:ExtendedHeaderLen])
		_, err := UnmarshalExtended(b)
		return err == nil
	}
	if len(data) >= CompactHeaderLen {
		var b [CompactHeaderLen]byte
		copy(b[:], data[:CompactHeaderLen])
		h, err := UnmarshalCompact(b)
		if err != nil {
			return false
		}
		return h.Version == VersionV1
	}
	return false
}

// PromoteHeader converts a CompactHeader to an ExtendedHeader for relay/DTN.
// Normative rule: MUST be called when HopCount > 0, egress != ingress, or DTN custody.
// For IPoUGRS (HeaderMode=="implicit"), returns zero ExtendedHeader — IPoUGRS never relays.
func PromoteHeader(compact CompactHeader, headerMode string) ExtendedHeader {
	if headerMode == HeaderModeImplicit {
		return ExtendedHeader{}
	}
	return ExtendedHeader{
		Version:      compact.Version,
		StreamID:     compact.StreamID,
		Flags:        compact.Flags,
		Sequence:     compact.Sequence,
		K:            compact.K,
		N:            compact.N,
		BearerIndex:  compact.BearerIndex,
		GenerationID: compact.GenerationID,
		TTL:          compact.TTL * 3, // 30s units → 10s units
	}
}

// HeaderOverhead returns the header size in bytes for a given header mode.
func HeaderOverhead(mode string) int {
	switch mode {
	case HeaderModeCompact:
		return CompactHeaderLen
	case HeaderModeExtended:
		return ExtendedHeaderLen
	case HeaderModeImplicit:
		return 0
	default:
		return CompactHeaderLen
	}
}

// marshalSymbolFrame wraps a CodedSymbol with the appropriate header for a bearer.
func marshalSymbolFrame(bearer *BearerProfile, streamID uint8, sym CodedSymbol, totalN int) []byte {
	switch bearer.HeaderMode {
	case HeaderModeImplicit:
		return sym.Data

	case HeaderModeExtended:
		hdr := MarshalExtended(ExtendedHeader{
			Version:      VersionV1,
			StreamID:     streamID,
			Flags:        FlagData,
			Sequence:     uint16(sym.SymbolIndex),
			K:            uint8(sym.K),
			N:            uint8(totalN),
			BearerIndex:  bearer.Index,
			GenerationID: sym.GenID,
		})
		frame := make([]byte, ExtendedHeaderLen+len(sym.Coefficients)+len(sym.Data))
		copy(frame, hdr[:])
		copy(frame[ExtendedHeaderLen:], sym.Coefficients)
		copy(frame[ExtendedHeaderLen+len(sym.Coefficients):], sym.Data)
		return frame

	default: // compact
		hdr := MarshalCompact(CompactHeader{
			Version:      VersionV1,
			StreamID:     streamID & 0x0F,
			Flags:        FlagData,
			Sequence:     uint16(sym.SymbolIndex) & 0x0FFF,
			K:            uint8(sym.K),
			N:            uint8(totalN),
			BearerIndex:  bearer.Index & 0x0F,
			GenerationID: sym.GenID & 0x03FF,
		})
		frame := make([]byte, CompactHeaderLen+len(sym.Coefficients)+len(sym.Data))
		copy(frame, hdr[:])
		copy(frame[CompactHeaderLen:], sym.Coefficients)
		copy(frame[CompactHeaderLen+len(sym.Coefficients):], sym.Data)
		return frame
	}
}

// parseSymbolFromFrame extracts a CodedSymbol from a framed HeMB message.
func parseSymbolFromFrame(data []byte) (sym CodedSymbol, streamID uint8, bearerIdx uint8, err error) {
	if len(data) >= ExtendedHeaderLen && data[0] == MagicByte0 && data[1] == MagicByte1 {
		var b [ExtendedHeaderLen]byte
		copy(b[:], data[:ExtendedHeaderLen])
		hdr, e := UnmarshalExtended(b)
		if e != nil {
			return CodedSymbol{}, 0, 0, fmt.Errorf("unmarshal extended: %w", e)
		}
		k := int(hdr.K)
		coeffEnd := ExtendedHeaderLen + k
		if len(data) < coeffEnd+1 {
			return CodedSymbol{}, 0, 0, fmt.Errorf("frame too short for K=%d", k)
		}
		sym = CodedSymbol{
			GenID:        hdr.GenerationID,
			SymbolIndex:  int(hdr.Sequence),
			K:            k,
			Coefficients: make([]byte, k),
			Data:         make([]byte, len(data)-coeffEnd),
		}
		copy(sym.Coefficients, data[ExtendedHeaderLen:coeffEnd])
		copy(sym.Data, data[coeffEnd:])
		return sym, hdr.StreamID, hdr.BearerIndex, nil
	}

	if len(data) >= CompactHeaderLen {
		var b [CompactHeaderLen]byte
		copy(b[:], data[:CompactHeaderLen])
		hdr, e := UnmarshalCompact(b)
		if e != nil {
			return CodedSymbol{}, 0, 0, fmt.Errorf("unmarshal compact: %w", e)
		}
		k := int(hdr.K)
		coeffEnd := CompactHeaderLen + k
		if len(data) < coeffEnd+1 {
			return CodedSymbol{}, 0, 0, fmt.Errorf("frame too short for K=%d", k)
		}
		sym = CodedSymbol{
			GenID:        hdr.GenerationID,
			SymbolIndex:  int(hdr.Sequence),
			K:            k,
			Coefficients: make([]byte, k),
			Data:         make([]byte, len(data)-coeffEnd),
		}
		copy(sym.Coefficients, data[CompactHeaderLen:coeffEnd])
		copy(sym.Data, data[coeffEnd:])
		return sym, hdr.StreamID, hdr.BearerIndex, nil
	}

	return CodedSymbol{}, 0, 0, ErrFrameTooShort
}
