package gateway

import (
	"context"
	"time"

	"meshsat/internal/transport"
)

// InboundMessage is a message received from an external gateway to inject into the mesh.
type InboundMessage struct {
	Text    string `json:"text"`
	To      string `json:"to,omitempty"`
	Channel int    `json:"channel,omitempty"`
	Source  string `json:"source"` // "mqtt", "iridium"
}

// GatewayStatus reports the current state of a gateway.
type GatewayStatus struct {
	Type             string    `json:"type"`
	Connected        bool      `json:"connected"`
	MessagesIn       int64     `json:"messages_in"`
	MessagesOut      int64     `json:"messages_out"`
	Errors           int64     `json:"errors"`
	DLQPending       int64     `json:"dlq_pending,omitempty"`
	LastActivity     time.Time `json:"last_activity,omitempty"`
	ConnectionUptime string    `json:"connection_uptime,omitempty"`
}

// Gateway abstracts an external message bridge (MQTT, future Iridium).
type Gateway interface {
	Start(ctx context.Context) error
	Stop() error
	Forward(ctx context.Context, msg *transport.MeshMessage) error
	Receive() <-chan InboundMessage
	Status() GatewayStatus
	Type() string
}
