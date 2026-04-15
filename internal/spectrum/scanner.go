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
	// Scan performs a single-shot power sweep and returns average power in dB.
	Scan(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error)
	// Available reports whether the scanning backend is ready.
	Available() bool
}

// RTLPowerScanner runs rtl_power as a subprocess (no CGO).
type RTLPowerScanner struct {
	binary string // path to rtl_power binary
}

// NewRTLPowerScanner creates a scanner using rtl_power.
// Returns nil if rtl_power is not found in PATH.
func NewRTLPowerScanner() *RTLPowerScanner {
	path, err := exec.LookPath("rtl_power")
	if err != nil {
		return nil
	}
	return &RTLPowerScanner{binary: path}
}

func (s *RTLPowerScanner) Available() bool {
	return s.binary != ""
}

// Scan runs a single-shot rtl_power sweep and returns power values in dB per bin.
// Command: rtl_power -f <low>:<high>:<bin> -i 1 -1
// Output CSV: date, time, Hz_low, Hz_high, Hz_step, num_samples, dB, dB, ...
func (s *RTLPowerScanner) Scan(ctx context.Context, freqLow, freqHigh, binSize int) ([]float64, error) {
	freqRange := fmt.Sprintf("%d:%d:%d", freqLow, freqHigh, binSize)
	cmd := exec.CommandContext(ctx, s.binary, "-f", freqRange, "-i", "1", "-1")
	cmd.Stderr = nil // suppress rtl_power stderr noise

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rtl_power: %w", err)
	}

	return parseRTLPowerOutput(string(out))
}

// parseRTLPowerOutput parses rtl_power CSV output into power dB values.
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
			continue // skip malformed lines
		}

		// Fields 0-5 are metadata; fields 6+ are power values in dB
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
