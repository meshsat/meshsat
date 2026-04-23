package timesync

import (
	"testing"
	"time"
)

// buildValidFrame constructs a 59-bit DCF77 frame encoding the given
// civil CET time (not CEST). Returns the bit slice and the expected
// UTC instant (= civil - 1h).
func buildValidFrame(t *testing.T, year, month, day, hour, minute, dow int) []int {
	t.Helper()
	bits := make([]int, 59)
	// bit 0 = M (0, already)
	// bit 18 = 1 (CET flag)
	bits[18] = 1
	// bit 20 = 1 (S start-of-time)
	bits[20] = 1

	encodeBCD(bits[21:28], minute, []int{1, 2, 4, 8, 10, 20, 40})
	bits[28] = parityBit(bits[21:28])

	encodeBCD(bits[29:35], hour, []int{1, 2, 4, 8, 10, 20})
	bits[35] = parityBit(bits[29:35])

	encodeBCD(bits[36:42], day, []int{1, 2, 4, 8, 10, 20})
	encodeBCD(bits[42:45], dow, []int{1, 2, 4})
	encodeBCD(bits[45:50], month, []int{1, 2, 4, 8, 10})
	encodeBCD(bits[50:58], year%100, []int{1, 2, 4, 8, 10, 20, 40, 80})
	bits[58] = parityBit(bits[36:58])

	return bits
}

// encodeBCD writes `value` into bits using the given decimal weights.
// Panics (via t.Fatal) if any assigned bit would be non-0/1.
func encodeBCD(bits []int, value int, weights []int) {
	// Greedy decomposition works for DCF77 because the weights are
	// either (1,2,4,8) or (10,20,40,80) blocks — standard BCD.
	remaining := value
	// Process weights from largest to smallest.
	for i := len(weights) - 1; i >= 0; i-- {
		if remaining >= weights[i] {
			bits[i] = 1
			remaining -= weights[i]
		}
	}
}

func parityBit(bits []int) int {
	p := 0
	for _, b := range bits {
		p ^= b
	}
	return p
}

func TestDecodeDCF77Frame_Valid(t *testing.T) {
	// 2024-12-25 12:00 CET, Wednesday (dow=3).
	// UTC = 11:00 (CET = UTC+1 in winter).
	bits := buildValidFrame(t, 2024, 12, 25, 12, 0, 3)
	utc, ok := decodeDCF77Frame(bits)
	if !ok {
		t.Fatalf("decodeDCF77Frame: ok=false for a valid frame")
	}
	want := time.Date(2024, 12, 25, 11, 0, 0, 0, time.UTC)
	if !utc.Equal(want) {
		t.Errorf("decoded utc = %v, want %v", utc, want)
	}
}

func TestDecodeDCF77Frame_CESTSummer(t *testing.T) {
	// 2024-06-15 14:00 CEST, Saturday (dow=6).
	// UTC = 12:00 (CEST = UTC+2 in summer).
	bits := buildValidFrame(t, 2024, 6, 15, 14, 0, 6)
	// Flip to CEST: clear CET, set CEST.
	bits[17] = 1
	bits[18] = 0
	// Date-block parity is unaffected; minute/hour parities likewise.
	utc, ok := decodeDCF77Frame(bits)
	if !ok {
		t.Fatalf("decodeDCF77Frame: ok=false for a valid CEST frame")
	}
	want := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	if !utc.Equal(want) {
		t.Errorf("decoded utc = %v, want %v", utc, want)
	}
}

func TestDecodeDCF77Frame_Length(t *testing.T) {
	if _, ok := decodeDCF77Frame(make([]int, 58)); ok {
		t.Error("decodeDCF77Frame accepted a 58-bit frame")
	}
	if _, ok := decodeDCF77Frame(make([]int, 60)); ok {
		t.Error("decodeDCF77Frame accepted a 60-bit frame")
	}
}

func TestDecodeDCF77Frame_StartBit(t *testing.T) {
	bits := buildValidFrame(t, 2024, 12, 25, 12, 0, 3)
	bits[0] = 1 // M must be 0
	if _, ok := decodeDCF77Frame(bits); ok {
		t.Error("decodeDCF77Frame accepted M=1")
	}
}

func TestDecodeDCF77Frame_MarkerBit(t *testing.T) {
	bits := buildValidFrame(t, 2024, 12, 25, 12, 0, 3)
	bits[20] = 0 // S must be 1
	if _, ok := decodeDCF77Frame(bits); ok {
		t.Error("decodeDCF77Frame accepted S=0")
	}
}

func TestDecodeDCF77Frame_MinuteParityCorruption(t *testing.T) {
	bits := buildValidFrame(t, 2024, 12, 25, 12, 17, 3)
	bits[21] ^= 1 // flip one data bit without touching parity
	if _, ok := decodeDCF77Frame(bits); ok {
		t.Error("decodeDCF77Frame accepted a bad minute parity")
	}
}

func TestDecodeDCF77Frame_HourParityCorruption(t *testing.T) {
	bits := buildValidFrame(t, 2024, 12, 25, 12, 0, 3)
	bits[29] ^= 1
	if _, ok := decodeDCF77Frame(bits); ok {
		t.Error("decodeDCF77Frame accepted a bad hour parity")
	}
}

func TestDecodeDCF77Frame_DateParityCorruption(t *testing.T) {
	bits := buildValidFrame(t, 2024, 12, 25, 12, 0, 3)
	bits[50] ^= 1
	if _, ok := decodeDCF77Frame(bits); ok {
		t.Error("decodeDCF77Frame accepted a bad date parity")
	}
}

func TestDecodeDCF77Frame_RangeCheck(t *testing.T) {
	// Month 13 is impossible. Break it in a way that still passes
	// parity (flip bits 45 and 46 together — parity preserved).
	bits := buildValidFrame(t, 2024, 12, 25, 12, 0, 3)
	// Zero the month bits 45-49, then set 45+46+49 to encode 1+2+10 = 13.
	for i := 45; i < 50; i++ {
		bits[i] = 0
	}
	bits[45] = 1
	bits[46] = 1
	bits[49] = 1
	// Repair P3 so the corruption is purely semantic, not parity.
	bits[58] = parityBit(bits[36:58])
	if _, ok := decodeDCF77Frame(bits); ok {
		t.Error("decodeDCF77Frame accepted month=13")
	}
}

func TestBCDDecode(t *testing.T) {
	cases := []struct {
		bits    []int
		weights []int
		want    int
	}{
		{[]int{0, 0, 0, 0, 0, 0, 0}, []int{1, 2, 4, 8, 10, 20, 40}, 0},
		{[]int{1, 0, 0, 0, 0, 0, 0}, []int{1, 2, 4, 8, 10, 20, 40}, 1},
		{[]int{1, 1, 1, 1, 0, 0, 0}, []int{1, 2, 4, 8, 10, 20, 40}, 15},
		{[]int{0, 0, 0, 0, 1, 0, 0}, []int{1, 2, 4, 8, 10, 20, 40}, 10},
		{[]int{0, 0, 0, 0, 1, 0, 1}, []int{1, 2, 4, 8, 10, 20, 40}, 50},
	}
	for _, c := range cases {
		got := bcdDecode(c.bits, c.weights)
		if got != c.want {
			t.Errorf("bcdDecode(%v, %v) = %d, want %d", c.bits, c.weights, got, c.want)
		}
	}
}

func TestEvenParity(t *testing.T) {
	if !evenParity([]int{0, 0, 0, 0}) {
		t.Error("evenParity(all zeros) = false")
	}
	if !evenParity([]int{1, 1, 0, 0}) {
		t.Error("evenParity(two ones) = false")
	}
	if evenParity([]int{1, 0, 0, 0}) {
		t.Error("evenParity(one one) = true")
	}
	if !evenParity([]int{1, 1, 1, 1}) {
		t.Error("evenParity(four ones) = false")
	}
}

func TestDetectPulsePolarity(t *testing.T) {
	// Simulate 15 seconds of a DCF77-like signal with ~100 ms LOW pulses
	// once per second (inverted: pulse_state = 0).
	var warmup []dcf77WarmupPulse
	for range 15 {
		warmup = append(warmup,
			dcf77WarmupPulse{dt: 900 * time.Millisecond, state: 1}, // idle HIGH
			dcf77WarmupPulse{dt: 100 * time.Millisecond, state: 0}, // pulse LOW
		)
	}
	if got := detectPulsePolarity(warmup); got != 0 {
		t.Errorf("detectPulsePolarity(inverted) = %d, want 0", got)
	}

	// Non-inverted: pulse_state = 1 (HIGH pulses).
	warmup = warmup[:0]
	for range 15 {
		warmup = append(warmup,
			dcf77WarmupPulse{dt: 900 * time.Millisecond, state: 0},
			dcf77WarmupPulse{dt: 100 * time.Millisecond, state: 1},
		)
	}
	if got := detectPulsePolarity(warmup); got != 1 {
		t.Errorf("detectPulsePolarity(normal) = %d, want 1", got)
	}

	// Balanced noise: no clear duty-cycle imbalance → undetermined.
	warmup = warmup[:0]
	for range 10 {
		warmup = append(warmup,
			dcf77WarmupPulse{dt: 500 * time.Millisecond, state: 1},
			dcf77WarmupPulse{dt: 500 * time.Millisecond, state: 0},
		)
	}
	if got := detectPulsePolarity(warmup); got != -1 {
		t.Errorf("detectPulsePolarity(balanced) = %d, want -1", got)
	}
}

// TestDCF77Source_TimeSourceInterface verifies NewDCF77Source returns a
// value satisfying the TimeSource interface at compile time.
func TestDCF77Source_TimeSourceInterface(t *testing.T) {
	var _ TimeSource = NewDCF77Source(DCF77Config{DataPin: 19, PONPin: 21})
}

func TestDCF77Source_NameStratum(t *testing.T) {
	s := NewDCF77Source(DCF77Config{DataPin: 19, PONPin: 21})
	if s.Name() != "dcf77" {
		t.Errorf("Name() = %q, want \"dcf77\"", s.Name())
	}
	if s.Stratum() != 1 {
		t.Errorf("Stratum() = %d, want 1", s.Stratum())
	}
}
