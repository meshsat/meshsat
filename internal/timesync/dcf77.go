package timesync

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/warthog618/go-gpiocdev"

	"meshsat/internal/transport"
)

// DCF77 longwave time source (Stratum 1).
//
// Reads a stand-alone SP6007-class DCF77 demodulator (DCF-1060N-800 or similar)
// wired to two Pi GPIOs:
//
//	Module pin V  -> Pi 3V3
//	Module pin G  -> Pi GND
//	Module pin T  -> DataPin  (edge-triggered input, explicit internal pull-up)
//	Module pin P1 -> PONPin   (output; LOW = receiver on, for SP6007)
//
// Most SP6007-class modules present an open-drain T output, so the host must
// provide a pull-up. The Pi 5 internal 50 kΩ pull-up is marginal but usable;
// add a 3–10 kΩ external pull-up to 3V3 on marginal kits.
//
// Decoder is the standard 59-bit DCF77 minute frame:
//
//	bit  0     M   (start, 0)
//	bit 20     S   (start-of-time, 1)
//	bits 21-27 minutes BCD
//	bit 28     P1  (even parity of bits 21-27)
//	bits 29-34 hours BCD
//	bit 35     P2  (even parity of bits 29-34)
//	bits 36-41 day of month BCD
//	bits 42-44 day of week BCD
//	bits 45-49 month BCD
//	bits 50-57 year within century BCD
//	bit 58     P3  (even parity of bits 36-57)
//
// A carrier-reduction pulse of ~100 ms encodes a 0, ~200 ms encodes a 1.
// Second 59 is silent (no pulse) — that silence is the minute mark.

// Pulse-width classification.
const (
	dcf77Bit0Min       = 60 * time.Millisecond
	dcf77Bit0Max       = 140 * time.Millisecond
	dcf77Bit1Min       = 160 * time.Millisecond
	dcf77Bit1Max       = 260 * time.Millisecond
	dcf77MinuteMarkMin = 1500 * time.Millisecond
	dcf77WarmupEdges   = 20
	dcf77WarmupMinSec  = 5.0
	dcf77FrameBits     = 59
	// Uncertainty reported to TimeService. 50 ms covers kernel
	// timestamp jitter + end-of-frame detection latency (we only
	// learn the minute mark arrived when the first pulse of the
	// next minute shows up, ~1.9 s later — but the decoded time
	// IS the minute mark, not the detection instant, so the
	// uncertainty is bounded by edge-timestamp jitter).
	dcf77UncertaintyNs = 50 * 1_000_000
)

// DCF77Config holds wiring and runtime options for the DCF77 source.
type DCF77Config struct {
	// DataPin is the BCM offset wired to the module's T output.
	DataPin int
	// PONPin is the BCM offset wired to the module's P1 enable input.
	// Zero disables PON management (PONManaged="systemd" achieves the same).
	PONPin int
	// PONActiveLow: true (default) drives PON LOW to enable the receiver,
	// matching SP6007-class modules. Set false for modules that need HIGH.
	PONActiveLow bool
	// PONManaged: "" or "bridge" = bridge drives PON; "systemd" = an
	// external host service holds PON and the bridge must not claim it.
	PONManaged string
}

// DCF77Source implements TimeSource against a DCF77 receiver.
type DCF77Source struct {
	cfg DCF77Config

	mu       sync.Mutex
	dataLine transport.GPIOLine
	ponLine  transport.GPIOLine
	events   chan dcf77Edge
}

type dcf77Edge struct {
	t     time.Time
	level int // level after the edge: 1 = rising, 0 = falling
}

// NewDCF77Source constructs a DCF77 time source.
func NewDCF77Source(cfg DCF77Config) *DCF77Source {
	return &DCF77Source{
		cfg:    cfg,
		events: make(chan dcf77Edge, 256),
	}
}

// Name implements TimeSource.
func (s *DCF77Source) Name() string { return "dcf77" }

// Stratum implements TimeSource. DCF77 is a primary time reference (PZF).
func (s *DCF77Source) Stratum() int { return 1 }

// Start implements TimeSource. Blocks for the lifetime of ctx.
func (s *DCF77Source) Start(ctx context.Context, cb CorrectionCallback) {
	if err := s.setup(); err != nil {
		log.Warn().Err(err).
			Int("data_bcm", s.cfg.DataPin).
			Int("pon_bcm", s.cfg.PONPin).
			Msg("timesync: dcf77 setup failed — source disabled")
		return
	}
	defer s.teardown()

	log.Info().
		Int("data_bcm", s.cfg.DataPin).
		Int("pon_bcm", s.cfg.PONPin).
		Bool("pon_active_low", s.cfg.PONActiveLow).
		Str("pon_managed", s.cfg.PONManaged).
		Msg("timesync: dcf77 source started")

	// One-shot diagnostic: read the data line immediately after claiming
	// it with pull-up. This appears in production logs and tells us
	// whether the line is alive at startup without any user involvement.
	if v, err := s.dataLine.Value(); err == nil {
		log.Info().
			Int("bcm", s.cfg.DataPin).
			Int("level", v).
			Msg("timesync: dcf77 data line initial level (pull-up bias)")
	}

	s.runDecoder(ctx, cb)
}

func (s *DCF77Source) setup() error {
	// Claim PON first (so the receiver is enabled by the time we
	// start watching edges on the data line).
	pon := strings.ToLower(s.cfg.PONManaged)
	if pon != "systemd" && s.cfg.PONPin > 0 {
		initial := 0
		if !s.cfg.PONActiveLow {
			initial = 1
		}
		line, err := transport.OpenOutput(s.cfg.PONPin, initial, "dcf77-pon")
		if err != nil {
			return fmt.Errorf("open dcf77 PON pin (BCM %d): %w", s.cfg.PONPin, err)
		}
		s.ponLine = line
	}

	// Claim the data line with EXPLICIT pull-up — the module is
	// typically open-drain on T and the Pi 5 RP1 pad default (pull-down
	// for BCM 9-27) would otherwise hold the line LOW.
	line, err := transport.WatchBothEdges(
		s.cfg.DataPin,
		gpiocdev.WithPullUp,
		"dcf77-data",
		s.handleEdge,
	)
	if err != nil {
		// Roll PON back before returning error, so a retry sees a
		// clean slate.
		if s.ponLine != nil {
			_ = s.ponLine.Close()
			s.ponLine = nil
		}
		return fmt.Errorf("watch dcf77 data pin (BCM %d): %w", s.cfg.DataPin, err)
	}
	s.dataLine = line
	return nil
}

func (s *DCF77Source) teardown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataLine != nil {
		_ = s.dataLine.Close()
		s.dataLine = nil
	}
	if s.ponLine != nil {
		_ = s.ponLine.Close()
		s.ponLine = nil
	}
}

// handleEdge is invoked by go-gpiocdev's kernel-driven event goroutine.
// It must not block; we push onto a generous buffered channel and drop
// on overrun rather than ever blocking the kernel pump.
func (s *DCF77Source) handleEdge(event gpiocdev.LineEvent) {
	level := 1
	if event.Type == gpiocdev.LineEventFallingEdge {
		level = 0
	}
	edge := dcf77Edge{t: time.Now(), level: level}
	select {
	case s.events <- edge:
	default:
		// Buffer overrun — decoder stalled. Very unlikely at DCF77's
		// ~120 edges/minute into a 256-deep queue, but if it does
		// happen we drop the edge rather than jam up the kernel side.
	}
}

func (s *DCF77Source) runDecoder(ctx context.Context, cb CorrectionCallback) {
	var (
		lastEdge    *dcf77Edge
		warmup      []dcf77WarmupPulse
		pulseState  = -1 // -1=unknown; 0 or 1 = the line level that represents a carrier-reduction pulse
		bits        []int
		silentSince = time.Now()
		edgeCount   int64
		framesOK    int64
		framesBad   int64
	)
	diag := time.NewTicker(30 * time.Second)
	defer diag.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case e := <-s.events:
			edgeCount++
			silentSince = e.t
			if lastEdge == nil {
				cp := e
				lastEdge = &cp
				continue
			}
			dt := e.t.Sub(lastEdge.t)
			prevState := 1 - e.level // the line level that just ended
			cp := e
			lastEdge = &cp

			// Polarity auto-detect.
			if pulseState < 0 {
				warmup = append(warmup, dcf77WarmupPulse{dt: dt, state: prevState})
				if len(warmup) >= dcf77WarmupEdges {
					pulseState = detectPulsePolarity(warmup)
					if pulseState >= 0 {
						log.Info().
							Int("pulse_level", pulseState).
							Msg("timesync: dcf77 polarity locked")
						bits = bits[:0]
					} else if len(warmup) > 200 {
						warmup = warmup[len(warmup)-120:]
					}
				}
				continue
			}

			// Decode phase (polarity locked).
			if prevState == pulseState {
				// Carrier-reduction pulse just ended — classify.
				switch {
				case dt >= dcf77Bit0Min && dt <= dcf77Bit0Max:
					bits = append(bits, 0)
				case dt >= dcf77Bit1Min && dt <= dcf77Bit1Max:
					bits = append(bits, 1)
					// else: glitch / out-of-spec, drop silently
				}
			} else if dt >= dcf77MinuteMarkMin {
				// Long silence ended — minute mark.
				if len(bits) == dcf77FrameBits {
					utc, ok := decodeDCF77Frame(bits)
					if ok {
						framesOK++
						offsetNs := utc.UnixNano() - time.Now().UnixNano()
						cb("dcf77", 1, offsetNs, dcf77UncertaintyNs)
						log.Info().
							Time("utc", utc).
							Float64("offset_ms", float64(offsetNs)/1e6).
							Int64("frames_ok", framesOK).
							Int64("frames_bad", framesBad).
							Msg("timesync: dcf77 frame decoded")
					} else {
						framesBad++
						log.Warn().
							Int64("frames_bad", framesBad).
							Msg("timesync: dcf77 frame rejected (parity or range)")
					}
				} else if len(bits) > 0 {
					log.Debug().Int("bits", len(bits)).Msg("timesync: dcf77 partial frame")
				}
				bits = bits[:0]
			}

		case now := <-diag.C:
			silent := now.Sub(silentSince)
			if silent > 30*time.Second {
				level := -1
				if s.dataLine != nil {
					if v, err := s.dataLine.Value(); err == nil {
						level = v
					}
				}
				log.Warn().
					Dur("silent_for", silent).
					Int("line_level", level).
					Int64("edges_total", edgeCount).
					Int("polarity", pulseState).
					Msg("timesync: dcf77 line quiet — check antenna, short, or SMPS noise")
			}
		}
	}
}

type dcf77WarmupPulse struct {
	dt    time.Duration
	state int
}

// detectPulsePolarity returns the line level that corresponds to a
// carrier-reduction pulse, or -1 if it cannot be determined from the
// supplied warmup window. A DCF77 signal spends roughly 100-200 ms per
// second in the pulse state and 800-900 ms in the idle state, so the
// idle state dominates the total duration. Whichever of {0, 1} holds
// the line the MINORITY of the time is the pulse level.
func detectPulsePolarity(warmup []dcf77WarmupPulse) int {
	var t0, t1 time.Duration
	for _, w := range warmup {
		if w.state == 0 {
			t0 += w.dt
		} else {
			t1 += w.dt
		}
	}
	total := t0 + t1
	if total.Seconds() < dcf77WarmupMinSec {
		return -1
	}
	if t0 == 0 || t1 == 0 {
		return -1
	}
	// Require a clear duty-cycle imbalance so noise doesn't lock us
	// onto the wrong polarity.
	var ratio float64
	if t0 < t1 {
		ratio = float64(t0) / float64(t1)
		if ratio < 0.5 {
			return 0 // LOW is the pulse (inverted — typical for SP6007)
		}
	} else {
		ratio = float64(t1) / float64(t0)
		if ratio < 0.5 {
			return 1 // HIGH is the pulse
		}
	}
	return -1
}

// decodeDCF77Frame validates the 59-bit frame and returns the decoded
// UTC time at the minute mark the frame belongs to.
func decodeDCF77Frame(bits []int) (time.Time, bool) {
	if len(bits) != dcf77FrameBits {
		return time.Time{}, false
	}
	if bits[0] != 0 {
		return time.Time{}, false
	}
	if bits[20] != 1 {
		return time.Time{}, false
	}

	minutes := bcdDecode(bits[21:28], []int{1, 2, 4, 8, 10, 20, 40})
	if !evenParity(bits[21:29]) {
		return time.Time{}, false
	}
	hours := bcdDecode(bits[29:35], []int{1, 2, 4, 8, 10, 20})
	if !evenParity(bits[29:36]) {
		return time.Time{}, false
	}
	day := bcdDecode(bits[36:42], []int{1, 2, 4, 8, 10, 20})
	month := bcdDecode(bits[45:50], []int{1, 2, 4, 8, 10})
	year := bcdDecode(bits[50:58], []int{1, 2, 4, 8, 10, 20, 40, 80})
	if !evenParity(bits[36:59]) {
		return time.Time{}, false
	}

	if month < 1 || month > 12 || day < 1 || day > 31 ||
		hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return time.Time{}, false
	}

	// Timezone: bit 17 = CEST (UTC+2), bit 18 = CET (UTC+1).
	// If neither is set, assume CET.
	tzOffsetHours := 1
	if bits[17] == 1 {
		tzOffsetHours = 2
	}
	civil := time.Date(2000+year, time.Month(month), day, hours, minutes, 0, 0, time.UTC)
	utc := civil.Add(-time.Duration(tzOffsetHours) * time.Hour)
	return utc, true
}

func bcdDecode(bits, weights []int) int {
	sum := 0
	for i, b := range bits {
		sum += b * weights[i]
	}
	return sum
}

func evenParity(bits []int) bool {
	p := 0
	for _, b := range bits {
		p ^= b
	}
	return p == 0
}
