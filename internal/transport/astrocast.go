package transport

import "context"

// AstrocastEvent represents a typed event from the Astronode S module.
type AstrocastEvent struct {
	Type    string `json:"type"`    // "sak_available", "cmd_available", "busy", "reset"
	Message string `json:"message"` // human-readable description
}

// AstrocastStatus represents the connection status of the Astronode S module.
type AstrocastStatus struct {
	Connected      bool   `json:"connected"`
	Port           string `json:"port"`
	ModuleState    string `json:"module_state"` // "idle", "busy", "reset"
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

// AstrocastModuleState represents the module's internal state (TLV-encoded MST_RR response).
type AstrocastModuleState struct {
	MsgQueued       uint8  `json:"msg_queued"`        // messages queued for uplink (tag 0x41)
	AckMsgQueued    uint8  `json:"ack_msg_queued"`    // ack messages queued (tag 0x42)
	LastResetReason uint8  `json:"last_reset_reason"` // last reset reason (tag 0x43)
	Uptime          uint32 `json:"uptime"`            // seconds since last reset (tag 0x44)
}

// AstrocastLastContact represents details about the last satellite contact (TLV-encoded LCD_RR response).
type AstrocastLastContact struct {
	StartTime uint32 `json:"start_time"` // contact start time (tag 0x51)
	EndTime   uint32 `json:"end_time"`   // contact end time (tag 0x52)
	PeakRSSI  uint8  `json:"peak_rssi"`  // peak RSSI unsigned (tag 0x53)
	PeakTime  uint32 `json:"peak_time"`  // peak RSSI time (tag 0x54)
}

// AstrocastEnvironment represents signal environment details (TLV-encoded END_RR response).
type AstrocastEnvironment struct {
	LastMACResult   uint8  `json:"last_mac_result"`    // last MAC result (tag 0x61)
	LastRSSI        uint8  `json:"last_rssi"`          // last RSSI unsigned (tag 0x62)
	TimeSinceSatDet uint32 `json:"time_since_sat_det"` // seconds since last satellite detection (tag 0x63)
}

// AstrocastPerformance represents performance counters (TLV-encoded PER_RR response).
// Tags 0x01-0x0E, each 4 bytes (14 counters total).
type AstrocastPerformance struct {
	SatSearchPhasesCnt   uint32 `json:"sat_search_phases_cnt"`   // 0x01
	SatDetectOpCnt       uint32 `json:"sat_detect_op_cnt"`       // 0x02
	SatDetectCnt         uint32 `json:"sat_detect_cnt"`          // 0x03
	SignalDemodPhasesCnt uint32 `json:"signal_demod_phases_cnt"` // 0x04
	SignalDemodAttempts  uint32 `json:"signal_demod_attempts"`   // 0x05
	SignalDemodSuccess   uint32 `json:"signal_demod_success"`    // 0x06
	AckDemodAttempts     uint32 `json:"ack_demod_attempts"`      // 0x07
	AckDemodSuccess      uint32 `json:"ack_demod_success"`       // 0x08
	Queued               uint32 `json:"queued"`                  // 0x09
	Dequeued             uint32 `json:"dequeued"`                // 0x0A
	AckReceived          uint32 `json:"ack_received"`            // 0x0B
	MsgTransmitted       uint32 `json:"msg_transmitted"`         // 0x0C
	MsgAcknowledged      uint32 `json:"msg_acknowledged"`        // 0x0D
	MsgTransmitFailed    uint32 `json:"msg_transmit_failed"`     // 0x0E
}

// AstrocastConfig represents the configuration read from CFG_RR.
type AstrocastConfig struct {
	ProductID      uint8 `json:"product_id"`
	HardwareRev    uint8 `json:"hardware_rev"`
	FirmwareMajor  uint8 `json:"firmware_major"`
	FirmwareMinor  uint8 `json:"firmware_minor"`
	FirmwarePatch  uint8 `json:"firmware_patch"`
	WithPLD        bool  `json:"with_pld_ack"`
	WithGeo        bool  `json:"with_geo"`
	WithEphemeris  bool  `json:"with_ephemeris"`
	WithDeepSleep  bool  `json:"with_deep_sleep"`
	WithAckEvent   bool  `json:"with_ack_event"`
	WithResetEvent bool  `json:"with_reset_event"`
	WithCmdEvent   bool  `json:"with_cmd_event"`
	WithTxPend     bool  `json:"with_tx_pend_event"`
}

// AstrocastModuleInfo holds module identification data (GUID, serial, product number).
type AstrocastModuleInfo struct {
	GUID          string `json:"guid,omitempty"`
	SerialNumber  string `json:"serial_number,omitempty"`
	ProductNumber string `json:"product_number,omitempty"`
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

	// Configuration management
	ReadConfig(ctx context.Context) (*AstrocastConfig, error)
	SaveConfig(ctx context.Context) error
	FactoryReset(ctx context.Context) error

	// Module identification
	ReadRTC(ctx context.Context) (uint32, error)
	ReadGUID(ctx context.Context) (string, error)
	ReadSerialNumber(ctx context.Context) (string, error)
	ReadProductNumber(ctx context.Context) (string, error)

	// Misc
	SaveContext(ctx context.Context) error
	ClearPerformance(ctx context.Context) error

	Close() error
}
