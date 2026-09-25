package hf10m

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

// LDPC(128, 64) over the normative parity-check matrix.
const (
	LDPCN = 128
	LDPCK = 64
	LDPCM = 64
)

// infoColumns are the systematic information positions: 0..62 and 67.
var infoColumns = func() []int {
	cols := make([]int, 0, LDPCK)
	for c := 0; c <= 62; c++ {
		cols = append(cols, c)
	}
	return append(cols, 67)
}()

// parityColumns are the remaining 64 positions.
var parityColumns = func() []int {
	isInfo := make([]bool, LDPCN)
	for _, c := range infoColumns {
		isInfo[c] = true
	}
	cols := make([]int, 0, LDPCM)
	for c := 0; c < LDPCN; c++ {
		if !isInfo[c] {
			cols = append(cols, c)
		}
	}
	return cols
}()

// hRows[r][c] is H[r][c] over GF(2).
var hRows [LDPCM][LDPCN]uint8

// hCheckNeighbours[r] lists the columns of row r; hVarNeighbours[c] the rows of column c.
var (
	hCheckNeighbours [LDPCM][]int
	hVarNeighbours   [LDPCN][]int
)

// encodeParityOf[p] lists the information indexes (0..63) whose XOR gives
// parity bit p, i.e. the row p of Hp^-1 * Hi.
var encodeParityOf [LDPCM][]int

func init() {
	lines := strings.Fields(strings.TrimSpace(hMatrixHex))
	if len(lines) != LDPCM {
		panic(fmt.Sprintf("hf10m: H has %d rows, want %d", len(lines), LDPCM))
	}
	for r, line := range lines {
		raw, err := hex.DecodeString(line)
		if err != nil || len(raw) != 16 {
			panic(fmt.Sprintf("hf10m: H row %d: %q", r, line))
		}
		for c := 0; c < LDPCN; c++ {
			if raw[c/8]>>(7-uint(c%8))&1 == 1 {
				hRows[r][c] = 1
				hCheckNeighbours[r] = append(hCheckNeighbours[r], c)
				hVarNeighbours[c] = append(hVarNeighbours[c], r)
			}
		}
	}
	// Systematic encoder: Hp * p = Hi * i  =>  p = Hp^-1 * Hi * i.
	// Build the augmented matrix [Hp | Hi] (64 x 128) and reduce Hp to I.
	var aug [LDPCM][LDPCM + LDPCK]uint8
	for r := 0; r < LDPCM; r++ {
		for j, c := range parityColumns {
			aug[r][j] = hRows[r][c]
		}
		for j, c := range infoColumns {
			aug[r][LDPCM+j] = hRows[r][c]
		}
	}
	for col := 0; col < LDPCM; col++ {
		pivot := -1
		for r := col; r < LDPCM; r++ {
			if aug[r][col] == 1 {
				pivot = r
				break
			}
		}
		if pivot < 0 {
			panic("hf10m: parity part of H is singular; the information columns 0..62,67 cannot be systematic")
		}
		aug[col], aug[pivot] = aug[pivot], aug[col]
		for r := 0; r < LDPCM; r++ {
			if r != col && aug[r][col] == 1 {
				for j := 0; j < LDPCM+LDPCK; j++ {
					aug[r][j] ^= aug[col][j]
				}
			}
		}
	}
	for p := 0; p < LDPCM; p++ {
		for j := 0; j < LDPCK; j++ {
			if aug[p][LDPCM+j] == 1 {
				encodeParityOf[p] = append(encodeParityOf[p], j)
			}
		}
	}
}

// Encode maps 64 information bits to a 128-bit codeword (one bit per byte).
func Encode(info []uint8) [LDPCN]uint8 {
	var cw [LDPCN]uint8
	for j, c := range infoColumns {
		cw[c] = info[j] & 1
	}
	for p, c := range parityColumns {
		var v uint8
		for _, j := range encodeParityOf[p] {
			v ^= info[j] & 1
		}
		cw[c] = v
	}
	return cw
}

// Syndrome reports whether the codeword satisfies every check.
func Syndrome(cw []uint8) bool {
	for r := 0; r < LDPCM; r++ {
		var s uint8
		for _, c := range hCheckNeighbours[r] {
			s ^= cw[c] & 1
		}
		if s != 0 {
			return false
		}
	}
	return true
}

// InfoBits extracts the 64 information bits from a codeword.
func InfoBits(cw []uint8) []uint8 {
	out := make([]uint8, LDPCK)
	for j, c := range infoColumns {
		out[j] = cw[c] & 1
	}
	return out
}

// Decode runs normalised min-sum belief propagation on log-likelihood
// ratios (positive = bit 0 more likely, as in the usual convention) and
// returns the hard decision and whether every parity check is satisfied.
func Decode(llr []float64, iterations int) ([LDPCN]uint8, bool) {
	if iterations <= 0 {
		iterations = 30
	}
	const alpha = 0.8
	// Messages variable->check (q) and check->variable (r), indexed by edge.
	var q, r [LDPCM][]float64
	for i := 0; i < LDPCM; i++ {
		q[i] = make([]float64, len(hCheckNeighbours[i]))
		r[i] = make([]float64, len(hCheckNeighbours[i]))
		for k, c := range hCheckNeighbours[i] {
			q[i][k] = llr[c]
		}
	}
	var hard [LDPCN]uint8
	posterior := make([]float64, LDPCN)
	for it := 0; it < iterations; it++ {
		// Check node update.
		for i := 0; i < LDPCM; i++ {
			n := len(q[i])
			for k := 0; k < n; k++ {
				sign := 1.0
				minAbs := math.Inf(1)
				for l := 0; l < n; l++ {
					if l == k {
						continue
					}
					v := q[i][l]
					if v < 0 {
						sign = -sign
					}
					if a := math.Abs(v); a < minAbs {
						minAbs = a
					}
				}
				r[i][k] = sign * alpha * minAbs
			}
		}
		// Variable node update and hard decision.
		copy(posterior, llr)
		for i := 0; i < LDPCM; i++ {
			for k, c := range hCheckNeighbours[i] {
				posterior[c] += r[i][k]
			}
		}
		for c := 0; c < LDPCN; c++ {
			if posterior[c] < 0 {
				hard[c] = 1
			} else {
				hard[c] = 0
			}
		}
		if Syndrome(hard[:]) {
			return hard, true
		}
		for i := 0; i < LDPCM; i++ {
			for k, c := range hCheckNeighbours[i] {
				q[i][k] = posterior[c] - r[i][k]
			}
		}
	}
	return hard, false
}
