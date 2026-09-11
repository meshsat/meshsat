package spectrum

import (
	"context"
	"testing"
	"time"
)

// withFastCalibration shortens the calibration clocks for a test and
// restores them afterwards. min window 200 ms, ceiling 800 ms, pause
// 5 ms, per-scan timeout 2 s. [MESHSAT-1017]
func withFastCalibration(t *testing.T) {
	t.Helper()
	oldMin, oldMax, oldN, oldTO, oldPause := CalibrationDuration, CalibrationMaxDuration, MinCalibrationSamples, calibrationScanTimeout, calibrationPause
	CalibrationDuration = 200 * time.Millisecond
	CalibrationMaxDuration = 800 * time.Millisecond
	MinCalibrationSamples = 5
	calibrationScanTimeout = 2 * time.Second
	calibrationPause = 5 * time.Millisecond
	t.Cleanup(func() {
		CalibrationDuration, CalibrationMaxDuration, MinCalibrationSamples, calibrationScanTimeout, calibrationPause = oldMin, oldMax, oldN, oldTO, oldPause
	})
}

// A band whose scans are fast calibrates inside the minimum window with
// a real spread of samples, as before.
func TestCalibrateFastBandUsesMinimumWindow(t *testing.T) {
	withFastCalibration(t)
	sc := newMockScanner([]float64{-60, -61, -59, -60})
	m := NewSpectrumMonitor(sc, DefaultBands[:1])
	band := DefaultBands[0]

	start := time.Now()
	bl := m.calibrate(context.Background(), band)
	took := time.Since(start)
	if bl == nil {
		t.Fatal("fast band did not calibrate")
	}
	if bl.Samples < MinCalibrationSamples {
		t.Fatalf("samples = %d, want >= %d", bl.Samples, MinCalibrationSamples)
	}
	if took < CalibrationDuration {
		t.Fatalf("calibration returned after %v, before the minimum window %v", took, CalibrationDuration)
	}
	if took > CalibrationMaxDuration {
		t.Fatalf("fast band took %v, longer than the ceiling %v", took, CalibrationMaxDuration)
	}
}

// The MESHSAT-1017 case: a scan that takes more than window/5 collects
// too few samples in the minimum window. Until 11 Sep 2026 that was a
// permanent failure; now the window extends and the band calibrates.
func TestCalibrateSlowBandExtendsToTheCeiling(t *testing.T) {
	withFastCalibration(t)
	sc := newMockScanner([]float64{-70, -70, -70})
	sc.delay = 60 * time.Millisecond // 200 ms window / 65 ms per scan = 3 samples, like the LTE bands
	m := NewSpectrumMonitor(sc, DefaultBands[:1])
	band := DefaultBands[0]

	start := time.Now()
	bl := m.calibrate(context.Background(), band)
	took := time.Since(start)
	if bl == nil {
		t.Fatal("slow band did not calibrate although the ceiling allowed it")
	}
	if bl.Samples < MinCalibrationSamples {
		t.Fatalf("samples = %d, want >= %d", bl.Samples, MinCalibrationSamples)
	}
	if took <= CalibrationDuration {
		t.Fatalf("took %v, expected the window to extend past %v", took, CalibrationDuration)
	}
	if took > CalibrationMaxDuration+100*time.Millisecond {
		t.Fatalf("took %v, past the ceiling %v", took, CalibrationMaxDuration)
	}
	m.mu.RLock()
	shown := m.status[band.Name].CalibrationDurationSec
	m.mu.RUnlock()
	if shown != int(CalibrationMaxDuration/time.Second) {
		t.Fatalf("UI countdown not stretched to the ceiling: got %d s", shown)
	}
}

// A band slower than the ceiling still fails, and stays calibrating.
func TestCalibrateHopelessBandFailsAtTheCeiling(t *testing.T) {
	withFastCalibration(t)
	sc := newMockScanner([]float64{-70, -70, -70})
	sc.delay = 300 * time.Millisecond // at most 2 to 3 samples in 800 ms
	m := NewSpectrumMonitor(sc, DefaultBands[:1])
	band := DefaultBands[0]

	start := time.Now()
	bl := m.calibrate(context.Background(), band)
	took := time.Since(start)
	if bl != nil {
		t.Fatalf("hopeless band calibrated with %d samples", bl.Samples)
	}
	if took < CalibrationMaxDuration {
		t.Fatalf("gave up after %v, before the ceiling %v", took, CalibrationMaxDuration)
	}
	m.mu.RLock()
	state := m.status[band.Name].State
	m.mu.RUnlock()
	if state != StateCalibrating {
		t.Fatalf("state = %q, want %q", state, StateCalibrating)
	}
}

// Cancelling the context ends calibration at once, whatever the clocks.
func TestCalibrateStopsOnCancel(t *testing.T) {
	withFastCalibration(t)
	sc := newMockScanner([]float64{-70})
	sc.delay = 50 * time.Millisecond
	m := NewSpectrumMonitor(sc, DefaultBands[:1])
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	start := time.Now()
	if bl := m.calibrate(ctx, DefaultBands[0]); bl != nil {
		t.Fatal("calibrate returned a baseline after cancel")
	}
	if time.Since(start) > CalibrationDuration {
		t.Fatalf("calibrate ran %v after cancel", time.Since(start))
	}
}
