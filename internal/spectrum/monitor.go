package spectrum

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/hemb"
)

// SigningService is the interface for audit logging (avoids circular import).
type SigningService interface {
	AuditEvent(eventType string, interfaceID, direction *string, deliveryID, ruleID *int64, detail string)
}

// SpectrumMonitor manages RTL-SDR spectrum monitoring and jamming detection.
type SpectrumMonitor struct {
	mu       sync.RWMutex
	scanner  Scanner
	bands    []Band
	status   map[string]*BandStatus
	baseline map[string]*Baseline
	signing  SigningService
	eventBus *hemb.EventBus
	cancel   context.CancelFunc
	enabled  bool

	// subs fan-out SpectrumEvents to the SSE stream endpoint and to the
	// main.go goroutine that relays transitions via CoT + hub reporter.
	// Slow subscribers get dropped frames (select/default) rather than
	// blocking the scan loop — losing a bin sample is harmless, but a
	// wedged scan loop would stop jamming detection entirely.
	subsMu sync.Mutex
	subs   []chan SpectrumEvent
}

// NewSpectrumMonitor creates a new monitor. It does not start scanning
// until Start is called. If no RTL-SDR is detected, the monitor stays
// dormant (returns disabled status but no errors).
func NewSpectrumMonitor(scanner Scanner, bands []Band) *SpectrumMonitor {
	status := make(map[string]*BandStatus, len(bands))
	for _, b := range bands {
		state := StateDisabled
		if scanner != nil && scanner.Available() {
			state = StateCalibrating
		}
		status[b.Name] = &BandStatus{
			Band:        b.Name,
			InterfaceID: b.InterfaceID,
			Label:       b.Label,
			State:       state,
			FreqLow:     b.FreqLow,
			FreqHigh:    b.FreqHigh,
			Since:       time.Now(),
		}
	}

	return &SpectrumMonitor{
		scanner:  scanner,
		bands:    bands,
		status:   status,
		baseline: make(map[string]*Baseline),
		eventBus: hemb.GlobalEventBus,
		enabled:  scanner != nil && scanner.Available(),
	}
}

// SetSigningService sets the audit logging service.
func (m *SpectrumMonitor) SetSigningService(ss SigningService) {
	m.signing = ss
}

// Start begins spectrum monitoring in a background goroutine.
// If the scanner is not available, this is a no-op.
func (m *SpectrumMonitor) Start(ctx context.Context) {
	if !m.enabled {
		log.Info().Msg("spectrum: RTL-SDR not available, monitoring disabled")
		return
	}

	ctx, m.cancel = context.WithCancel(ctx)
	log.Info().Int("bands", len(m.bands)).Msg("spectrum: starting RTL-SDR monitoring")

	go m.run(ctx)
}

// Stop halts spectrum monitoring.
func (m *SpectrumMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

// Status returns the current state of all monitored bands.
func (m *SpectrumMonitor) Status() []BandStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]BandStatus, 0, len(m.status))
	for _, bs := range m.status {
		result = append(result, *bs)
	}
	return result
}

// IsJammed returns true if the interface associated with the given ID
// is currently in a jammed state.
func (m *SpectrumMonitor) IsJammed(interfaceID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, bs := range m.status {
		if bs.InterfaceID == interfaceID && bs.State == StateJamming {
			return true
		}
	}
	return false
}

// Enabled reports whether RTL-SDR monitoring is active.
func (m *SpectrumMonitor) Enabled() bool {
	return m.enabled
}

// Subscribe registers a subscriber for scan + transition events. Returns a
// read-only channel and an unsubscribe function. The channel is buffered;
// if the consumer falls behind it silently drops frames — acceptable for
// the waterfall (resyncs on next scan) and for transitions (those are
// rare and also written to the audit log, so a dropped transition is
// still recoverable via a status query).
func (m *SpectrumMonitor) Subscribe() (<-chan SpectrumEvent, func()) {
	ch := make(chan SpectrumEvent, 64)
	m.subsMu.Lock()
	m.subs = append(m.subs, ch)
	m.subsMu.Unlock()

	unsub := func() {
		m.subsMu.Lock()
		defer m.subsMu.Unlock()
		for i, s := range m.subs {
			if s == ch {
				m.subs = append(m.subs[:i], m.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, unsub
}

func (m *SpectrumMonitor) publish(evt SpectrumEvent) {
	m.subsMu.Lock()
	subs := make([]chan SpectrumEvent, len(m.subs))
	copy(subs, m.subs)
	m.subsMu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- evt:
		default: // slow consumer — drop
		}
	}
}

func (m *SpectrumMonitor) run(ctx context.Context) {
	// Phase 1: Baseline calibration — sequential per band.
	for _, band := range m.bands {
		if ctx.Err() != nil {
			return
		}
		// Mark this band as the active calibration target so the UI
		// can show a live countdown + progress bar. Queued bands keep
		// their CalibrationStartedAt == zero and read as "queued".
		m.mu.Lock()
		m.status[band.Name].CalibrationStartedAt = time.Now()
		m.status[band.Name].CalibrationDurationSec = int(CalibrationDuration / time.Second)
		m.mu.Unlock()

		bl := m.calibrate(ctx, band)
		if bl != nil {
			m.mu.Lock()
			m.baseline[band.Name] = bl
			m.status[band.Name].State = StateClear
			m.status[band.Name].BaselineMean = bl.Mean
			m.status[band.Name].BaselineStd = bl.Std
			m.status[band.Name].Since = time.Now()
			m.status[band.Name].CalibrationStartedAt = time.Time{}
			m.status[band.Name].CalibrationDurationSec = 0
			m.mu.Unlock()
			log.Info().
				Str("band", band.Name).
				Float64("mean", bl.Mean).
				Float64("std", bl.Std).
				Int("samples", bl.Samples).
				Msg("spectrum: baseline calibrated")
		} else {
			// Failed to calibrate (insufficient samples). Clear the
			// progress indicator so the UI stops showing the bar for
			// this band — it's stuck in StateCalibrating with no ETA.
			m.mu.Lock()
			m.status[band.Name].CalibrationStartedAt = time.Time{}
			m.status[band.Name].CalibrationDurationSec = 0
			m.mu.Unlock()
		}
	}

	// Phase 2: Continuous monitoring. In parallel, kick a background
	// goroutine that re-attempts calibration for any band that failed
	// Phase 1 (state stayed "calibrating", no baseline). A wedged
	// dongle recovers over time — polling every 60 s means a transient
	// USB hang self-heals without a container restart, and the UI's
	// progress indicator picks up naturally when each retry fires.
	// [MESHSAT-509]
	go m.recalibrateUnresolved(ctx)

	ticker := time.NewTicker(ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scanAllBands(ctx)
		}
	}
}

// recalibrateUnresolved loops forever, every 60 s, and retries
// calibration for any band whose Phase 1 attempt didn't produce a
// baseline. Matches the Phase 1 loop's per-band behaviour exactly so
// the UI's progress bar / "queued" indicator work identically.
func (m *SpectrumMonitor) recalibrateUnresolved(ctx context.Context) {
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for _, band := range m.bands {
			if ctx.Err() != nil {
				return
			}
			m.mu.RLock()
			hasBaseline := m.baseline[band.Name] != nil
			m.mu.RUnlock()
			if hasBaseline {
				continue
			}
			// Mark active + retry
			m.mu.Lock()
			m.status[band.Name].CalibrationStartedAt = time.Now()
			m.status[band.Name].CalibrationDurationSec = int(CalibrationDuration / time.Second)
			m.mu.Unlock()
			bl := m.calibrate(ctx, band)
			if bl != nil {
				m.mu.Lock()
				m.baseline[band.Name] = bl
				m.status[band.Name].State = StateClear
				m.status[band.Name].BaselineMean = bl.Mean
				m.status[band.Name].BaselineStd = bl.Std
				m.status[band.Name].Since = time.Now()
				m.status[band.Name].CalibrationStartedAt = time.Time{}
				m.status[band.Name].CalibrationDurationSec = 0
				m.mu.Unlock()
				log.Info().Str("band", band.Name).
					Float64("mean", bl.Mean).Float64("std", bl.Std).
					Int("samples", bl.Samples).
					Msg("spectrum: baseline calibrated (retry)")
			} else {
				m.mu.Lock()
				m.status[band.Name].CalibrationStartedAt = time.Time{}
				m.status[band.Name].CalibrationDurationSec = 0
				m.mu.Unlock()
			}
		}
	}
}

func (m *SpectrumMonitor) calibrate(ctx context.Context, band Band) *Baseline {
	deadline := time.Now().Add(CalibrationDuration)
	var allPowers []float64

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil
		}

		// rtl_power timeout. The rtl-sdr-blog fork (required for the
		// RTL-SDR Blog V4's R828D tuner) adds noticeable cold-start
		// overhead — dongle auto-detection + tuner init can run past
		// 12 s on the Pi 5 + USB 2.0 hub. 30 s is generous but caps
		// runaway scans. Scan time is still dominated by the 1 s
		// integration window in practice. [MESHSAT-509]
		scanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		powers, err := m.scanner.Scan(scanCtx, band.FreqLow, band.FreqHigh, band.BinSize)
		cancel()

		if err != nil {
			log.Debug().Err(err).Str("band", band.Name).Msg("spectrum: calibration scan failed")
			time.Sleep(time.Second)
			continue
		}

		avg := avgPower(powers)
		maxPower := maxVal(powers)
		allPowers = append(allPowers, avg)

		// Publish a scan event during calibration too. Baseline mean
		// and std are zero until calibration finishes, so the UI
		// normalises against the raw sample range — the waterfall
		// paints from second one instead of sitting blank for the full
		// 2.5-minute cold-boot calibration window. Calibration
		// progress fields let the UI show a live countdown.
		// [MESHSAT-509]
		m.mu.RLock()
		calStart := m.status[band.Name].CalibrationStartedAt
		calDur := m.status[band.Name].CalibrationDurationSec
		m.mu.RUnlock()
		m.publish(SpectrumEvent{
			Kind:                   EventScan,
			Band:                   band.Name,
			Label:                  band.Label,
			InterfaceID:            band.InterfaceID,
			FreqLow:                band.FreqLow,
			FreqHigh:               band.FreqHigh,
			BinSize:                band.BinSize,
			Timestamp:              time.Now(),
			Powers:                 powers,
			AvgDB:                  avg,
			MaxDB:                  maxPower,
			State:                  StateCalibrating,
			BaselineMean:           0,
			BaselineStd:            0,
			ThreshJammingDB:        0,
			ThreshInterferenceDB:   0,
			CalibrationStartedAt:   calStart,
			CalibrationDurationSec: calDur,
		})

		time.Sleep(time.Second)
	}

	if len(allPowers) < 5 {
		log.Warn().Str("band", band.Name).Int("samples", len(allPowers)).
			Msg("spectrum: insufficient calibration samples")
		return nil
	}

	mean, std := meanStd(allPowers)
	return &Baseline{Mean: mean, Std: std, Samples: len(allPowers)}
}

func (m *SpectrumMonitor) scanAllBands(ctx context.Context) {
	for _, band := range m.bands {
		if ctx.Err() != nil {
			return
		}

		bl := m.baseline[band.Name]
		if bl == nil {
			continue // not calibrated yet
		}

		// rtl_power timeout. The rtl-sdr-blog fork (required for the
		// RTL-SDR Blog V4's R828D tuner) adds noticeable cold-start
		// overhead — dongle auto-detection + tuner init can run past
		// 12 s on the Pi 5 + USB 2.0 hub. 30 s is generous but caps
		// runaway scans. Scan time is still dominated by the 1 s
		// integration window in practice. [MESHSAT-509]
		scanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		powers, err := m.scanner.Scan(scanCtx, band.FreqLow, band.FreqHigh, band.BinSize)
		cancel()

		if err != nil {
			log.Debug().Err(err).Str("band", band.Name).Msg("spectrum: scan failed")
			continue
		}

		avg := avgPower(powers)
		maxPower := maxVal(powers)
		newState := m.evaluate(band.Name, avg, maxPower, bl)

		m.mu.Lock()
		bs := m.status[band.Name]
		bs.PowerDB = avg
		oldState := bs.State

		if newState != oldState {
			bs.State = newState
			bs.Since = time.Now()
			bs.Consecutive = 1
		} else {
			bs.Consecutive++
		}
		m.mu.Unlock()

		now := time.Now()
		m.publish(SpectrumEvent{
			Kind:                 EventScan,
			Band:                 band.Name,
			Label:                band.Label,
			InterfaceID:          band.InterfaceID,
			FreqLow:              band.FreqLow,
			FreqHigh:             band.FreqHigh,
			BinSize:              band.BinSize,
			Timestamp:            now,
			Powers:               powers,
			AvgDB:                avg,
			MaxDB:                maxPower,
			State:                newState,
			BaselineMean:         bl.Mean,
			BaselineStd:          bl.Std,
			ThreshJammingDB:      bl.Mean + JammingSigma*bl.Std,
			ThreshInterferenceDB: bl.Mean + InterferenceSigma*bl.Std,
		})

		if newState != oldState {
			m.onTransition(band, oldState, newState, avg, maxPower)
			m.publish(SpectrumEvent{
				Kind:                 EventTransition,
				Band:                 band.Name,
				Label:                band.Label,
				InterfaceID:          band.InterfaceID,
				FreqLow:              band.FreqLow,
				FreqHigh:             band.FreqHigh,
				BinSize:              band.BinSize,
				Timestamp:            now,
				AvgDB:                avg,
				MaxDB:                maxPower,
				State:                newState,
				OldState:             oldState,
				BaselineMean:         bl.Mean,
				BaselineStd:          bl.Std,
				ThreshJammingDB:      bl.Mean + JammingSigma*bl.Std,
				ThreshInterferenceDB: bl.Mean + InterferenceSigma*bl.Std,
			})
		}
	}
}

// evaluate applies the detection algorithm to determine spectrum state.
func (m *SpectrumMonitor) evaluate(bandName string, avgPower, maxPower float64, bl *Baseline) SpectrumState {
	m.mu.RLock()
	bs := m.status[bandName]
	consecutive := bs.Consecutive
	currentState := bs.State
	since := bs.Since
	m.mu.RUnlock()

	// Hysteresis: ignore transitions during cooldown
	if time.Since(since) < time.Duration(CooldownSeconds)*time.Second && currentState != StateClear {
		return currentState
	}

	// Check for broadband jamming: average power > baseline + 3*sigma
	if avgPower > bl.Mean+JammingSigma*bl.Std {
		if currentState == StateJamming {
			return StateJamming // maintain
		}
		if currentState != StateJamming && consecutive >= JammingConsecutive-1 {
			return StateJamming // confirm after consecutive threshold
		}
		return currentState // still counting up
	}

	// Check for narrowband interference: peak > baseline + 6*sigma
	if maxPower > bl.Mean+InterferenceSigma*bl.Std {
		if currentState == StateInterference {
			return StateInterference
		}
		if consecutive >= JammingConsecutive-1 {
			return StateInterference
		}
		return currentState
	}

	// Check for recovery: power within baseline +/- 1*sigma
	if avgPower <= bl.Mean+RecoverySigma*bl.Std {
		if currentState == StateClear {
			return StateClear
		}
		if consecutive >= RecoveryConsecutive-1 {
			return StateClear // confirmed recovery
		}
		return currentState // still counting
	}

	return currentState
}

func (m *SpectrumMonitor) onTransition(band Band, oldState, newState SpectrumState, avgPower, maxPower float64) {
	log.Warn().
		Str("band", band.Name).
		Str("interface", band.InterfaceID).
		Str("from", string(oldState)).
		Str("to", string(newState)).
		Float64("power_db", avgPower).
		Msg("spectrum: state transition")

	// Publish SSE event
	if m.eventBus != nil {
		detail := fmt.Sprintf("band=%s interface=%s power=%.1fdB",
			band.Name, band.InterfaceID, avgPower)
		payload, _ := json.Marshal(map[string]string{"detail": detail})
		m.eventBus.Publish(hemb.Event{
			Type:      hemb.EventType(fmt.Sprintf("SPECTRUM_%s", strings.ToUpper(string(newState)))),
			Timestamp: time.Now(),
			Payload:   payload,
		})
	}

	// Audit log
	if m.signing != nil {
		ifaceID := band.InterfaceID
		direction := "inbound"
		m.signing.AuditEvent(
			"spectrum_"+string(newState),
			&ifaceID,
			&direction,
			nil, nil,
			fmt.Sprintf("band=%s freq=%d-%dHz power=%.1fdB peak=%.1fdB prev=%s",
				band.Name, band.FreqLow, band.FreqHigh, avgPower, maxPower, oldState),
		)
	}
}

// DetectRTLSDR checks if an RTL-SDR dongle is connected via USB.
// Scans /sys/bus/usb/devices/ for known Realtek RTL2832U VID:PIDs.
func DetectRTLSDR() bool {
	entries, err := os.ReadDir("/sys/bus/usb/devices")
	if err != nil {
		return false
	}

	for _, entry := range entries {
		base := filepath.Join("/sys/bus/usb/devices", entry.Name())

		vidBytes, err := os.ReadFile(filepath.Join(base, "idVendor"))
		if err != nil {
			continue
		}
		pidBytes, err := os.ReadFile(filepath.Join(base, "idProduct"))
		if err != nil {
			continue
		}

		vid := strings.TrimSpace(string(vidBytes))
		pid := strings.TrimSpace(string(pidBytes))

		if vid == RTLSDR_VID && (pid == RTLSDR_PID_2832 || pid == RTLSDR_PID_2838) {
			return true
		}
	}
	return false
}

// Helper math functions

// avgPower / maxVal / meanStd skip NaN and ±Inf so a bad scan (rtl_power
// emits -Inf for all-zero FFT bins on the first post-tune read) doesn't
// poison the baseline or produce a JSON-unserialisable status response.
// Observed live: a single -Inf bin leaked through avgPower, made the
// baseline mean -Inf, and encoding/json dropped /api/spectrum/status to
// 0 bytes (NaN/Inf are not valid JSON per RFC 8259). [MESHSAT-509]
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func avgPower(values []float64) float64 {
	var sum float64
	var n int
	for _, v := range values {
		if !isFinite(v) {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return -100
	}
	return sum / float64(n)
}

func maxVal(values []float64) float64 {
	m := math.Inf(-1)
	for _, v := range values {
		if !isFinite(v) {
			continue
		}
		if v > m {
			m = v
		}
	}
	if math.IsInf(m, -1) {
		return -100
	}
	return m
}

// minStdFloor prevents the sigma classifier from collapsing to
// zero-width on LTE-style bands that are dominated by a locked
// carrier (observed std of 0.01 dB). Without this floor, 1σ = 0.01
// dB and any ordinary scan crosses the 3σ jamming line by 100× the
// intended margin, producing constant false-positive alerts.
const minStdFloor = 0.5

func meanStd(values []float64) (float64, float64) {
	var sum float64
	var n int
	for _, v := range values {
		if !isFinite(v) {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return 0, minStdFloor
	}
	mean := sum / float64(n)

	var sumSq float64
	for _, v := range values {
		if !isFinite(v) {
			continue
		}
		d := v - mean
		sumSq += d * d
	}
	std := math.Sqrt(sumSq / float64(n))
	if std < minStdFloor {
		std = minStdFloor
	}
	return mean, std
}
