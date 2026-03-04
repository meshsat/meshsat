package transport

// Hand-rolled protobuf primitives for Meshtastic wire protocol.
// Ported from HAL — zero external dependencies, pure encoding/binary.

import "fmt"

// Wire types
const (
	wireVarint          = 0
	wireFixed64         = 1
	wireLengthDelimited = 2
	wireFixed32         = 5
)

// readTag reads a protobuf field tag and returns (fieldNumber, wireType, newOffset, error).
func readTag(data []byte, pos int) (uint32, int, int, error) {
	val, n := readVarint(data, pos)
	if n <= 0 {
		return 0, 0, pos, fmt.Errorf("invalid tag at position %d", pos)
	}
	fieldNum := uint32(val >> 3)
	wireType := int(val & 0x07)
	return fieldNum, wireType, pos + n, nil
}

// readVarint reads a protobuf varint and returns (value, bytesConsumed).
// Returns n=0 if malformed or data exhausted.
func readVarint(data []byte, pos int) (uint64, int) {
	var val uint64
	var shift uint
	for i := 0; i < 10; i++ {
		if pos+i >= len(data) {
			return 0, 0
		}
		b := data[pos+i]
		val |= uint64(b&0x7F) << shift
		if b < 0x80 {
			return val, i + 1
		}
		shift += 7
	}
	return 0, 0 // varint too long
}

// readLengthDelimited reads a length-delimited field.
func readLengthDelimited(data []byte, pos int) ([]byte, int, error) {
	length, n := readVarint(data, pos)
	if n <= 0 {
		return nil, pos, fmt.Errorf("invalid length at position %d", pos)
	}
	pos += n
	end := pos + int(length)
	if end > len(data) || length > 65536 {
		return nil, pos, fmt.Errorf("length-delimited field exceeds data bounds")
	}
	return data[pos:end], end, nil
}

// skipField advances past a field based on its wire type.
// Returns -1 if the field cannot be skipped.
func skipField(data []byte, pos int, wireType int) int {
	switch wireType {
	case wireVarint:
		_, n := readVarint(data, pos)
		if n <= 0 {
			return -1
		}
		return pos + n
	case wireFixed64:
		if pos+8 > len(data) {
			return -1
		}
		return pos + 8
	case wireLengthDelimited:
		_, newPos, err := readLengthDelimited(data, pos)
		if err != nil {
			return -1
		}
		return newPos
	case wireFixed32:
		if pos+4 > len(data) {
			return -1
		}
		return pos + 4
	default:
		return -1
	}
}

// appendVarint appends a varint-encoded value to buf.
func appendVarint(buf []byte, val uint64) []byte {
	for val >= 0x80 {
		buf = append(buf, byte(val)|0x80)
		val >>= 7
	}
	buf = append(buf, byte(val))
	return buf
}

// appendTag appends a protobuf field tag (field number + wire type).
func appendTag(buf []byte, field uint32, wireType int) []byte {
	return appendVarint(buf, uint64(field)<<3|uint64(wireType))
}

// appendFixed32 appends a uint32 as 4 bytes little-endian (protobuf fixed32).
func appendFixed32(buf []byte, v uint32) []byte {
	return append(buf, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// appendLengthDelimited appends a length-delimited field (tag + length + data).
func appendLengthDelimited(buf []byte, field uint32, data []byte) []byte {
	buf = appendTag(buf, field, wireLengthDelimited)
	buf = appendVarint(buf, uint64(len(data)))
	buf = append(buf, data...)
	return buf
}

// appendVarintField appends a varint field (tag + value).
func appendVarintField(buf []byte, field uint32, val uint64) []byte {
	buf = appendTag(buf, field, wireVarint)
	buf = appendVarint(buf, val)
	return buf
}

// decodeZigzag decodes a zigzag-encoded sint32/sint64 value.
func decodeZigzag(v uint64) int64 {
	return int64((v >> 1) ^ -(v & 1))
}

// encodeZigzag encodes a signed integer using zigzag encoding.
func encodeZigzag(v int64) uint64 {
	return uint64((v << 1) ^ (v >> 63))
}
