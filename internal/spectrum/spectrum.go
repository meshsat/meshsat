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

// DefaultBands are the RF bands monitored by the RTL-SDR for jamming
// detection. All windows are kept narrow (<= 3 MHz span) so each per-band
// scan completes inside the 5s timeout in monitor.scanAllBands — rtl_power
// retunes in ~2.4 MHz steps, so wider spans compound retune latency and
// time out.
//
// LTE notes: we can only cover the low-band European allocations with the
// R820T tuner (24 MHz - 1.766 GHz). Band 3 (1800) and Band 7 (2600) are
// out of range. Band 20 (800) and Band 8 (900) are the most common EU
// low-band allocations and catch wideband jammers aimed at cellular.
// We monitor a 3 MHz slice at the centre of each DL allocation — enough
// to detect broadband jamming, which is what matters for failover. A
// narrowband jammer on a specific LTE carrier would be caught by the
// modem's own RSSI/SNR reporting.
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
	{
		// GPS L1 C/A: 1575.42 MHz ± ~1 MHz (±1.023 MHz chip rate).
		// GPS jamming is a documented EW vector and the modem/GNSS module
		// cannot tell us it is being jammed versus losing sky view — the
		// SDR can. When jamming is detected timesync should derate GPS
		// stratum and fall back to peer-consensus time.
		Name:        "gps_l1",
		FreqLow:     1574420000,
		FreqHigh:    1576420000,
		BinSize:     25000,
		InterfaceID: "gps_0",
		Label:       "GPS L1",
	},
	{
		// LTE Band 20 DL: 791-821 MHz (EU 800). Monitor 3 MHz at centre
		// ~806 MHz. Broadband jamming on this band kills 4G + SMS. On
		// jamming, gateway-level logic can preemptively switch to Iridium
		// SBD for ops messaging.
		Name:        "lte_b20_dl",
		FreqLow:     804500000,
		FreqHigh:    807500000,
		BinSize:     50000,
		InterfaceID: "cellular_0",
		Label:       "LTE Band 20 DL (800)",
	},
	{
		// LTE Band 8 DL: 925-960 MHz (EU 900). Monitor 3 MHz at centre
		// ~942.5 MHz. Dual-band coverage guards against the common
		// scenario where one of the two bands is jammed but the other
		// isn't — the modem can fall back to the clear band on its own,
		// and we can surface that in the UI.
		Name:        "lte_b8_dl",
		FreqLow:     941000000,
		FreqHigh:    944000000,
		BinSize:     50000,
		InterfaceID: "cellular_0",
		Label:       "LTE Band 8 DL (900)",
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
