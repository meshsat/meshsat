package channel

import "time"

// RegisterDefaults registers the 6 built-in channels.
func RegisterDefaults(r *Registry) {
	r.Register(ChannelDescriptor{
		ID:         "mesh",
		Label:      "Meshtastic LoRa",
		IsPaid:     false,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 237,
		RetryConfig: RetryConfig{
			Enabled:    false,
			MaxRetries: 1,
		},
		Options: []OptionField{
			{Key: "channel", Label: "Mesh Channel", Type: "number", Default: "0"},
			{Key: "node", Label: "Target Node", Type: "text", Default: ""},
		},
	})

	r.Register(ChannelDescriptor{
		ID:         "iridium",
		Label:      "Iridium SBD",
		IsPaid:     true,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 340,
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 180 * time.Second,
			MaxWait:     30 * time.Minute,
			MaxRetries:  0, // infinite
			BackoffFunc: "isu",
		},
		Options: []OptionField{
			{Key: "priority", Label: "Priority", Type: "select", Default: "1", Options: []string{"0", "1", "2"}},
			{Key: "include_gps", Label: "Include GPS", Type: "checkbox", Default: "false"},
		},
	})

	r.Register(ChannelDescriptor{
		ID:         "astrocast",
		Label:      "Astrocast",
		IsPaid:     true,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 160,
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 300 * time.Second,
			MaxWait:     60 * time.Minute,
			MaxRetries:  5,
			BackoffFunc: "exponential",
		},
		Options: []OptionField{
			{Key: "power_mode", Label: "Power Mode", Type: "select", Default: "balanced", Options: []string{"low_power", "balanced", "performance"}},
			{Key: "fragment", Label: "Auto-Fragment", Type: "checkbox", Default: "true"},
		},
	})

	r.Register(ChannelDescriptor{
		ID:         "cellular",
		Label:      "Cellular SMS",
		IsPaid:     true,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 160,
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 30 * time.Second,
			MaxWait:     5 * time.Minute,
			MaxRetries:  3,
			BackoffFunc: "exponential",
		},
	})

	r.Register(ChannelDescriptor{
		ID:         "webhook",
		Label:      "Webhook HTTP",
		IsPaid:     false,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 0, // unlimited
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 5 * time.Second,
			MaxWait:     5 * time.Minute,
			MaxRetries:  5,
			BackoffFunc: "exponential",
		},
		Options: []OptionField{
			{Key: "url", Label: "Webhook URL", Type: "text", Default: ""},
			{Key: "method", Label: "HTTP Method", Type: "select", Default: "POST", Options: []string{"POST", "PUT"}},
		},
	})

	r.Register(ChannelDescriptor{
		ID:         "mqtt",
		Label:      "MQTT Broker",
		IsPaid:     false,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 0, // unlimited
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 1 * time.Second,
			MaxWait:     1 * time.Minute,
			MaxRetries:  10,
			BackoffFunc: "exponential",
		},
		Options: []OptionField{
			{Key: "topic", Label: "MQTT Topic", Type: "text", Default: ""},
		},
	})
}
