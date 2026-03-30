package hemb

// GF(256) arithmetic with irreducible polynomial x^8 + x^4 + x^3 + x + 1 (0x11B).
// Self-contained copy — this package has ZERO imports from internal/routing.
// Multiplication and inversion use precomputed exp/log lookup tables.

import (
	"crypto/rand"
	"errors"
	"fmt"
)

var expTable [512]byte // anti-log table (doubled for wrap-around)
var logTable [256]byte // log table

func init() { initGFTables() }

// initGFTables builds exp and log tables using primitive element 0x03.
// Generator 0x02 only has order 51 under polynomial 0x11B — NOT primitive.
// Generator 0x03 (= x+1) has order 255, generating the full multiplicative group.
func initGFTables() {
	var x uint16 = 1
	for i := 0; i < 255; i++ {
		expTable[i] = byte(x)
		logTable[byte(x)] = byte(i)
		hi := x << 1
		if hi >= 256 {
			hi ^= 0x11B
		}
		x = hi ^ x // multiply by generator 0x03 = (x+1)
	}
	// Duplicate exp[0..254] into exp[255..509] so gfMul can index
	// log[a]+log[b] (range 0..508) without modular reduction.
	for i := 0; i < 255; i++ {
		expTable[i+255] = expTable[i]
	}
}

// gfAdd returns a + b in GF(256). Addition is XOR.
func gfAdd(a, b byte) byte { return a ^ b }

// gfMul returns a * b in GF(256) using exp/log table lookup.
func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return expTable[int(logTable[a])+int(logTable[b])]
}

// gfInv returns the multiplicative inverse of a in GF(256).
// Panics if a == 0 (zero has no inverse).
func gfInv(a byte) byte {
	if a == 0 {
		panic("gfInv: zero has no inverse in GF(256)")
	}
	return expTable[255-int(logTable[a])]
}

// gfMatrix is a matrix over GF(256) in row-major order.
type gfMatrix struct {
	Rows, Cols int
	Data       []byte
}

func newGFMatrix(rows, cols int) *gfMatrix {
	return &gfMatrix{Rows: rows, Cols: cols, Data: make([]byte, rows*cols)}
}

func (m *gfMatrix) get(row, col int) byte    { return m.Data[row*m.Cols+col] }
func (m *gfMatrix) set(row, col int, v byte) { m.Data[row*m.Cols+col] = v }

// ErrRankDeficient is returned when Gaussian elimination cannot find
// K linearly independent rows.
var ErrRankDeficient = errors.New("hemb: rank deficient — insufficient independent symbols")

// gaussianEliminate solves coeffs * X = payloads over GF(256).
// coeffs is N-by-K (N >= K), payloads is N slices of equal length.
// Returns K decoded payload slices, or ErrRankDeficient if rank < K.
func gaussianEliminate(coeffs *gfMatrix, payloads [][]byte) ([][]byte, error) {
	n := coeffs.Rows
	k := coeffs.Cols
	if n < k {
		return nil, fmt.Errorf("%w: have %d rows, need %d", ErrRankDeficient, n, k)
	}
	if len(payloads) != n {
		return nil, fmt.Errorf("hemb: payload count %d != row count %d", len(payloads), n)
	}
	if k == 0 {
		return nil, nil
	}

	payloadLen := len(payloads[0])
	for i := 1; i < n; i++ {
		if len(payloads[i]) != payloadLen {
			return nil, fmt.Errorf("hemb: payload %d length %d, expected %d", i, len(payloads[i]), payloadLen)
		}
	}

	// Deep copy to avoid mutating originals.
	mat := newGFMatrix(n, k)
	copy(mat.Data, coeffs.Data)
	pld := make([][]byte, n)
	for i := 0; i < n; i++ {
		pld[i] = make([]byte, payloadLen)
		copy(pld[i], payloads[i])
	}

	// Forward elimination with partial pivoting.
	for col := 0; col < k; col++ {
		pivotRow := -1
		for row := col; row < n; row++ {
			if mat.get(row, col) != 0 {
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

		// Scale pivot row so diagonal = 1.
		inv := gfInv(mat.get(col, col))
		for c := 0; c < k; c++ {
			mat.set(col, c, gfMul(mat.get(col, c), inv))
		}
		for j := 0; j < payloadLen; j++ {
			pld[col][j] = gfMul(pld[col][j], inv)
		}

		// Eliminate all other rows in this column.
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := mat.get(row, col)
			if factor == 0 {
				continue
			}
			for c := 0; c < k; c++ {
				mat.set(row, c, gfAdd(mat.get(row, c), gfMul(factor, mat.get(col, c))))
			}
			for j := 0; j < payloadLen; j++ {
				pld[row][j] = gfAdd(pld[row][j], gfMul(factor, pld[col][j]))
			}
		}
	}

	result := make([][]byte, k)
	for i := 0; i < k; i++ {
		result[i] = pld[i]
	}
	return result, nil
}

// ComputeRank returns the rank of an N×K coefficient matrix over GF(256).
// The input matrix is not mutated (a copy is used internally).
func ComputeRank(rows [][]byte, k int) int {
	n := len(rows)
	if n == 0 || k == 0 {
		return 0
	}

	// Deep copy into a flat matrix.
	mat := newGFMatrix(n, k)
	for i, row := range rows {
		for j := 0; j < k && j < len(row); j++ {
			mat.set(i, j, row[j])
		}
	}

	// Forward elimination — count pivots found.
	rank := 0
	for col := 0; col < k; col++ {
		pivotRow := -1
		for row := rank; row < n; row++ {
			if mat.get(row, col) != 0 {
				pivotRow = row
				break
			}
		}
		if pivotRow < 0 {
			continue // no pivot in this column
		}

		// Swap pivot into position.
		if pivotRow != rank {
			for c := 0; c < k; c++ {
				mat.Data[rank*k+c], mat.Data[pivotRow*k+c] = mat.Data[pivotRow*k+c], mat.Data[rank*k+c]
			}
		}

		// Scale pivot row.
		inv := gfInv(mat.get(rank, col))
		for c := 0; c < k; c++ {
			mat.set(rank, c, gfMul(mat.get(rank, c), inv))
		}

		// Eliminate below.
		for row := rank + 1; row < n; row++ {
			factor := mat.get(row, col)
			if factor == 0 {
				continue
			}
			for c := 0; c < k; c++ {
				mat.set(row, c, gfAdd(mat.get(row, c), gfMul(factor, mat.get(rank, c))))
			}
		}
		rank++
	}
	return rank
}

// GaussStep describes one step of Gaussian elimination for animation.
type GaussStep struct {
	Op     string   `json:"op"`      // "swap", "scale", "eliminate"
	Row    int      `json:"row"`     // target row
	Col    int      `json:"col"`     // pivot column
	SrcRow int      `json:"src_row"` // source row (for eliminate)
	Factor byte     `json:"factor"`  // GF(256) factor used
	Matrix [][]byte `json:"matrix"`  // snapshot of coefficient matrix after this step
}

// GaussianEliminationSteps performs Gaussian elimination and records
// each intermediate step for animated playback. Returns the steps and
// the final rank achieved.
func GaussianEliminationSteps(rows [][]byte, k int) ([]GaussStep, int) {
	n := len(rows)
	if n == 0 || k == 0 {
		return nil, 0
	}

	// Deep copy into working matrix.
	mat := make([][]byte, n)
	for i, row := range rows {
		mat[i] = make([]byte, k)
		copy(mat[i], row)
	}

	snapshot := func() [][]byte {
		s := make([][]byte, n)
		for i := range mat {
			s[i] = make([]byte, k)
			copy(s[i], mat[i])
		}
		return s
	}

	var steps []GaussStep
	rank := 0

	for col := 0; col < k; col++ {
		pivotRow := -1
		for row := rank; row < n; row++ {
			if mat[row][col] != 0 {
				pivotRow = row
				break
			}
		}
		if pivotRow < 0 {
			continue
		}

		// Swap pivot into position.
		if pivotRow != rank {
			mat[rank], mat[pivotRow] = mat[pivotRow], mat[rank]
			steps = append(steps, GaussStep{
				Op: "swap", Row: rank, Col: col, SrcRow: pivotRow,
				Matrix: snapshot(),
			})
		}

		// Scale pivot row so diagonal = 1.
		inv := gfInv(mat[rank][col])
		for c := 0; c < k; c++ {
			mat[rank][c] = gfMul(mat[rank][c], inv)
		}
		steps = append(steps, GaussStep{
			Op: "scale", Row: rank, Col: col, Factor: inv,
			Matrix: snapshot(),
		})

		// Eliminate all other rows in this column.
		for row := 0; row < n; row++ {
			if row == rank || mat[row][col] == 0 {
				continue
			}
			factor := mat[row][col]
			for c := 0; c < k; c++ {
				mat[row][c] = gfAdd(mat[row][c], gfMul(factor, mat[rank][c]))
			}
			steps = append(steps, GaussStep{
				Op: "eliminate", Row: row, Col: col, SrcRow: rank, Factor: factor,
				Matrix: snapshot(),
			})
		}
		rank++
	}
	return steps, rank
}

// randBytes fills buf with cryptographically random bytes.
func randBytes(buf []byte) {
	if _, err := rand.Read(buf); err != nil {
		panic("hemb: crypto/rand failed: " + err.Error())
	}
}
