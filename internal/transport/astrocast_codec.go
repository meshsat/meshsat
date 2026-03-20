package transport

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// Astronode S ASCII hex protocol commands (official opcode map)
const (
	AstroCmdCfgWR  uint8 = 0x05 // Write configuration
	AstroCmdWifWR  uint8 = 0x06 // Wi-Fi configuration
	AstroCmdSscWR  uint8 = 0x07 // Satellite search config
	AstroCmdCfgSR  uint8 = 0x10 // Save config to flash
	AstroCmdCfgFR  uint8 = 0x11 // Factory reset
	AstroCmdCfgRR  uint8 = 0x15 // Read configuration
	AstroCmdRtcRR  uint8 = 0x17 // Read RTC time
	AstroCmdNcoRR  uint8 = 0x18 // Read next contact opportunity
	AstroCmdMgiRR  uint8 = 0x19 // Read module GUID
	AstroCmdMsnRR  uint8 = 0x1A // Read serial number
	AstroCmdMpnRR  uint8 = 0x1B // Read product number
	AstroCmdPldER  uint8 = 0x25 // Enqueue uplink payload
	AstroCmdPldDR  uint8 = 0x26 // Dequeue downlink payload
	AstroCmdPldFR  uint8 = 0x27 // Free (ack) downlink slot
	AstroCmdGeoWR  uint8 = 0x35 // Write geolocation
	AstroCmdSakRR  uint8 = 0x45 // Read satellite ACK
	AstroCmdSakCR  uint8 = 0x46 // Clear satellite ACK
	AstroCmdCmdRR  uint8 = 0x47 // Read downlink command
	AstroCmdCmdCR  uint8 = 0x48 // Clear downlink command
	AstroCmdResetR uint8 = 0x55 // Reset module
	AstroCmdGpoSR  uint8 = 0x62 // GPIO output set
	AstroCmdGpiRR  uint8 = 0x63 // GPIO input read
	AstroCmdEvtRR  uint8 = 0x65 // Read event register
	AstroCmdCtxSR  uint8 = 0x66 // Context save (NOT satellite search)
	AstroCmdPerRR  uint8 = 0x67 // Read performance counters
	AstroCmdPerCR  uint8 = 0x68 // Clear performance counters
	AstroCmdMstRR  uint8 = 0x69 // Read module state
	AstroCmdLcdRR  uint8 = 0x6A // Read last contact details
	AstroCmdEndRR  uint8 = 0x6B // Read environment details
)

// Astronode event register bits from EVT_RR (correct per official docs)
const (
	AstroEvtSAKAvail uint8 = 0x01 // Bit 0: satellite acknowledged a queued message
	AstroEvtReset    uint8 = 0x02 // Bit 1: module reset happened
	AstroEvtCmdAvail uint8 = 0x04 // Bit 2: downlink command waiting
	AstroEvtBusy     uint8 = 0x08 // Bit 3: module communicating with satellite
)

// Astronode error codes (2 bytes LE in error response payload)
const (
	AstroErrCRCNotValid     uint16 = 0x0001 // CRC not valid
	AstroErrLengthNotValid  uint16 = 0x0011 // Length not valid
	AstroErrOpcodeNotValid  uint16 = 0x0121 // Opcode not valid
	AstroErrFormatNotValid  uint16 = 0x0601 // Format not valid
	AstroErrFlashWriteFail  uint16 = 0x0611 // Flash writing failed
	AstroErrBufferFull      uint16 = 0x2501 // Buffer full (PLD_ER)
	AstroErrDuplicateID     uint16 = 0x2511 // Duplicate ID
	AstroErrBufferEmpty     uint16 = 0x2601 // Buffer empty (PLD_DR)
	AstroErrInvalidPosition uint16 = 0x3501 // Invalid position (GEO_WR)
	AstroErrNoACK           uint16 = 0x4501 // No ACK (SAK_RR)
	AstroErrNothingToClear  uint16 = 0x4601 // Nothing to clear (SAK_CR)
)

// TLV tag IDs for MST_RR response
const (
	AstroTLVMsgQueued       uint8 = 0x41 // 1 byte: messages queued for uplink
	AstroTLVAckMsgQueued    uint8 = 0x42 // 1 byte: ack messages queued
	AstroTLVLastResetReason uint8 = 0x43 // 1 byte: last reset reason
	AstroTLVUptime          uint8 = 0x44 // 4 bytes: uptime in seconds
)

// TLV tag IDs for LCD_RR response
const (
	AstroTLVStartTime uint8 = 0x51 // 4 bytes: contact start time
	AstroTLVEndTime   uint8 = 0x52 // 4 bytes: contact end time
	AstroTLVPeakRSSI  uint8 = 0x53 // 1 byte: peak RSSI (unsigned)
	AstroTLVPeakTime  uint8 = 0x54 // 4 bytes: peak RSSI time
)

// TLV tag IDs for END_RR response
const (
	AstroTLVLastMACResult   uint8 = 0x61 // 1 byte: last MAC result
	AstroTLVLastRSSI        uint8 = 0x62 // 1 byte: last RSSI (unsigned)
	AstroTLVTimeSinceSatDet uint8 = 0x63 // 4 bytes: seconds since last satellite detection
)

// Answer timeout per official spec
const AstroAnswerTimeoutMs = 1500

// AstroMaxResponse is the maximum response size from the module.
const AstroMaxResponse = 178

// AstroFrame represents a decoded frame from the Astronode S protocol.
type AstroFrame struct {
	CommandID uint8
	Payload   []byte
}

// EncodeAstroFrame encodes a command into the Astronode ASCII hex frame format:
// [STX 0x02] [OPCODE as 2 hex chars] [PAYLOAD as 2N hex chars] [CRC16 as 4 hex chars (byte-swapped)] [ETX 0x03]
// CRC16-CCITT is computed over the RAW opcode + payload bytes, then byte-swapped before hex encoding.
func EncodeAstroFrame(cmd uint8, payload []byte) []byte {
	// Build raw data for CRC: opcode + payload
	rawData := make([]byte, 1+len(payload))
	rawData[0] = cmd
	copy(rawData[1:], payload)

	// CRC16-CCITT over raw bytes
	crc := CRC16CCITT(rawData)

	// Byte-swap CRC: send low byte first, then high byte
	crcSwapped := [2]byte{byte(crc & 0xFF), byte(crc >> 8)}

	// Build ASCII hex frame
	// STX + hex(opcode) + hex(payload) + hex(crc_swapped) + ETX
	hexLen := 2 + len(payload)*2 + 4 // opcode(2) + payload(2N) + crc(4)
	frame := make([]byte, 0, 1+hexLen+1)

	frame = append(frame, 0x02) // STX

	// Encode opcode as 2 uppercase hex chars
	frame = append(frame, hexByte(cmd)...)

	// Encode payload as 2N uppercase hex chars
	for _, b := range payload {
		frame = append(frame, hexByte(b)...)
	}

	// Encode byte-swapped CRC as 4 uppercase hex chars
	frame = append(frame, hexByte(crcSwapped[0])...)
	frame = append(frame, hexByte(crcSwapped[1])...)

	frame = append(frame, 0x03) // ETX

	return frame
}

// DecodeAstroFrame decodes an ASCII hex frame from the Astronode S module.
// Frame format: [STX 0x02] [hex data] [ETX 0x03]
// Hex data contains: opcode(2 chars) + payload(2N chars) + CRC(4 chars, byte-swapped)
func DecodeAstroFrame(data []byte) (*AstroFrame, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("astro frame too short: %d bytes", len(data))
	}
	if data[0] != 0x02 {
		return nil, fmt.Errorf("astro frame missing STX (got 0x%02x)", data[0])
	}
	if data[len(data)-1] != 0x03 {
		return nil, fmt.Errorf("astro frame missing ETX (got 0x%02x)", data[len(data)-1])
	}

	// Extract hex content between STX and ETX
	hexContent := data[1 : len(data)-1]

	// Minimum: opcode(2) + CRC(4) = 6 hex chars
	if len(hexContent) < 6 {
		return nil, fmt.Errorf("astro frame hex content too short: %d chars", len(hexContent))
	}
	if len(hexContent)%2 != 0 {
		return nil, fmt.Errorf("astro frame hex content has odd length: %d", len(hexContent))
	}

	// Decode all hex content to raw bytes
	rawBytes, err := hex.DecodeString(strings.ToUpper(string(hexContent)))
	if err != nil {
		return nil, fmt.Errorf("astro frame hex decode: %w", err)
	}

	// rawBytes = [opcode(1)] [payload(N)] [crc_lo(1)] [crc_hi(1)]
	if len(rawBytes) < 3 { // opcode + 2 CRC bytes minimum
		return nil, fmt.Errorf("astro frame decoded data too short: %d bytes", len(rawBytes))
	}

	// Split: opcode+payload vs CRC
	opcodeAndPayload := rawBytes[:len(rawBytes)-2]
	crcBytes := rawBytes[len(rawBytes)-2:]

	// CRC was byte-swapped: received as [lo, hi], reconstruct as uint16
	gotCRC := uint16(crcBytes[0]) | uint16(crcBytes[1])<<8

	// Compute expected CRC over raw opcode + payload
	expectedCRC := CRC16CCITT(opcodeAndPayload)

	if gotCRC != expectedCRC {
		return nil, fmt.Errorf("astro frame CRC mismatch: expected 0x%04x, got 0x%04x", expectedCRC, gotCRC)
	}

	cmd := opcodeAndPayload[0]
	var payload []byte
	if len(opcodeAndPayload) > 1 {
		payload = opcodeAndPayload[1:]
	}

	return &AstroFrame{
		CommandID: cmd,
		Payload:   payload,
	}, nil
}

// hexByte encodes a single byte as 2 uppercase hex ASCII characters.
func hexByte(b byte) []byte {
	const hexChars = "0123456789ABCDEF"
	return []byte{hexChars[b>>4], hexChars[b&0x0F]}
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

// parseTLV parses TLV-encoded data from an Astronode response payload.
// Returns a map of tag -> value (raw bytes). Each TLV entry is:
// [tag:1] [length:1] [value:N]
func parseTLV(data []byte) (map[uint8][]byte, error) {
	result := make(map[uint8][]byte)
	i := 0
	for i < len(data) {
		if i+2 > len(data) {
			return result, fmt.Errorf("TLV truncated at offset %d: need tag+length", i)
		}
		tag := data[i]
		length := int(data[i+1])
		i += 2
		if i+length > len(data) {
			return result, fmt.Errorf("TLV truncated at offset %d: tag 0x%02x needs %d bytes, have %d", i-2, tag, length, len(data)-i)
		}
		val := make([]byte, length)
		copy(val, data[i:i+length])
		result[tag] = val
		i += length
	}
	return result, nil
}

// tlvUint8 extracts a uint8 value from a TLV map, returning 0 if not found.
func tlvUint8(tlv map[uint8][]byte, tag uint8) uint8 {
	v, ok := tlv[tag]
	if !ok || len(v) < 1 {
		return 0
	}
	return v[0]
}

// tlvUint32LE extracts a uint32 little-endian value from a TLV map, returning 0 if not found.
func tlvUint32LE(tlv map[uint8][]byte, tag uint8) uint32 {
	v, ok := tlv[tag]
	if !ok || len(v) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(v)
}

// IsErrorResponse returns true if the frame is an error response (opcode 0xFF).
func IsErrorResponse(f *AstroFrame) bool {
	return f.CommandID == 0xFF
}

// ParseErrorCode extracts the 2-byte LE error code from an error response payload.
func ParseErrorCode(payload []byte) uint16 {
	if len(payload) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(payload[:2])
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
