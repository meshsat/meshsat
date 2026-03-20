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

// AstrocastSAK represents a satellite acknowledgment for an uplink payload.
type AstrocastSAK struct {
	PayloadID uint16 `json:"payload_id"`
}

// AstrocastCommand represents a downlink command received from the Astrocast cloud.
type AstrocastCommand struct {
	CreatedDate uint32 `json:"created_date"` // Unix timestamp
	Data        []byte `json:"data"`         // Up to 8 or 40 bytes depending on subscription
}

// AstrocastGeolocation represents a GPS position to write to the module.
type AstrocastGeolocation struct {
	Latitude  int32 `json:"latitude"`  // degrees * 1e7
	Longitude int32 `json:"longitude"` // degrees * 1e7
}

// AstrocastNextContact represents the next satellite contact opportunity.
type AstrocastNextContact struct {
	Delay uint32 `json:"delay"` // seconds until next contact
}

// AstrocastModuleState represents the module's internal state.
type AstrocastModuleState struct {
	UplinkPending   uint8  `json:"uplink_pending"`    // queued uplink count
	DownlinkPending uint8  `json:"downlink_pending"`  // queued downlink count
	Uptime          uint32 `json:"uptime"`            // seconds since last reset
	LastResetReason uint8  `json:"last_reset_reason"` // reset cause code
}

// AstrocastLastContact represents details about the last satellite contact.
type AstrocastLastContact struct {
	TimeSinceLast uint32 `json:"time_since_last"` // seconds since last contact
	PeakRSSI      int16  `json:"peak_rssi"`       // dBm
	TimePeakRSSI  uint32 `json:"time_peak_rssi"`  // seconds since peak RSSI
}

// AstrocastEnvironment represents signal environment details.
type AstrocastEnvironment struct {
	LastSatRSSI int16 `json:"last_sat_rssi"` // dBm from last satellite signal
}

// AstrocastPerformance represents performance counters.
type AstrocastPerformance struct {
	FragmentsSent uint32 `json:"fragments_sent"` // total uplink fragments sent
	FragmentsAckd uint32 `json:"fragments_ackd"` // total fragments acknowledged
	ResetCount    uint32 `json:"reset_count"`    // total module resets
}

// AstrocastTransport abstracts how MeshSat talks to the Astronode S module.
type AstrocastTransport interface {
	Send(ctx context.Context, data []byte) (*AstrocastResult, error)
	Receive(ctx context.Context) ([]byte, error)
	GetStatus(ctx context.Context) (*AstrocastStatus, error)
	Subscribe(ctx context.Context) (<-chan AstrocastEvent, error)

	// Satellite ACK — confirm uplink delivery
	ReadSAK(ctx context.Context) (*AstrocastSAK, error)
	ClearSAK(ctx context.Context) error

	// Downlink commands from Astrocast cloud
	ReadCommand(ctx context.Context) (*AstrocastCommand, error)
	ClearCommand(ctx context.Context) error

	// Geolocation — write GPS position (free, no MTU cost)
	WriteGeolocation(ctx context.Context, geo AstrocastGeolocation) error

	// Module diagnostics
	GetNextContact(ctx context.Context) (*AstrocastNextContact, error)
	GetModuleState(ctx context.Context) (*AstrocastModuleState, error)
	GetLastContact(ctx context.Context) (*AstrocastLastContact, error)
	GetEnvironment(ctx context.Context) (*AstrocastEnvironment, error)
	GetPerformance(ctx context.Context) (*AstrocastPerformance, error)

	Close() error
}
