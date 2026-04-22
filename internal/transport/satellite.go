package transport

import (
	"context"
	"encoding/json"
	"time"
)

// SatEvent represents a typed event from the satellite modem.
type SatEvent struct {
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Time    string          `json:"time"`
	Signal  int             `json:"signal,omitempty"` // bars 0-5, only for "signal" events
}

// SatStatus represents the connection status of the satellite modem.
// NetworkAvailable / NetworkAvailableSince are populated only for SBD
// transports with the MESHSAT_IRIDIUM_NETAV_PIN wired (MESHSAT-666);
// omitempty hides them on 9704/IMT deployments. Zero-value bool is the
// same as "unwired", so consumers that want to distinguish must check
// the transport type.
type SatStatus struct {
	Connected             bool      `json:"connected"`
	Port                  string    `json:"port"`
	IMEI                  string    `json:"imei"`
	Model                 string    `json:"model"`
	Type                  string    `json:"type"` // "sbd" (9603) or "imt" (9704)
	Firmware              string    `json:"firmware,omitempty"`
	NetworkAvailable      bool      `json:"network_available,omitempty"`
	NetworkAvailableSince time.Time `json:"network_available_since,omitempty"`
	LastRingAlert         time.Time `json:"last_ring_alert,omitempty"`
	RIPulseCount          int64     `json:"ri_pulse_count,omitempty"`
}

// IridiumTime represents the Iridium network system time from AT-MSSTM.
type IridiumTime struct {
	SystemTime uint32 `json:"system_time"` // raw 90ms tick count
	EpochUTC   string `json:"epoch_utc"`   // converted to RFC3339
	IsValid    bool   `json:"is_valid"`    // false if modem returned "no network service"
}

// SignalInfo represents satellite signal quality.
type SignalInfo struct {
	Bars       int    `json:"bars"` // 0-5
	Timestamp  string `json:"timestamp"`
	Assessment string `json:"assessment"`       // "none", "poor", "fair", "good", "excellent"
	Source     string `json:"source,omitempty"` // "sbd" (9603), "imt" (9704), or empty for HAL
}

// SatResult represents the result of a satellite send/receive operation.
// Used by both SBD (9603) and IMT (9704) transports. For IMT, the MOStatus
// field contains native JSPR result codes (not synthetic SBD mapping).
type SatResult struct {
	MOStatus   int    `json:"mo_status"`
	MOMSN      int    `json:"mo_msn"`
	MTReceived bool   `json:"mt_received"` // from HAL (true when MT piggybacked)
	MTStatus   int    `json:"mt_status"`
	MTMSN      int    `json:"mt_msn"`
	MTLength   int    `json:"mt_length"`
	MTQueued   int    `json:"mt_queued"`
	StatusText string `json:"status_text"`
}

// SBDResult is an alias for SatResult for backward compatibility.
type SBDResult = SatResult

// MOSuccess returns true if the MO (Mobile Originated) transfer succeeded.
// MO status 0-4 indicates successful transfer to the GSS; values >= 5 are failures
// (e.g. 32 = no network service).
func (r *SatResult) MOSuccess() bool {
	return r.MOStatus >= 0 && r.MOStatus <= 4
}

// GeolocationInfo represents an Iridium-derived geolocation estimate (AT-MSGEO).
// The coordinates represent the satellite sub-point (where the satellite was
// when it last communicated with the modem), NOT the modem's position.
// Multiple readings over time can be overlaid to estimate modem position.
type GeolocationInfo struct {
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	AltKm     float64 `json:"alt_km"`
	Accuracy  float64 `json:"accuracy_km"` // estimated accuracy in km (~200 km for single reading)
	Timestamp string  `json:"timestamp"`
}

// SatTransport abstracts how MeshSat talks to a satellite modem.
// This is the base interface shared by all satellite transports (SBD, IMT).
type SatTransport interface {
	Subscribe(ctx context.Context) (<-chan SatEvent, error)
	Send(ctx context.Context, data []byte) (*SatResult, error)
	SendText(ctx context.Context, text string) (*SatResult, error)
	Receive(ctx context.Context) ([]byte, error)
	MailboxCheck(ctx context.Context) (*SatResult, error)
	GetSignal(ctx context.Context) (*SignalInfo, error)
	GetSignalFast(ctx context.Context) (*SignalInfo, error)
	GetStatus(ctx context.Context) (*SatStatus, error)
	GetFirmwareVersion(ctx context.Context) (string, error)
	Close() error
}

// SBDTransport extends SatTransport with methods specific to the Iridium 9603 SBD modem.
// Only DirectSatTransport and HALSatTransport implement this interface.
type SBDTransport interface {
	SatTransport
	// GetGeolocation returns the satellite sub-point via AT-MSGEO (SBD only).
	GetGeolocation(ctx context.Context) (*GeolocationInfo, error)
	// MOBufferEmpty checks AT+SBDSX and returns true if the MO buffer is empty
	// (meaning a previous SBDIX already transmitted and cleared it).
	MOBufferEmpty(ctx context.Context) (bool, error)
	// GetSystemTime returns the Iridium network time via AT-MSSTM.
	GetSystemTime(ctx context.Context) (*IridiumTime, error)
	// Sleep puts the modem into low-power sleep mode (if sleep pin is configured).
	Sleep(ctx context.Context) error
	// Wake brings the modem out of sleep mode (if sleep pin is configured).
	Wake(ctx context.Context) error
	// HardPowerCycle drives the OnOff pin through a 500 ms OFF pulse,
	// waits for the modem to boot, then re-initialises the AT session.
	// Returns an error if the OnOff pin is not configured (MESHSAT-668).
	HardPowerCycle(ctx context.Context) error
}
