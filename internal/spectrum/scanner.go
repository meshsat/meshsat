package spectrum

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Scanner abstracts RTL-SDR spectrum scanning for testability.
type Scanner interface {
	// Scan performs a single-shot power sweep and returns power-per-bin in dB.
	Scan(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error)
	// Available reports whether the scanning backend is ready.
	Available() bool
}

// RTLPowerScanner runs rtl_power_fftw as a subprocess (no CGO).
//
// We explicitly do NOT use upstream rtl_power — its single-URB sync-read
// code path (`rtlsdr_read_sync` with BULK_TIMEOUT=0) hangs forever on the
// RTL-SDR Blog V4 regardless of reset_buffer priming. rtl_power_fftw
// uses multi-buffer async reads in a separate thread, which keeps the
// V4's bulk endpoint streaming — confirmed on parallax 2026-04-17
// (rtl_power hangs indefinitely, rtl_power_fftw returns in < 2 s).
//
// Output format we parse:
//
//	# Acquisition start: 2026-04-17 10:30:00 UTC
//	# Acquisition end: 2026-04-17 10:30:01 UTC
//	#
//	# frequency [Hz] power spectral density [dB/Hz]
//	868000000 -68.5
//	868025000 -69.1
//	...
//
// Non-comment lines are "<freq_hz> <power_dB>" pairs.
type RTLPowerScanner struct {
	binary string // path to rtl_power_fftw
}

// NewRTLPowerScanner creates a scanner using rtl_power_fftw, with a
// final fallback to rtl_power if rtl_power_fftw is not installed. Returns
// nil in two independent cases: (a) neither scanner binary is on PATH,
// or (b) no RTL-SDR dongle is physically present. Both conditions mean
// spectrum monitoring is genuinely unavailable — we must not fake a
// "calibrating" state for a band we can't scan. [MESHSAT-509 — observed
// on tesseract01 where rtl_power_fftw shipped in the image but no
// dongle was plugged in, yet the UI showed "calibrating" forever.]
func NewRTLPowerScanner() *RTLPowerScanner {
	var binary string
	if path, err := exec.LookPath("rtl_power_fftw"); err == nil {
		binary = path
	} else if path, err := exec.LookPath("rtl_power"); err == nil {
		binary = path
	} else {
		return nil
	}
	if !DetectRTLSDR() {
		return nil
	}
	return &RTLPowerScanner{binary: binary}
}

// Available re-checks the dongle at call time so an unplugged device
// silently tips the monitor to "disabled" rather than stays stuck in
// "calibrating". Cheap — one sysfs scan.
func (s *RTLPowerScanner) Available() bool {
	if s == nil || s.binary == "" {
		return false
	}
	return DetectRTLSDR()
}

// Scan runs a single-shot power sweep. Dispatches to the appropriate
// CLI invocation based on which binary is in use so we don't regress
// on older images.
func (s *RTLPowerScanner) Scan(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error) {
	if strings.HasSuffix(s.binary, "rtl_power_fftw") {
		return s.scanFFTW(ctx, freqLow, freqHigh, binSize)
	}
	return s.scanLegacy(ctx, freqLow, freqHigh, binSize)
}

// scanFFTW invokes rtl_power_fftw. We map our (low, high, binSize) into
// rpfftw's (-f low:high, -b bins). rpfftw requires an even bin count;
// we round up to the nearest even number of bins. `-n 1` = single
// spectrum averaged from one FFT — fast enough for our 3 s scan cadence.
// `-q` keeps stderr quiet after the first run.
func (s *RTLPowerScanner) scanFFTW(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error) {
	span := freqHigh - freqLow
	if span <= 0 || binSize <= 0 {
		return nil, fmt.Errorf("invalid band: low=%d high=%d bin=%d", freqLow, freqHigh, binSize)
	}
	bins := span / binSize
	if bins < 2 {
		bins = 2
	}
	if bins%2 != 0 {
		bins++ // rpfftw requires even bins
	}
	freqArg := fmt.Sprintf("%d:%d", freqLow, freqHigh)
	cmd := exec.CommandContext(ctx, s.binary,
		"-f", freqArg,
		"-b", strconv.Itoa(bins),
		"-n", "1",
		"-q",
	)
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rtl_power_fftw: %w", err)
	}
	return parseFFTWOutput(string(out))
}

// scanLegacy is the old rtl_power path, retained for fallback if only
// rtl_power is present (e.g. during a partial rollback).
func (s *RTLPowerScanner) scanLegacy(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error) {
	freqRange := fmt.Sprintf("%d:%d:%d", freqLow, freqHigh, binSize)
	cmd := exec.CommandContext(ctx, s.binary, "-f", freqRange, "-i", "1", "-1")
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rtl_power: %w", err)
	}
	return parseRTLPowerOutput(string(out))
}

// parseFFTWOutput parses rtl_power_fftw CSV-ish stdout into power values.
// Each non-comment line is "<freq_hz> <power_dB>". We discard the
// frequency column — the caller supplies the band layout separately.
func parseFFTWOutput(output string) ([]float64, error) {
	var powers []float64
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		powers = append(powers, val)
	}
	if len(powers) == 0 {
		return nil, fmt.Errorf("no power data in rtl_power_fftw output")
	}
	return powers, nil
}

// parseRTLPowerOutput parses legacy rtl_power CSV output into power values.
// Each line: date, time, Hz_low, Hz_high, Hz_step, num_samples, dB, dB, ...
// Multiple lines may be produced for wide scans; we concatenate all dB values.
func parseRTLPowerOutput(output string) ([]float64, error) {
	var powers []float64
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, ", ")
		if len(fields) < 7 {
			continue
		}
		for _, f := range fields[6:] {
			val, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
			if err != nil {
				continue
			}
			powers = append(powers, val)
		}
	}
	if len(powers) == 0 {
		return nil, fmt.Errorf("no power data in rtl_power output")
	}
	return powers, nil
}
