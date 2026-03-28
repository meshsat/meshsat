package routing

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// RLNCVersion is the wire format version for RLNC coded packets.
const RLNCVersion byte = 0x01

// Wire format header sizes.
const (
	rlncHashLen      = 32                      // SHA-256 resource hash
	rlncFixedHdrLen  = rlncHashLen + 1 + 2 + 1 // hash + version + generation_id + K
	rlncMinPacketLen = rlncFixedHdrLen + 1 + 1 // at least 1 coeff + 1 byte payload
)

// Errors for RLNC operations.
var (
	ErrRLNCPacketTooShort = errors.New("rlnc: packet too short")
	ErrRLNCBadVersion     = errors.New("rlnc: unsupported version")
	ErrRLNCBadK           = errors.New("rlnc: K must be > 0")
	ErrRLNCNotDecodable   = errors.New("rlnc: not enough independent packets to decode")
)

// RLNCCodedPacket is a network-coded packet combining K source segments
// using random GF(256) coefficients.
//
// Wire format:
//
//	[32B resource_hash][1B version][2B generation_id LE][1B K][K bytes coefficients][payload]
type RLNCCodedPacket struct {
	ResourceHash [32]byte
	Version      byte
	GenerationID uint16
	K            byte
	Coefficients []byte // K random GF(256) coefficients
	Payload      []byte // linear combination of K segment payloads
}

// MarshalRLNCPacket serializes a coded packet to wire format.
func MarshalRLNCPacket(pkt *RLNCCodedPacket) []byte {
	k := int(pkt.K)
	totalLen := rlncFixedHdrLen + k + len(pkt.Payload)
	buf := make([]byte, totalLen)

	copy(buf[:rlncHashLen], pkt.ResourceHash[:])
	buf[rlncHashLen] = pkt.Version
	binary.LittleEndian.PutUint16(buf[rlncHashLen+1:], pkt.GenerationID)
	buf[rlncHashLen+3] = pkt.K
	copy(buf[rlncFixedHdrLen:], pkt.Coefficients)
	copy(buf[rlncFixedHdrLen+k:], pkt.Payload)

	return buf
}

// UnmarshalRLNCPacket deserializes a coded packet from wire format.
func UnmarshalRLNCPacket(data []byte) (*RLNCCodedPacket, error) {
	if len(data) < rlncMinPacketLen {
		return nil, fmt.Errorf("%w: got %d bytes, need at least %d", ErrRLNCPacketTooShort, len(data), rlncMinPacketLen)
	}

	pkt := &RLNCCodedPacket{}
	copy(pkt.ResourceHash[:], data[:rlncHashLen])
	pkt.Version = data[rlncHashLen]
	if pkt.Version != RLNCVersion {
		return nil, fmt.Errorf("%w: got 0x%02x", ErrRLNCBadVersion, pkt.Version)
	}
	pkt.GenerationID = binary.LittleEndian.Uint16(data[rlncHashLen+1:])
	pkt.K = data[rlncHashLen+3]
	if pkt.K == 0 {
		return nil, ErrRLNCBadK
	}

	k := int(pkt.K)
	coeffEnd := rlncFixedHdrLen + k
	if len(data) < coeffEnd+1 {
		return nil, fmt.Errorf("%w: need %d bytes for K=%d coefficients + payload", ErrRLNCPacketTooShort, coeffEnd+1, k)
	}

	pkt.Coefficients = make([]byte, k)
	copy(pkt.Coefficients, data[rlncFixedHdrLen:coeffEnd])

	pkt.Payload = make([]byte, len(data)-coeffEnd)
	copy(pkt.Payload, data[coeffEnd:])

	return pkt, nil
}

// RLNCGeneration tracks the decoding state for one generation of coded packets.
// A generation contains K original segments; coded packets are linear combinations
// of those segments with random GF(256) coefficients.
type RLNCGeneration struct {
	ID          uint16
	K           int
	PayloadSize int
	Received    []*RLNCCodedPacket
	Decoded     bool
	DecodedData [][]byte // K decoded segment payloads (nil until Decoded is true)
}

// NewRLNCGeneration creates a generation tracker for K source segments.
func NewRLNCGeneration(id uint16, k int, payloadSize int) *RLNCGeneration {
	return &RLNCGeneration{
		ID:          id,
		K:           k,
		PayloadSize: payloadSize,
	}
}

// AddPacket adds a coded packet to the generation.
// Returns true if the generation now has enough packets to attempt decoding
// (i.e., len(Received) >= K).
func (g *RLNCGeneration) AddPacket(pkt *RLNCCodedPacket) bool {
	if g.Decoded {
		return true
	}
	g.Received = append(g.Received, pkt)
	return len(g.Received) >= g.K
}

// TryDecode attempts Gaussian elimination when enough independent packets
// have been received. Returns K decoded segment payloads on success.
// Returns ErrRLNCNotDecodable if the received packets are not linearly independent.
func (g *RLNCGeneration) TryDecode() ([][]byte, error) {
	if g.Decoded {
		return g.DecodedData, nil
	}
	if len(g.Received) < g.K {
		return nil, fmt.Errorf("%w: have %d packets, need %d", ErrRLNCNotDecodable, len(g.Received), g.K)
	}

	n := len(g.Received)
	k := g.K

	// Build the N-by-K coefficient matrix and N payload vectors.
	coeffs := NewGFMatrix(n, k)
	payloads := make([][]byte, n)

	for i, pkt := range g.Received {
		for j := 0; j < k; j++ {
			coeffs.Set(i, j, pkt.Coefficients[j])
		}
		payloads[i] = pkt.Payload
	}

	decoded, err := GaussianEliminate(coeffs, payloads)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRLNCNotDecodable, err)
	}

	g.Decoded = true
	g.DecodedData = decoded
	return decoded, nil
}

// EncodeGeneration creates N coded packets from K original segment payloads.
// N = ceil(K * redundancy). Each coded packet has K random GF(256) coefficients;
// its payload is the corresponding linear combination of the source segments.
// All segments must have equal length. redundancy must be >= 1.0.
func EncodeGeneration(genID uint16, resourceHash [32]byte, segments [][]byte, redundancy float64) []*RLNCCodedPacket {
	k := len(segments)
	if k == 0 {
		return nil
	}
	if redundancy < 1.0 {
		redundancy = 1.0
	}

	// Pad shorter segments to uniform length.
	payloadSize := 0
	for _, seg := range segments {
		if len(seg) > payloadSize {
			payloadSize = len(seg)
		}
	}
	padded := make([][]byte, k)
	for i, seg := range segments {
		padded[i] = make([]byte, payloadSize)
		copy(padded[i], seg)
	}

	// Number of coded packets to generate.
	n := int(float64(k)*redundancy + 0.999999) // ceiling
	if n < k {
		n = k
	}

	packets := make([]*RLNCCodedPacket, n)
	for i := 0; i < n; i++ {
		coefficients := make([]byte, k)
		randBytes(coefficients)

		// Ensure at least one coefficient is non-zero to avoid a zero packet.
		allZero := true
		for _, c := range coefficients {
			if c != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			coefficients[0] = 1
		}

		// Compute the linear combination: payload = sum(coefficients[j] * padded[j])
		payload := make([]byte, payloadSize)
		for j := 0; j < k; j++ {
			if coefficients[j] == 0 {
				continue
			}
			for b := 0; b < payloadSize; b++ {
				payload[b] = gfAdd(payload[b], gfMul(coefficients[j], padded[j][b]))
			}
		}

		packets[i] = &RLNCCodedPacket{
			ResourceHash: resourceHash,
			Version:      RLNCVersion,
			GenerationID: genID,
			K:            byte(k),
			Coefficients: coefficients,
			Payload:      payload,
		}
	}

	return packets
}
