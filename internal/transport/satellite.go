package transport

import (
	"context"
	"encoding/json"
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
type SatStatus struct {
	Connected bool   `json:"connected"`
	Port      string `json:"port"`
	IMEI      string `json:"imei"`
	Model     string `json:"model"`
	Type      string `json:"type"` // "sbd" (9603) or "imt" (9704)
	Firmware  string `json:"firmware,omitempty"`
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

// SBDResult represents the result of an SBD operation.
type SBDResult struct {
	MOStatus   int    `json:"mo_status"`
	MOMSN      int    `json:"mo_msn"`
	MTReceived bool   `json:"mt_received"` // from HAL (true when MT piggybacked)
	MTStatus   int    `json:"mt_status"`
	MTMSN      int    `json:"mt_msn"`
	MTLength   int    `json:"mt_length"`
	MTQueued   int    `json:"mt_queued"`
	StatusText string `json:"status_text"`
}

// MOSuccess returns true if the MO (Mobile Originated) transfer succeeded.
// MO status 0-4 indicates successful transfer to the GSS; values >= 5 are failures
// (e.g. 32 = no network service).
func (r *SBDResult) MOSuccess() bool {
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

// SatTransport abstracts how MeshSat talks to the satellite modem.
type SatTransport interface {
	Subscribe(ctx context.Context) (<-chan SatEvent, error)
	Send(ctx context.Context, data []byte) (*SBDResult, error)
	SendText(ctx context.Context, text string) (*SBDResult, error)
	Receive(ctx context.Context) ([]byte, error)
	MailboxCheck(ctx context.Context) (*SBDResult, error)
	GetSignal(ctx context.Context) (*SignalInfo, error)
	GetSignalFast(ctx context.Context) (*SignalInfo, error)
	GetStatus(ctx context.Context) (*SatStatus, error)
	GetGeolocation(ctx context.Context) (*GeolocationInfo, error)
	// MOBufferEmpty checks AT+SBDSX and returns true if the MO buffer is empty
	// (meaning a previous SBDIX already transmitted and cleared it).
	MOBufferEmpty(ctx context.Context) (bool, error)
	// GetSystemTime returns the Iridium network time via AT-MSSTM.
	GetSystemTime(ctx context.Context) (*IridiumTime, error)
	// GetFirmwareVersion returns the modem firmware version string.
	GetFirmwareVersion(ctx context.Context) (string, error)
	// Sleep puts the modem into low-power sleep mode (if sleep pin is configured).
	Sleep(ctx context.Context) error
	// Wake brings the modem out of sleep mode (if sleep pin is configured).
	Wake(ctx context.Context) error
	Close() error
}
