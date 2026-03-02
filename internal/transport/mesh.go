package transport

import (
	"context"
	"encoding/json"
)

// MeshEvent represents a typed event from the Meshtastic radio.
type MeshEvent struct {
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Time    string          `json:"time"`
}

// MeshNode represents a node in the mesh network.
type MeshNode struct {
	Num             uint32   `json:"num"`
	UserID          string   `json:"user_id"`
	LongName        string   `json:"long_name"`
	ShortName       string   `json:"short_name"`
	HWModel         int      `json:"hw_model"`
	HWModelName     string   `json:"hw_model_name"`
	Latitude        float64  `json:"latitude"`
	Longitude       float64  `json:"longitude"`
	Altitude        int32    `json:"altitude"`
	Sats            int      `json:"sats"`
	BatteryLevel    int      `json:"battery_level"`
	Voltage         float32  `json:"voltage"`
	ChannelUtil     float32  `json:"channel_util,omitempty"`
	AirUtilTx       float32  `json:"air_util_tx,omitempty"`
	Temperature     *float32 `json:"temperature,omitempty"`
	Humidity        *float32 `json:"humidity,omitempty"`
	Pressure        *float32 `json:"pressure,omitempty"`
	UptimeSeconds   int      `json:"uptime_seconds,omitempty"`
	SNR             float32  `json:"snr"`
	RSSI            int32    `json:"rssi,omitempty"`
	SignalQuality   string   `json:"signal_quality,omitempty"`
	DiagnosticNotes string   `json:"diagnostic_notes,omitempty"`
	LastHeard       int64    `json:"last_heard"`
	LastHeardStr    string   `json:"last_heard_str"`
}

// MeshMessage represents a decoded mesh packet.
type MeshMessage struct {
	From        uint32  `json:"from"`
	To          uint32  `json:"to"`
	Channel     uint32  `json:"channel"`
	ID          uint32  `json:"id"`
	PortNum     int     `json:"portnum"`
	PortNumName string  `json:"portnum_name"`
	DecodedText string  `json:"decoded_text"`
	RxTime      int64   `json:"rx_time"`
	RxSNR       float32 `json:"rx_snr"`
	HopLimit    int     `json:"hop_limit"`
	HopStart    int     `json:"hop_start"`
	Timestamp   string  `json:"timestamp"`
}

// MeshStatus represents the connection status of the Meshtastic device.
type MeshStatus struct {
	Connected   bool   `json:"connected"`
	Transport   string `json:"transport"`
	Address     string `json:"address"`
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name"`
	HWModel     int    `json:"hw_model"`
	HWModelName string `json:"hw_model_name"`
	NumNodes    int    `json:"num_nodes"`
}

// SendRequest is a text message send request.
type SendRequest struct {
	Text    string `json:"text"`
	To      string `json:"to,omitempty"`
	Channel int    `json:"channel,omitempty"`
}

// RawRequest is a raw packet send request.
type RawRequest struct {
	To      string `json:"to,omitempty"`
	PortNum int    `json:"portnum"`
	Payload string `json:"payload"` // base64
	Channel int    `json:"channel,omitempty"`
	WantAck bool   `json:"want_ack,omitempty"`
}

// ChannelRequest configures a radio channel.
type ChannelRequest struct {
	Index           uint32 `json:"index"`
	Name            string `json:"name"`
	PSK             string `json:"psk"` // base64
	Role            string `json:"role"`
	UplinkEnabled   bool   `json:"uplink_enabled"`
	DownlinkEnabled bool   `json:"downlink_enabled"`
}

// Waypoint represents a map waypoint.
type Waypoint struct {
	ID          uint32  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Icon        int     `json:"icon"`
	Expire      int64   `json:"expire"`
}

// MeshTransport abstracts how MeshSat talks to the Meshtastic radio.
type MeshTransport interface {
	Subscribe(ctx context.Context) (<-chan MeshEvent, error)
	SendMessage(ctx context.Context, req SendRequest) error
	SendRaw(ctx context.Context, req RawRequest) error
	GetNodes(ctx context.Context) ([]MeshNode, error)
	GetStatus(ctx context.Context) (*MeshStatus, error)
	GetMessages(ctx context.Context, limit int) ([]MeshMessage, error)
	GetConfig(ctx context.Context) (map[string]interface{}, error)
	AdminReboot(ctx context.Context, nodeNum uint32, delay int) error
	AdminFactoryReset(ctx context.Context, nodeNum uint32) error
	Traceroute(ctx context.Context, nodeNum uint32) error
	SetRadioConfig(ctx context.Context, section string, data json.RawMessage) error
	SetModuleConfig(ctx context.Context, section string, data json.RawMessage) error
	SetChannel(ctx context.Context, req ChannelRequest) error
	SendWaypoint(ctx context.Context, wp Waypoint) error
	Close() error
}
