package spectrum

import (
	"context"
	"strings"
	"testing"
)

// TestDefaultBandsScanSingleHop pins the rule that every default band,
// crop gutters included, fits one tune of the dongle. A widened span
// above SingleHopSpanHz makes rtl_power_fftw hop and turned the LTE
// bands' 2 s scan into 9.5 s, which starved calibration forever (both
// kits, 11 Sep 2026, MESHSAT-1017). [MESHSAT-1017]
func TestDefaultBandsScanSingleHop(t *testing.T) {
	for _, b := range DefaultBands {
		g, err := BandScanGeometry(b)
		if err != nil {
			t.Fatalf("%s: %v", b.Name, err)
		}
		if g.SpanHz() > SingleHopSpanHz {
			t.Errorf("%s: widened span %d Hz exceeds the single-hop limit %d Hz (crop %d x %d Hz on %d Hz)",
				b.Name, g.SpanHz(), SingleHopSpanHz, g.CropPad, b.BinSize, b.FreqHigh-b.FreqLow)
		}
		if g.EffBins%2 != 0 {
			t.Errorf("%s: rtl_power_fftw needs an even bin count, got %d", b.Name, g.EffBins)
		}
		if g.Bins <= 0 || g.Bins > g.EffBins {
			t.Errorf("%s: interior bins %d out of range for %d requested", b.Name, g.Bins, g.EffBins)
		}
	}
}

// TestScanGeometryMatchesFieldArgv checks the arithmetic against the
// command lines observed on the kits: LoRa 867950000:868650000 / 28
// bins, GPS L1 1574270000:1576570000 / 92 bins, and the LTE bands the
// same shape as GPS after the 11 Sep 2026 change.
func TestScanGeometryMatchesFieldArgv(t *testing.T) {
	cases := []struct {
		name          string
		low, high, bs int
		crop          int
		wLow, wHigh   int
		effBins, bins int
	}{
		{"lora_868", 868000000, 868600000, 25000, 2, 867950000, 868650000, 28, 24},
		{"aprs_144", 144700000, 144900000, 12500, 2, 144675000, 144925000, 20, 16},
		{"gps_l1", 1574420000, 1576420000, 25000, 6, 1574270000, 1576570000, 92, 80},
		{"lte_b20_dl", 805000000, 807000000, 25000, 6, 804850000, 807150000, 92, 80},
		{"lte_b8_dl", 941500000, 943500000, 25000, 6, 941350000, 943650000, 92, 80},
		{"odd bins rounded up", 100000000, 100075000, 25000, 0, 100000000, 100100000, 4, 3},
	}
	for _, c := range cases {
		g, err := scanGeometry(c.low, c.high, c.bs, c.crop)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if g.WidenedLow != c.wLow || g.WidenedHigh != c.wHigh || g.EffBins != c.effBins || g.Bins != c.bins {
			t.Errorf("%s: got %d:%d bins=%d interior=%d, want %d:%d bins=%d interior=%d",
				c.name, g.WidenedLow, g.WidenedHigh, g.EffBins, g.Bins, c.wLow, c.wHigh, c.effBins, c.bins)
		}
	}
	if _, err := scanGeometry(10, 10, 25000, 2); err == nil {
		t.Error("zero span accepted")
	}
}

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
