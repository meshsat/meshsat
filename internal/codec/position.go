package codec

import (
	"encoding/binary"
	"errors"
	"math"
)

const (
	// HeaderFull is the magic prefix for a full position encoding.
	HeaderFull byte = 0x50

	// HeaderDelta is the magic prefix for a delta position encoding.
	HeaderDelta byte = 0x44

	// FullPositionSize is the wire size of a full position frame (header + payload).
	FullPositionSize = 1 + 15

	// DeltaPositionSize is the wire size of a delta position frame (header + payload).
	DeltaPositionSize = 1 + 10

	// latLonScale is the multiplier for lat/lon degree→microdegree conversion.
	latLonScale = 1e6

	// maxDelta is the largest lat/lon delta that fits in an int16 (±32767 microdegrees ≈ ±0.033°).
	maxDelta = math.MaxInt16
)

// Position represents a GPS fix with auxiliary telemetry.
type Position struct {
	Lat     float64 // degrees
	Lon     float64 // degrees
	Alt     int16   // meters
	Heading uint16  // degrees 0-359
	Speed   uint16  // cm/s
	Battery uint8   // percent 0-100
}

// EncodePosition encodes a Position into 16 bytes (1-byte header + 15-byte payload).
func EncodePosition(p Position) []byte {
	buf := make([]byte, FullPositionSize)
	buf[0] = HeaderFull
	encodePositionPayload(buf[1:], p)
	return buf
}

// DecodePosition decodes a full position frame. The input must be exactly 16 bytes
// and start with the HeaderFull magic byte.
func DecodePosition(data []byte) (Position, error) {
	if len(data) < FullPositionSize {
		return Position{}, errors.New("codec: position data too short")
	}
	if data[0] != HeaderFull {
		return Position{}, errors.New("codec: invalid position header")
	}
	return decodePositionPayload(data[1:]), nil
}

// encodePositionPayload writes 15 bytes of position data into buf.
func encodePositionPayload(buf []byte, p Position) {
	lat := int32(math.Round(p.Lat * latLonScale))
	lon := int32(math.Round(p.Lon * latLonScale))

	binary.LittleEndian.PutUint32(buf[0:4], uint32(lat))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(lon))
	binary.LittleEndian.PutUint16(buf[8:10], uint16(p.Alt))
	binary.LittleEndian.PutUint16(buf[10:12], p.Heading)
	binary.LittleEndian.PutUint16(buf[12:14], p.Speed)
	buf[14] = p.Battery
}

// decodePositionPayload reads 15 bytes into a Position.
func decodePositionPayload(buf []byte) Position {
	lat := int32(binary.LittleEndian.Uint32(buf[0:4]))
	lon := int32(binary.LittleEndian.Uint32(buf[4:8]))
	return Position{
		Lat:     float64(lat) / latLonScale,
		Lon:     float64(lon) / latLonScale,
		Alt:     int16(binary.LittleEndian.Uint16(buf[8:10])),
		Heading: binary.LittleEndian.Uint16(buf[10:12]),
		Speed:   binary.LittleEndian.Uint16(buf[12:14]),
		Battery: buf[14],
	}
}

// DeltaEncoder compresses sequential positions by encoding small movements as
// deltas, falling back to full encoding when the delta exceeds int16 range.
type DeltaEncoder struct {
	last *Position
}

// EncodeDelta encodes a position, using delta compression when possible.
// First position or large movements produce a full 16-byte frame.
// Small movements produce an 11-byte delta frame.
func (d *DeltaEncoder) EncodeDelta(p Position) []byte {
	if d.last == nil {
		pos := p // copy
		d.last = &pos
		return EncodePosition(p)
	}

	dlat := int32(math.Round(p.Lat*latLonScale)) - int32(math.Round(d.last.Lat*latLonScale))
	dlon := int32(math.Round(p.Lon*latLonScale)) - int32(math.Round(d.last.Lon*latLonScale))
	dalt := int32(p.Alt) - int32(d.last.Alt)

	if dlat < -maxDelta || dlat > maxDelta ||
		dlon < -maxDelta || dlon > maxDelta ||
		dalt < math.MinInt8 || dalt > math.MaxInt8 {
		pos := p
		d.last = &pos
		return EncodePosition(p)
	}

	buf := make([]byte, DeltaPositionSize)
	buf[0] = HeaderDelta
	binary.LittleEndian.PutUint16(buf[1:3], uint16(int16(dlat)))
	binary.LittleEndian.PutUint16(buf[3:5], uint16(int16(dlon)))
	buf[5] = byte(int8(dalt))
	binary.LittleEndian.PutUint16(buf[6:8], p.Heading)
	binary.LittleEndian.PutUint16(buf[8:10], p.Speed)
	buf[10] = p.Battery

	pos := p
	d.last = &pos
	return buf
}

// DecodeDelta decodes either a full or delta position frame. It auto-detects
// the encoding from the header byte.
func (d *DeltaEncoder) DecodeDelta(data []byte) (Position, error) {
	if len(data) == 0 {
		return Position{}, errors.New("codec: empty data")
	}

	switch data[0] {
	case HeaderFull:
		p, err := DecodePosition(data)
		if err != nil {
			return Position{}, err
		}
		pos := p
		d.last = &pos
		return p, nil

	case HeaderDelta:
		if len(data) < DeltaPositionSize {
			return Position{}, errors.New("codec: delta data too short")
		}
		if d.last == nil {
			return Position{}, errors.New("codec: delta without prior full position")
		}

		dlat := int16(binary.LittleEndian.Uint16(data[1:3]))
		dlon := int16(binary.LittleEndian.Uint16(data[3:5]))
		dalt := int8(data[5])

		lastLatMicro := int32(math.Round(d.last.Lat * latLonScale))
		lastLonMicro := int32(math.Round(d.last.Lon * latLonScale))

		p := Position{
			Lat:     float64(lastLatMicro+int32(dlat)) / latLonScale,
			Lon:     float64(lastLonMicro+int32(dlon)) / latLonScale,
			Alt:     d.last.Alt + int16(dalt),
			Heading: binary.LittleEndian.Uint16(data[6:8]),
			Speed:   binary.LittleEndian.Uint16(data[8:10]),
			Battery: data[10],
		}

		pos := p
		d.last = &pos
		return p, nil

	default:
		return Position{}, errors.New("codec: unknown header byte")
	}
}

// Reset clears the last position, forcing the next encode to produce a full frame.
func (d *DeltaEncoder) Reset() {
	d.last = nil
}
