package hemb

import (
	"errors"
	"fmt"
	"io"
)

// CodedSymbol is a single RLNC-coded symbol — a linear combination of K
// source segments with random GF(256) coefficients.
type CodedSymbol struct {
	GenID        uint16
	SymbolIndex  int
	K            int    // number of source segments
	Coefficients []byte // K bytes — GF(256) coefficients
	Data         []byte // coded payload = sum(coeff[j] * segment[j])
}

// ErrNotDecodable is returned when TryDecode has insufficient independent symbols.
var ErrNotDecodable = errors.New("hemb: not decodable — insufficient independent symbols")

// EncodeGeneration takes K source segments and produces N coded symbols.
// Each coded symbol has K random GF(256) coefficients; its payload is the
// corresponding linear combination of the source segments.
// All segments must have equal length. N must be >= K.
// The rand parameter provides randomness for coefficient generation.
func EncodeGeneration(genID uint16, segments [][]byte, n int, r io.Reader) ([]CodedSymbol, error) {
	k := len(segments)
	if k == 0 {
		return nil, nil
	}
	if n < k {
		return nil, fmt.Errorf("hemb: N=%d < K=%d", n, k)
	}
	if k > 255 {
		return nil, fmt.Errorf("hemb: K=%d exceeds maximum 255", k)
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

	symbols := make([]CodedSymbol, n)
	for i := 0; i < n; i++ {
		coefficients := make([]byte, k)
		if _, err := io.ReadFull(r, coefficients); err != nil {
			return nil, fmt.Errorf("hemb: read random coefficients: %w", err)
		}

		// Ensure at least one coefficient is non-zero.
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

		// Compute linear combination: payload = sum(coeff[j] * padded[j])
		payload := make([]byte, payloadSize)
		for j := 0; j < k; j++ {
			if coefficients[j] == 0 {
				continue
			}
			for b := 0; b < payloadSize; b++ {
				payload[b] = gfAdd(payload[b], gfMul(coefficients[j], padded[j][b]))
			}
		}

		symbols[i] = CodedSymbol{
			GenID:        genID,
			SymbolIndex:  i,
			K:            k,
			Coefficients: coefficients,
			Data:         payload,
		}
	}

	return symbols, nil
}

// rlncGeneration tracks decoding state for one generation of coded symbols.
type rlncGeneration struct {
	id          uint16
	k           int
	payloadSize int
	received    []CodedSymbol
	decoded     bool
	decodedData [][]byte
}

func newRLNCGeneration(id uint16, k int, payloadSize int) *rlncGeneration {
	return &rlncGeneration{id: id, k: k, payloadSize: payloadSize}
}

// addSymbol adds a coded symbol. Returns true if enough symbols have been
// collected to attempt decoding (len(received) >= K).
func (g *rlncGeneration) addSymbol(sym CodedSymbol) bool {
	if g.decoded {
		return true
	}
	g.received = append(g.received, sym)
	return len(g.received) >= g.k
}

// TryDecode attempts Gaussian elimination on received coded symbols.
// Returns K decoded segment payloads on success, ErrNotDecodable if
// the received symbols are not linearly independent (rank < K).
func TryDecode(symbols []CodedSymbol, k int) ([][]byte, error) {
	if len(symbols) < k {
		return nil, fmt.Errorf("%w: have %d symbols, need %d", ErrNotDecodable, len(symbols), k)
	}

	n := len(symbols)
	_ = len(symbols[0].Data) // payloadLen used in later phase

	// Build N-by-K coefficient matrix and N payload vectors.
	coeffs := newGFMatrix(n, k)
	payloads := make([][]byte, n)
	for i, sym := range symbols {
		for j := 0; j < k; j++ {
			coeffs.set(i, j, sym.Coefficients[j])
		}
		payloads[i] = sym.Data
	}

	decoded, err := gaussianEliminate(coeffs, payloads)
	if err != nil {
		if errors.Is(err, ErrRankDeficient) {
			return nil, fmt.Errorf("%w: %v", ErrNotDecodable, err)
		}
		return nil, err
	}

	return decoded, nil
}
