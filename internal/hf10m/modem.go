package hf10m

import (
	"errors"
	"math"
	"math/cmplx"
)

// Modem constants (recipe section "Modulation").
const (
	Baud         = 100.0
	DeviationHz  = 50.0 // mark +50 Hz, space -50 Hz
	BasebandRate = 2000 // samples/s the demodulator runs at: 20 samples per symbol
	SamplesSym   = BasebandRate / int(Baud)
	// CentreHz is the working centre; the station moves it inside 28.120-28.189 MHz.
	CentreHz = 28_124_000
	// SearchHz is the coarse frequency search span either side of the centre.
	SearchHz = 300.0
	// UWMinMatch is the correlator threshold on the 64-bit unique word.
	UWMinMatch = 52
)

// ErrNoBurst is returned when no unique word is found.
var ErrNoBurst = errors.New("hf10m: no burst found")

// BurstBits returns the bit stream after the Costas tones for a burst.
func BurstBits(burst []byte) []uint8 { return bytesToBits(burst) }

// ModulateIQ renders Costas tones then the burst bits as continuous-phase
// 2-FSK at baseband: complex samples at fs, tones at offset±50 Hz around
// DC. offsetHz simulates a tuning error. The returned slice starts with
// the Costas symbols and ends with the tail.
func ModulateIQ(burst []byte, fs float64, offsetHz float64) []complex128 {
	bits := BurstBits(burst)
	sps := int(math.Round(fs / Baud))
	out := make([]complex128, 0, (len(CostasOffsetsHz)+len(bits))*sps)
	phase := 0.0
	emit := func(freq float64) {
		for i := 0; i < sps; i++ {
			out = append(out, cmplx.Rect(1, phase))
			phase += 2 * math.Pi * (freq + offsetHz) / fs
			if phase > math.Pi {
				phase -= 2 * math.Pi
			} else if phase < -math.Pi {
				phase += 2 * math.Pi
			}
		}
	}
	for _, f := range CostasOffsetsHz {
		emit(f)
	}
	for _, b := range bits {
		if b == 1 {
			emit(DeviationHz)
		} else {
			emit(-DeviationHz)
		}
	}
	return out
}

// ModulateAudio renders the burst as a real audio signal with the tones
// around audioCentreHz (1000 Hz puts 28.124 MHz USB on a radio dialled to
// 28.123 MHz), amplitude in [-1, 1], plus 100 ms of silence either side.
func ModulateAudio(burst []byte, fs float64, audioCentreHz float64) []float64 {
	iq := ModulateIQ(burst, fs, 0)
	pad := int(fs / 10)
	out := make([]float64, 0, len(iq)+2*pad)
	out = append(out, make([]float64, pad)...)
	for n, s := range iq {
		// Upconvert the baseband to the audio centre: real part of s * e^{j w t}.
		w := 2 * math.Pi * audioCentreHz * float64(n) / fs
		out = append(out, real(s)*math.Cos(w)-imag(s)*math.Sin(w))
	}
	return append(out, make([]float64, pad)...)
}

// DecimateTo2k mixes a complex stream at fs down by mixHz and decimates it
// to BasebandRate with a boxcar stage followed by a windowed-sinc FIR
// low-pass at 600 Hz. fs must be a multiple of 2000.
func DecimateTo2k(iq []complex128, fs float64, mixHz float64) ([]complex128, error) {
	factor := int(math.Round(fs)) / BasebandRate
	if factor < 1 || int(math.Round(fs))%BasebandRate != 0 {
		return nil, errors.New("hf10m: sample rate must be a multiple of 2000")
	}
	// Mix.
	mixed := make([]complex128, len(iq))
	phase := 0.0
	step := -2 * math.Pi * mixHz / fs
	for i, s := range iq {
		mixed[i] = s * cmplx.Rect(1, phase)
		phase += step
		if phase > math.Pi {
			phase -= 2 * math.Pi
		} else if phase < -math.Pi {
			phase += 2 * math.Pi
		}
	}
	// Stage 1: boxcar decimation to at most 20 kHz.
	stage1 := 1
	for f := factor; f%2 == 0 && fs/float64(stage1*2) >= 20000; f /= 2 {
		stage1 *= 2
	}
	for f := factor / stage1; f%3 == 0 && fs/float64(stage1*3) >= 20000; f /= 3 {
		stage1 *= 3
	}
	for f := factor / stage1; f%5 == 0 && fs/float64(stage1*5) >= 20000; f /= 5 {
		stage1 *= 5
	}
	var mid []complex128
	if stage1 > 1 {
		mid = make([]complex128, 0, len(mixed)/stage1)
		for i := 0; i+stage1 <= len(mixed); i += stage1 {
			var acc complex128
			for j := 0; j < stage1; j++ {
				acc += mixed[i+j]
			}
			mid = append(mid, acc/complex(float64(stage1), 0))
		}
	} else {
		mid = mixed
	}
	midRate := fs / float64(stage1)
	stage2 := factor / stage1
	if stage2 == 1 {
		return mid, nil
	}
	// Stage 2: FIR low-pass (Hamming windowed sinc), cutoff 600 Hz, then decimate.
	cutoff := 600.0 / midRate
	taps := 4*stage2 + 1
	if taps < 31 {
		taps = 31
	}
	if taps%2 == 0 {
		taps++
	}
	h := make([]float64, taps)
	m := taps - 1
	sum := 0.0
	for n := 0; n < taps; n++ {
		x := float64(n) - float64(m)/2
		var v float64
		if x == 0 {
			v = 2 * cutoff
		} else {
			v = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		v *= 0.54 - 0.46*math.Cos(2*math.Pi*float64(n)/float64(m))
		h[n] = v
		sum += v
	}
	for n := range h {
		h[n] /= sum
	}
	out := make([]complex128, 0, len(mid)/stage2)
	for i := 0; i < len(mid); i += stage2 {
		var acc complex128
		for n := 0; n < taps; n++ {
			k := i - n
			if k >= 0 && k < len(mid) {
				acc += mid[k] * complex(h[n], 0)
			}
		}
		out = append(out, acc)
	}
	return out, nil
}

// DemodInfo describes a decoded burst.
type DemodInfo struct {
	OffsetHz  float64 // tuning error found by the search
	Timing    int     // sample phase 0..19
	UWMatches int     // bits of the unique word that matched
	Blocks    int
	StartSym  int // symbol index of the unique word's first bit
	MeanLLR   float64
}

// symbolEnergies computes, for symbol s at timing phase t and offset f,
// the mark and space non-coherent energies.
func symbolEnergies(x []complex128, s, t int, f float64) (mark, space float64) {
	start := s*SamplesSym + t
	if start+SamplesSym > len(x) || start < 0 {
		return 0, 0
	}
	var am, as complex128
	wm := 2 * math.Pi * (f + DeviationHz) / BasebandRate
	ws := 2 * math.Pi * (f - DeviationHz) / BasebandRate
	for n := 0; n < SamplesSym; n++ {
		v := x[start+n]
		am += v * cmplx.Rect(1, -wm*float64(n))
		as += v * cmplx.Rect(1, -ws*float64(n))
	}
	return real(am)*real(am) + imag(am)*imag(am), real(as)*real(as) + imag(as)*imag(as)
}

// softBits returns per-symbol soft values (positive = space = 0) for
// nsym symbols from symbol s0.
func softBits(x []complex128, s0, nsym, t int, f float64) []float64 {
	out := make([]float64, nsym)
	for i := 0; i < nsym; i++ {
		m, s := symbolEnergies(x, s0+i, t, f)
		if m+s > 0 {
			out[i] = (s - m) / (m + s)
		}
	}
	return out
}

var uwBits = bytesToBits(UniqueWord)

// uwCorr correlates the soft values at position p with the unique word
// (+1 for a 0 bit, -1 for a 1 bit): the metric that peaks at the true
// tuning offset and timing, where the hard match count only plateaus.
func uwCorr(soft []float64, p int) float64 {
	if p < 0 || p+len(uwBits) > len(soft) {
		return 0
	}
	c := 0.0
	for i, b := range uwBits {
		if b == 0 {
			c += soft[p+i]
		} else {
			c -= soft[p+i]
		}
	}
	return c
}

// uwScore counts matching bits of the unique word against hard decisions
// starting at position p of soft.
func uwScore(soft []float64, p int) int {
	if p < 0 || p+len(uwBits) > len(soft) {
		return 0
	}
	n := 0
	for i, b := range uwBits {
		hard := uint8(0)
		if soft[p+i] < 0 {
			hard = 1
		}
		if hard == b {
			n++
		}
	}
	return n
}

// Demodulate finds a burst in a baseband stream at BasebandRate (2000
// samples/s, tones around DC) and decodes it. The search covers ±SearchHz
// of tuning error and every symbol timing phase.
func Demodulate(x []complex128) (*Frame, *DemodInfo, error) {
	nsym := len(x)/SamplesSym - 1
	if nsym < len(uwBits)+3*8 {
		return nil, nil, ErrNoBurst
	}
	// Stage 1: coarse offset, timing phase 0, whole buffer, UW correlator.
	best := DemodInfo{UWMatches: -1}
	bestCorr := math.Inf(-1)
	for f := -SearchHz; f <= SearchHz; f += 10 {
		soft := softBits(x, 0, nsym, 0, f)
		for p := 0; p+len(uwBits) <= nsym; p++ {
			if c := uwCorr(soft, p); c > bestCorr {
				bestCorr = c
				best = DemodInfo{OffsetHz: f, Timing: 0, UWMatches: uwScore(soft, p), StartSym: p}
			}
		}
	}
	if best.UWMatches < UWMinMatch-8 {
		return nil, &best, ErrNoBurst
	}
	// Stage 2: fine offset and timing around the candidate.
	fine := best
	fineCorr := math.Inf(-1)
	for f := best.OffsetHz - 10; f <= best.OffsetHz+10; f += 2 {
		for t := 0; t < SamplesSym; t++ {
			for dp := -2; dp <= 2; dp++ {
				p := best.StartSym + dp
				if p < 0 {
					continue
				}
				soft := softBits(x, p, len(uwBits), t, f)
				if c := uwCorr(soft, 0); c > fineCorr {
					fineCorr = c
					fine = DemodInfo{OffsetHz: f, Timing: t, UWMatches: uwScore(soft, 0), StartSym: p}
				}
			}
		}
	}
	if fine.UWMatches < UWMinMatch {
		return nil, &fine, ErrNoBurst
	}
	// Block count: three bytes after the unique word, majority.
	hdrStart := fine.StartSym + len(uwBits)
	cnt := softBits(x, hdrStart, 24, fine.Timing, fine.OffsetHz)
	if len(cnt) < 24 {
		return nil, &fine, ErrFrameShort
	}
	hard := make([]uint8, 24)
	for i, v := range cnt {
		if v < 0 {
			hard[i] = 1
		}
	}
	cb := bitsToBytes(hard)
	n, ok := MajorityBlockCount(cb[0], cb[1], cb[2])
	if !ok {
		return nil, &fine, ErrBlocks
	}
	fine.Blocks = n
	payloadStart := hdrStart + 24
	need := n * LDPCN
	if payloadStart+need > nsym {
		return nil, &fine, ErrFrameShort
	}
	soft := softBits(x, payloadStart, need, fine.Timing, fine.OffsetHz)
	llr := make([]float64, need)
	sum := 0.0
	for i, v := range soft {
		llr[i] = 6 * v
		sum += math.Abs(v)
	}
	fine.MeanLLR = sum / float64(need)
	raw, err := DecodeBlocksLLR(n, llr)
	if err != nil {
		return nil, &fine, err
	}
	f, err := Unmarshal(raw)
	if err != nil {
		return nil, &fine, err
	}
	return f, &fine, nil
}
