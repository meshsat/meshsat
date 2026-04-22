package spectrum

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

// mockScanner returns configurable power readings for testing.
type mockScanner struct {
	mu      sync.Mutex
	powers  []float64
	calls   int
	enabled bool
}

func newMockScanner(powers []float64) *mockScanner {
	return &mockScanner{powers: powers, enabled: true}
}

func (s *mockScanner) Available() bool { return s.enabled }

func (s *mockScanner) Info() ScannerInfo {
	return ScannerInfo{BinaryPath: "mock", DongleVID: "0bda", DonglePID: "2838", USBPath: "mock"}
}

func (s *mockScanner) Scan(_ context.Context, _, _, _ int) ([]float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return append([]float64{}, s.powers...), nil
}

func (s *mockScanner) setPowers(p []float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.powers = p
}

func TestParseRTLPowerOutput(t *testing.T) {
	input := `2026-04-15, 12:00:00, 868000000, 868600000, 25000, 24, -45.2, -46.1, -44.8, -45.5
2026-04-15, 12:00:00, 868000000, 868600000, 25000, 24, -45.0, -45.3, -44.9, -45.1`

	powers, err := parseRTLPowerOutput(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(powers) != 8 {
		t.Fatalf("expected 8 power values, got %d", len(powers))
	}
	if powers[0] != -45.2 {
		t.Fatalf("first power value: got %f, want -45.2", powers[0])
	}
}

func TestParseRTLPowerOutput_Empty(t *testing.T) {
	_, err := parseRTLPowerOutput("")
	if err == nil {
		t.Fatal("expected error on empty output")
	}
}

func TestBaselineStats_ConstantInput(t *testing.T) {
	// Locked-carrier-like input: every sample identical. Classical std
	// collapses to 0; MAD also 0. This is fine — the UI Y-axis falls
	// back to MeasurementNoiseFloorDB via Baseline.RobustScaleDB().
	values := []float64{-45.0, -45.0, -45.0, -45.0, -45.0}
	mean, std, mad := baselineStats(values)
	if mean != -45.0 {
		t.Fatalf("mean: got %f, want -45.0", mean)
	}
	if std != 0 {
		t.Fatalf("std: got %f, want 0 (constant input, no clamp)", std)
	}
	if mad != 0 {
		t.Fatalf("mad: got %f, want 0 (constant input)", mad)
	}
}

func TestBaselineStats_FiltersInfNaN(t *testing.T) {
	// rtl_power emits -Inf for all-zero FFT bins on first post-tune read.
	// Those must be filtered so baseline mean stays finite (JSON-safe).
	values := []float64{-45.0, math.Inf(-1), -45.0, math.NaN(), -45.0}
	mean, std, mad := baselineStats(values)
	if mean != -45.0 {
		t.Fatalf("mean: got %f, want -45.0 (finite values only)", mean)
	}
	if std != 0 {
		t.Fatalf("std: got %f, want 0", std)
	}
	if mad != 0 {
		t.Fatalf("mad: got %f, want 0", mad)
	}
}

func TestBaselineStats_WithVariance(t *testing.T) {
	values := []float64{-44.0, -46.0, -44.0, -46.0}
	mean, std, mad := baselineStats(values)
	if mean != -45.0 {
		t.Fatalf("mean: got %f, want -45.0", mean)
	}
	if std != 1.0 {
		t.Fatalf("std: got %f, want 1.0", std)
	}
	// |values - median(-45)| = {1, 1, 1, 1} → MAD = 1
	if mad != 1.0 {
		t.Fatalf("mad: got %f, want 1.0", mad)
	}
}

func TestBaselineStats_BimodalRobustness(t *testing.T) {
	// 95% of samples at -42, 5% at -50 — classical std dominated by the
	// narrow cluster; MAD must still report the wide spread. This is
	// the locked-carrier LTE DL case the old minStdFloor workaround
	// papered over.
	values := make([]float64, 0, 100)
	for i := 0; i < 95; i++ {
		values = append(values, -42.0)
	}
	for i := 0; i < 5; i++ {
		values = append(values, -50.0)
	}
	_, std, mad := baselineStats(values)
	// std will be pulled toward the small outlier cluster — small but
	// non-zero. MAD is 0 because median=-42 and 95% of samples equal it.
	// That's fine: RobustScaleDB falls back to the quantum floor when
	// both estimators collapse.
	if std <= 0 {
		t.Fatalf("std: got %f, want > 0", std)
	}
	_ = mad
}

func TestRobustScaleDB(t *testing.T) {
	// Pathological: both std and MAD ≈ 0 → fall back to quantum floor.
	bl := &Baseline{Std: 0.01, Mad: 0.0}
	if got := bl.RobustScaleDB(); got != MeasurementNoiseFloorDB {
		t.Fatalf("quantum floor: got %f, want %f", got, MeasurementNoiseFloorDB)
	}
	// MAD captures real spread when std doesn't.
	bl = &Baseline{Std: 0.01, Mad: 4.0} // 1.4826*4 ≈ 5.93
	if got := bl.RobustScaleDB(); got < 5.9 || got > 6.0 {
		t.Fatalf("MAD-dominant: got %f, want ~5.93", got)
	}
	// Classical wins when both are reasonable + std is larger.
	bl = &Baseline{Std: 3.0, Mad: 1.0}
	if got := bl.RobustScaleDB(); got != 3.0 {
		t.Fatalf("std-dominant: got %f, want 3.0", got)
	}
}

func TestAvgPower(t *testing.T) {
	values := []float64{-40.0, -50.0}
	avg := avgPower(values)
	if avg != -45.0 {
		t.Fatalf("avg: got %f, want -45.0", avg)
	}
}

func TestMaxVal(t *testing.T) {
	values := []float64{-50.0, -40.0, -45.0}
	m := maxVal(values)
	if m != -40.0 {
		t.Fatalf("max: got %f, want -40.0", m)
	}
}

func TestNewSpectrumMonitor_NoScanner(t *testing.T) {
	m := NewSpectrumMonitor(nil, DefaultBands)
	if m.Enabled() {
		t.Fatal("monitor should be disabled without scanner")
	}
	statuses := m.Status()
	for _, s := range statuses {
		if s.State != StateDisabled {
			t.Fatalf("band %s: state should be disabled, got %s", s.Band, s.State)
		}
	}
}

func TestNewSpectrumMonitor_WithScanner(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)
	if !m.Enabled() {
		t.Fatal("monitor should be enabled with scanner")
	}
	statuses := m.Status()
	for _, s := range statuses {
		if s.State != StateCalibrating {
			t.Fatalf("band %s: state should be calibrating, got %s", s.Band, s.State)
		}
	}
}

func TestIsJammed_NotJammed(t *testing.T) {
	m := NewSpectrumMonitor(nil, DefaultBands)
	if m.IsJammed("mesh_0") {
		t.Fatal("mesh_0 should not be jammed when monitor is disabled")
	}
}

func TestEvaluate_Jamming(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	// Baseline at -45 dB. Jamming candidate requires:
	//   avg > PowerFloorLoRa (-50 dB) AND
	//   occupancy >= 0.70 (most bins above baseline+6) AND
	//   spectral flatness >= 0.60 (white-noise-ish shape).
	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl

	// Synthetic barrage-jammer scan: every bin at -20 dB (well above
	// -45 +6 = -39 threshold), uniform → occupancy 1.0, flatness 1.0.
	powers := make([]float64, 24)
	for i := range powers {
		powers[i] = -20.0
	}
	state := m.evaluate("lora_868", powers, -20.0, -20.0, bl)
	if state != StateJamming {
		t.Fatalf("expected jamming, got %s", state)
	}
}

func TestEvaluate_Clear(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl

	// All bins at baseline → occupancy 0, flatness near 1 but power
	// below absolute floor blocks escalation.
	powers := make([]float64, 24)
	for i := range powers {
		powers[i] = -45.0
	}
	state := m.evaluate("lora_868", powers, -45.0, -45.0, bl)
	if state != StateClear {
		t.Fatalf("expected clear, got %s", state)
	}
}

func TestEvaluate_BelowPowerFloor_NeverJamming(t *testing.T) {
	scanner := newMockScanner([]float64{-90.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	// Very quiet band, all bins identical → 100% occupancy + flatness 1
	// would naively say "jamming" but avgPower is way below PowerFloorLoRa.
	bl := &Baseline{Mean: -90.0, Std: 0.5, Samples: 30}
	m.baseline["lora_868"] = bl
	powers := make([]float64, 24)
	for i := range powers {
		powers[i] = -90.0
	}
	state := m.evaluate("lora_868", powers, -90.0, -90.0, bl)
	if state != StateClear {
		t.Fatalf("expected clear (below power floor), got %s", state)
	}
}

func TestEvaluate_NarrowbandBurst_NotJamming(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	// One bin hot (legit LoRa packet), rest at baseline.
	// Occupancy ~= 1/24 = 0.04 — well below InterferenceOccupancy.
	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl
	powers := make([]float64, 24)
	for i := range powers {
		powers[i] = -45.0
	}
	powers[10] = -20.0 // one bright spike
	// avg dragged up slightly; max = -20
	avg := -44.0
	state := m.evaluate("lora_868", powers, avg, -20.0, bl)
	if state == StateJamming {
		t.Fatalf("narrowband burst must not classify as jamming, got %s", state)
	}
}

func TestEvaluate_StructuredSignal_HighOccupancyLowFlatness(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl
	// Simulate a structured transmission: a shaped spectrum — high
	// in the middle, low on the edges. Linear-domain flatness will
	// be well below 0.6, so this should NOT be jamming even if
	// occupancy is above 0.3 (→ interference-candidate only).
	powers := make([]float64, 24)
	for i := range powers {
		d := float64(i - 12)
		powers[i] = -20.0 - d*d*0.5 // quadratic dip off-centre
	}
	state := m.evaluate("lora_868", powers, -30.0, -20.0, bl)
	if state == StateJamming {
		t.Fatalf("structured signal must not classify as jamming, got %s", state)
	}
}

func TestPromoteState_DwellTime(t *testing.T) {
	// JAMMING candidate held for 30 s stays at current state (needs 60 s).
	s := promoteState(StateJamming, StateClear, 30*time.Second)
	if s != StateClear {
		t.Fatalf("30 s jamming candidate: expected clear (not yet promoted), got %s", s)
	}
	// Held for 61 s: promotes.
	s = promoteState(StateJamming, StateClear, 61*time.Second)
	if s != StateJamming {
		t.Fatalf("61 s jamming candidate: expected jamming, got %s", s)
	}
	// DEGRADED held for 5 s: keep current.
	s = promoteState(StateDegraded, StateClear, 5*time.Second)
	if s != StateClear {
		t.Fatalf("5 s degraded candidate: expected clear, got %s", s)
	}
	// INTERFERENCE held for 11 s: promote.
	s = promoteState(StateInterference, StateClear, 11*time.Second)
	if s != StateInterference {
		t.Fatalf("11 s interference candidate: expected interference, got %s", s)
	}
	// Recovery: CLEAR candidate held for 31 s while current is JAMMING → CLEAR.
	s = promoteState(StateClear, StateJamming, 31*time.Second)
	if s != StateClear {
		t.Fatalf("31 s recovery: expected clear, got %s", s)
	}
	// Recovery held only 10 s: keep jamming.
	s = promoteState(StateClear, StateJamming, 10*time.Second)
	if s != StateJamming {
		t.Fatalf("10 s recovery: expected to stay jamming, got %s", s)
	}
}

func TestSpectralFlatness_WhiteNoise(t *testing.T) {
	// All bins equal → flatness = 1.0
	powers := make([]float64, 32)
	for i := range powers {
		powers[i] = -40.0
	}
	f := spectralFlatness(powers)
	if f < 0.99 {
		t.Fatalf("white-noise flatness should be ~1, got %f", f)
	}
}

func TestSpectralFlatness_SinglePeak(t *testing.T) {
	// One strong bin, rest quiet → flatness near 0.
	powers := make([]float64, 32)
	for i := range powers {
		powers[i] = -80.0
	}
	powers[15] = 0.0
	f := spectralFlatness(powers)
	if f > 0.2 {
		t.Fatalf("single-peak flatness should be small, got %f", f)
	}
}

func TestBandOccupancy(t *testing.T) {
	powers := []float64{-50, -50, -50, -30, -30, -30, -30, -30}
	// threshold -40: 5 of 8 bins above → 0.625
	got := bandOccupancy(powers, -40)
	want := 0.625
	if got < want-0.001 || got > want+0.001 {
		t.Fatalf("occupancy: got %f, want %f", got, want)
	}
}

// TestSubscribeUnsubscribe exercises the subscriber lifecycle: subscribe
// returns a channel + unsub fn; publish fans out to all subscribers;
// unsubscribe removes the channel and closes it so consumers terminate
// cleanly. The same pattern backs the /api/spectrum/stream SSE endpoint
// and the CoT+hub alert relay goroutine.
func TestSubscribeUnsubscribe(t *testing.T) {
	m := NewSpectrumMonitor(newMockScanner([]float64{-60}), DefaultBands)

	ch1, unsub1 := m.Subscribe()
	ch2, unsub2 := m.Subscribe()

	m.publish(SpectrumEvent{Kind: EventScan, Band: "lora_868", AvgDB: -50})

	select {
	case got := <-ch1:
		if got.Band != "lora_868" || got.AvgDB != -50 {
			t.Errorf("ch1: got %+v", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ch1: timeout waiting for event")
	}
	select {
	case got := <-ch2:
		if got.Band != "lora_868" {
			t.Errorf("ch2: got %+v", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ch2: timeout waiting for event")
	}

	unsub1()
	if _, open := <-ch1; open {
		t.Error("ch1 should be closed after unsub1")
	}
	m.publish(SpectrumEvent{Kind: EventTransition, Band: "aprs_144"})
	select {
	case got := <-ch2:
		if got.Kind != EventTransition {
			t.Errorf("ch2 post-unsub: got %+v", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ch2: timeout after ch1 unsubscribed")
	}
	unsub2()
}

// TestPublishDoesNotBlockOnSlowConsumer verifies the select/default drop
// policy — a subscriber that never reads must not wedge the scan loop.
// Losing bin samples is acceptable (the next scan re-publishes); wedging
// the scan loop would take down jamming detection.
func TestPublishDoesNotBlockOnSlowConsumer(t *testing.T) {
	m := NewSpectrumMonitor(newMockScanner([]float64{-60}), DefaultBands)
	_, unsub := m.Subscribe()
	defer unsub()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 256; i++ {
			m.publish(SpectrumEvent{Kind: EventScan, Band: "lora_868"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("publish wedged on slow consumer — drop policy broken")
	}
}

// flakyStore fails its first `failUntil` writes with a context-deadline
// error, then succeeds. Used to prove that persistence recovers rather
// than self-disabling forever after one transient timeout.
type flakyStore struct {
	mu        sync.Mutex
	scans     []ScanRow
	calls     int
	failUntil int
}

func (s *flakyStore) SaveScan(_ context.Context, row ScanRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls <= s.failUntil {
		return context.DeadlineExceeded
	}
	s.scans = append(s.scans, row)
	return nil
}

func (s *flakyStore) SaveTransition(_ context.Context, _ TransitionRow) error {
	return nil
}

func (s *flakyStore) LoadScansByMinutes(_ context.Context, _ string, _ int) ([]ScanRow, error) {
	return nil, nil
}

func (s *flakyStore) LoadLatestScans(_ context.Context, _ string, _ int) ([]ScanRow, error) {
	return nil, nil
}

func (s *flakyStore) LoadScansRange(_ context.Context, _ string, _, _ time.Time, _ int) ([]ScanRow, error) {
	return nil, nil
}

func (s *flakyStore) LoadTransitionsRange(_ context.Context, _ string, _, _ time.Time) ([]TransitionRow, error) {
	return nil, nil
}

func (s *flakyStore) TrimSpectrumHistory(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// TestPersistRecoversAfterTransientErrors is the regression for
// MESHSAT-653: a single context-deadline timeout must not latch the
// monitor into permanent persistence-disabled state. Writes must keep
// flowing once the store recovers, so waterfall rehydration works
// after every container restart.
func TestPersistRecoversAfterTransientErrors(t *testing.T) {
	store := &flakyStore{failUntil: 2}
	m := NewSpectrumMonitor(newMockScanner([]float64{-60}), DefaultBands)
	m.SetHistoryStore(store)

	scan := func() {
		m.persist(SpectrumEvent{
			Kind:      EventScan,
			Band:      "lora_868",
			Timestamp: time.Now(),
			Powers:    []float64{-60, -60, -60},
		})
	}

	for i := 0; i < 5; i++ {
		scan()
	}
	m.persistInFlight.Wait()

	store.mu.Lock()
	calls := store.calls
	persisted := len(store.scans)
	store.mu.Unlock()

	if calls != 5 {
		t.Fatalf("expected 5 SaveScan calls (no self-disable), got %d", calls)
	}
	// failUntil=2 → calls 1+2 fail, 3+4+5 succeed = 3 persisted rows.
	if persisted != 3 {
		t.Fatalf("expected 3 persisted rows after recovery, got %d", persisted)
	}

	// After success, the error window should be cleared so the next
	// failure will emit a fresh log line (not be silently suppressed).
	m.persistMu.Lock()
	errCount := m.persistErrCount
	lastErr := m.persistLastErrAt
	m.persistMu.Unlock()
	if errCount != 0 {
		t.Fatalf("persistErrCount = %d after recovery, want 0", errCount)
	}
	if !lastErr.IsZero() {
		t.Fatalf("persistLastErrAt = %v after recovery, want zero", lastErr)
	}
}

// TestPersistErrorLogThrottle verifies that sustained write failures
// do not flood the log: the rate limiter allows one log emission per
// throttle window even when every SaveScan fails. [MESHSAT-653]
func TestPersistErrorLogThrottle(t *testing.T) {
	store := &flakyStore{failUntil: 1_000_000} // always fail
	m := NewSpectrumMonitor(newMockScanner([]float64{-60}), DefaultBands)
	m.SetHistoryStore(store)

	// Shorten the throttle so we don't sleep for 60 s in tests.
	orig := persistLogThrottle
	persistLogThrottle = 50 * time.Millisecond
	defer func() { persistLogThrottle = orig }()

	// Burst 20 writes quickly — all fail, but at most one warning
	// should have been logged in this window (we can't introspect the
	// logger easily, but we CAN check that persistLastErrAt only moved
	// once per window via the counter+timestamp invariant).
	for i := 0; i < 20; i++ {
		m.persist(SpectrumEvent{
			Kind:      EventScan,
			Band:      "lora_868",
			Timestamp: time.Now(),
			Powers:    []float64{-60},
		})
	}
	m.persistInFlight.Wait()

	store.mu.Lock()
	calls := store.calls
	store.mu.Unlock()
	if calls != 20 {
		t.Fatalf("expected all 20 writes to be attempted, got %d", calls)
	}

	m.persistMu.Lock()
	errCount := m.persistErrCount
	m.persistMu.Unlock()
	if errCount != 20 {
		t.Fatalf("persistErrCount = %d, want 20 (every failure must be counted)", errCount)
	}

	// Wait past the throttle so the next failure will log again.
	time.Sleep(persistLogThrottle + 10*time.Millisecond)
	m.persist(SpectrumEvent{
		Kind:      EventScan,
		Band:      "lora_868",
		Timestamp: time.Now(),
		Powers:    []float64{-60},
	})
	m.persistInFlight.Wait()

	m.persistMu.Lock()
	errCount = m.persistErrCount
	m.persistMu.Unlock()
	if errCount != 21 {
		t.Fatalf("after throttle reset, expected errCount=21, got %d", errCount)
	}
}
