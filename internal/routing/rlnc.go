package routing

import (
	"crypto/rand"
	"errors"
	"fmt"
)

// GF(256) arithmetic with irreducible polynomial x^8 + x^4 + x^3 + x + 1 (0x11B).
// This is the standard AES/Reed-Solomon polynomial. Multiplication and inversion
// use precomputed exp/log lookup tables for constant-time performance.

var gfExp [512]byte // anti-log table (doubled for wrap-around convenience)
var gfLog [256]byte // log table

func init() { initGFTables() }

// initGFTables builds exp and log tables using primitive element 0x03.
// Generator 0x02 only has order 51 under polynomial 0x11B, so it is NOT
// primitive. Generator 0x03 (= x+1) has order 255, generating the full
// multiplicative group. This matches the standard AES S-box construction.
func initGFTables() {
	var x uint16 = 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[byte(x)] = byte(i)
		// Multiply x by generator 0x03 in GF(256):
		// 0x03 = (x + 1), so x * 0x03 = x*x + x = (x<<1) ^ x, with reduction.
		hi := x << 1
		if hi >= 256 {
			hi ^= 0x11B
		}
		x = hi ^ x
	}
	// Duplicate exp[0..254] into exp[255..509] so that gfMul can index
	// log[a]+log[b] (which ranges 0..508) without modular reduction.
	for i := 0; i < 255; i++ {
		gfExp[i+255] = gfExp[i]
	}
	// gfLog[0] is left as 0 but is never used (zero is handled separately in gfMul).
	// gfLog[1] = 0 is correct since 3^0 = 1 in GF(256).
}

// gfAdd returns a + b in GF(256). Addition is XOR.
func gfAdd(a, b byte) byte {
	return a ^ b
}

// gfMul returns a * b in GF(256) using exp/log table lookup.
func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// gfInv returns the multiplicative inverse of a in GF(256).
// Panics if a == 0 (zero has no inverse).
func gfInv(a byte) byte {
	if a == 0 {
		panic("gfInv: zero has no inverse in GF(256)")
	}
	return gfExp[255-int(gfLog[a])]
}

// GFMatrix is a matrix over GF(256) in row-major order.
type GFMatrix struct {
	Rows, Cols int
	Data       []byte // Rows*Cols elements
}

// NewGFMatrix creates a zero-initialized matrix over GF(256).
func NewGFMatrix(rows, cols int) *GFMatrix {
	return &GFMatrix{
		Rows: rows,
		Cols: cols,
		Data: make([]byte, rows*cols),
	}
}

// Get returns the element at (row, col).
func (m *GFMatrix) Get(row, col int) byte {
	return m.Data[row*m.Cols+col]
}

// Set sets the element at (row, col).
func (m *GFMatrix) Set(row, col int, val byte) {
	m.Data[row*m.Cols+col] = val
}

// ErrRankDeficient is returned when Gaussian elimination cannot find
// K linearly independent rows (the system is underdetermined).
var ErrRankDeficient = errors.New("rlnc: rank deficient — not enough independent packets")

// GaussianEliminate solves the system coeffs * X = payloads over GF(256).
// coeffs is an N-by-K matrix (N >= K rows), payloads is N slices of equal length.
// Returns K decoded payload slices, or ErrRankDeficient if rank < K.
//
// The algorithm performs row reduction with partial pivoting on [coeffs | payloads]
// to extract the original K segments.
func GaussianEliminate(coeffs *GFMatrix, payloads [][]byte) ([][]byte, error) {
	n := coeffs.Rows
	k := coeffs.Cols
	if n < k {
		return nil, fmt.Errorf("%w: have %d rows, need %d", ErrRankDeficient, n, k)
	}
	if len(payloads) != n {
		return nil, fmt.Errorf("rlnc: payload count %d != row count %d", len(payloads), n)
	}
	if k == 0 {
		return nil, nil
	}

	payloadLen := len(payloads[0])
	for i := 1; i < n; i++ {
		if len(payloads[i]) != payloadLen {
			return nil, fmt.Errorf("rlnc: payload %d has length %d, expected %d", i, len(payloads[i]), payloadLen)
		}
	}

	// Deep copy coefficients and payloads so we don't mutate the originals.
	mat := NewGFMatrix(n, k)
	copy(mat.Data, coeffs.Data)

	pld := make([][]byte, n)
	for i := 0; i < n; i++ {
		pld[i] = make([]byte, payloadLen)
		copy(pld[i], payloads[i])
	}

	// Forward elimination with partial pivoting.
	for col := 0; col < k; col++ {
		// Find pivot row (first non-zero in this column at or below diagonal).
		pivotRow := -1
		for row := col; row < n; row++ {
			if mat.Get(row, col) != 0 {
				pivotRow = row
				break
			}
		}
		if pivotRow < 0 {
			return nil, fmt.Errorf("%w: column %d has no pivot", ErrRankDeficient, col)
		}

		// Swap pivot row into position.
		if pivotRow != col {
			for c := 0; c < k; c++ {
				mat.Data[col*k+c], mat.Data[pivotRow*k+c] = mat.Data[pivotRow*k+c], mat.Data[col*k+c]
			}
			pld[col], pld[pivotRow] = pld[pivotRow], pld[col]
		}

		// Scale pivot row so that the diagonal element becomes 1.
		inv := gfInv(mat.Get(col, col))
		for c := 0; c < k; c++ {
			mat.Set(col, c, gfMul(mat.Get(col, c), inv))
		}
		for j := 0; j < payloadLen; j++ {
			pld[col][j] = gfMul(pld[col][j], inv)
		}

		// Eliminate all other rows in this column.
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := mat.Get(row, col)
			if factor == 0 {
				continue
			}
			for c := 0; c < k; c++ {
				mat.Set(row, c, gfAdd(mat.Get(row, c), gfMul(factor, mat.Get(col, c))))
			}
			for j := 0; j < payloadLen; j++ {
				pld[row][j] = gfAdd(pld[row][j], gfMul(factor, pld[col][j]))
			}
		}
	}

	// The first K rows of pld now contain the decoded segments.
	result := make([][]byte, k)
	for i := 0; i < k; i++ {
		result[i] = pld[i]
	}
	return result, nil
}

// randBytes fills buf with cryptographically random bytes.
func randBytes(buf []byte) {
	if _, err := rand.Read(buf); err != nil {
		panic("rlnc: crypto/rand failed: " + err.Error())
	}
}
