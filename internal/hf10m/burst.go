package hf10m

import (
	"errors"
	"fmt"
)

// Burst framing constants (recipe section "Burst").
var (
	// Preamble is the click-track, 8 x 0x55.
	Preamble = []byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55}
	// UniqueWord is the 63-bit m-sequence (x^6 + x + 1, all-ones init) padded with a 0.
	UniqueWord = []byte{0xFD, 0x59, 0xBB, 0x49, 0xC5, 0xE5, 0x18, 0x40}
	// LegacyUniqueWord was used by older shouts.
	LegacyUniqueWord = []byte{0x2E, 0xFC, 0x37, 0x49}
	// Tail is the timing pad after the coded payload.
	Tail = []byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55}
	// CostasOffsetsHz are the seven wake-up tones, offsets from the centre.
	CostasOffsetsHz = []float64{50, -150, 150, -250, 350, 250, -50}
)

const (
	MaxBlocks     = 40
	InterleaveWay = 16
	BlockBytes    = LDPCN / 8
)

var (
	ErrBlocks    = errors.New("hf10m: block count out of range 1..40")
	ErrBurstSize = errors.New("hf10m: coded payload length does not match the block count")
)

// bytesToBits unpacks MSB first (numpy.unpackbits order).
func bytesToBits(b []byte) []uint8 {
	out := make([]uint8, 0, len(b)*8)
	for _, x := range b {
		for i := 7; i >= 0; i-- {
			out = append(out, (x>>uint(i))&1)
		}
	}
	return out
}

// bitsToBytes packs MSB first, zero-padding the last byte.
func bitsToBytes(bits []uint8) []byte {
	out := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		if b&1 == 1 {
			out[i/8] |= 0x80 >> uint(i%8)
		}
	}
	return out
}

// interleave writes row-major into rows x 16 and reads column-major
// (reshape(rows, 16).T.ravel()).
func interleave(bits []uint8) []uint8 {
	rows := len(bits) / InterleaveWay
	out := make([]uint8, len(bits))
	k := 0
	for c := 0; c < InterleaveWay; c++ {
		for r := 0; r < rows; r++ {
			out[k] = bits[r*InterleaveWay+c]
			k++
		}
	}
	return out
}

// deinterleave inverts interleave.
func deinterleave(bits []uint8) []uint8 {
	rows := len(bits) / InterleaveWay
	out := make([]uint8, len(bits))
	k := 0
	for c := 0; c < InterleaveWay; c++ {
		for r := 0; r < rows; r++ {
			out[r*InterleaveWay+c] = bits[k]
			k++
		}
	}
	return out
}

// EncodeBlocks LDPC-wraps an inner frame: zero-pad to a multiple of 64 bits,
// encode each 64-bit word to 128 bits, interleave 16 ways. Returns the
// block count and the coded bytes (n x 16).
func EncodeBlocks(frame []byte) (n int, coded []byte, err error) {
	bits := bytesToBits(frame)
	for len(bits)%LDPCK != 0 {
		bits = append(bits, 0)
	}
	n = len(bits) / LDPCK
	if n < 1 || n > MaxBlocks {
		return 0, nil, ErrBlocks
	}
	all := make([]uint8, 0, n*LDPCN)
	for i := 0; i < n; i++ {
		cw := Encode(bits[i*LDPCK : (i+1)*LDPCK])
		all = append(all, cw[:]...)
	}
	return n, bitsToBytes(interleave(all)), nil
}

// DecodeBlocksHard decodes coded bytes with hard decisions (a clean
// channel or a test); soft decoding is DecodeBlocksLLR.
func DecodeBlocksHard(n int, coded []byte) ([]byte, error) {
	llr := make([]float64, len(coded)*8)
	for i, b := range bytesToBits(coded) {
		if b == 1 {
			llr[i] = -4
		} else {
			llr[i] = 4
		}
	}
	return DecodeBlocksLLR(n, llr)
}

// DecodeBlocksLLR deinterleaves n x 128 log-likelihood ratios, decodes each
// block and returns the recovered inner-frame bytes (padding included). A
// block whose checks never converge is still returned with its hard
// decision; the CRC has the last word.
func DecodeBlocksLLR(n int, llr []float64) ([]byte, error) {
	if n < 1 || n > MaxBlocks {
		return nil, ErrBlocks
	}
	if len(llr) != n*LDPCN {
		return nil, fmt.Errorf("%w: %d soft bits for %d blocks", ErrBurstSize, len(llr), n)
	}
	// Deinterleave the soft values with the same permutation as the bits.
	rows := len(llr) / InterleaveWay
	de := make([]float64, len(llr))
	k := 0
	for c := 0; c < InterleaveWay; c++ {
		for r := 0; r < rows; r++ {
			de[r*InterleaveWay+c] = llr[k]
			k++
		}
	}
	info := make([]uint8, 0, n*LDPCK)
	for i := 0; i < n; i++ {
		cw, _ := Decode(de[i*LDPCN:(i+1)*LDPCN], 40)
		info = append(info, InfoBits(cw[:])...)
	}
	return bitsToBytes(info), nil
}

// BuildBurst returns the byte stream after the Costas tones: preamble,
// unique word, three copies of the block count, coded blocks, tail.
func BuildBurst(frame []byte) ([]byte, int, error) {
	n, coded, err := EncodeBlocks(frame)
	if err != nil {
		return nil, 0, err
	}
	out := make([]byte, 0, len(Preamble)+len(UniqueWord)+3+len(coded)+len(Tail))
	out = append(out, Preamble...)
	out = append(out, UniqueWord...)
	out = append(out, byte(n), byte(n), byte(n))
	out = append(out, coded...)
	out = append(out, Tail...)
	return out, n, nil
}

// MajorityBlockCount votes three block-count bytes.
func MajorityBlockCount(a, b, c byte) (int, bool) {
	switch {
	case a == b || a == c:
		return int(a), a >= 1 && a <= MaxBlocks
	case b == c:
		return int(b), b >= 1 && b <= MaxBlocks
	}
	return 0, false
}

// ParseBurst reads a burst byte stream that starts at the unique word
// (preamble already consumed by the receiver) and returns the inner frame.
func ParseBurst(afterPreamble []byte) (*Frame, error) {
	if len(afterPreamble) < len(UniqueWord)+3 {
		return nil, ErrFrameShort
	}
	p := afterPreamble
	if string(p[:len(UniqueWord)]) != string(UniqueWord) {
		return nil, errors.New("hf10m: unique word not found")
	}
	p = p[len(UniqueWord):]
	n, ok := MajorityBlockCount(p[0], p[1], p[2])
	if !ok {
		return nil, ErrBlocks
	}
	p = p[3:]
	if len(p) < n*BlockBytes {
		return nil, ErrFrameShort
	}
	raw, err := DecodeBlocksHard(n, p[:n*BlockBytes])
	if err != nil {
		return nil, err
	}
	return Unmarshal(raw)
}
