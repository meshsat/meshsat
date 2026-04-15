package spectrum

import (
	"context"
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

func TestMeanStd(t *testing.T) {
	values := []float64{-45.0, -45.0, -45.0, -45.0, -45.0}
	mean, std := meanStd(values)
	if mean != -45.0 {
		t.Fatalf("mean: got %f, want -45.0", mean)
	}
	if std != 0 {
		t.Fatalf("std: got %f, want 0", std)
	}
}

func TestMeanStd_WithVariance(t *testing.T) {
	values := []float64{-44.0, -46.0, -44.0, -46.0}
	mean, std := meanStd(values)
	if mean != -45.0 {
		t.Fatalf("mean: got %f, want -45.0", mean)
	}
	if std != 1.0 {
		t.Fatalf("std: got %f, want 1.0", std)
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

	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl

	// Set status to clear with enough consecutive samples
	m.status["lora_868"].State = StateClear
	m.status["lora_868"].Consecutive = JammingConsecutive
	m.status["lora_868"].Since = time.Now().Add(-time.Minute)

	// Power well above jamming threshold (mean + 3*sigma = -45 + 3 = -42)
	state := m.evaluate("lora_868", -38.0, -38.0, bl)
	if state != StateJamming {
		t.Fatalf("expected jamming, got %s", state)
	}
}

func TestEvaluate_Clear(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl

	m.status["lora_868"].State = StateClear
	m.status["lora_868"].Consecutive = 0
	m.status["lora_868"].Since = time.Now().Add(-time.Minute)

	// Power at baseline level
	state := m.evaluate("lora_868", -45.0, -45.0, bl)
	if state != StateClear {
		t.Fatalf("expected clear, got %s", state)
	}
}

func TestEvaluate_Recovery(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl

	// Currently jammed, enough consecutive recovery samples
	m.status["lora_868"].State = StateJamming
	m.status["lora_868"].Consecutive = RecoveryConsecutive
	m.status["lora_868"].Since = time.Now().Add(-time.Minute)

	// Power back at baseline
	state := m.evaluate("lora_868", -45.0, -45.0, bl)
	if state != StateClear {
		t.Fatalf("expected recovery to clear, got %s", state)
	}
}

func TestEvaluate_Interference(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl

	m.status["lora_868"].State = StateClear
	m.status["lora_868"].Consecutive = JammingConsecutive
	m.status["lora_868"].Since = time.Now().Add(-time.Minute)

	// Average normal but peak spike exceeds 6*sigma (mean + 6 = -39)
	state := m.evaluate("lora_868", -44.0, -35.0, bl)
	if state != StateInterference {
		t.Fatalf("expected interference, got %s", state)
	}
}

func TestEvaluate_Hysteresis(t *testing.T) {
	scanner := newMockScanner([]float64{-45.0})
	m := NewSpectrumMonitor(scanner, DefaultBands)

	bl := &Baseline{Mean: -45.0, Std: 1.0, Samples: 30}
	m.baseline["lora_868"] = bl

	// Recently transitioned to jamming (within cooldown)
	m.status["lora_868"].State = StateJamming
	m.status["lora_868"].Consecutive = 0
	m.status["lora_868"].Since = time.Now() // just now

	// Even though power is normal, hysteresis keeps it jammed
	state := m.evaluate("lora_868", -45.0, -45.0, bl)
	if state != StateJamming {
		t.Fatalf("expected hysteresis to maintain jamming, got %s", state)
	}
}
