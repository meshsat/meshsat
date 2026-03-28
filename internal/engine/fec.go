package engine

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"

	"github.com/klauspost/reedsolomon"
)

// FEC (Forward Error Correction) transform stage using Reed-Solomon coding.
// Protects payloads against bit-level corruption on noisy channels (LoRa at
// range, degraded satellite). The encoded format is self-describing: the
// receiver reads k, m, and shard size from a 5-byte header, so no out-of-band
// configuration is needed for decode.

// FECVersion1 is the original wire format version.
const FECVersion1 byte = 0x01

// FECVersion2 adds a flags byte for interleave parameters.
const FECVersion2 byte = 0x02

// FECVersion is the current default encode version.
const FECVersion = FECVersion2

// fecHeaderLenV1 is the fixed header size for v1: version+k+m+shardSize(2).
const fecHeaderLenV1 = 5

// fecHeaderLenV2 is the fixed header size for v2: version+k+m+shardSize(2)+flags(1).
const fecHeaderLenV2 = 6

// fecHeaderLen is the minimum header size (v1 compatible).
const fecHeaderLen = fecHeaderLenV1

// FEC flags byte (v2):
//
//	bit 0:    interleaved (1 = yes)
//	bits 1-4: interleave_depth encoding (0-15 → depth 1-16)
//	bits 5-7: reserved (must be 0)
const (
	fecFlagInterleaved byte = 0x01
)

// fecOrigLenTrailer is the size of the original-length trailer (4 bytes LE)
// embedded at the start of the encoded payload, before the shards.
const fecOrigLenTrailer = 4

// FECMetrics tracks encode/decode statistics.
type FECMetrics struct {
	EncodeOK        atomic.Int64
	EncodeFail      atomic.Int64
	DecodeOK        atomic.Int64
	DecodeFail      atomic.Int64
	ShardsRecovered atomic.Int64
}

// fecEncodeOpts holds optional parameters for FEC encoding.
type fecEncodeOpts struct {
	interleave      bool
	interleaveDepth int
}

// fecEncode applies Reed-Solomon FEC to data, producing a self-describing
// encoded v2 payload: [header(6B)][origLen(4B LE)][k+m shards of equal size].
//
// dataShards (k) and parityShards (m) control the redundancy ratio.
// Up to m erasures can be recovered.
func fecEncode(data []byte, dataShards, parityShards int, opts ...fecEncodeOpts) ([]byte, error) {
	if dataShards < 1 || dataShards > 255 {
		return nil, fmt.Errorf("fec: data_shards must be 1-255 (got %d)", dataShards)
	}
	if parityShards < 1 || parityShards > 255 {
		return nil, fmt.Errorf("fec: parity_shards must be 1-255 (got %d)", parityShards)
	}
	if dataShards+parityShards > 256 {
		return nil, fmt.Errorf("fec: data_shards + parity_shards must be <= 256")
	}

	var opt fecEncodeOpts
	if len(opts) > 0 {
		opt = opts[0]
	}

	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		return nil, fmt.Errorf("fec: create encoder: %w", err)
	}

	// Prepend 4-byte original length so we can strip padding on decode.
	origLen := len(data)
	prefixed := make([]byte, fecOrigLenTrailer+origLen)
	binary.LittleEndian.PutUint32(prefixed[:4], uint32(origLen))
	copy(prefixed[4:], data)

	// Pad to a multiple of dataShards.
	padded := prefixed
	rem := len(padded) % dataShards
	if rem != 0 {
		padded = append(padded, make([]byte, dataShards-rem)...)
	}

	shardSize := len(padded) / dataShards

	// Split into data shards.
	shards := make([][]byte, dataShards+parityShards)
	for i := 0; i < dataShards; i++ {
		shards[i] = padded[i*shardSize : (i+1)*shardSize]
	}
	// Allocate parity shards.
	for i := dataShards; i < dataShards+parityShards; i++ {
		shards[i] = make([]byte, shardSize)
	}

	// Encode parity.
	if err := enc.Encode(shards); err != nil {
		return nil, fmt.Errorf("fec: encode: %w", err)
	}

	if shardSize > 65535 {
		return nil, fmt.Errorf("fec: shard size %d exceeds uint16 max", shardSize)
	}

	// Build v2 wire format: header(6B) + all shards concatenated.
	var flags byte
	if opt.interleave && opt.interleaveDepth > 0 {
		flags |= fecFlagInterleaved
		// Encode depth as 0-15 in bits 1-4 (representing depth 1-16).
		depthEnc := opt.interleaveDepth - 1
		if depthEnc < 0 {
			depthEnc = 0
		}
		if depthEnc > 15 {
			depthEnc = 15
		}
		flags |= byte(depthEnc<<1) & 0x1E
	}

	totalShards := len(shards)
	out := make([]byte, fecHeaderLenV2+totalShards*shardSize)
	out[0] = FECVersion2
	out[1] = byte(dataShards)
	out[2] = byte(parityShards)
	binary.LittleEndian.PutUint16(out[3:5], uint16(shardSize))
	out[5] = flags

	// Concatenate all shards.
	off := fecHeaderLenV2
	for _, s := range shards {
		copy(out[off:], s)
		off += shardSize
	}

	// Apply interleaving to the shard data (after header).
	if flags&fecFlagInterleaved != 0 {
		shardData := out[fecHeaderLenV2:]
		interleaved := fecInterleave(shardData, opt.interleaveDepth)
		copy(out[fecHeaderLenV2:], interleaved)
	}

	return out, nil
}

// fecDecode reads the self-describing FEC header (v1 or v2) and reconstructs
// the original data. If all shards are intact no reconstruction is needed; if
// some are missing (nil), up to parityShards can be recovered.
func fecDecode(data []byte) ([]byte, error) {
	if len(data) < fecHeaderLenV1 {
		return nil, fmt.Errorf("fec: data too short for header (%d bytes)", len(data))
	}

	version := data[0]
	if version != FECVersion1 && version != FECVersion2 {
		return nil, fmt.Errorf("fec: unsupported version 0x%02x", version)
	}

	dataShards := int(data[1])
	parityShards := int(data[2])
	shardSize := int(binary.LittleEndian.Uint16(data[3:5]))

	if dataShards < 1 || parityShards < 1 {
		return nil, fmt.Errorf("fec: invalid shard counts k=%d m=%d", dataShards, parityShards)
	}
	if shardSize < 1 {
		return nil, fmt.Errorf("fec: invalid shard size %d", shardSize)
	}

	// Parse version-specific header fields.
	headerLen := fecHeaderLenV1
	var interleaved bool
	var interleaveDepth int

	if version == FECVersion2 {
		if len(data) < fecHeaderLenV2 {
			return nil, fmt.Errorf("fec: v2 data too short for header (%d bytes)", len(data))
		}
		headerLen = fecHeaderLenV2
		flags := data[5]
		interleaved = flags&fecFlagInterleaved != 0
		if interleaved {
			interleaveDepth = int((flags>>1)&0x0F) + 1 // decode 0-15 → 1-16
		}
	}

	totalShards := dataShards + parityShards
	expectedLen := headerLen + totalShards*shardSize
	if len(data) < expectedLen {
		return nil, fmt.Errorf("fec: payload too short (need %d, got %d)", expectedLen, len(data))
	}

	// De-interleave shard data if needed.
	shardData := data[headerLen : headerLen+totalShards*shardSize]
	if interleaved && interleaveDepth > 0 {
		shardData = fecDeinterleave(shardData, interleaveDepth)
	}

	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		return nil, fmt.Errorf("fec: create decoder: %w", err)
	}

	// Split payload into shards.
	shards := make([][]byte, totalShards)
	for i := 0; i < totalShards; i++ {
		shard := make([]byte, shardSize)
		copy(shard, shardData[i*shardSize:(i+1)*shardSize])
		shards[i] = shard
	}

	// Verify and reconstruct if needed.
	ok, err := enc.Verify(shards)
	if err != nil || !ok {
		if err := enc.Reconstruct(shards); err != nil {
			return nil, fmt.Errorf("fec: reconstruct failed: %w", err)
		}
	}

	// Concatenate data shards.
	result := make([]byte, 0, dataShards*shardSize)
	for i := 0; i < dataShards; i++ {
		result = append(result, shards[i]...)
	}

	// Read original length from the 4-byte LE prefix.
	if len(result) < fecOrigLenTrailer {
		return nil, fmt.Errorf("fec: decoded data too short for length prefix")
	}
	origLen := int(binary.LittleEndian.Uint32(result[:fecOrigLenTrailer]))
	result = result[fecOrigLenTrailer:]

	if origLen > len(result) {
		return nil, fmt.Errorf("fec: original length %d exceeds decoded data %d", origLen, len(result))
	}

	return result[:origLen], nil
}

// fecDecodeWithErasures is like fecDecode but accepts a list of shard indices
// that are known to be erased (nil). Used for testing erasure recovery.
func fecDecodeWithErasures(data []byte, erasedIndices []int) ([]byte, int, error) {
	if len(data) < fecHeaderLenV1 {
		return nil, 0, fmt.Errorf("fec: data too short for header (%d bytes)", len(data))
	}

	version := data[0]
	if version != FECVersion1 && version != FECVersion2 {
		return nil, 0, fmt.Errorf("fec: unsupported version 0x%02x", version)
	}

	dataShards := int(data[1])
	parityShards := int(data[2])
	shardSize := int(binary.LittleEndian.Uint16(data[3:5]))

	if dataShards < 1 || parityShards < 1 || shardSize < 1 {
		return nil, 0, fmt.Errorf("fec: invalid params k=%d m=%d s=%d", dataShards, parityShards, shardSize)
	}

	// Parse version-specific fields.
	headerLen := fecHeaderLenV1
	var interleaved bool
	var interleaveDepth int

	if version == FECVersion2 {
		if len(data) < fecHeaderLenV2 {
			return nil, 0, fmt.Errorf("fec: v2 data too short for header (%d bytes)", len(data))
		}
		headerLen = fecHeaderLenV2
		flags := data[5]
		interleaved = flags&fecFlagInterleaved != 0
		if interleaved {
			interleaveDepth = int((flags>>1)&0x0F) + 1
		}
	}

	totalShards := dataShards + parityShards
	expectedLen := headerLen + totalShards*shardSize
	if len(data) < expectedLen {
		return nil, 0, fmt.Errorf("fec: payload too short (need %d, got %d)", expectedLen, len(data))
	}

	// De-interleave if needed.
	shardData := data[headerLen : headerLen+totalShards*shardSize]
	if interleaved && interleaveDepth > 0 {
		shardData = fecDeinterleave(shardData, interleaveDepth)
	}

	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		return nil, 0, fmt.Errorf("fec: create decoder: %w", err)
	}

	// Split payload into shards, nil-out erased ones.
	shards := make([][]byte, totalShards)
	for i := 0; i < totalShards; i++ {
		shard := make([]byte, shardSize)
		copy(shard, shardData[i*shardSize:(i+1)*shardSize])
		shards[i] = shard
	}

	for _, idx := range erasedIndices {
		if idx >= 0 && idx < totalShards {
			shards[idx] = nil
		}
	}

	// Reconstruct erased shards.
	if err := enc.Reconstruct(shards); err != nil {
		return nil, 0, fmt.Errorf("fec: reconstruct failed: %w", err)
	}

	recovered := len(erasedIndices)

	// Concatenate data shards and strip padding.
	result := make([]byte, 0, dataShards*shardSize)
	for i := 0; i < dataShards; i++ {
		result = append(result, shards[i]...)
	}

	if len(result) < fecOrigLenTrailer {
		return nil, 0, fmt.Errorf("fec: decoded data too short for length prefix")
	}
	origLen := int(binary.LittleEndian.Uint32(result[:fecOrigLenTrailer]))
	result = result[fecOrigLenTrailer:]

	if origLen > len(result) {
		return nil, 0, fmt.Errorf("fec: original length %d exceeds decoded data %d", origLen, len(result))
	}

	return result[:origLen], recovered, nil
}

// fecInterleave interleaves shard data by writing bytes in a depth-strided
// pattern. This spreads consecutive shard bytes across the wire, making burst
// errors affect multiple shards instead of concentrating in one (which RS can
// then recover from). depth controls the interleave stride.
func fecInterleave(data []byte, depth int) []byte {
	if depth <= 1 || len(data) == 0 {
		return data
	}
	n := len(data)
	out := make([]byte, n)
	stride := (n + depth - 1) / depth
	idx := 0
	for d := 0; d < depth; d++ {
		for s := 0; s < stride; s++ {
			src := s*depth + d
			if src < n {
				out[idx] = data[src]
				idx++
			}
		}
	}
	return out[:idx]
}

// fecDeinterleave reverses the interleaving applied by fecInterleave.
func fecDeinterleave(data []byte, depth int) []byte {
	if depth <= 1 || len(data) == 0 {
		return data
	}
	n := len(data)
	out := make([]byte, n)
	stride := (n + depth - 1) / depth
	idx := 0
	for d := 0; d < depth; d++ {
		for s := 0; s < stride; s++ {
			dst := s*depth + d
			if dst < n {
				out[dst] = data[idx]
				idx++
			}
		}
	}
	return out[:n]
}

// parseIntParam reads an integer from a transform params map with a default.
func parseIntParam(params map[string]string, key string, defVal int) int {
	s, ok := params[key]
	if !ok || s == "" {
		return defVal
	}
	v := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			v = v*10 + int(c-'0')
		}
	}
	if v == 0 {
		return defVal
	}
	return v
}
