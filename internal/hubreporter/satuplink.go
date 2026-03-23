// Package hubreporter — satellite uplink binary encoder/decoder.
//
// When internet is down, the bridge encodes critical data (position, SOS, health)
// into compact binary frames that fit within a 340-byte SBD payload for relay
// to the Hub via Iridium satellite.
//
// Wire format: big-endian, hand-packed (NOT protobuf — must fit in 340 bytes).
package hubreporter

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// SatUplinkMagic identifies bridge-originated satellite uplink messages.
var SatUplinkMagic = [2]byte{0x4D, 0x53} // "MS"

// Satellite uplink message types.
const (
	SatMsgPosition      byte = 0x01
	SatMsgSOS           byte = 0x02
	SatMsgHealthSummary byte = 0x03

	satUplinkVersion byte = 1
	satHeaderLen          = 4 // magic(2) + version(1) + type(1)

	// maxBridgeIDLen is the maximum bridge ID length (truncated if longer).
	maxBridgeIDLen = 16
	// maxSOSMessageLen is the maximum SOS text length.
	maxSOSMessageLen = 64
	// maxDeviceIDLen is the maximum device ID length.
	maxDeviceIDLen = 16
	// maxIfaceName is the maximum interface name length.
	maxIfaceName = 16
)

// SatUplinkHeader is the first 4 bytes of every satellite uplink message.
type SatUplinkHeader struct {
	Magic   [2]byte
	Version byte
	MsgType byte
}

// SatIfaceStatus represents the status of a single interface in a health summary.
type SatIfaceStatus struct {
	Name   string
	Online bool
	Signal byte // 0-5
}

// --- Encoding ---

func writeHeader(buf []byte, msgType byte) {
	buf[0] = SatUplinkMagic[0]
	buf[1] = SatUplinkMagic[1]
	buf[2] = satUplinkVersion
	buf[3] = msgType
}

func writeLenPrefixedString(buf []byte, s string, maxLen int) int {
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	buf[0] = byte(len(s))
	copy(buf[1:], s)
	return 1 + len(s)
}

// EncodeSatPosition encodes a position message for satellite uplink.
// Target size: ~30 bytes. Layout:
//
//	Header(4) + bridgeID_len(1) + bridgeID(var) + lat(4,float32) + lon(4,float32) +
//	alt(2,int16 meters) + source(1) + timestamp(4,uint32 unix)
func EncodeSatPosition(bridgeID string, lat, lon float64, alt float32, source byte, timestamp time.Time) []byte {
	if len(bridgeID) > maxBridgeIDLen {
		bridgeID = bridgeID[:maxBridgeIDLen]
	}
	size := satHeaderLen + 1 + len(bridgeID) + 4 + 4 + 2 + 1 + 4
	buf := make([]byte, size)
	writeHeader(buf, SatMsgPosition)
	off := satHeaderLen
	off += writeLenPrefixedString(buf[off:], bridgeID, maxBridgeIDLen)
	binary.BigEndian.PutUint32(buf[off:], math.Float32bits(float32(lat)))
	off += 4
	binary.BigEndian.PutUint32(buf[off:], math.Float32bits(float32(lon)))
	off += 4
	altI16 := int16(alt)
	binary.BigEndian.PutUint16(buf[off:], uint16(altI16))
	off += 2
	buf[off] = source
	off++
	binary.BigEndian.PutUint32(buf[off:], uint32(timestamp.Unix()))
	return buf
}

// EncodeSatSOS encodes an SOS message for satellite uplink.
// Target size: ~40 bytes. Layout:
//
//	Header(4) + bridgeID_len(1) + bridgeID + deviceID_len(1) + deviceID +
//	lat(4,float32) + lon(4,float32) + msg_len(1) + msg(var, max 64) + timestamp(4)
func EncodeSatSOS(bridgeID string, deviceID string, lat, lon float64, message string, timestamp time.Time) []byte {
	if len(bridgeID) > maxBridgeIDLen {
		bridgeID = bridgeID[:maxBridgeIDLen]
	}
	if len(deviceID) > maxDeviceIDLen {
		deviceID = deviceID[:maxDeviceIDLen]
	}
	if len(message) > maxSOSMessageLen {
		message = message[:maxSOSMessageLen]
	}
	size := satHeaderLen + 1 + len(bridgeID) + 1 + len(deviceID) + 4 + 4 + 1 + len(message) + 4
	buf := make([]byte, size)
	writeHeader(buf, SatMsgSOS)
	off := satHeaderLen
	off += writeLenPrefixedString(buf[off:], bridgeID, maxBridgeIDLen)
	off += writeLenPrefixedString(buf[off:], deviceID, maxDeviceIDLen)
	binary.BigEndian.PutUint32(buf[off:], math.Float32bits(float32(lat)))
	off += 4
	binary.BigEndian.PutUint32(buf[off:], math.Float32bits(float32(lon)))
	off += 4
	off += writeLenPrefixedString(buf[off:], message, maxSOSMessageLen)
	binary.BigEndian.PutUint32(buf[off:], uint32(timestamp.Unix()))
	return buf
}

// EncodeSatHealth encodes a health summary for satellite uplink.
// Target size: ~60 bytes. Layout:
//
//	Header(4) + bridgeID_len(1) + bridgeID + uptime(4) + cpu(1) + mem(1) + disk(1) +
//	iface_count(1) + per-iface(name_len(1) + name + status(1) + signal(1)) + timestamp(4)
func EncodeSatHealth(bridgeID string, uptimeSec uint32, cpuPct, memPct, diskPct byte, interfaces []SatIfaceStatus, timestamp time.Time) []byte {
	if len(bridgeID) > maxBridgeIDLen {
		bridgeID = bridgeID[:maxBridgeIDLen]
	}
	// Calculate size
	ifaceSize := 0
	for _, iface := range interfaces {
		name := iface.Name
		if len(name) > maxIfaceName {
			name = name[:maxIfaceName]
		}
		ifaceSize += 1 + len(name) + 1 + 1 // name_len + name + status + signal
	}
	size := satHeaderLen + 1 + len(bridgeID) + 4 + 1 + 1 + 1 + 1 + ifaceSize + 4
	buf := make([]byte, size)
	writeHeader(buf, SatMsgHealthSummary)
	off := satHeaderLen
	off += writeLenPrefixedString(buf[off:], bridgeID, maxBridgeIDLen)
	binary.BigEndian.PutUint32(buf[off:], uptimeSec)
	off += 4
	buf[off] = cpuPct
	off++
	buf[off] = memPct
	off++
	buf[off] = diskPct
	off++
	buf[off] = byte(len(interfaces))
	off++
	for _, iface := range interfaces {
		name := iface.Name
		if len(name) > maxIfaceName {
			name = name[:maxIfaceName]
		}
		off += writeLenPrefixedString(buf[off:], name, maxIfaceName)
		if iface.Online {
			buf[off] = 1
		} else {
			buf[off] = 0
		}
		off++
		buf[off] = iface.Signal
		off++
	}
	binary.BigEndian.PutUint32(buf[off:], uint32(timestamp.Unix()))
	return buf
}

// --- Decoding ---

var (
	ErrTooShort    = errors.New("satuplink: data too short")
	ErrBadMagic    = errors.New("satuplink: invalid magic bytes")
	ErrBadVersion  = errors.New("satuplink: unsupported version")
	ErrTruncated   = errors.New("satuplink: truncated payload")
	ErrUnknownType = errors.New("satuplink: unknown message type")
)

// IsSatUplink checks if data starts with the satellite uplink magic bytes.
func IsSatUplink(data []byte) bool {
	return len(data) >= satHeaderLen && data[0] == SatUplinkMagic[0] && data[1] == SatUplinkMagic[1]
}

// DecodeSatUplink decodes the header and returns the message type and payload (after header).
func DecodeSatUplink(data []byte) (header SatUplinkHeader, payload []byte, err error) {
	if len(data) < satHeaderLen {
		return header, nil, ErrTooShort
	}
	header.Magic = [2]byte{data[0], data[1]}
	if header.Magic != SatUplinkMagic {
		return header, nil, ErrBadMagic
	}
	header.Version = data[2]
	if header.Version != satUplinkVersion {
		return header, nil, ErrBadVersion
	}
	header.MsgType = data[3]
	return header, data[satHeaderLen:], nil
}

func readLenPrefixedString(data []byte) (string, int, error) {
	if len(data) < 1 {
		return "", 0, ErrTruncated
	}
	n := int(data[0])
	if len(data) < 1+n {
		return "", 0, ErrTruncated
	}
	return string(data[1 : 1+n]), 1 + n, nil
}

// DecodeSatPosition decodes a position payload (after header).
func DecodeSatPosition(payload []byte) (bridgeID string, lat, lon float64, alt float32, source byte, timestamp time.Time, err error) {
	off := 0
	bridgeID, n, err := readLenPrefixedString(payload[off:])
	if err != nil {
		return
	}
	off += n
	if off+4+4+2+1+4 > len(payload) {
		err = ErrTruncated
		return
	}
	lat = float64(math.Float32frombits(binary.BigEndian.Uint32(payload[off:])))
	off += 4
	lon = float64(math.Float32frombits(binary.BigEndian.Uint32(payload[off:])))
	off += 4
	alt = float32(int16(binary.BigEndian.Uint16(payload[off:])))
	off += 2
	source = payload[off]
	off++
	timestamp = time.Unix(int64(binary.BigEndian.Uint32(payload[off:])), 0).UTC()
	return
}

// DecodeSatSOS decodes an SOS payload (after header).
func DecodeSatSOS(payload []byte) (bridgeID, deviceID string, lat, lon float64, message string, timestamp time.Time, err error) {
	off := 0
	bridgeID, n, err := readLenPrefixedString(payload[off:])
	if err != nil {
		return
	}
	off += n
	deviceID, n, err = readLenPrefixedString(payload[off:])
	if err != nil {
		return
	}
	off += n
	if off+4+4 > len(payload) {
		err = ErrTruncated
		return
	}
	lat = float64(math.Float32frombits(binary.BigEndian.Uint32(payload[off:])))
	off += 4
	lon = float64(math.Float32frombits(binary.BigEndian.Uint32(payload[off:])))
	off += 4
	message, n, err = readLenPrefixedString(payload[off:])
	if err != nil {
		return
	}
	off += n
	if off+4 > len(payload) {
		err = ErrTruncated
		return
	}
	timestamp = time.Unix(int64(binary.BigEndian.Uint32(payload[off:])), 0).UTC()
	return
}

// DecodeSatHealth decodes a health summary payload (after header).
func DecodeSatHealth(payload []byte) (bridgeID string, uptimeSec uint32, cpuPct, memPct, diskPct byte, interfaces []SatIfaceStatus, timestamp time.Time, err error) {
	off := 0
	bridgeID, n, err := readLenPrefixedString(payload[off:])
	if err != nil {
		return
	}
	off += n
	if off+4+1+1+1+1 > len(payload) {
		err = ErrTruncated
		return
	}
	uptimeSec = binary.BigEndian.Uint32(payload[off:])
	off += 4
	cpuPct = payload[off]
	off++
	memPct = payload[off]
	off++
	diskPct = payload[off]
	off++
	ifaceCount := int(payload[off])
	off++
	interfaces = make([]SatIfaceStatus, 0, ifaceCount)
	for i := 0; i < ifaceCount; i++ {
		var name string
		name, n, err = readLenPrefixedString(payload[off:])
		if err != nil {
			return
		}
		off += n
		if off+2 > len(payload) {
			err = ErrTruncated
			return
		}
		iface := SatIfaceStatus{
			Name:   name,
			Online: payload[off] != 0,
			Signal: payload[off+1],
		}
		off += 2
		interfaces = append(interfaces, iface)
	}
	if off+4 > len(payload) {
		err = ErrTruncated
		return
	}
	timestamp = time.Unix(int64(binary.BigEndian.Uint32(payload[off:])), 0).UTC()
	return
}
