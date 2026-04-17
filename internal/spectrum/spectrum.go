package spectrum

import "time"

// SpectrumState represents the detected state of a monitored frequency band.
type SpectrumState string

const (
	StateClear        SpectrumState = "clear"
	StateDegraded     SpectrumState = "degraded" // sustained elevation, not yet EW-plausible
	StateInterference SpectrumState = "interference"
	StateJamming      SpectrumState = "jamming"
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

	// CalibrationStartedAt is non-zero only for the band whose 30 s
	// calibration window is currently running. The UI derives a
	// countdown + progress bar from it. Bands still queued behind the
	// active one have this field zero and state=calibrating — the UI
	// shows them as "queued". [MESHSAT-509]
	CalibrationStartedAt time.Time `json:"calibration_started_at,omitempty"`

	// CalibrationDurationSec echoes the server's constant so the UI
	// doesn't hardcode it. 30 s currently; changing it backend-side
	// flows through to the client without a client redeploy.
	CalibrationDurationSec int `json:"calibration_duration_sec,omitempty"`

	// Candidate tracking for dwell-time state promotion. These are
	// internal bookkeeping exposed via JSON so the UI can render
	// "jamming in 42 s" during a sustained-but-not-yet-promoted
	// event. CandidateState is the tier the latest scan voted for;
	// CandidateSince is when that tier was first observed continuously
	// (reset when the tier changes). [MESHSAT-509]
	CandidateState SpectrumState `json:"candidate_state,omitempty"`
	CandidateSince time.Time     `json:"candidate_since,omitempty"`

	// Latest-scan ITU-R SM.1880 occupancy (0..1) and Wiener-entropy
	// spectral flatness (0..1). Exposed on the status endpoint so the
	// UI seeds these metrics immediately on page load, before the
	// first SSE scan event arrives.
	LastOccupancy float64 `json:"occupancy"`
	LastFlatness  float64 `json:"flatness"`
}

// Baseline holds the calibrated noise floor statistics for a band.
type Baseline struct {
	Mean    float64
	Std     float64
	Samples int
}

// SpectrumEventKind distinguishes per-scan samples from state transitions.
type SpectrumEventKind string

const (
	// EventScan carries the per-bin power array from one completed sweep.
	// Consumed by the waterfall UI via SSE — high frequency, small payload.
	EventScan SpectrumEventKind = "scan"
	// EventTransition announces a state change (e.g. clear -> jamming).
	// Consumed by TAK/CoT relay, hub reporter, and dashboard popup.
	EventTransition SpectrumEventKind = "transition"
)

// SpectrumEvent is the unit of fan-out from the monitor to alert
// consumers and the waterfall stream. Both kinds carry enough metadata
// that a consumer does not need to re-query status separately.
type SpectrumEvent struct {
	Kind         SpectrumEventKind `json:"kind"`
	Band         string            `json:"band"`
	Label        string            `json:"label"`
	InterfaceID  string            `json:"interface_id"`
	FreqLow      int               `json:"freq_low"`
	FreqHigh     int               `json:"freq_high"`
	BinSize      int               `json:"bin_size"`
	Timestamp    time.Time         `json:"timestamp"`
	Powers       []float64         `json:"powers,omitempty"` // populated only for EventScan
	AvgDB        float64           `json:"avg_db"`
	MaxDB        float64           `json:"max_db"`
	State        SpectrumState     `json:"state"`
	OldState     SpectrumState     `json:"old_state,omitempty"` // populated only for EventTransition
	BaselineMean float64           `json:"baseline_mean"`
	BaselineStd  float64           `json:"baseline_std"`
	// Derived thresholds included so the UI does not duplicate the
	// sigma arithmetic and can draw the jamming/interference lines
	// directly.
	ThreshJammingDB      float64 `json:"thresh_jamming_db"`
	ThreshInterferenceDB float64 `json:"thresh_interference_db"`
	// Calibration progress echoed on every scan event during Phase 1
	// so the UI can render a live countdown + progress bar without
	// re-polling /api/spectrum/status. Zero-valued on Phase 2 events.
	CalibrationStartedAt   time.Time `json:"calibration_started_at,omitempty"`
	CalibrationDurationSec int       `json:"calibration_duration_sec,omitempty"`

	// ITU-R SM.1880 occupancy — fraction (0..1) of FFT bins whose
	// power is ≥ baseline + 6 dB in this scan. Barrage jammers ≥ 0.70;
	// narrowband spikes 0.30-0.70; legitimate bursts < 0.20. Exposed
	// in scan events so the page can render MIJI-9-grade detail.
	Occupancy float64 `json:"occupancy"`

	// Spectral flatness (Wiener entropy, 0..1) of this scan's power
	// distribution in linear units. Near 1.0 = white-noise barrage;
	// < 0.4 = structured signal. Pair with occupancy to discriminate
	// barrage jamming from legit load.
	Flatness float64 `json:"flatness"`

	// Since is the timestamp of the last state transition for this
	// band. UI computes "jamming for 0:00:34" dwell from now - since.
	// Required for MIJI-9 reporting (FM 3-12: report duration).
	Since time.Time `json:"since,omitempty"`
}

// Detection thresholds.
//
// Design brief: naive `> baseline + 3σ` falsely flags every legitimate
// LoRa / APRS burst and every LTE carrier jitter as "jamming". Real
// jammers are distinguished by a combination of (a) absolute dBm
// power floor — below which a jammer is physically implausible,
// (b) spectral occupancy — fraction of bins above the noise floor;
// barrage jammers cover most of the band, legit bursts cover a few
// bins, (c) spectral flatness / entropy — jammer noise is flat,
// structured signals aren't, and (d) persistence — real jammers
// sustain for seconds to minutes; LoRa bursts are <1 s, APRS
// <900 ms. See docs/spectrum-detection.md / research inline comments
// for citations. [MESHSAT-509]
const (
	// Elevation above band baseline that triggers the DEGRADED watch.
	// Still permissive; only promotes further with persistence.
	DegradedDeltaDB = 6.0

	// Spectral occupancy thresholds (fraction of FFT bins above
	// (baseline_mean + 6 dB) in a single scan).
	InterferenceOccupancy = 0.30 // narrowband spike or small cluster
	JammingOccupancy      = 0.70 // barrage covers most of the band

	// Spectral flatness (geometric mean / arithmetic mean of linear
	// power, 0..1). Barrage white-noise jammers approach 1.0; LoRa,
	// LTE, APRS all have flatness < 0.4 typically. We require
	// flatness >= 0.60 for a jamming verdict.
	JammingFlatness = 0.60

	// Persistence windows. A single-sample 3σ spike is just traffic.
	// Real jammers persist for tens of seconds to minutes.
	DegradedPersistenceSec     = 30  // moderate elevation sustained
	InterferencePersistenceSec = 10  // narrowband spike sustained
	JammingPersistenceSec      = 60  // broadband + flat + occupied

	// Hysteresis — how long a band must be "clean" before demoting.
	RecoveryPersistenceSec = 30

	// Per-band absolute power floors (dBm-ish — RTL-SDR doesn't
	// output calibrated dBm, so this is baseline-relative plus a
	// band-specific offset). Below the floor, no amount of spectral
	// activity counts as jamming — it's physically implausible.
	// Units: absolute dB as reported by rtl_power_fftw.
	// Calibrate empirically on a quiet site; these are conservative
	// defaults based on residential Leiden observation.
	PowerFloorLoRa  = -50.0 // LoRa 868 ISM
	PowerFloorAPRS  = -60.0 // VHF 2 m
	PowerFloorGPS   = -50.0 // L1 — normally below noise, jammer clearly above
	PowerFloorLTE20 = -40.0 // DL carrier already ~-70 to -110; jammer >>baseline+20
	PowerFloorLTE8  = -40.0

	CalibrationDuration = 30 * time.Second
	ScanInterval        = 3 * time.Second
)

// PowerFloorForBand returns the minimum absolute power (dB) below which
// a band is considered clear regardless of sigma excursions. Defaults
// to a conservative -45 dB for unknown bands.
func PowerFloorForBand(band string) float64 {
	switch band {
	case "lora_868":
		return PowerFloorLoRa
	case "aprs_144":
		return PowerFloorAPRS
	case "gps_l1":
		return PowerFloorGPS
	case "lte_b20_dl":
		return PowerFloorLTE20
	case "lte_b8_dl":
		return PowerFloorLTE8
	default:
		return -45.0
	}
}

// RTL-SDR USB identifiers (Realtek RTL2832U).
const (
	RTLSDR_VID      = "0bda"
	RTLSDR_PID_2832 = "2832"
	RTLSDR_PID_2838 = "2838"
)
