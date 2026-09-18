package spectrum

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
)

// RTLTCPScanner is the spectrum reader since MESHSAT-1222: one long-lived
// rtl_tcp process holds the dongle and streams samples asynchronously
// (rtlsdr_read_async), and the bridge retunes it per band over loopback TCP
// and computes the band's power spectrum itself.
//
// Why not rtl_power_fftw any more: it reads with rtlsdr_read_sync and
// librtlsdr's BULK_TIMEOUT is 0, so a read the RTL-SDR Blog V4 never answers
// blocks forever, and the bridge started one per band per pass, about 1,800
// opens an hour. Every so often a read hung after "Found Rafael Micro R828D
// tuner", the 90 s timeout killed it, and the device-health ladder cut the
// hub port: six times in 23 h on tesseract, every 30 to 60 min on parallax,
// until the heal path's USB reset left parallax's dongle off the bus
// (17 Sep 2026). Here the dongle is opened once per process lifetime, the
// process is always stopped with SIGTERM so rtlsdr_close runs, and a stream
// that stops is noticed in seconds and restarted.
type RTLTCPScanner struct {
	binary     string
	addr       string
	sampleRate int
	gainTenths int
	repeats    int
	settle     time.Duration // wait after a retune before samples count
	stallAfter time.Duration // no bytes for this long = the stream stalled
	startWait  time.Duration // how long a fresh process gets to accept us

	// spawn starts the reader process and present reports whether a dongle
	// is on the bus. Production: rtl_tcp and DetectRTLSDR. Tests replace
	// both with an in-process fake server.
	spawn   func(s *RTLTCPScanner) (*rtlTCPProc, error)
	present func() bool

	opMu sync.Mutex // one Scan at a time

	mu        sync.Mutex // guards everything below
	proc      *rtlTCPProc
	lastDone  <-chan struct{} // exit of the most recently started process
	conn      net.Conn
	notify    chan struct{}
	ring      []byte
	total     int64 // sample bytes received on conn (header excluded)
	lastData  time.Time
	readErr   error
	tuned     int
	backoff   time.Duration
	nextStart time.Time
}

// rtlTCPProc is a running reader process.
type rtlTCPProc struct {
	stop func()          // SIGTERM, then SIGKILL after a grace period; waits for exit
	done <-chan struct{} // closed when the process has exited
	tail func() string   // last lines it printed, for error messages
}

// Reader defaults. The loopback port follows the kits' 6xxx scheme; 6051 is
// taken on both kits. 2.4 Msps is the highest rate the RTL2832U streams
// without drops, and every band fits inside it (TestDefaultBandsPlanRTLTCP).
const (
	rtlTCPBinary            = "rtl_tcp"
	DefaultRTLTCPPort       = 6056
	RTLTCPSampleRate        = 2_400_000
	rtlTCPGainTenths        = 200 // 20.0 dB, same headroom rationale as scanFFTW [MESHSAT-658]
	rtlTCPRepeats           = 1000
	rtlTCPSettle            = 400 * time.Millisecond
	rtlTCPStallAfter        = 5 * time.Second
	rtlTCPStartWait         = 20 * time.Second
	rtlTCPRingBytes         = 8 << 20
	rtlTCPBackoffMin        = 2 * time.Second
	rtlTCPBackoffMax        = 60 * time.Second
	rtlTCPUsableFraction    = 0.45 // of the sample rate each side of DC that is flat enough to measure
	rtlTCPDCGuardHz         = 50_000
	rtlTCPHeaderLen         = 12
	rtlTCPCmdSetFreq        = 0x01
	rtlTCPCmdSetGainMode    = 0x03
	rtlTCPCmdSetGain        = 0x04
	rtlTCPCmdSetAGCMode     = 0x08
	rtlTCPLinkedListBuffers = 50 // rtl_tcp -n: bounds its memory if we ever fall behind
	rtlTCPAsyncBuffers      = 8  // rtl_tcp -b: URBs kept queued on the dongle
)

// ErrNoDongle is returned by a scan when no RTL-SDR is on the USB bus. The
// monitor ends the pass on it instead of trying every band.
var ErrNoDongle = errors.New("no RTL-SDR dongle on the USB bus")

// NewRTLTCPScanner returns the rtl_tcp reader, or nil when rtl_tcp is not on
// PATH or no dongle is present. MESHSAT_SPECTRUM_RTLTCP_PORT moves the
// loopback port.
func NewRTLTCPScanner() *RTLTCPScanner {
	path, err := exec.LookPath(rtlTCPBinary)
	if err != nil || !DetectRTLSDR() {
		return nil
	}
	port := DefaultRTLTCPPort
	if v := os.Getenv("MESHSAT_SPECTRUM_RTLTCP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			port = p
		}
	}
	s := newRTLTCPScanner(path, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	s.spawn = spawnRTLTCP
	s.present = DetectRTLSDR
	return s
}

func newRTLTCPScanner(binary, addr string) *RTLTCPScanner {
	return &RTLTCPScanner{
		binary:     binary,
		addr:       addr,
		sampleRate: RTLTCPSampleRate,
		gainTenths: rtlTCPGainTenths,
		repeats:    rtlTCPRepeats,
		settle:     rtlTCPSettle,
		stallAfter: rtlTCPStallAfter,
		startWait:  rtlTCPStartWait,
		backoff:    rtlTCPBackoffMin,
	}
}

// Available reports whether the binary is known and a dongle is present.
func (s *RTLTCPScanner) Available() bool {
	if s == nil || s.binary == "" {
		return false
	}
	return s.present == nil || s.present()
}

// Info describes the reader and the dongle for the hardware panel.
func (s *RTLTCPScanner) Info() ScannerInfo {
	info := ScannerInfo{Reader: "rtl_tcp"}
	if s != nil {
		info.BinaryPath = s.binary
	}
	if dev := findRTLSDRDevice(); dev != nil {
		info.DongleVID = dev.VID
		info.DonglePID = dev.PID
		info.USBPath = dev.Path
		info.ProductName = dev.Product
	}
	return info
}

// Close stops the reader process. The next Scan starts a fresh one without
// waiting for a backoff. Safe to call from another goroutine while a Scan
// waits for samples: the Scan returns with an error.
func (s *RTLTCPScanner) Close() error {
	s.stopReader("closed")
	s.mu.Lock()
	s.nextStart = time.Time{}
	s.mu.Unlock()
	return nil
}

// Scan measures one band. It starts the reader if needed, retunes, waits for
// the tuner to settle, captures repeats x M samples and returns the band's
// interior bins in dB, cropPad bins trimmed from each side exactly as the
// per-band scanner did.
func (s *RTLTCPScanner) Scan(ctx context.Context, freqLow, freqHigh, binSize, cropPad int) ([]float64, error) {
	if cropPad <= 0 {
		cropPad = 2
	}
	g, err := scanGeometry(freqLow, freqHigh, binSize, cropPad)
	if err != nil {
		return nil, err
	}
	plan, err := planRTLTCP(g, binSize, s.sampleRate)
	if err != nil {
		return nil, err
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	if err := s.ensureReader(ctx); err != nil {
		return nil, err
	}
	if err := s.tune(ctx, plan.Center); err != nil {
		return nil, err
	}
	need := 2 * plan.M * s.repeats
	raw, err := s.capture(ctx, need)
	if err != nil {
		return nil, err
	}
	if constantBytes(raw) {
		s.stopReader("constant sample stream")
		return nil, errors.New("rtl_tcp: every sample byte identical, the ADC is not streaming")
	}
	powers := bandPowers(raw, plan, s.repeats, s.sampleRate)

	s.mu.Lock()
	s.backoff = rtlTCPBackoffMin
	s.mu.Unlock()

	end := g.CropPad + g.Bins
	if end > len(powers) {
		end = len(powers)
	}
	if g.CropPad < len(powers) {
		powers = powers[g.CropPad:end]
	}
	return powers, nil
}

// ensureReader starts the process and connects when nothing is running. A
// start that fails schedules the next attempt with exponential backoff, and
// no attempt is made while the dongle is absent.
func (s *RTLTCPScanner) ensureReader(ctx context.Context) error {
	s.mu.Lock()
	running := s.conn != nil && s.readErr == nil
	wait := time.Until(s.nextStart)
	s.mu.Unlock()
	if running {
		return nil
	}
	s.stopReader("") // clear a reader whose connection died
	if wait > 0 {
		return fmt.Errorf("rtl_tcp: restart backoff, next attempt in %s", wait.Truncate(100*time.Millisecond))
	}
	if s.present != nil && !s.present() {
		return ErrNoDongle
	}
	if err := s.startReader(ctx); err != nil {
		s.mu.Lock()
		s.nextStart = time.Now().Add(s.backoff)
		s.backoff *= 2
		if s.backoff > rtlTCPBackoffMax {
			s.backoff = rtlTCPBackoffMax
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *RTLTCPScanner) startReader(ctx context.Context) error {
	if s.spawn == nil {
		return errors.New("rtl_tcp: no reader configured")
	}
	// Never overlap two readers. A Close from another goroutine (the heal
	// ladder's RestartScan) may still be stopping the previous process;
	// starting the next one before it exits made the new rtl_tcp fail on
	// the busy dongle while our dial reached the old one's socket
	// (tesseract, 18 Sep 2026).
	s.mu.Lock()
	prev := s.lastDone
	s.mu.Unlock()
	if prev != nil {
		t := time.NewTimer(15 * time.Second)
		select {
		case <-prev:
			t.Stop()
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
			return errors.New("rtl_tcp: the previous reader process has not exited")
		}
	}
	proc, err := s.spawn(s)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.lastDone = proc.done
	s.mu.Unlock()
	fail := func(err error) error {
		proc.stop()
		if t := proc.tail(); t != "" {
			return fmt.Errorf("%w (rtl_tcp said: %s)", err, t)
		}
		return err
	}

	// rtl_tcp opens and initialises the dongle before it listens; poll the
	// port instead of parsing its stdout, which is block-buffered on a pipe.
	deadline := time.Now().Add(s.startWait)
	var conn net.Conn
	for {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		select {
		case <-proc.done:
			return fail(errors.New("rtl_tcp exited before accepting a connection"))
		default:
		}
		c, err := net.DialTimeout("tcp", s.addr, time.Second)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("rtl_tcp did not accept on %s within %s: %v", s.addr, s.startWait, err))
		}
		time.Sleep(200 * time.Millisecond)
	}

	hdr := make([]byte, rtlTCPHeaderLen)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := readFull(conn, hdr); err != nil {
		conn.Close()
		return fail(fmt.Errorf("rtl_tcp header: %w", err))
	}
	_ = conn.SetReadDeadline(time.Time{})
	if string(hdr[:4]) != "RTL0" {
		conn.Close()
		return fail(fmt.Errorf("rtl_tcp header magic %q", hdr[:4]))
	}
	for _, c := range [][2]uint32{
		{rtlTCPCmdSetGainMode, 1},
		{rtlTCPCmdSetGain, uint32(s.gainTenths)},
		{rtlTCPCmdSetAGCMode, 0},
	} {
		if err := writeRTLTCPCommand(conn, byte(c[0]), c[1]); err != nil {
			conn.Close()
			return fail(fmt.Errorf("rtl_tcp command 0x%02x: %w", c[0], err))
		}
	}

	notify := make(chan struct{}, 1)
	s.mu.Lock()
	s.proc = proc
	s.conn = conn
	s.notify = notify
	if s.ring == nil {
		s.ring = make([]byte, rtlTCPRingBytes)
	}
	s.total = 0
	s.lastData = time.Now()
	s.readErr = nil
	s.tuned = 0
	s.mu.Unlock()
	go s.drain(conn, notify)
	if err := ctx.Err(); err != nil {
		// Cancelled while connecting (RestartScan, Suspend, shutdown):
		// a Close that ran before the assignment above found nothing to
		// stop, so stop it here.
		s.stopReader("")
		return err
	}
	log.Info().Str("addr", s.addr).Msg("spectrum: rtl_tcp reader streaming")
	return nil
}

// stopReader closes the connection and stops the process. A non-empty
// reason is logged. A capture waiting on the stream wakes and returns.
func (s *RTLTCPScanner) stopReader(reason string) {
	s.mu.Lock()
	proc, conn, notify := s.proc, s.conn, s.notify
	s.proc, s.conn = nil, nil
	s.tuned = 0
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	if notify != nil {
		select {
		case notify <- struct{}{}:
		default:
		}
	}
	if proc != nil {
		if reason != "" {
			log.Warn().Str("reason", reason).Msg("spectrum: stopping the rtl_tcp reader")
		}
		proc.stop()
	}
}

// drain reads the sample stream for as long as the connection lives, so
// rtl_tcp never queues up, and keeps the newest rtlTCPRingBytes in the ring.
func (s *RTLTCPScanner) drain(conn net.Conn, notify chan struct{}) {
	buf := make([]byte, 64<<10)
	for {
		n, err := conn.Read(buf)
		s.mu.Lock()
		if s.conn != conn {
			s.mu.Unlock()
			return
		}
		if n > 0 {
			s.appendRingLocked(buf[:n])
			s.lastData = time.Now()
		}
		if err != nil {
			s.readErr = err
		}
		s.mu.Unlock()
		select {
		case notify <- struct{}{}:
		default:
		}
		if err != nil {
			return
		}
	}
}

func (s *RTLTCPScanner) appendRingLocked(p []byte) {
	size := int64(len(s.ring))
	for len(p) > 0 {
		off := int(s.total % size)
		n := copy(s.ring[off:], p)
		p = p[n:]
		s.total += int64(n)
	}
}

// copyRingLocked copies n bytes starting at stream offset start. It fails
// when those bytes have already been overwritten.
func (s *RTLTCPScanner) copyRingLocked(start int64, n int) ([]byte, bool) {
	size := int64(len(s.ring))
	if start < s.total-size || start+int64(n) > s.total {
		return nil, false
	}
	out := make([]byte, n)
	for done := 0; done < n; {
		off := int((start + int64(done)) % size)
		done += copy(out[done:], s.ring[off:])
	}
	return out, true
}

// tune retunes when the band needs another frequency and waits out the
// settle time, so samples still in flight from the old frequency are not
// counted. A band scanned twice in a row (calibration) needs no retune.
func (s *RTLTCPScanner) tune(ctx context.Context, freq int) error {
	s.mu.Lock()
	conn, tuned := s.conn, s.tuned
	s.mu.Unlock()
	if conn == nil {
		return errors.New("rtl_tcp: not connected")
	}
	if tuned == freq {
		return nil
	}
	if err := writeRTLTCPCommand(conn, rtlTCPCmdSetFreq, uint32(freq)); err != nil {
		s.stopReader("retune failed")
		return fmt.Errorf("rtl_tcp retune: %w", err)
	}
	s.mu.Lock()
	s.tuned = freq
	s.mu.Unlock()
	return sleepCtx(ctx, s.settle)
}

// capture returns the next n sample bytes that arrive from now on. A stream
// that delivers nothing for stallAfter is a stall: the reader is stopped so
// the next Scan starts a fresh process.
func (s *RTLTCPScanner) capture(ctx context.Context, n int) ([]byte, error) {
	s.mu.Lock()
	start := s.total
	notify := s.notify
	s.mu.Unlock()
	if start%2 != 0 { // keep I/Q pairs aligned
		start++
	}
	for attempt := 0; ; {
		s.mu.Lock()
		conn, readErr, last, total := s.conn, s.readErr, s.lastData, s.total
		var out []byte
		ok := false
		if conn != nil && total >= start+int64(n) {
			out, ok = s.copyRingLocked(start, n)
		}
		s.mu.Unlock()
		switch {
		case conn == nil:
			return nil, errors.New("rtl_tcp: reader stopped")
		case ok:
			return out, nil
		case total >= start+int64(n):
			// Overwritten before we copied it (only when this goroutine
			// was starved for longer than the ring holds). Take the next
			// window instead.
			attempt++
			if attempt > 3 {
				return nil, errors.New("rtl_tcp: sample window overwritten repeatedly")
			}
			start = total + total%2
			continue
		case readErr != nil:
			s.stopReader("stream closed")
			return nil, fmt.Errorf("rtl_tcp stream: %w", readErr)
		case time.Since(last) > s.stallAfter:
			s.stopReader("no samples")
			return nil, fmt.Errorf("rtl_tcp stalled: no samples for %s", time.Since(last).Truncate(time.Second))
		}
		wait := s.stallAfter - time.Since(last)
		if wait < 50*time.Millisecond {
			wait = 50 * time.Millisecond
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-notify:
		case <-t.C:
		}
		t.Stop()
	}
}

// rtlTCPPlan maps a band onto an M-point DFT at the reader's sample rate:
// bins of exactly binSize, the widened band starting at DFT bin FirstBin,
// the LO at Center. A band narrow enough to sit on one side of DC is tuned
// so the DC spike and its guard fall outside it; a wider band is centred
// and the one bin on DC is interpolated, as rtl_power_fftw did.
type rtlTCPPlan struct {
	Center   int
	M        int
	FirstBin int
	Bins     int
	DCBin    int // index into the band's bins that sits on DC, or -1
}

func planRTLTCP(g ScanGeometry, binSize, sampleRate int) (rtlTCPPlan, error) {
	if binSize <= 0 || sampleRate%binSize != 0 {
		return rtlTCPPlan{}, fmt.Errorf("rtl_tcp: bin size %d Hz does not divide the %d Hz sample rate", binSize, sampleRate)
	}
	m := sampleRate / binSize
	if m%2 != 0 {
		return rtlTCPPlan{}, fmt.Errorf("rtl_tcp: bin size %d Hz gives an odd DFT size %d", binSize, m)
	}
	bins := g.EffBins
	usable := int(float64(m) * rtlTCPUsableFraction) // bins each side of DC
	guard := (rtlTCPDCGuardHz + binSize - 1) / binSize
	if guard < 2 {
		guard = 2
	}
	var first int
	dc := -1
	switch {
	case guard+bins <= usable:
		first = m/2 + guard
	case bins/2 < m/2 && bins/2-g.CropPad <= usable:
		// Centred: only the interior has to be flat, the crop gutters
		// at the edges are discarded (GPS L1, LTE B8/B20: 2.3 MHz).
		first = m/2 - bins/2
		dc = bins / 2
	default:
		return rtlTCPPlan{}, fmt.Errorf("rtl_tcp: band of %d Hz does not fit %d Hz of usable bandwidth", bins*binSize, 2*usable*binSize)
	}
	// Bin k's centre is Center + (k - m/2)*binSize; the band's bin 0 centre
	// is WidenedLow + binSize/2.
	center := g.WidenedLow + binSize/2 + (m/2-first)*binSize
	return rtlTCPPlan{Center: center, M: m, FirstBin: first, Bins: bins, DCBin: dc}, nil
}

// bandPowers computes the band's bins from raw rtl_tcp bytes with
// rtl_power_fftw's arithmetic: samples are (byte - 127.0) with no window,
// power per bin is |X_k|^2 summed over the repeats, and the result is
// 10*log10(pwr / repeats / M / sampleRate) dB. Only the band's bins are
// computed, as direct DFT sums, which on the kits' bands costs a few
// million multiply-adds per scan. [MESHSAT-1222]
func bandPowers(raw []byte, p rtlTCPPlan, repeats, sampleRate int) []float64 {
	m := p.M
	// twiddle[j][n] = exp(-2*pi*i*(k-m/2)*n/m) for the band's bin j.
	twRe := make([][]float64, p.Bins)
	twIm := make([][]float64, p.Bins)
	for j := 0; j < p.Bins; j++ {
		k := p.FirstBin + j - m/2
		re := make([]float64, m)
		im := make([]float64, m)
		for n := 0; n < m; n++ {
			a := -2 * math.Pi * float64(k*n%m) / float64(m)
			re[n], im[n] = math.Cos(a), math.Sin(a)
		}
		twRe[j], twIm[j] = re, im
	}
	pwr := make([]float64, p.Bins)
	xr := make([]float64, m)
	xi := make([]float64, m)
	frames := len(raw) / (2 * m)
	if frames > repeats {
		frames = repeats
	}
	for f := 0; f < frames; f++ {
		base := f * 2 * m
		for n := 0; n < m; n++ {
			xr[n] = float64(raw[base+2*n]) - 127.0
			xi[n] = float64(raw[base+2*n+1]) - 127.0
		}
		for j := 0; j < p.Bins; j++ {
			re, im := twRe[j], twIm[j]
			var sr, si float64
			for n := 0; n < m; n++ {
				sr += xr[n]*re[n] - xi[n]*im[n]
				si += xr[n]*im[n] + xi[n]*re[n]
			}
			pwr[j] += sr*sr + si*si
		}
	}
	if p.DCBin >= 0 && p.DCBin < p.Bins {
		switch {
		case p.DCBin > 0 && p.DCBin+1 < p.Bins:
			pwr[p.DCBin] = (pwr[p.DCBin-1] + pwr[p.DCBin+1]) / 2
		case p.DCBin > 0:
			pwr[p.DCBin] = pwr[p.DCBin-1]
		case p.DCBin+1 < p.Bins:
			pwr[p.DCBin] = pwr[p.DCBin+1]
		}
	}
	out := make([]float64, p.Bins)
	if frames == 0 {
		frames = 1
	}
	norm := float64(frames) * float64(m) * float64(sampleRate)
	for j, v := range pwr {
		if v <= 0 {
			out[j] = -200
			continue
		}
		out[j] = 10 * math.Log10(v/norm)
	}
	return out
}

func constantBytes(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for _, c := range b[1:] {
		if c != b[0] {
			return false
		}
	}
	return true
}

func writeRTLTCPCommand(conn net.Conn, cmd byte, param uint32) error {
	var b [5]byte
	b[0] = cmd
	binary.BigEndian.PutUint32(b[1:], param)
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	_, err := conn.Write(b[:])
	return err
}

func readFull(conn net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := conn.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// spawnRTLTCP starts rtl_tcp on the scanner's loopback address. The process
// is always asked to stop with SIGTERM first: rtl_tcp's handler cancels the
// async read and the main loop closes the device, which a SIGKILL skips.
func spawnRTLTCP(s *RTLTCPScanner) (*rtlTCPProc, error) {
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(s.binary,
		"-a", host,
		"-p", port,
		"-s", strconv.Itoa(s.sampleRate),
		"-g", strconv.FormatFloat(float64(s.gainTenths)/10, 'f', 1, 64),
		"-b", strconv.Itoa(rtlTCPAsyncBuffers),
		"-n", strconv.Itoa(rtlTCPLinkedListBuffers),
	)
	tail := &lineTail{max: 8}
	cmd.Stdout = tail
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("rtl_tcp start: %w", err)
	}
	done := make(chan struct{})
	go func() {
		err := cmd.Wait()
		log.Info().Err(err).Str("last_output", tail.String()).Msg("spectrum: rtl_tcp exited")
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				log.Warn().Msg("spectrum: rtl_tcp ignored SIGTERM for 5 s, killing it")
				_ = cmd.Process.Kill()
				<-done
			}
		})
	}
	return &rtlTCPProc{stop: stop, done: done, tail: tail.String}, nil
}

// lineTail keeps the last few lines a process printed, for error messages.
// rtl_tcp prints "set freq ..." on every retune, so dropping old lines is
// the point.
type lineTail struct {
	mu    sync.Mutex
	max   int
	lines []string
	part  string
}

func (t *lineTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.part + string(p)
	parts := strings.Split(s, "\n")
	t.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "set ") || strings.HasPrefix(l, "ll+") || strings.HasPrefix(l, "ll-") {
			continue
		}
		t.lines = append(t.lines, l)
		if len(t.lines) > t.max {
			t.lines = t.lines[len(t.lines)-t.max:]
		}
	}
	return len(p), nil
}

func (t *lineTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "; ")
}
