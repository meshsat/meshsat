package spectrum

import "time"

// SpectrumState represents the detected state of a monitored frequency band.
type SpectrumState string

const (
	StateClear        SpectrumState = "clear"
	StateJamming      SpectrumState = "jamming"
	StateInterference SpectrumState = "interference"
	StateCalibrating  SpectrumState = "calibrating"
	StateDisabled     SpectrumState = "disabled"
)

// Band defines a monitored frequency range and its associated transport interface.
type Band struct {
	Name        string // e.g. "lora_868", "aprs_144"
	FreqLow     int    // Hz
	FreqHigh    int    // Hz
	BinSize     int    // Hz per FFT bin
	InterfaceID string // e.g. "mesh_0", "ax25_0"
	Label       string // human-readable label
}

// DefaultBands are the two primary bands monitored by the RTL-SDR.
// Both are well within the RTL-SDR's 24MHz-1.7GHz range.
var DefaultBands = []Band{
	{
		Name:        "lora_868",
		FreqLow:     868000000,
		FreqHigh:    868600000,
		BinSize:     25000,
		InterfaceID: "mesh_0",
		Label:       "LoRa EU868",
	},
	{
		Name:        "aprs_144",
		FreqLow:     144700000,
		FreqHigh:    144900000,
		BinSize:     12500,
		InterfaceID: "ax25_0",
		Label:       "APRS 144.8 MHz",
	},
}

// BandStatus represents the current state of a monitored frequency band.
type BandStatus struct {
	Band         string        `json:"band"`
	InterfaceID  string        `json:"interface_id"`
	Label        string        `json:"label"`
	State        SpectrumState `json:"state"`
	PowerDB      float64       `json:"power_db"`
	BaselineMean float64       `json:"baseline_mean"`
	BaselineStd  float64       `json:"baseline_std"`
	Since        time.Time     `json:"since"`
	Consecutive  int           `json:"consecutive_samples"`
	FreqLow      int           `json:"freq_low"`
	FreqHigh     int           `json:"freq_high"`
}

// Baseline holds the calibrated noise floor statistics for a band.
type Baseline struct {
	Mean    float64
	Std     float64
	Samples int
}

// Detection thresholds (from issue spec).
const (
	JammingSigma      = 3.0 // power > mean + 3*sigma = jamming
	InterferenceSigma = 6.0 // narrowband spike > mean + 6*sigma = interference
	RecoverySigma     = 1.0 // power within mean +/- 1*sigma = clear

	JammingConsecutive  = 5  // consecutive samples to confirm jamming
	RecoveryConsecutive = 10 // consecutive samples to confirm recovery
	CooldownSeconds     = 30 // hysteresis cooldown after state change

	CalibrationDuration = 30 * time.Second // baseline calibration period
	ScanInterval        = 3 * time.Second  // spectrum scan interval
)

// RTL-SDR USB identifiers (Realtek RTL2832U).
const (
	RTLSDR_VID      = "0bda"
	RTLSDR_PID_2832 = "2832"
	RTLSDR_PID_2838 = "2838"
)
