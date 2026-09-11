package spectrum

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"sort"
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

	// History persistence. Optional — if nil, monitor runs as before
	// and the detail/prefill features degrade gracefully. Assigned via
	// SetHistoryStore in main.go after the DB is open. [MESHSAT-650]
	//
	// Writes retry forever. A 5 s context timeout is a transient symptom
	// (DB busy during boot-time contention with migrations / keystore /
	// HeMB init), not a permanent failure — so we never latch "disabled".
	// Errors are rate-limited to one warning per minute to avoid log
	// flooding, and a recovery line fires when writes resume after an
	// error window. [MESHSAT-653]
	store            HistoryStore
	retentionHours   int
	persistInFlight  sync.WaitGroup // drained on Stop so tests don't leak goroutines
	persistMu        sync.Mutex
	persistErrCount  int       // errors observed in the current error window; 0 when healthy
	persistLastErrAt time.Time // last time we logged a persistence warning (for rate limiting)

	// subs fan-out SpectrumEvents to the SSE stream endpoint and to the
	// main.go goroutine that relays transitions via CoT + hub reporter.
	// Slow subscribers get dropped frames (select/default) rather than
	// blocking the scan loop — losing a bin sample is harmless, but a
	// wedged scan loop would stop jamming detection entirely.
	subsMu sync.Mutex
	subs   []chan SpectrumEvent

	// hardware stats for the /api/spectrum/hardware endpoint —
	// protected by mu along with status/baseline.
	lastScanAt       time.Time
	lastScanDuration time.Duration
	scanErrorCount   int64
	lastScanError    string
	lastScanErrorAt  time.Time

	// Device health inputs [MESHSAT-817]. scanMu serialises every
	// scanner exec: the calibration retry loop and the 3 s scan loop
	// used to run rtl_power_fftw concurrently and fought for the one
	// dongle (tesseract, 5 Sep 2026: usb_claim_interface -6 once a
	// second). curCancel kills the running child on RestartScan.
	scanMu           sync.Mutex
	curCancel        context.CancelFunc
	consecutiveFails int
	lastGoodScan     time.Time
	startedAt        time.Time

	// MIJI/CoT relay outcome tracker. Owned here so the HTTP layer
	// has a single accessor (SpectrumMonitor.RelayTracker()); the
	// main.go relay goroutine calls RecordSuccess/RecordFailure after
	// each attempt.
	relay *RelayTracker
}

// RelayTracker returns the live per-destination MIJI/CoT relay
// tracker. Initialised lazily on first call so existing test helpers
// that construct a raw &SpectrumMonitor{...} still work.
func (m *SpectrumMonitor) RelayTracker() *RelayTracker {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.relay == nil {
		m.relay = NewRelayTracker()
	}
	return m.relay
}

// HardwareStatus is the payload returned by /api/spectrum/hardware.
// Combines the static scanner descriptor with runtime health signals
// so operators can diagnose "is the dongle alive and scanning?".
type HardwareStatus struct {
	Available         bool        `json:"available"`
	Scanner           ScannerInfo `json:"scanner"`
	LastScanAt        time.Time   `json:"last_scan_at,omitempty"`
	LastScanMs        int64       `json:"last_scan_ms"`
	ScanErrorCount    int64       `json:"scan_error_count"`
	LastScanError     string      `json:"last_scan_error,omitempty"`
	LastScanErrorAt   time.Time   `json:"last_scan_error_at,omitempty"`
	ScanIntervalSec   int         `json:"scan_interval_sec"`
	CalibrationDurSec int         `json:"calibration_duration_sec"`
	ConsecutiveErrors int         `json:"consecutive_errors"`
	LastGoodScanAt    time.Time   `json:"last_good_scan_at,omitempty"`
}

// Hardware returns the current hardware + scan-loop health snapshot.
// Called by /api/spectrum/hardware; cheap read-lock.
func (m *SpectrumMonitor) Hardware() HardwareStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	hs := HardwareStatus{
		Available:         m.enabled,
		LastScanAt:        m.lastScanAt,
		LastScanMs:        m.lastScanDuration.Milliseconds(),
		ScanErrorCount:    m.scanErrorCount,
		LastScanError:     m.lastScanError,
		LastScanErrorAt:   m.lastScanErrorAt,
		ScanIntervalSec:   int(ScanInterval / time.Second),
		CalibrationDurSec: int(CalibrationDuration / time.Second),
		ConsecutiveErrors: m.consecutiveFails,
		LastGoodScanAt:    m.lastGoodScan,
	}
	if m.scanner != nil {
		hs.Scanner = m.scanner.Info()
	}
	return hs
}

// scanOnce runs one scanner exec under scanMu so the calibration and
// scan loops never overlap on the dongle, and keeps the health counters.
// [MESHSAT-817]
func (m *SpectrumMonitor) scanOnce(ctx context.Context, band Band, timeout time.Duration) ([]float64, error) {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	scanCtx, cancel := context.WithTimeout(ctx, timeout)
	m.mu.Lock()
	m.curCancel = cancel
	m.mu.Unlock()
	powers, err := m.scanner.Scan(scanCtx, band.FreqLow, band.FreqHigh, band.BinSize, band.EffectiveCropPad())
	cancel()
	m.mu.Lock()
	m.curCancel = nil
	if err != nil {
		m.consecutiveFails++
		m.lastScanError = err.Error()
		m.lastScanErrorAt = time.Now()
	} else {
		m.consecutiveFails = 0
		m.lastGoodScan = time.Now()
	}
	m.mu.Unlock()
	return powers, err
}

// Uptime is how long the monitor has been running (zero before Start).
func (m *SpectrumMonitor) Uptime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.startedAt.IsZero() {
		return 0
	}
	return time.Since(m.startedAt)
}

// LastGoodScan is when a scan last returned samples, and how many scans
// have failed in a row since. [MESHSAT-817]
func (m *SpectrumMonitor) LastGoodScan() (time.Time, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastGoodScan, m.consecutiveFails
}

// RestartScan kills the scanner child currently running (a hung
// rtl_power_fftw holds the dongle for the whole 90 s timeout); the loops
// carry on with a fresh exec. The failure counter is left alone so only a
// scan that really returns samples counts as recovery. Level 1 of the
// device health ladder for the RTL-SDR. [MESHSAT-817]
func (m *SpectrumMonitor) RestartScan(_ context.Context) error {
	m.mu.Lock()
	enabled, cancel := m.enabled, m.curCancel
	m.mu.Unlock()
	if !enabled {
		return fmt.Errorf("spectrum monitor disabled")
	}
	if cancel != nil {
		log.Warn().Msg("spectrum: cancelling the running scan")
		cancel()
	}
	return nil
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

// SetHistoryStore wires the persistence backend. Safe to call before
// Start; calling after Start is allowed (writes just begin flowing on
// the next scan) but uncommon. A nil store is allowed and treated as
// "persistence disabled" — the monitor stays fully functional for
// live scanning and jamming detection, only the prefill/detail-view
// features go dormant. [MESHSAT-650]
func (m *SpectrumMonitor) SetHistoryStore(s HistoryStore) {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.store = s
	m.persistErrCount = 0
	m.persistLastErrAt = time.Time{}
}

// SetRetentionHours overrides the retention window used by the trim
// goroutine. Value is clamped to [1, MaxRetentionHours]; zero or
// negative inputs fall back to DefaultRetentionHours. Called once at
// startup from main.go after reading MESHSAT_SPECTRUM_RETENTION_HOURS.
func (m *SpectrumMonitor) SetRetentionHours(hours int) {
	m.retentionHours = ClampRetention(hours)
}

// Start begins spectrum monitoring in a background goroutine.
// If the scanner is not available, this is a no-op.
func (m *SpectrumMonitor) Start(ctx context.Context) {
	if !m.Enabled() {
		log.Info().Msg("spectrum: RTL-SDR not available, monitoring disabled")
		return
	}
	m.startLoops(ctx)
}

// startLoops launches the scan and retention goroutines. Caller has
// established that a scanner is attached.
func (m *SpectrumMonitor) startLoops(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	m.mu.Lock()
	m.startedAt = time.Now()
	m.mu.Unlock()
	log.Info().Int("bands", len(m.bands)).Msg("spectrum: starting RTL-SDR monitoring")

	go m.run(ctx)
	// Retention loop only when we have a store — otherwise there's
	// nothing to trim. Runs forever until ctx cancels.
	if m.store != nil {
		go m.retentionLoop(ctx)
	}
}

// Attach gives a monitor that started without a dongle a scanner at
// runtime and starts monitoring, exactly as if the dongle had been
// present at boot: every band goes back to calibrating and the
// scan/retention loops start. A no-op when monitoring already runs or
// the scanner is not available. Reports whether monitoring started.
//
// Why: the RTL-SDR is the one kit device that is detected once at
// startup. When it fell off parallax's USB bus on 10 Sep 2026 the next
// deploy restarted the bridge with no dongle, the monitor came up
// disabled and stayed disabled, and a re-seat would have needed another
// container restart to be noticed. [MESHSAT-1002]
func (m *SpectrumMonitor) Attach(ctx context.Context, scanner Scanner) bool {
	if scanner == nil || !scanner.Available() {
		return false
	}
	m.mu.Lock()
	if m.enabled {
		m.mu.Unlock()
		return false
	}
	m.scanner = scanner
	m.enabled = true
	now := time.Now()
	for _, bs := range m.status {
		bs.State = StateCalibrating
		bs.Since = now
		bs.CalibrationStartedAt = time.Time{}
	}
	m.mu.Unlock()
	log.Info().Str("binary", scanner.Info().BinaryPath).Msg("spectrum: RTL-SDR attached at runtime, monitoring enabled")
	m.startLoops(ctx)
	return true
}

// DongleWatchInterval is how often AttachWhenPresent looks for a dongle.
const DongleWatchInterval = 30 * time.Second

// AttachWhenPresent polls the USB bus until an RTL-SDR appears, then
// builds a scanner and attaches it to the monitor. Meant for a monitor
// that was constructed without a scanner because no dongle was present
// at startup; returns as soon as monitoring starts or ctx ends. The
// scanner binary must be on PATH, otherwise there is nothing to attach
// and the loop exits at once. [MESHSAT-1002]
func AttachWhenPresent(ctx context.Context, m *SpectrumMonitor, interval time.Duration) {
	if m == nil || interval <= 0 {
		return
	}
	if _, err := exec.LookPath("rtl_power_fftw"); err != nil {
		if _, err := exec.LookPath("rtl_power"); err != nil {
			return
		}
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if m.Enabled() {
				return
			}
			if !DetectRTLSDR() {
				continue
			}
			if s := NewRTLPowerScanner(); s != nil && m.Attach(ctx, s) {
				return
			}
		}
	}
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

// Enabled reports whether RTL-SDR monitoring is active. It can flip
// from false to true at runtime through Attach. [MESHSAT-1002]
func (m *SpectrumMonitor) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
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

	m.persist(evt)
}

// persist hands an event off to the history store in a background
// goroutine so a slow DB write never delays the next scan. Scan
// events turn into one ScanRow; transitions generate both a Scan row
// (implicit — already stored on the last scan) AND a TransitionRow
// used for alert markers. Errors are logged once and then suppressed
// so we don't flood the log on a long outage. [MESHSAT-650]
func (m *SpectrumMonitor) persist(evt SpectrumEvent) {
	m.persistMu.Lock()
	store := m.store
	m.persistMu.Unlock()
	if store == nil {
		return
	}

	m.persistInFlight.Add(1)
	go func() {
		defer m.persistInFlight.Done()
		// 15 s is enough headroom for early-boot SQLite contention while
		// still keeping a single stuck write from piling up goroutines.
		// Healthy saves finish in <10 ms; the long tail we care about is
		// migrations / keystore / HeMB init fighting for the writer lock.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		switch evt.Kind {
		case EventScan:
			// Only persist scans with actual bin data — transition-time
			// re-publishes carry powers==nil (they reuse the scan just
			// written) so storing an empty-powers row would waste space.
			if len(evt.Powers) == 0 {
				return
			}
			row := ScanRow{
				TS:           evt.Timestamp,
				Band:         evt.Band,
				State:        string(evt.State),
				AvgDB:        evt.AvgDB,
				MaxDB:        evt.MaxDB,
				BaselineMean: evt.BaselineMean,
				BaselineStd:  evt.BaselineStd,
				Powers:       evt.Powers,
			}
			if err := store.SaveScan(ctx, row); err != nil {
				m.notePersistError("save scan", err)
				return
			}
			m.notePersistSuccess()
		case EventTransition:
			row := TransitionRow{
				TS:           evt.Timestamp,
				Band:         evt.Band,
				OldState:     string(evt.OldState),
				NewState:     string(evt.State),
				PeakDB:       evt.MaxDB,
				PeakFreqHz:   int64(evt.EventPeakFreqHz),
				BaselineMean: evt.BaselineMean,
				BaselineStd:  evt.BaselineStd,
			}
			if err := store.SaveTransition(ctx, row); err != nil {
				m.notePersistError("save transition", err)
				return
			}
			m.notePersistSuccess()
		}
	}()
}

// persistLogThrottle gates warning emissions so a sustained DB outage
// doesn't flood logs. One line per window is enough to surface the
// problem without drowning the signal. Exposed as a var so tests can
// shorten the window; do not mutate in production code. [MESHSAT-653]
var persistLogThrottle = 1 * time.Minute

// notePersistError bumps the error counter and emits a rate-limited
// warning. Writes are NOT disabled — the monitor keeps retrying on
// every scan because a context timeout is a transient symptom, not a
// permanent failure. [MESHSAT-653]
func (m *SpectrumMonitor) notePersistError(op string, err error) {
	m.persistMu.Lock()
	m.persistErrCount++
	shouldLog := m.persistLastErrAt.IsZero() ||
		time.Since(m.persistLastErrAt) >= persistLogThrottle
	count := m.persistErrCount
	if shouldLog {
		m.persistLastErrAt = time.Now()
	}
	m.persistMu.Unlock()
	if shouldLog {
		log.Warn().Err(err).Str("op", op).Int("errors_in_window", count).
			Msg("spectrum: history persistence write failed — will keep retrying")
	}
}

// notePersistSuccess clears the error window and, if the window was
// non-empty, logs a recovery line so the operator sees the resolution.
// Cheap no-op on the healthy path (single lock + counter read).
func (m *SpectrumMonitor) notePersistSuccess() {
	m.persistMu.Lock()
	count := m.persistErrCount
	if count == 0 {
		m.persistMu.Unlock()
		return
	}
	m.persistErrCount = 0
	m.persistLastErrAt = time.Time{}
	m.persistMu.Unlock()
	log.Info().Int("suppressed_errors", count).
		Msg("spectrum: history persistence recovered")
}

// retentionLoop trims old rows on a regular cadence. Runs hourly —
// bounds DB growth with negligible write amplification. The first
// trim fires ~30 s after start so a restart triggers cleanup without
// waiting an hour.
func (m *SpectrumMonitor) retentionLoop(ctx context.Context) {
	initial := time.NewTimer(30 * time.Second)
	defer initial.Stop()
	select {
	case <-ctx.Done():
		return
	case <-initial.C:
	}
	m.runTrim(ctx)

	tick := time.NewTicker(1 * time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.runTrim(ctx)
		}
	}
}

func (m *SpectrumMonitor) runTrim(ctx context.Context) {
	m.persistMu.Lock()
	store := m.store
	hours := m.retentionHours
	m.persistMu.Unlock()
	if store == nil {
		return
	}
	if hours <= 0 {
		hours = DefaultRetentionHours
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n, err := store.TrimSpectrumHistory(tctx, cutoff)
	if err != nil {
		log.Warn().Err(err).Int("retention_hours", hours).Msg("spectrum: retention trim failed")
		return
	}
	if n > 0 {
		log.Info().Int64("rows", n).Int("retention_hours", hours).
			Msg("spectrum: trimmed history rows older than retention window")
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
			m.status[band.Name].BaselineMad = bl.Mad
			m.status[band.Name].Since = time.Now()
			m.status[band.Name].CalibrationStartedAt = time.Time{}
			m.status[band.Name].CalibrationDurationSec = 0
			m.mu.Unlock()
			log.Info().
				Str("band", band.Name).
				Float64("mean", bl.Mean).
				Float64("std", bl.Std).
				Float64("mad", bl.Mad).
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
				m.status[band.Name].BaselineMad = bl.Mad
				m.status[band.Name].Since = time.Now()
				m.status[band.Name].CalibrationStartedAt = time.Time{}
				m.status[band.Name].CalibrationDurationSec = 0
				m.mu.Unlock()
				log.Info().Str("band", band.Name).
					Float64("mean", bl.Mean).Float64("std", bl.Std).Float64("mad", bl.Mad).
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

// calibrate builds a band's baseline: it scans for at least
// CalibrationDuration and, when that window yielded fewer than
// MinCalibrationSamples, keeps going until it has them or
// CalibrationMaxDuration is up. The sample count, not the clock, decides
// the outcome; the clock only bounds the cost. A band that cannot reach
// the minimum inside the ceiling returns nil with the measured scan
// duration in the warning, so a slow scan is visible in the log instead
// of failing quietly every minute. [MESHSAT-1017]
func (m *SpectrumMonitor) calibrate(ctx context.Context, band Band) *Baseline {
	start := time.Now()
	minDeadline := start.Add(CalibrationDuration)
	maxDeadline := start.Add(CalibrationMaxDuration)
	if maxDeadline.Before(minDeadline) {
		maxDeadline = minDeadline
	}
	var allPowers []float64
	var scanTotal time.Duration
	scans := 0
	extended := false

	for {
		if ctx.Err() != nil {
			return nil
		}
		now := time.Now()
		if !now.Before(minDeadline) {
			if len(allPowers) >= MinCalibrationSamples || !now.Before(maxDeadline) {
				break
			}
			if !extended {
				// Past the minimum window with too few samples: this
				// band's scans are slow. Stretch the UI's countdown to
				// the ceiling once, then keep sampling.
				extended = true
				m.mu.Lock()
				m.status[band.Name].CalibrationDurationSec = int(CalibrationMaxDuration / time.Second)
				m.mu.Unlock()
			}
		}

		scanStart := time.Now()
		powers, err := m.scanOnce(ctx, band, calibrationScanTimeout)
		scanTotal += time.Since(scanStart)
		scans++

		if err != nil {
			log.Debug().Err(err).Str("band", band.Name).Msg("spectrum: calibration scan failed")
			time.Sleep(calibrationPause)
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

		time.Sleep(calibrationPause)
	}

	var perScan time.Duration
	if scans > 0 {
		perScan = scanTotal / time.Duration(scans)
	}
	if len(allPowers) < MinCalibrationSamples {
		log.Warn().Str("band", band.Name).Int("samples", len(allPowers)).
			Int("min", MinCalibrationSamples).
			Dur("scan_avg", perScan).Dur("window", time.Since(start)).
			Msg("spectrum: insufficient calibration samples")
		return nil
	}
	if extended {
		log.Warn().Str("band", band.Name).Int("samples", len(allPowers)).
			Dur("scan_avg", perScan).Dur("window", time.Since(start)).
			Msg("spectrum: calibration needed the extended window, this band's scans are slow")
	}

	mean, std, mad := baselineStats(allPowers)
	return &Baseline{Mean: mean, Std: std, Mad: mad, Samples: len(allPowers)}
}

func (m *SpectrumMonitor) scanAllBands(ctx context.Context) {
	for _, band := range m.bands {
		if ctx.Err() != nil {
			return
		}

		// recalibrateUnresolved writes this map under mu from its own
		// goroutine; read it under the lock too. [MESHSAT-1017]
		m.mu.RLock()
		bl := m.baseline[band.Name]
		m.mu.RUnlock()
		if bl == nil {
			continue // not calibrated yet
		}

		// One exec per band per tick; the timeout rationale is on
		// calibrationScanTimeout. [MESHSAT-509, MESHSAT-656]
		scanStart := time.Now()
		powers, err := m.scanOnce(ctx, band, calibrationScanTimeout)
		scanDur := time.Since(scanStart)

		m.mu.Lock()
		m.lastScanAt = time.Now()
		m.lastScanDuration = scanDur
		if err != nil {
			m.scanErrorCount++
			m.lastScanError = err.Error()
			m.lastScanErrorAt = m.lastScanAt
		}
		m.mu.Unlock()

		if err != nil {
			log.Debug().Err(err).Str("band", band.Name).Msg("spectrum: scan failed")
			continue
		}

		avg := avgPower(powers)
		maxPower := maxVal(powers)
		candidate := m.evaluate(band.Name, powers, avg, maxPower, bl)

		// Compute MIJI-9 report metrics once per scan — cheap (O(n)
		// over ~12-120 bins) and exposed to the UI so the operator
		// doesn't have to eyeball the waterfall to judge barrage vs
		// narrowband.
		occupancy := bandOccupancy(powers, bl.Mean+6.0)
		flatness := spectralFlatness(powers)

		// Locate peak bin for MIJI-9 peak-freq reporting. nBins=0 is
		// impossible at this point (err already checked) but guard
		// defensively. peakFreqHz = FreqLow + (idx + 0.5) * binWidth.
		peakIdx := 0
		if len(powers) > 0 {
			pmax := powers[0]
			for i, p := range powers {
				if isFinite(p) && p > pmax {
					pmax = p
					peakIdx = i
				}
			}
		}
		peakFreqHz := band.FreqLow
		if len(powers) > 0 {
			binWidthHz := float64(band.FreqHigh-band.FreqLow) / float64(len(powers))
			peakFreqHz = band.FreqLow + int((float64(peakIdx)+0.5)*binWidthHz)
		}

		now := time.Now()
		m.mu.Lock()
		bs := m.status[band.Name]
		bs.PowerDB = avg
		bs.LastOccupancy = occupancy
		bs.LastFlatness = flatness
		oldState := bs.State

		// Candidate tracking: if the tier changed, reset dwell timer.
		if bs.CandidateState != candidate {
			bs.CandidateState = candidate
			bs.CandidateSince = now
		}
		heldFor := now.Sub(bs.CandidateSince)

		newState := promoteState(candidate, oldState, heldFor)

		if newState != oldState {
			bs.State = newState
			bs.Since = now
			bs.Consecutive = 1
			// Reset event-peak tracking — new state means new event;
			// the old peak belonged to the previous state window.
			bs.EventPeakDB = maxPower
			bs.EventPeakFreqHz = peakFreqHz
		} else {
			bs.Consecutive++
			// Only ratchet the event peak upward — MIJI-9 wants the
			// highest observed power for the whole event, not the
			// current scan's max (which falls back when a jammer pauses).
			if maxPower > bs.EventPeakDB {
				bs.EventPeakDB = maxPower
				bs.EventPeakFreqHz = peakFreqHz
			}
		}
		eventPeakDB := bs.EventPeakDB
		eventPeakFreqHz := bs.EventPeakFreqHz
		m.mu.Unlock()

		m.publish(SpectrumEvent{
			Kind:         EventScan,
			Band:         band.Name,
			Label:        band.Label,
			InterfaceID:  band.InterfaceID,
			FreqLow:      band.FreqLow,
			FreqHigh:     band.FreqHigh,
			BinSize:      band.BinSize,
			Timestamp:    now,
			Powers:       powers,
			AvgDB:        avg,
			MaxDB:        maxPower,
			State:        newState,
			BaselineMean: bl.Mean,
			BaselineStd:  bl.Std,
			BaselineMad:  bl.Mad,
			// Post-redesign: thresholds are now derived from absolute
			// power floor + occupancy/flatness features, not a single
			// sigma multiplier. Keep the JSON keys for UI backward
			// compat; expose the baseline+6 dB bin-activity cutoff
			// (occupancy comparison line) as ThreshInterferenceDB and
			// baseline+DegradedDeltaDB as ThreshJammingDB so the UI's
			// reference lines still make visual sense.
			ThreshJammingDB:      bl.Mean + DegradedDeltaDB,
			ThreshInterferenceDB: bl.Mean + 6.0,
			Occupancy:            occupancy,
			Flatness:             flatness,
			Since:                bs.Since,
			EventPeakDB:          eventPeakDB,
			EventPeakFreqHz:      eventPeakFreqHz,
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
				BaselineMad:          bl.Mad,
				ThreshJammingDB:      bl.Mean + DegradedDeltaDB,
				ThreshInterferenceDB: bl.Mean + 6.0,
				Occupancy:            occupancy,
				Flatness:             flatness,
				Since:                now,
				EventPeakDB:          eventPeakDB,
				EventPeakFreqHz:      eventPeakFreqHz,
			})
		}
	}
}

// evaluate applies the multi-feature detection algorithm.
//
// Input: a full scan's per-bin powers, plus aggregate avg/max and
// the band's calibrated baseline. Output: the state this scan votes
// for. State transitions apply persistence windows on top — see
// scanAllBands.
//
// Features combined:
//   - Absolute dBm floor (PowerFloorForBand) — below floor, never
//     jamming regardless of sigma; a -80 dBm noise swell is not an
//     EW event. Prevents runaway false positives on quiet bands.
//   - Spectral occupancy — fraction of bins above (baseline + 6 dB).
//     Barrage jammer ≥ 70 %; narrowband spike 30-70 %; legit burst
//     < 20 %.
//   - Spectral flatness — 0..1. White-noise jammers approach 1;
//     structured signals (LoRa, LTE, APRS) stay < 0.4. Separates a
//     "band is loud" event from "band is jammed".
//   - Average power elevation above baseline — flags DEGRADED
//     attention level for sustained moderate rise.
//
// Persistence (dwell time) is applied in scanAllBands by comparing
// how long the candidate state has been stable. Single-sample spikes
// (LoRa/APRS bursts, cellular fades) never promote beyond CLEAR.
//
// [MESHSAT-509 — research-grounded redesign after naive 3σ produced
//
//	constant false positives on residential Leiden RF.]
func (m *SpectrumMonitor) evaluate(bandName string, powers []float64, avgPower, maxPower float64, bl *Baseline) SpectrumState {
	// Absolute floor short-circuit: if the average power across the
	// whole band is below the band-specific floor, no spectral
	// activity counts as jamming — it's physically implausible for
	// a close-range jammer to produce less power than thermal noise.
	floor := PowerFloorForBand(bandName)
	if avgPower < floor {
		return StateClear
	}

	// Features
	thresholdDB := bl.Mean + 6.0 // same 6 dB cutoff the research recommends
	occupancy := bandOccupancy(powers, thresholdDB)
	flatness := spectralFlatness(powers)
	elevation := avgPower - bl.Mean

	// Tier decision — highest-severity wins, then persistence gates
	// whether we actually adopt it (in the caller).
	switch {
	case occupancy >= JammingOccupancy && flatness >= JammingFlatness:
		return StateJamming // candidate — needs 60 s sustain
	case occupancy >= InterferenceOccupancy:
		return StateInterference // candidate — needs 10 s sustain
	case elevation >= DegradedDeltaDB:
		return StateDegraded // candidate — needs 30 s sustain
	default:
		return StateClear
	}
}

// bandOccupancy returns the fraction (0..1) of bins whose power is
// above thresholdDB. Used to distinguish barrage jamming (≥70 %) from
// legit narrowband activity (<20 %).
func bandOccupancy(powers []float64, thresholdDB float64) float64 {
	if len(powers) == 0 {
		return 0
	}
	hits := 0
	n := 0
	for _, p := range powers {
		if !isFinite(p) {
			continue
		}
		if p >= thresholdDB {
			hits++
		}
		n++
	}
	if n == 0 {
		return 0
	}
	return float64(hits) / float64(n)
}

// spectralFlatness (Wiener entropy) = geomean / arithmean of LINEAR
// power. Range [0..1]. 1.0 means white noise (every frequency equal
// power — a barrage jammer). Structured signals have spectral shape
// that suppresses flatness well below 1. LTE / LoRa / APRS all stay
// below ~0.4 in practice. We convert each bin from dB to linear via
// 10^(dB/10) before computing — flatness on dB values is meaningless.
func spectralFlatness(powers []float64) float64 {
	if len(powers) == 0 {
		return 0
	}
	var sumLin float64
	var sumLogLin float64
	n := 0
	for _, p := range powers {
		if !isFinite(p) {
			continue
		}
		// Clamp very negative values to avoid 10^(-200) underflow.
		if p < -150 {
			p = -150
		}
		lin := math.Pow(10, p/10)
		if lin <= 0 {
			continue
		}
		sumLin += lin
		sumLogLin += math.Log(lin)
		n++
	}
	if n == 0 || sumLin == 0 {
		return 0
	}
	arithMean := sumLin / float64(n)
	geoMean := math.Exp(sumLogLin / float64(n))
	if arithMean == 0 {
		return 0
	}
	return geoMean / arithMean
}

// promoteState applies the dwell-time / persistence check.
// candidate is what the current scan voted for; current is the band's
// latest confirmed state; heldFor is how long the candidate has been
// stable (reset on any tier change). Returns the new state.
//
// Promotion requires the tier-specific dwell time. Demotion (to
// CLEAR) requires RecoveryPersistenceSec of CLEAR-candidate.
func promoteState(candidate, current SpectrumState, heldFor time.Duration) SpectrumState {
	switch candidate {
	case StateJamming:
		if heldFor >= JammingPersistenceSec*time.Second {
			return StateJamming
		}
	case StateInterference:
		if heldFor >= InterferencePersistenceSec*time.Second {
			return StateInterference
		}
	case StateDegraded:
		if heldFor >= DegradedPersistenceSec*time.Second {
			return StateDegraded
		}
	case StateClear:
		if current == StateClear {
			return StateClear
		}
		if heldFor >= RecoveryPersistenceSec*time.Second {
			return StateClear
		}
	}
	return current // persistence not met yet; keep previous
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
// Shares scanner.go's findRTLSDRDevice walker so the presence check
// and the detailed hardware readout never disagree.
func DetectRTLSDR() bool {
	return findRTLSDRDevice() != nil
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

// baselineStats returns (mean, std, mad) for a finite-value sample set.
// Replaces the old meanStd() + minStdFloor workaround. std now reports
// the classical estimator without clamping (MESHSAT-509 used to floor
// it at 0.5 dB to stop the old sigma classifier from false-alarming
// on locked-carrier LTE bands; the detector no longer uses std so the
// clamp is unnecessary and hid a real signal property).
//
// Callers that need a "typical fluctuation size" (e.g. the UI Y-axis)
// should call Baseline.RobustScaleDB() which combines std, MAD, and
// the measurement-quantum floor. Raw std and MAD are stored separately
// on Baseline so analysis downstream can reason about the shape of
// the noise floor (locked-carrier bands have MAD ≫ std; Gaussian bands
// have MAD·1.4826 ≈ std).
func baselineStats(values []float64) (mean, std, mad float64) {
	finite := make([]float64, 0, len(values))
	for _, v := range values {
		if isFinite(v) {
			finite = append(finite, v)
		}
	}
	if len(finite) == 0 {
		return 0, 0, 0
	}

	var sum float64
	for _, v := range finite {
		sum += v
	}
	mean = sum / float64(len(finite))

	var sumSq float64
	for _, v := range finite {
		d := v - mean
		sumSq += d * d
	}
	std = math.Sqrt(sumSq / float64(len(finite)))

	// MAD: median(|x_i - median(x)|). ITU-R SM.1880 Annex 2 §5
	// recommends robust estimators for spectrum-occupancy baselines.
	// For Gaussian data, 1.4826 · MAD ≈ σ; for locked-carrier or
	// bimodal distributions (LTE DL, narrowband beacons), MAD is
	// radically more honest about the spread.
	sorted := make([]float64, len(finite))
	copy(sorted, finite)
	sort.Float64s(sorted)
	med := median(sorted)
	abs := make([]float64, len(finite))
	for i, v := range finite {
		abs[i] = math.Abs(v - med)
	}
	sort.Float64s(abs)
	mad = median(abs)

	return mean, std, mad
}

// median returns the middle element (or average of the two middle
// elements for even-length inputs) of a pre-sorted slice. Panics on
// empty input; callers must check.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return 0.5 * (sorted[n/2-1] + sorted[n/2])
}
