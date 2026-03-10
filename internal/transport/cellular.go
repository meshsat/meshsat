package transport

import (
	"context"
	"encoding/json"
)

// CellEvent represents a typed event from the cellular modem.
type CellEvent struct {
	Type    string          `json:"type"` // connected, disconnected, signal, sms_received, network_change
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
	Time    string          `json:"time"`
	Signal  int             `json:"signal,omitempty"` // bars 0-5, only for "signal" events
}

// CellStatus represents the connection status of the cellular modem.
type CellStatus struct {
	Connected    bool   `json:"connected"`
	Port         string `json:"port"`
	IMEI         string `json:"imei"`
	Model        string `json:"model"`
	Operator     string `json:"operator"`
	NetworkType  string `json:"network_type"`           // 2G, 3G, 4G, LTE
	SIMState     string `json:"sim_state"`              // READY, NOT_INSERTED, PIN_REQUIRED
	Registration string `json:"registration"`           // not_registered, registered_home, searching, denied, registered_roaming
	PhoneNumber  string `json:"phone_number,omitempty"` // subscriber number from AT+CNUM or SIM card DB
	ICCID        string `json:"iccid,omitempty"`        // SIM card ICCID from AT+CCID
	SIMLabel     string `json:"sim_label,omitempty"`    // user-assigned SIM card label
}

// SIMCardInfo holds saved SIM card settings for auto-apply during modem init.
type SIMCardInfo struct {
	ICCID string
	Phone string
	PIN   string
	Label string
}

// SIMCardLookupFunc looks up saved settings for a SIM card by its ICCID.
type SIMCardLookupFunc func(iccid string) (*SIMCardInfo, error)

// CellSignalInfo represents cellular signal quality.
type CellSignalInfo struct {
	Bars       int    `json:"bars"`       // 0-5
	DBm        int    `json:"dbm"`        // raw dBm
	Technology string `json:"technology"` // GSM, WCDMA, LTE
	Timestamp  string `json:"timestamp"`
	Assessment string `json:"assessment"` // "none", "poor", "fair", "good", "excellent"
}

// SMSMessage represents an incoming or outgoing SMS.
type SMSMessage struct {
	Index  int    `json:"index,omitempty"`
	Sender string `json:"sender"`
	Text   string `json:"text"`
	Time   string `json:"time"`
}

// CellDataStatus represents the LTE data connection state.
type CellDataStatus struct {
	Active    bool   `json:"active"`
	APN       string `json:"apn"`
	IPAddress string `json:"ip_address"`
	Interface string `json:"interface"`          // e.g. "wwan0"
	TxBytes   int64  `json:"tx_bytes,omitempty"` // bytes transmitted on data interface
	RxBytes   int64  `json:"rx_bytes,omitempty"` // bytes received on data interface
}

// CellInfo represents cell tower information from the modem.
type CellInfo struct {
	MCC         string `json:"mcc"`
	MNC         string `json:"mnc"`
	LAC         string `json:"lac"`
	CellID      string `json:"cell_id"`
	NetworkType string `json:"network_type"` // GSM, WCDMA, LTE, NR5G
	RSRP        *int   `json:"rsrp,omitempty"`
	RSRQ        *int   `json:"rsrq,omitempty"`
}

// CellBroadcastMsg represents a cell broadcast (CBS/CMAS/EU-Alert) message.
type CellBroadcastMsg struct {
	SerialNumber int    `json:"serial_number"`
	MessageID    int    `json:"message_id"`
	Channel      int    `json:"channel"`
	Severity     string `json:"severity"` // extreme, severe, amber, test, unknown
	Text         string `json:"text"`
}

// CellTransport abstracts how MeshSat talks to a cellular modem.
type CellTransport interface {
	Subscribe(ctx context.Context) (<-chan CellEvent, error)
	SendSMS(ctx context.Context, to string, text string) error
	GetSignal(ctx context.Context) (*CellSignalInfo, error)
	GetStatus(ctx context.Context) (*CellStatus, error)
	GetDataStatus(ctx context.Context) (*CellDataStatus, error)
	ConnectData(ctx context.Context, apn string) error
	DisconnectData(ctx context.Context) error
	UnlockPIN(ctx context.Context, pin string) error
	GetCellInfo(ctx context.Context) (*CellInfo, error)
	Close() error
}
