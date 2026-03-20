package transport

import (
	"encoding/binary"
	"fmt"
)

// Astronode S binary protocol commands
const (
	AstroCmdCfgWR  uint8 = 0x05 // Write configuration
	AstroCmdNcoRR  uint8 = 0x18 // Read next contact opportunity
	AstroCmdPldER  uint8 = 0x25 // Enqueue uplink payload
	AstroCmdPldDR  uint8 = 0x26 // Dequeue downlink payload
	AstroCmdPldFR  uint8 = 0x27 // Free (ack) downlink slot
	AstroCmdGeoWR  uint8 = 0x35 // Write geolocation
	AstroCmdSakRR  uint8 = 0x45 // Read satellite ACK
	AstroCmdSakCR  uint8 = 0x46 // Clear satellite ACK
	AstroCmdCmdRR  uint8 = 0x47 // Read downlink command
	AstroCmdCmdCR  uint8 = 0x48 // Clear downlink command
	AstroCmdResetR uint8 = 0x55 // Reset module
	AstroCmdEvtRR  uint8 = 0x65 // Read event register
	AstroCmdSatSR  uint8 = 0x66 // Satellite search status
	AstroCmdPerRR  uint8 = 0x67 // Read performance counters
	AstroCmdMstRR  uint8 = 0x69 // Read module state
	AstroCmdLcdRR  uint8 = 0x6A // Read last contact details
	AstroCmdEndRR  uint8 = 0x6B // Read environment details
)

// Astronode event types from EVT_RR
const (
	AstroEvtSatDetected   uint8 = 0x01
	AstroEvtUplinkDone    uint8 = 0x02
	AstroEvtDownlinkReady uint8 = 0x04
	AstroEvtReset         uint8 = 0x08
)

// AstroFrame represents a binary frame for the Astronode S protocol.
type AstroFrame struct {
	CommandID uint8
	Payload   []byte
}

// EncodeAstroFrame encodes a command into the Astronode binary frame format:
// [STX(0x02)] [LEN:2 LE] [CMD:1] [PAYLOAD:N] [CRC16:2 LE] [ETX(0x03)]
func EncodeAstroFrame(cmd uint8, payload []byte) []byte {
	dataLen := 1 + len(payload) // cmd + payload
	frame := make([]byte, 0, 1+2+dataLen+2+1)

	frame = append(frame, 0x02)                                      // STX
	frame = append(frame, byte(dataLen&0xFF), byte(dataLen>>8&0xFF)) // LEN (LE)
	frame = append(frame, cmd)                                       // CMD
	frame = append(frame, payload...)                                // PAYLOAD

	// CRC16 over CMD + PAYLOAD
	crc := CRC16CCITT(frame[3 : 3+dataLen])
	frame = append(frame, byte(crc&0xFF), byte(crc>>8&0xFF)) // CRC (LE)
	frame = append(frame, 0x03)                              // ETX

	return frame
}

// DecodeAstroFrame decodes a binary frame from the Astronode S module.
// Returns the command ID and payload, or error if frame is malformed.
func DecodeAstroFrame(data []byte) (*AstroFrame, error) {
	if len(data) < 6 { // STX + LEN(2) + CMD(1) + CRC(2) + ETX = minimum 7, but cmd alone = 6 usable
		return nil, fmt.Errorf("astro frame too short: %d bytes", len(data))
	}
	if data[0] != 0x02 {
		return nil, fmt.Errorf("astro frame missing STX (got 0x%02x)", data[0])
	}
	if data[len(data)-1] != 0x03 {
		return nil, fmt.Errorf("astro frame missing ETX (got 0x%02x)", data[len(data)-1])
	}

	dataLen := int(binary.LittleEndian.Uint16(data[1:3]))
	if 3+dataLen+2+1 != len(data) {
		return nil, fmt.Errorf("astro frame length mismatch: header says %d, got %d total", dataLen, len(data))
	}

	// Verify CRC
	crcData := data[3 : 3+dataLen]
	expectedCRC := CRC16CCITT(crcData)
	gotCRC := binary.LittleEndian.Uint16(data[3+dataLen : 3+dataLen+2])
	if expectedCRC != gotCRC {
		return nil, fmt.Errorf("astro frame CRC mismatch: expected 0x%04x, got 0x%04x", expectedCRC, gotCRC)
	}

	return &AstroFrame{
		CommandID: data[3],
		Payload:   data[4 : 3+dataLen],
	}, nil
}

// CRC16CCITT computes CRC-16/CCITT-FALSE (poly 0x1021, init 0xFFFF).
func CRC16CCITT(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Fragment header format (1 byte):
// [MSG_ID:4bit][FRAG_NUM:2bit][FRAG_TOTAL:2bit]
// MSG_ID: 0-15 (wrapping counter)
// FRAG_NUM: 0-3 (fragment index)
// FRAG_TOTAL: 0-3 (total fragments - 1, so 1=0, 2=1, 3=2, 4=3)

const (
	// AstroMaxUplink is the max uplink payload for Astronode S.
	AstroMaxUplink = 160
	// AstroMaxDownlink is the max downlink payload.
	AstroMaxDownlink = 40
	// AstroFragPayload is max payload per fragment (uplink minus 1-byte header).
	AstroFragPayload = AstroMaxUplink - 1
)

// AstroFragment represents one fragment of a larger message.
type AstroFragment struct {
	MsgID     uint8 // 0-15
	FragNum   uint8 // 0-3
	FragTotal uint8 // 1-4 (actual count)
	Payload   []byte
}

// EncodeFragmentHeader encodes the 1-byte fragment header.
func EncodeFragmentHeader(msgID, fragNum, fragTotal uint8) byte {
	return (msgID << 4) | ((fragNum & 0x03) << 2) | ((fragTotal - 1) & 0x03)
}

// DecodeFragmentHeader decodes the 1-byte fragment header.
func DecodeFragmentHeader(b byte) (msgID, fragNum, fragTotal uint8) {
	msgID = b >> 4
	fragNum = (b >> 2) & 0x03
	fragTotal = (b & 0x03) + 1
	return
}

// FragmentMessage splits a message into fragments for Astrocast uplink.
// Each fragment is at most AstroMaxUplink bytes (1 header + up to 159 payload).
// Returns nil if the message fits in a single uplink (no fragmentation needed).
func FragmentMessage(msgID uint8, data []byte) [][]byte {
	if len(data) <= AstroMaxUplink {
		// No fragmentation needed — send as-is (no header overhead)
		return nil
	}

	numFrags := (len(data) + AstroFragPayload - 1) / AstroFragPayload
	if numFrags > 4 {
		numFrags = 4 // max 4 fragments (4 * 159 = 636 bytes)
		data = data[:4*AstroFragPayload]
	}

	fragments := make([][]byte, numFrags)
	for i := 0; i < numFrags; i++ {
		start := i * AstroFragPayload
		end := start + AstroFragPayload
		if end > len(data) {
			end = len(data)
		}

		frag := make([]byte, 1+end-start)
		frag[0] = EncodeFragmentHeader(msgID&0x0F, uint8(i), uint8(numFrags))
		copy(frag[1:], data[start:end])
		fragments[i] = frag
	}
	return fragments
}

// ReassemblyBuffer collects fragments and reassembles complete messages.
type ReassemblyBuffer struct {
	pending map[uint8]*reassemblyEntry // keyed by msgID
}

type reassemblyEntry struct {
	fragments map[uint8][]byte // keyed by fragNum
	total     uint8
	created   int64 // unix timestamp
}

// NewReassemblyBuffer creates a new fragment reassembly buffer.
func NewReassemblyBuffer() *ReassemblyBuffer {
	return &ReassemblyBuffer{
		pending: make(map[uint8]*reassemblyEntry),
	}
}

// AddFragment adds a fragment and returns the reassembled message if complete.
// Returns nil if more fragments are needed.
func (rb *ReassemblyBuffer) AddFragment(frag AstroFragment, now int64) []byte {
	entry, ok := rb.pending[frag.MsgID]
	if !ok {
		entry = &reassemblyEntry{
			fragments: make(map[uint8][]byte),
			total:     frag.FragTotal,
			created:   now,
		}
		rb.pending[frag.MsgID] = entry
	}

	entry.fragments[frag.FragNum] = frag.Payload

	if uint8(len(entry.fragments)) < entry.total {
		return nil // more fragments needed
	}

	// Reassemble in order
	var result []byte
	for i := uint8(0); i < entry.total; i++ {
		result = append(result, entry.fragments[i]...)
	}
	delete(rb.pending, frag.MsgID)
	return result
}

// Expire removes entries older than maxAge seconds.
func (rb *ReassemblyBuffer) Expire(now int64, maxAgeSec int64) {
	for id, entry := range rb.pending {
		if now-entry.created > maxAgeSec {
			delete(rb.pending, id)
		}
	}
}
