package reticulum

import "time"

// Keepalive constants derived from the Reticulum spec.
const (
	// KeepaliveBitsPerSec is the bandwidth consumed per link.
	// 0.45 bps means 100 links on a 1200 bps channel = 3.75% capacity.
	KeepaliveBitsPerSec = 0.45

	// KeepaliveInterval is the time between keepalive packets.
	KeepaliveInterval = 18 * time.Second

	// KeepaliveTimeout is how long without activity before a link is dead.
	KeepaliveTimeout = 60 * time.Second

	// KeepalivePacketLen: link_id(32) + random(1) = 33 bytes.
	KeepalivePacketLen = 33
)

// KeepalivePacket is a minimal heartbeat for link liveness.
type KeepalivePacket struct {
	LinkID [LinkIDLen]byte
	Random byte
}

// Marshal serializes a keepalive packet (type byte + payload).
func (kp *KeepalivePacket) Marshal() []byte {
	buf := make([]byte, 1+KeepalivePacketLen)
	buf[0] = BridgeKeepalive
	copy(buf[1:], kp.LinkID[:])
	buf[1+LinkIDLen] = kp.Random
	return buf
}

// UnmarshalKeepalive parses a keepalive packet.
func UnmarshalKeepalive(data []byte) (*KeepalivePacket, error) {
	if len(data) < 1+KeepalivePacketLen {
		return nil, ErrTooShort
	}
	if data[0] != BridgeKeepalive {
		return nil, ErrWrongType
	}
	kp := &KeepalivePacket{}
	copy(kp.LinkID[:], data[1:1+LinkIDLen])
	kp.Random = data[1+LinkIDLen]
	return kp, nil
}

// MaxLinksForBandwidth returns the maximum concurrent links that fit within
// the given bandwidth budget at the given capacity percentage.
func MaxLinksForBandwidth(bandwidthBps int, capacityPct float64) int {
	if bandwidthBps <= 0 || capacityPct <= 0 {
		return 0
	}
	budget := float64(bandwidthBps) * capacityPct / 100.0
	return int(budget / KeepaliveBitsPerSec)
}
