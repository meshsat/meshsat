package transport

import "context"

// AstrocastEvent represents a typed event from the Astronode S module.
type AstrocastEvent struct {
	Type    string `json:"type"`    // "uplink_ack", "downlink", "sat_search", "reset"
	Message string `json:"message"` // human-readable description
}

// AstrocastStatus represents the connection status of the Astronode S module.
type AstrocastStatus struct {
	Connected      bool   `json:"connected"`
	Port           string `json:"port"`
	ModuleState    string `json:"module_state"` // "idle", "satellite_search", "communication"
	UplinkQueued   int    `json:"uplink_queued"`
	DownlinkQueued int    `json:"downlink_queued"`
	LastSatContact string `json:"last_sat_contact,omitempty"`
}

// AstrocastResult represents the result of an uplink enqueue operation.
type AstrocastResult struct {
	MessageID uint16 `json:"message_id"`
	Queued    bool   `json:"queued"`
}

// AstrocastTransport abstracts how MeshSat talks to the Astronode S module.
type AstrocastTransport interface {
	Send(ctx context.Context, data []byte) (*AstrocastResult, error)
	Receive(ctx context.Context) ([]byte, error)
	GetStatus(ctx context.Context) (*AstrocastStatus, error)
	Subscribe(ctx context.Context) (<-chan AstrocastEvent, error)
	Close() error
}
