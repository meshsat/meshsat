package engine

import (
	"testing"

	"meshsat/internal/transport"
)

// crc8Poly07 is the CRC-8 (poly 0x07, init 0) HeMB v1's compact header used.
func crc8Poly07(data []byte) byte {
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

// HeMB v1's compact-header check had no magic: any routing packet of 8 bytes
// or more whose byte 7 was the CRC-8 of bytes 0-6 and whose top two bits were
// 00 was taken for a HeMB symbol and dropped, before the time-sync, custody
// and Reticulum handling, whether HeMB was on or not (about 1 in 256, and for
// a Reticulum HEADER_1 packet the same destination and hop count every time).
// With HeMB v1 gone such a packet reaches its handler. [MESHSAT-1763]
func TestRoutingPacketWithCRCCoincidenceIsNotDropped(t *testing.T) {
	p := NewProcessor(nil, nil)
	var got []byte
	p.SetTimeSyncHandler(func(data []byte, _ string) { got = data })

	pkt := []byte{0x14, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x00, 0xAA, 0xBB, 0xCC, 0xDD}
	pkt[7] = crc8Poly07(pkt[:7])
	p.handleRoutingPacket(transport.MeshEvent{}, pkt, "mesh_0")

	if got == nil {
		t.Fatal("a time-sync request whose byte 7 is the CRC-8 of bytes 0-6 never reached its handler")
	}
}
