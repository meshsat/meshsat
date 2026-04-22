package spectrum

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Scanner abstracts RTL-SDR spectrum scanning for testability.
type Scanner interface {
	// Scan performs a single-shot power sweep and returns power-per-bin in dB.
	Scan(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error)
	// Available reports whether the scanning backend is ready.
	Available() bool
	// Info returns static metadata about the scanner backend for the
	// hardware-status UI (binary path, dongle identifiers, USB path).
	Info() ScannerInfo
}

// ScannerInfo is the static descriptor the UI renders in the hardware
// status panel — what binary we're using, what dongle is attached,
// and where it lives on the bus.
type ScannerInfo struct {
	BinaryPath string `json:"binary_path"`
	DongleVID  string `json:"dongle_vid"`
	DonglePID  string `json:"dongle_pid"`
	USBPath    string `json:"usb_path"`
	// ProductName is the USB product-string (e.g. "RTL2838UHIDIR"), read
	// from /sys/bus/usb/devices/<id>/product. Empty if sysfs doesn't
	// expose it for this kernel config.
	ProductName string `json:"product_name"`
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
	// binary chosen at init and NEVER switched at runtime. The scanner
	// must not demote from rtl_power_fftw to legacy rtl_power on
	// repeated failures: rtl_power hangs indefinitely on the RTL-SDR
	// Blog V4 (documented above), so "demoting" to it strands the
	// scanner in a stuck state with zero samples forever. A transient
	// fftw failure at boot — the Blog V4's tuner often needs a couple
	// of cold-start retries — is not evidence that fftw is broken on
	// this kit. Retry forever on the same binary per MESHSAT-653's
	// philosophy. [MESHSAT-655]
	binary string
}

// NewRTLPowerScanner creates a scanner using rtl_power_fftw when it's
// available, falling back to legacy rtl_power ONLY when fftw is not
// installed at all (partial rollback / pre-MESHSAT-509 image). Returns
// nil in two independent cases: (a) neither scanner binary is on PATH,
// or (b) no RTL-SDR dongle is physically present. Both conditions mean
// spectrum monitoring is genuinely unavailable — we must not fake a
// "calibrating" state for a band we can't scan. [MESHSAT-509 — observed
// on tesseract01 where rtl_power_fftw shipped in the image but no
// dongle was plugged in, yet the UI showed "calibrating" forever.]
func NewRTLPowerScanner() *RTLPowerScanner {
	fftwPath, _ := exec.LookPath("rtl_power_fftw")
	legacyPath, _ := exec.LookPath("rtl_power")

	switch {
	case fftwPath != "":
		if !DetectRTLSDR() {
			return nil
		}
		return &RTLPowerScanner{binary: fftwPath}
	case legacyPath != "":
		if !DetectRTLSDR() {
			return nil
		}
		return &RTLPowerScanner{binary: legacyPath}
	default:
		return nil
	}
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

// Info walks /sys/bus/usb/devices to locate the first Realtek RTL283X
// entry and return its descriptor alongside the scanner binary path.
// Returned fields are empty strings if a given bit of metadata isn't
// readable — the UI treats empty as "n/a" rather than erroring.
func (s *RTLPowerScanner) Info() ScannerInfo {
	info := ScannerInfo{}
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

// rtlsdrDevice is the internal detail findRTLSDRDevice returns. Not
// exported because callers want ScannerInfo, which is stable.
type rtlsdrDevice struct {
	VID, PID, Path, Product string
}

// findRTLSDRDevice returns the first matching Realtek RTL283X USB
// device, or nil if none. Shared between DetectRTLSDR (bool-only
// presence check) and Info (metadata).
func findRTLSDRDevice() *rtlsdrDevice {
	entries, err := os.ReadDir("/sys/bus/usb/devices")
	if err != nil {
		return nil
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
		if vid != RTLSDR_VID {
			continue
		}
		if pid != RTLSDR_PID_2832 && pid != RTLSDR_PID_2838 {
			continue
		}
		// product is optional
		product := ""
		if prodBytes, err := os.ReadFile(filepath.Join(base, "product")); err == nil {
			product = strings.TrimSpace(string(prodBytes))
		}
		return &rtlsdrDevice{
			VID:     vid,
			PID:     pid,
			Path:    entry.Name(),
			Product: product,
		}
	}
	return nil
}

// Scan runs a single-shot power sweep. Dispatches on the binary chosen
// at init — never switched at runtime (see struct docs + MESHSAT-655).
func (s *RTLPowerScanner) Scan(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error) {
	if strings.HasSuffix(s.binary, "rtl_power_fftw") {
		return s.scanFFTW(ctx, freqLow, freqHigh, binSize)
	}
	return s.scanLegacy(ctx, freqLow, freqHigh, binSize)
}

// scanFFTW invokes rtl_power_fftw. We map our (low, high, binSize) into
// rpfftw's (-f low:high, -b bins). rpfftw requires an even bin count;
// we round up to the nearest even number of bins.
//
// `-n 1000` = average 1000 FFTs per bin. A single un-averaged FFT of
// thermal noise has 5-15 dB per-bin variance, which dominates the
// waterfall and renders as rainbow speckle regardless of the actual
// spectrum structure (see MESHSAT-649). 1000 repeats collapses per-bin
// RMS by sqrt(1000)≈32x to sub-dB, matching what desktop SDR tools
// (SigDigger, gqrx, CubicSDR) produce. At 2.4 Msps with FFT size 1024
// this adds ~0.43 s of capture time per band — the per-band scan is
// still dominated by the RTL-SDR Blog V4 tuner init (~5 s cold) so the
// end-to-end cost bump is marginal and well inside the 30 s timeout.
// `-q` keeps stderr quiet after the first run.
//
// Edge repair (MESHSAT-652): rtl_power_fftw applies a Hann FFT window
// that rolls off sensitivity by 2-3 dB at the first + last ~2 output
// bins of every scan. Before MESHSAT-649 this was hidden by the ~10 dB
// per-bin noise of -n 1; after -n 1000 drove per-bin RMS sub-dB, the
// rolloff is 10× the noise floor and dominates the waterfall palette
// on narrow bands (LoRa 24 bins, APRS 16 bins, GPS 80 bins) as dark-
// burgundy stripes at both edges — the edge bins clip below turbo(0).
// Widen the request by `cropPad` bins on each side, then drop them
// from the returned slice. The caller sees exactly the band + bin
// width it asked for, but only from the flat interior of the tuner
// response. Bands that already do frequency hopping (LTE B20/B8, 3
// MHz > 2.4 MHz RTL-SDR bandwidth) don't show the artifact because
// the hops stitch over their own edges, but this path is hit for
// every band so they all benefit consistently.
func (s *RTLPowerScanner) scanFFTW(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error) {
	span := freqHigh - freqLow
	if span <= 0 || binSize <= 0 {
		return nil, fmt.Errorf("invalid band: low=%d high=%d bin=%d", freqLow, freqHigh, binSize)
	}
	bins := span / binSize
	if bins < 2 {
		bins = 2
	}

	const cropPad = 2
	widenedLow := freqLow - cropPad*binSize
	widenedHigh := freqHigh + cropPad*binSize
	effBins := (widenedHigh - widenedLow) / binSize
	if effBins%2 != 0 {
		// rpfftw requires even bins — bump the upper edge so we keep
		// the extra bin in the discarded right-side gutter.
		effBins++
		widenedHigh = widenedLow + effBins*binSize
	}

	freqArg := fmt.Sprintf("%d:%d", widenedLow, widenedHigh)
	cmd := exec.CommandContext(ctx, s.binary,
		"-f", freqArg,
		"-b", strconv.Itoa(effBins),
		"-n", "1000",
		"-q",
	)
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rtl_power_fftw: %w", err)
	}
	powers, err := parseFFTWOutput(string(out))
	if err != nil {
		return nil, err
	}
	// Return exactly the requested interior band. Guard against short
	// reads (rare — rpfftw almost always returns effBins samples — but
	// we'd rather give the caller what's available than a panic).
	end := cropPad + bins
	if end > len(powers) {
		end = len(powers)
	}
	if cropPad < len(powers) {
		powers = powers[cropPad:end]
	}
	return powers, nil
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
