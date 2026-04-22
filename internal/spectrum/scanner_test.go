package spectrum

import (
	"context"
	"strings"
	"testing"
)

// TestRTLPowerScanner_NoDemotionOnRepeatedFFTWFailures is the MESHSAT-655
// regression: before the fix, 3 consecutive fftw failures flipped the
// scanner onto the legacy rtl_power binary, which hangs forever on the
// RTL-SDR Blog V4's R828D tuner — stranding calibration for 2+ hours on
// parallax 2026-04-22. The contract now is: once fftw was chosen at
// init, it must stay chosen for the life of the scanner.
func TestRTLPowerScanner_NoDemotionOnRepeatedFFTWFailures(t *testing.T) {
	s := &RTLPowerScanner{binary: "/nonexistent/path/rtl_power_fftw"}

	for i := 0; i < 5; i++ {
		_, err := s.Scan(context.Background(), 868_000_000, 868_600_000, 25_000, 2)
		if err == nil {
			t.Fatalf("iteration %d: expected error from nonexistent binary, got nil", i)
		}
		if !strings.HasPrefix(err.Error(), "rtl_power_fftw:") {
			t.Fatalf("iteration %d: expected rtl_power_fftw-prefixed error (fftw path), got %q", i, err.Error())
		}
	}

	if !strings.HasSuffix(s.binary, "rtl_power_fftw") {
		t.Fatalf("binary was mutated after failures: got %q, want suffix rtl_power_fftw", s.binary)
	}
}

// TestRTLPowerScanner_LegacyOnlyWhenBinaryIsLegacy verifies the other
// half of the policy: scanLegacy runs only when the scanner was
// initialised against the legacy binary (NewRTLPowerScanner only does
// this when rtl_power_fftw is absent from PATH). If Scan dispatched to
// scanLegacy for an fftw-suffixed binary — or vice versa — the demotion
// regression could sneak back in disguised as a dispatch bug.
func TestRTLPowerScanner_LegacyOnlyWhenBinaryIsLegacy(t *testing.T) {
	s := &RTLPowerScanner{binary: "/nonexistent/path/rtl_power"}

	_, err := s.Scan(context.Background(), 868_000_000, 868_600_000, 25_000, 2)
	if err == nil {
		t.Fatal("expected error from nonexistent binary, got nil")
	}
	if !strings.HasPrefix(err.Error(), "rtl_power:") {
		t.Fatalf("expected rtl_power-prefixed error (legacy path), got %q", err.Error())
	}
	if strings.HasPrefix(err.Error(), "rtl_power_fftw:") {
		t.Fatalf("legacy binary dispatched to fftw path: %q", err.Error())
	}
}
