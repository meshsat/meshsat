package channel

import "time"

// RegisterDefaults registers the 9 built-in channels.
func RegisterDefaults(r *Registry) {
	r.Register(ChannelDescriptor{
		ID:            "mesh",
		Label:         "Meshtastic LoRa",
		IsPaid:        false,
		CanSend:       true,
		CanReceive:    true,
		BinaryCapable: true,
		MaxPayload:    237,
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
		ID:            "iridium",
		Label:         "Iridium SBD",
		IsPaid:        true,
		CanSend:       true,
		CanReceive:    true,
		BinaryCapable: true,
		MaxPayload:    340,
		DefaultTTL:    3600 * time.Second,
		IsSatellite:   true,
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 180 * time.Second,
			MaxWait:     30 * time.Minute,
			MaxRetries:  10, // sane default — prevents runaway retries burning Iridium credits
			BackoffFunc: "isu",
		},
		Options: []OptionField{
			{Key: "priority", Label: "Priority", Type: "select", Default: "1", Options: []string{"0", "1", "2"}},
			{Key: "include_gps", Label: "Include GPS", Type: "checkbox", Default: "false"},
		},
	})

	r.Register(ChannelDescriptor{
		ID:            "astrocast",
		Label:         "Astrocast",
		IsPaid:        true,
		CanSend:       true,
		CanReceive:    true,
		BinaryCapable: true,
		MaxPayload:    160,
		DefaultTTL:    3600 * time.Second,
		IsSatellite:   true,
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
		DefaultTTL: 86400 * time.Second,
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 30 * time.Second,
			MaxWait:     5 * time.Minute,
			MaxRetries:  3,
			BackoffFunc: "exponential",
		},
	})

	r.Register(ChannelDescriptor{
		ID:            "zigbee",
		Label:         "ZigBee 3.0",
		IsPaid:        false,
		CanSend:       true,
		CanReceive:    true,
		BinaryCapable: true,
		MaxPayload:    100, // ~100 bytes after ZigBee APS/NWK headers
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 2 * time.Second,
			MaxWait:     30 * time.Second,
			MaxRetries:  3,
			BackoffFunc: "exponential",
		},
		Options: []OptionField{
			{Key: "cluster_id", Label: "Cluster ID", Type: "text", Default: "0x0006"},
			{Key: "endpoint", Label: "Endpoint", Type: "number", Default: "1"},
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
		ID:         "aprs",
		Label:      "APRS (Direwolf)",
		IsPaid:     false,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 256, // practical APRS packet limit
		RetryConfig: RetryConfig{
			Enabled:    false,
			MaxRetries: 1,
		},
		Options: []OptionField{
			{Key: "callsign", Label: "Callsign", Type: "text", Default: ""},
			{Key: "ssid", Label: "SSID", Type: "number", Default: "10"},
			{Key: "kiss_host", Label: "Direwolf KISS Host", Type: "text", Default: "localhost"},
			{Key: "kiss_port", Label: "Direwolf KISS Port", Type: "number", Default: "8001"},
			{Key: "frequency_mhz", Label: "Frequency (MHz)", Type: "text", Default: "144.800"},
		},
	})

	r.Register(ChannelDescriptor{
		ID:         "tak",
		Label:      "TAK/CoT",
		IsPaid:     false,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 0, // unlimited (TCP)
		DefaultTTL: 300 * time.Second,
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 5 * time.Second,
			MaxWait:     5 * time.Minute,
			MaxRetries:  5,
			BackoffFunc: "exponential",
		},
		Options: []OptionField{
			{Key: "tak_host", Label: "TAK Server Host", Type: "text", Default: ""},
			{Key: "tak_port", Label: "TAK Server Port", Type: "number", Default: "8087"},
			{Key: "tak_ssl", Label: "Use TLS", Type: "checkbox", Default: "false"},
			{Key: "callsign_prefix", Label: "Callsign Prefix", Type: "text", Default: "MESHSAT"},
		},
	})

	r.Register(ChannelDescriptor{
		ID:         "mqtt",
		Label:      "MQTT Broker",
		IsPaid:     false,
		CanSend:    true,
		CanReceive: true,
		MaxPayload: 0, // unlimited
		DefaultTTL: 300 * time.Second,
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

	r.Register(ChannelDescriptor{
		ID:            "ipougrs",
		Label:         "IPoUGRS (GSM Ring Signal)",
		IsPaid:        false,
		CanSend:       true,
		CanReceive:    true,
		BinaryCapable: true,
		MaxPayload:    1,
		DefaultTTL:    3600 * time.Second,
		RetryConfig: RetryConfig{
			Enabled:     true,
			InitialWait: 10 * time.Second,
			MaxWait:     2 * time.Minute,
			MaxRetries:  20,
			BackoffFunc: "linear",
		},
	})
}
