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
}

// SatStatus represents the connection status of the satellite modem.
type SatStatus struct {
	Connected bool   `json:"connected"`
	Port      string `json:"port"`
	IMEI      string `json:"imei"`
	Model     string `json:"model"`
}

// SignalInfo represents satellite signal quality.
type SignalInfo struct {
	Bars       int    `json:"bars"` // 0-5
	Timestamp  string `json:"timestamp"`
	Assessment string `json:"assessment"` // "none", "poor", "fair", "good", "excellent"
}

// SBDResult represents the result of an SBD operation.
type SBDResult struct {
	MOStatus   int    `json:"mo_status"`
	MOMSN      int    `json:"mo_msn"`
	MTStatus   int    `json:"mt_status"`
	MTMSN      int    `json:"mt_msn"`
	MTLength   int    `json:"mt_length"`
	MTQueued   int    `json:"mt_queued"`
	StatusText string `json:"status_text"`
}

// SatTransport abstracts how MeshSat talks to the satellite modem.
type SatTransport interface {
	Subscribe(ctx context.Context) (<-chan SatEvent, error)
	Send(ctx context.Context, data []byte) (*SBDResult, error)
	Receive(ctx context.Context) ([]byte, error)
	MailboxCheck(ctx context.Context) (*SBDResult, error)
	GetSignal(ctx context.Context) (*SignalInfo, error)
	GetStatus(ctx context.Context) (*SatStatus, error)
	Close() error
}
