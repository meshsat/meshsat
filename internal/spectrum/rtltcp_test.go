package spectrum

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRTLTCP speaks rtl_tcp's protocol on a loopback listener: a 12-byte
// "RTL0" header, 5-byte big-endian commands from the client, and a stream
// of interleaved u8 I/Q samples. The stream is a complex tone at toneHz
// (absolute), so its baseband offset follows the retune commands.
type fakeRTLTCP struct {
	t        *testing.T
	ln       net.Listener
	toneHz   float64
	amp      float64
	constant atomic.Bool

	stallAfterBytes atomic.Int64 // stop streaming after this many bytes in a session (0 = never)
	exitDelay       atomic.Int64 // how long a stopped "process" takes to exit, ns
	spawnedAt       atomic.Int64
	exitedAt        atomic.Int64
	spawns          atomic.Int32
	center          atomic.Int64
	freqCmds        atomic.Int32
	gainCmds        atomic.Int32

	mu   sync.Mutex
	conn net.Conn
}

func newFakeRTLTCP(t *testing.T, toneHz float64) *fakeRTLTCP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRTLTCP{t: t, ln: ln, toneHz: toneHz, amp: 60}
	go f.accept()
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		if f.conn != nil {
			f.conn.Close()
		}
		f.mu.Unlock()
	})
	return f
}

func (f *fakeRTLTCP) accept() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		go f.session(c)
	}
}

func (f *fakeRTLTCP) session(c net.Conn) {
	hdr := make([]byte, 12)
	copy(hdr, "RTL0")
	binary.BigEndian.PutUint32(hdr[4:], 6) // R828D
	binary.BigEndian.PutUint32(hdr[8:], 29)
	if _, err := c.Write(hdr); err != nil {
		return
	}
	go func() {
		cmd := make([]byte, 5)
		for {
			if _, err := readFull(c, cmd); err != nil {
				return
			}
			switch cmd[0] {
			case rtlTCPCmdSetFreq:
				f.center.Store(int64(binary.BigEndian.Uint32(cmd[1:])))
				f.freqCmds.Add(1)
			case rtlTCPCmdSetGain:
				f.gainCmds.Add(1)
			}
		}
	}()
	const chunk = 16 << 10
	buf := make([]byte, chunk)
	var phase float64
	var sent int64
	for {
		limit := f.stallAfterBytes.Load()
		if limit > 0 && sent >= limit {
			// Stalled stream: hold the connection open, send nothing.
			time.Sleep(10 * time.Millisecond)
			if _, err := c.Read(make([]byte, 0)); err != nil {
				return
			}
			continue
		}
		step := 2 * math.Pi * (f.toneHz - float64(f.center.Load())) / RTLTCPSampleRate
		for i := 0; i < chunk; i += 2 {
			if f.constant.Load() {
				buf[i], buf[i+1] = 127, 127
				continue
			}
			buf[i] = byte(127.5 + f.amp*math.Cos(phase))
			buf[i+1] = byte(127.5 + f.amp*math.Sin(phase))
			phase += step
		}
		phase = math.Mod(phase, 2*math.Pi)
		if _, err := c.Write(buf); err != nil {
			return
		}
		sent += chunk
		time.Sleep(2 * time.Millisecond)
	}
}

// scanner returns an RTLTCPScanner wired to the fake: spawn counts starts
// and its stop closes the session, as SIGTERM to rtl_tcp would.
func (f *fakeRTLTCP) scanner() *RTLTCPScanner {
	s := newRTLTCPScanner("rtl_tcp", f.ln.Addr().String())
	s.settle = 50 * time.Millisecond
	s.stallAfter = 300 * time.Millisecond
	s.startWait = 2 * time.Second
	s.repeats = 200
	s.present = func() bool { return true }
	s.spawn = func(*RTLTCPScanner) (*rtlTCPProc, error) {
		f.spawns.Add(1)
		f.spawnedAt.Store(time.Now().UnixNano())
		done := make(chan struct{})
		var once sync.Once
		return &rtlTCPProc{
			stop: func() {
				once.Do(func() {
					f.mu.Lock()
					if f.conn != nil {
						f.conn.Close()
					}
					f.mu.Unlock()
					time.Sleep(time.Duration(f.exitDelay.Load()))
					f.exitedAt.Store(time.Now().UnixNano())
					close(done)
				})
			},
			done: done,
			tail: func() string { return "" },
		}, nil
	}
	return s
}

func argmax(v []float64) int {
	best := 0
	for i, x := range v {
		if x > v[best] {
			best = i
		}
	}
	return best
}

func TestDefaultBandsPlanRTLTCP(t *testing.T) {
	for _, b := range DefaultBands {
		g, err := BandScanGeometry(b)
		if err != nil {
			t.Fatalf("%s: geometry: %v", b.Name, err)
		}
		p, err := planRTLTCP(g, b.BinSize, RTLTCPSampleRate)
		if err != nil {
			t.Fatalf("%s: plan: %v", b.Name, err)
		}
		if p.FirstBin < 1 || p.FirstBin+p.Bins > p.M-1 {
			t.Errorf("%s: bins %d..%d fall off the %d-point DFT", b.Name, p.FirstBin, p.FirstBin+p.Bins-1, p.M)
		}
		// Bin 0 of the widened band must be centred on WidenedLow + bin/2.
		got := p.Center + (p.FirstBin-p.M/2)*b.BinSize
		if want := g.WidenedLow + b.BinSize/2; got != want {
			t.Errorf("%s: first bin centred at %d, want %d", b.Name, got, want)
		}
		usable := int(float64(p.M) * rtlTCPUsableFraction)
		if p.DCBin < 0 {
			// Narrow band: DC and its guard outside the band, all bins flat.
			if p.FirstBin <= p.M/2 {
				t.Errorf("%s: narrow band starts at bin %d, on or below DC %d", b.Name, p.FirstBin, p.M/2)
			}
			if p.FirstBin+p.Bins-p.M/2 > usable {
				t.Errorf("%s: narrow band reaches past the usable %d bins", b.Name, usable)
			}
		} else {
			if p.DCBin != p.Bins/2 || p.FirstBin+p.DCBin != p.M/2 {
				t.Errorf("%s: DC bin %d does not sit on DFT bin %d", b.Name, p.DCBin, p.M/2)
			}
			if p.Bins/2-g.CropPad > usable {
				t.Errorf("%s: interior reaches past the usable %d bins", b.Name, usable)
			}
		}
	}
}

// The APRS and mesh bands are centred on the channel they watch. The per-band
// scanner tuned to the middle and overwrote the middle bin to hide DC, which
// hid the channel itself; the new plan must keep DC outside both.
func TestNarrowChannelBandsKeepDCOutside(t *testing.T) {
	for _, name := range []string{"aprs_144", "mesh_869"} {
		var band *Band
		for i := range DefaultBands {
			if DefaultBands[i].Name == name {
				band = &DefaultBands[i]
			}
		}
		if band == nil {
			t.Fatalf("%s not in DefaultBands", name)
		}
		g, _ := BandScanGeometry(*band)
		p, err := planRTLTCP(g, band.BinSize, RTLTCPSampleRate)
		if err != nil {
			t.Fatal(err)
		}
		if p.DCBin >= 0 {
			t.Errorf("%s: DC bin %d is inside the band", name, p.DCBin)
		}
		if lo := p.Center + rtlTCPDCGuardHz; g.WidenedLow < lo-band.BinSize {
			t.Errorf("%s: band starts at %d, inside the DC guard up to %d", name, g.WidenedLow, lo)
		}
	}
}

func TestBandPowersToneAndScale(t *testing.T) {
	g, err := scanGeometry(867_800_000, 868_600_000, 25_000, 2)
	if err != nil {
		t.Fatal(err)
	}
	p, err := planRTLTCP(g, 25_000, RTLTCPSampleRate)
	if err != nil {
		t.Fatal(err)
	}
	const repeats = 100
	const amp = 100.0
	j := 10 // band bin with the tone at its centre
	off := float64((p.FirstBin + j - p.M/2) * 25_000)
	raw := make([]byte, 2*p.M*repeats)
	for n := 0; n < p.M*repeats; n++ {
		ph := 2 * math.Pi * off * float64(n) / RTLTCPSampleRate
		raw[2*n] = byte(math.Round(127 + amp*math.Cos(ph)))
		raw[2*n+1] = byte(math.Round(127 + amp*math.Sin(ph)))
	}
	pw := bandPowers(raw, p, repeats, RTLTCPSampleRate)
	if len(pw) != p.Bins {
		t.Fatalf("got %d bins, want %d", len(pw), p.Bins)
	}
	if k := argmax(pw); k != j {
		t.Fatalf("tone landed in bin %d, want %d", k, j)
	}
	// A coherent tone of amplitude A gives |X_k| = M*A per frame, so
	// rtl_power_fftw's 10*log10(pwr/repeats/M/sr) is 10*log10(M*A^2/sr).
	want := 10 * math.Log10(float64(p.M)*amp*amp/RTLTCPSampleRate)
	if d := math.Abs(pw[j] - want); d > 0.5 {
		t.Errorf("tone bin %.2f dB, want %.2f dB (rtl_power_fftw scale)", pw[j], want)
	}
	for i, v := range pw {
		if i != j && v > pw[j]-30 {
			t.Errorf("bin %d at %.1f dB is within 30 dB of the tone %.1f dB", i, v, pw[j])
		}
	}
}

func TestRTLTCPScanFindsToneAndFollowsRetune(t *testing.T) {
	// Tone at the centre of lora_868 bin 20 (867.8 MHz + 20.5 bins).
	f := newFakeRTLTCP(t, 868_312_500)
	s := f.scanner()
	defer s.Close()
	ctx := context.Background()

	lora, err := s.Scan(ctx, 867_800_000, 868_600_000, 25_000, 2)
	if err != nil {
		t.Fatalf("lora scan: %v", err)
	}
	if len(lora) != 32 {
		t.Fatalf("lora: %d bins, want 32", len(lora))
	}
	if k := argmax(lora); k != 20 {
		t.Fatalf("lora: tone in bin %d, want 20", k)
	}

	aprs, err := s.Scan(ctx, 144_700_000, 144_900_000, 12_500, 2)
	if err != nil {
		t.Fatalf("aprs scan: %v", err)
	}
	if len(aprs) != 16 {
		t.Fatalf("aprs: %d bins, want 16", len(aprs))
	}
	if lora[20]-maxVal(aprs) < 20 {
		t.Errorf("aprs band still sees the 868 MHz tone: max %.1f dB vs %.1f dB", maxVal(aprs), lora[20])
	}

	lora2, err := s.Scan(ctx, 867_800_000, 868_600_000, 25_000, 2)
	if err != nil {
		t.Fatalf("second lora scan: %v", err)
	}
	if k := argmax(lora2); k != 20 {
		t.Errorf("after the retune back: tone in bin %d, want 20", k)
	}
	if n := f.spawns.Load(); n != 1 {
		t.Errorf("reader started %d times for three scans, want once", n)
	}
	if n := f.freqCmds.Load(); n != 3 {
		t.Errorf("%d retune commands for lora, aprs, lora; want 3", n)
	}
	// One gain command at connect plus one after each retune (issue #42).
	if n := f.gainCmds.Load(); n != 1+3 {
		t.Errorf("%d gain commands, want 4 (connect + one per retune)", n)
	}
	// Same band again: no retune, no settle.
	if _, err := s.Scan(ctx, 867_800_000, 868_600_000, 25_000, 2); err != nil {
		t.Fatal(err)
	}
	if n := f.freqCmds.Load(); n != 3 {
		t.Errorf("a repeat scan of the same band retuned (%d commands)", n)
	}
}

func TestRTLTCPStallStopsReaderAndNextScanRestarts(t *testing.T) {
	f := newFakeRTLTCP(t, 868_312_500)
	f.stallAfterBytes.Store(16 << 10)
	s := f.scanner()
	defer s.Close()

	start := time.Now()
	_, err := s.Scan(context.Background(), 867_800_000, 868_600_000, 25_000, 2)
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("want a stall error, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("stall noticed after %s, want about stallAfter", d)
	}

	f.stallAfterBytes.Store(0)
	if _, err := s.Scan(context.Background(), 867_800_000, 868_600_000, 25_000, 2); err != nil {
		t.Fatalf("scan after the stall: %v", err)
	}
	if n := f.spawns.Load(); n != 2 {
		t.Errorf("reader started %d times, want 2 (fresh process after the stall)", n)
	}
}

func TestRTLTCPStartFailureBacksOff(t *testing.T) {
	s := newRTLTCPScanner("rtl_tcp", "127.0.0.1:1")
	s.present = func() bool { return true }
	var spawns atomic.Int32
	s.spawn = func(*RTLTCPScanner) (*rtlTCPProc, error) {
		spawns.Add(1)
		return nil, errors.New("Failed to open rtlsdr device #0")
	}
	ctx := context.Background()
	if _, err := s.Scan(ctx, 867_800_000, 868_600_000, 25_000, 2); err == nil {
		t.Fatal("want the start error")
	}
	_, err := s.Scan(ctx, 867_800_000, 868_600_000, 25_000, 2)
	if err == nil || !strings.Contains(err.Error(), "backoff") {
		t.Fatalf("want a backoff error, got %v", err)
	}
	if n := spawns.Load(); n != 1 {
		t.Errorf("start attempted %d times inside the backoff, want 1", n)
	}
	s.mu.Lock()
	next := s.backoff
	s.mu.Unlock()
	if next != 2*rtlTCPBackoffMin {
		t.Errorf("backoff after one failure %s, want %s", next, 2*rtlTCPBackoffMin)
	}
}

func TestRTLTCPNoDongleDoesNotStart(t *testing.T) {
	s := newRTLTCPScanner("rtl_tcp", "127.0.0.1:1")
	s.present = func() bool { return false }
	s.spawn = func(*RTLTCPScanner) (*rtlTCPProc, error) {
		t.Fatal("spawned a reader with no dongle on the bus")
		return nil, nil
	}
	_, err := s.Scan(context.Background(), 867_800_000, 868_600_000, 25_000, 2)
	if !errors.Is(err, ErrNoDongle) {
		t.Fatalf("want ErrNoDongle, got %v", err)
	}
}

func TestRTLTCPConstantStreamIsAnError(t *testing.T) {
	f := newFakeRTLTCP(t, 868_312_500)
	f.constant.Store(true)
	s := f.scanner()
	defer s.Close()
	_, err := s.Scan(context.Background(), 867_800_000, 868_600_000, 25_000, 2)
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("want a constant-stream error, got %v", err)
	}
}

func TestRTLTCPCloseWakesAWaitingCapture(t *testing.T) {
	f := newFakeRTLTCP(t, 868_312_500)
	f.stallAfterBytes.Store(16 << 10)
	s := f.scanner()
	s.stallAfter = 10 * time.Second
	errc := make(chan error, 1)
	go func() {
		_, err := s.Scan(context.Background(), 867_800_000, 868_600_000, 25_000, 2)
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	s.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("scan returned samples from a closed reader")
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("scan returned %s after Close", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not wake the waiting scan")
	}
}

// Cost of one production scan of the widest band (92 bins, 1000 repeats).
func BenchmarkBandPowersWidestBand(b *testing.B) {
	widest := DefaultBands[0]
	for _, band := range DefaultBands[1:] {
		if mustGeom(band).EffBins > mustGeom(widest).EffBins {
			widest = band
		}
	}
	g := mustGeom(widest)
	p, err := planRTLTCP(g, widest.BinSize, RTLTCPSampleRate)
	if err != nil {
		b.Fatal(err)
	}
	raw := make([]byte, 2*p.M*rtlTCPRepeats)
	for i := range raw {
		raw[i] = byte(127 + (i*37)%11 - 5)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bandPowers(raw, p, rtlTCPRepeats, RTLTCPSampleRate)
	}
}

func mustGeom(b Band) ScanGeometry {
	g, err := BandScanGeometry(b)
	if err != nil {
		panic(err)
	}
	return g
}

// A Close from another goroutine that is still waiting for the old process
// to exit must not let the next Scan start a second one alongside it.
func TestRTLTCPNeverOverlapsTwoReaders(t *testing.T) {
	f := newFakeRTLTCP(t, 868_312_500)
	f.exitDelay.Store(int64(400 * time.Millisecond))
	s := f.scanner()
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Scan(ctx, 867_800_000, 868_600_000, 25_000, 2); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	time.Sleep(50 * time.Millisecond) // Close is now inside the slow exit
	if _, err := s.Scan(ctx, 867_800_000, 868_600_000, 25_000, 2); err != nil {
		t.Fatalf("scan after Close: %v", err)
	}
	<-closed
	if n := f.spawns.Load(); n != 2 {
		t.Fatalf("spawned %d readers, want 2", n)
	}
	if f.spawnedAt.Load() < f.exitedAt.Load() {
		t.Errorf("second reader started %s before the first one exited",
			time.Duration(f.exitedAt.Load()-f.spawnedAt.Load()))
	}
}
