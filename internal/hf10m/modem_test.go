package hf10m

import (
	"math"
	"math/rand"
	"testing"
)

func exampleBurst(t *testing.T) ([]byte, *Frame) {
	t.Helper()
	var dest [16]byte
	copy(dest[:], mustHex(t, "0123456789abcdef0123456789abcdef"))
	f := &Frame{Origin: "N0CALL", Dest: dest, MsgID: 42, FragTotal: 1, Payload: []byte("no internet here. all ok. next check 0900")}
	packed, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	burst, _, err := BuildBurst(packed)
	if err != nil {
		t.Fatal(err)
	}
	return burst, f
}

// addNoise adds complex AWGN at the given SNR measured in the full 2 kHz
// baseband (the signal is constant envelope, power 1).
func addNoise(x []complex128, snrDB float64, rng *rand.Rand) []complex128 {
	sigma := math.Sqrt(math.Pow(10, -snrDB/10) / 2)
	out := make([]complex128, len(x))
	for i, s := range x {
		out[i] = s + complex(rng.NormFloat64()*sigma, rng.NormFloat64()*sigma)
	}
	return out
}

func TestModemCleanRoundTrip(t *testing.T) {
	burst, want := exampleBurst(t)
	iq := ModulateIQ(burst, BasebandRate, 0)
	// A little silence before and after, as a receiver would have.
	pad := make([]complex128, 400)
	sig := append(append(append([]complex128{}, pad...), iq...), pad...)
	f, info, err := Demodulate(sig)
	if err != nil {
		t.Fatalf("%v (%+v)", err, info)
	}
	if f.Origin != want.Origin || string(f.Payload) != string(want.Payload) || f.MsgID != 42 {
		t.Fatalf("decoded %+v", f)
	}
	if info.UWMatches != 64 || info.Blocks != 9 || math.Abs(info.OffsetHz) > 2 {
		t.Fatalf("info %+v", info)
	}
}

// Noise, tuning error and a timing offset, as off the air.
func TestModemNoisyOffsetTiming(t *testing.T) {
	burst, want := exampleBurst(t)
	rng := rand.New(rand.NewSource(3))
	cases := []struct {
		snr, offset float64
		shift       int
	}{
		{20, 0, 0}, {20, 200, 7}, {10, -200, 13}, {10, 120, 3}, {5, -60, 9}, {5, 250, 17},
	}
	for _, c := range cases {
		iq := ModulateIQ(burst, BasebandRate, c.offset)
		pad := make([]complex128, 300+c.shift)
		sig := addNoise(append(append(append([]complex128{}, pad...), iq...), make([]complex128, 300)...), c.snr, rng)
		f, info, err := Demodulate(sig)
		if err != nil {
			t.Fatalf("snr %.0f offset %.0f shift %d: %v (%+v)", c.snr, c.offset, c.shift, err, info)
		}
		if string(f.Payload) != string(want.Payload) || f.Origin != "N0CALL" {
			t.Fatalf("snr %.0f offset %.0f: decoded %+v", c.snr, c.offset, f)
		}
		if math.Abs(info.OffsetHz-c.offset) > 6 {
			t.Fatalf("snr %.0f: offset estimate %.1f for %.1f", c.snr, info.OffsetHz, c.offset)
		}
		t.Logf("snr %2.0f dB offset %4.0f Hz shift %2d: uw %d/64 est %.0f Hz timing %d mean|llr| %.2f", c.snr, c.offset, c.shift, info.UWMatches, info.OffsetHz, info.Timing, info.MeanLLR)
	}
}

// The RTL-SDR path: 240 kHz IQ with the LO parked 1 kHz below the
// centre, mixed and decimated to 2 kHz before the demodulator.
func TestModemFromWidebandIQ(t *testing.T) {
	burst, want := exampleBurst(t)
	const fs = 240000.0
	iq := ModulateIQ(burst, fs, 1000+37) // signal sits 1 kHz up, 37 Hz tuning error
	rng := rand.New(rand.NewSource(11))
	sigma := math.Sqrt(math.Pow(10, -(-10.0)/10) / 2) // -10 dB in 240 kHz = ~11 dB in 2 kHz
	for i := range iq {
		iq[i] += complex(rng.NormFloat64()*sigma, rng.NormFloat64()*sigma)
	}
	bb, err := DecimateTo2k(iq, fs, 1000)
	if err != nil {
		t.Fatal(err)
	}
	f, info, err := Demodulate(bb)
	if err != nil {
		t.Fatalf("%v (%+v)", err, info)
	}
	if string(f.Payload) != string(want.Payload) {
		t.Fatalf("decoded %+v", f)
	}
	if math.Abs(info.OffsetHz-37) > 6 {
		t.Fatalf("offset estimate %.1f", info.OffsetHz)
	}
}

func TestModemNoBurstInNoise(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	noise := addNoise(make([]complex128, 20000), -30, rng)
	if _, _, err := Demodulate(noise); err == nil {
		t.Fatal("decoded a frame from noise")
	}
}

func TestModulateAudioShape(t *testing.T) {
	burst, _ := exampleBurst(t)
	a := ModulateAudio(burst, 8000, 1000)
	// 7 Costas + 179*8 bits at 100 baud = 1439 symbols * 80 samples + 2 * 800 pad.
	if want := 1439*80 + 1600; len(a) != want {
		t.Fatalf("len %d, want %d", len(a), want)
	}
	for _, v := range a {
		if v > 1.0001 || v < -1.0001 {
			t.Fatal("audio out of range")
		}
	}
}
