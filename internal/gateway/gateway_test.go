package gateway

import (
	"encoding/json"
	"testing"

	"meshsat/internal/transport"
)

func TestEncodeDecodeCompact(t *testing.T) {
	msg := &transport.MeshMessage{
		From:        0x12345678,
		To:          0xFFFFFFFF,
		Channel:     0,
		ID:          42,
		PortNum:     1,
		PortNumName: "TEXT_MESSAGE_APP",
		DecodedText: "Hello from mesh",
		RxTime:      1700000000,
		RxSNR:       10.5,
	}

	// Encode with position
	data, err := EncodeCompact(msg, true)
	if err != nil {
		t.Fatalf("EncodeCompact: %v", err)
	}
	if len(data) > maxSBDPayload {
		t.Errorf("encoded too large: %d > %d", len(data), maxSBDPayload)
	}

	// Decode
	inbound, err := DecodeCompact(data)
	if err != nil {
		t.Fatalf("DecodeCompact: %v", err)
	}
	if inbound.Text != "Hello from mesh" {
		t.Errorf("text mismatch: got %q, want %q", inbound.Text, "Hello from mesh")
	}
	if inbound.Source != "iridium" {
		t.Errorf("source: got %q, want %q", inbound.Source, "iridium")
	}
}

func TestEncodeDecodeCompactNoPosition(t *testing.T) {
	msg := &transport.MeshMessage{
		From:        0xAABBCCDD,
		PortNum:     67,
		DecodedText: "Short",
		RxTime:      1700000001,
	}

	data, err := EncodeCompact(msg, false)
	if err != nil {
		t.Fatalf("EncodeCompact: %v", err)
	}

	inbound, err := DecodeCompact(data)
	if err != nil {
		t.Fatalf("DecodeCompact: %v", err)
	}
	if inbound.Text != "Short" {
		t.Errorf("text mismatch: got %q", inbound.Text)
	}
}

func TestEncodeCompactLongText(t *testing.T) {
	// Create a message with text longer than SBD payload
	longText := make([]byte, 500)
	for i := range longText {
		longText[i] = 'A'
	}

	msg := &transport.MeshMessage{
		From:        1,
		PortNum:     1,
		DecodedText: string(longText),
		RxTime:      1700000000,
	}

	data, err := EncodeCompact(msg, false)
	if err != nil {
		t.Fatalf("EncodeCompact: %v", err)
	}
	if len(data) > maxSBDPayload {
		t.Errorf("encoded too large: %d > %d", len(data), maxSBDPayload)
	}
}

func TestDecodeCompactTooShort(t *testing.T) {
	_, err := DecodeCompact([]byte{0x01})
	if err == nil {
		t.Error("expected error for too-short data")
	}
}

func TestMQTTConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     MQTTConfig
		wantErr bool
	}{
		{
			name:    "empty broker URL",
			cfg:     MQTTConfig{},
			wantErr: true,
		},
		{
			name: "invalid scheme",
			cfg: MQTTConfig{
				BrokerURL: "http://localhost:1883",
			},
			wantErr: true,
		},
		{
			name: "valid tcp",
			cfg: MQTTConfig{
				BrokerURL:   "tcp://localhost:1883",
				TopicPrefix: "msh/test",
				ChannelName: "LongFast",
				QoS:         1,
				KeepAlive:   60,
			},
			wantErr: false,
		},
		{
			name: "valid ssl",
			cfg: MQTTConfig{
				BrokerURL:   "ssl://broker.example.com:8883",
				TopicPrefix: "msh/test",
				ChannelName: "LongFast",
				QoS:         0,
				KeepAlive:   30,
			},
			wantErr: false,
		},
		{
			name: "invalid QoS",
			cfg: MQTTConfig{
				BrokerURL: "tcp://localhost:1883",
				QoS:       3,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseMQTTConfig(t *testing.T) {
	input := `{"broker_url":"tcp://test:1883","username":"user","password":"pass","qos":2}`
	cfg, err := ParseMQTTConfig(input)
	if err != nil {
		t.Fatalf("ParseMQTTConfig: %v", err)
	}
	if cfg.BrokerURL != "tcp://test:1883" {
		t.Errorf("broker_url: got %q", cfg.BrokerURL)
	}
	if cfg.Username != "user" {
		t.Errorf("username: got %q", cfg.Username)
	}
	if cfg.QoS != 2 {
		t.Errorf("qos: got %d", cfg.QoS)
	}
	// Defaults should be filled in
	if cfg.TopicPrefix != "msh/cubeos" {
		t.Errorf("topic_prefix default: got %q", cfg.TopicPrefix)
	}
}

func TestMQTTConfigRedacted(t *testing.T) {
	cfg := MQTTConfig{
		BrokerURL: "tcp://test:1883",
		Password:  "secret123",
	}
	redacted := cfg.Redacted()
	if redacted.Password != "****" {
		t.Errorf("password not redacted: %q", redacted.Password)
	}
	// Original should be unchanged
	if cfg.Password != "secret123" {
		t.Error("original password was modified")
	}
}

func TestMQTTConfigTLSValidation(t *testing.T) {
	// cert without key should fail
	cfg := MQTTConfig{
		BrokerURL: "ssl://broker:8883",
		TLSCert:   "/tmp/nonexistent.pem",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when tls_cert set without tls_key")
	}

	// key without cert should fail
	cfg = MQTTConfig{
		BrokerURL: "ssl://broker:8883",
		TLSKey:    "/tmp/nonexistent.pem",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when tls_key set without tls_cert")
	}

	// nonexistent cert file should fail
	cfg = MQTTConfig{
		BrokerURL: "ssl://broker:8883",
		TLSCert:   "/tmp/nonexistent-cert.pem",
		TLSKey:    "/tmp/nonexistent-key.pem",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for nonexistent tls_cert file")
	}

	// nonexistent CA file should fail
	cfg = MQTTConfig{
		BrokerURL: "ssl://broker:8883",
		TLSCA:     "/tmp/nonexistent-ca.pem",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for nonexistent tls_ca file")
	}
}

func TestMQTTConfigBuildTLSConfig(t *testing.T) {
	// No TLS fields → nil config
	cfg := MQTTConfig{}
	tlsCfg, err := cfg.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tlsCfg != nil {
		t.Error("expected nil tls config when no TLS fields set")
	}

	// TLSInsecure only → non-nil config with InsecureSkipVerify
	cfg = MQTTConfig{TLSInsecure: true}
	tlsCfg, err = cfg.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("expected non-nil tls config")
	}
	if !tlsCfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true")
	}
	if tlsCfg.MinVersion != 0x0303 { // tls.VersionTLS12
		t.Errorf("MinVersion: got %#x, want TLS 1.2", tlsCfg.MinVersion)
	}
}

func TestLastMOStatusFailed(t *testing.T) {
	tests := []struct {
		moStatus int
		want     bool
	}{
		{-1, true},  // no SBDIX attempted — treat as failed
		{0, false},  // success
		{1, false},  // success
		{2, false},  // success
		{3, false},  // success
		{4, false},  // success
		{5, true},   // failure
		{17, true},  // gateway not responding
		{32, true},  // no network service
		{35, true},  // ISU busy
		{36, true},  // registration cooldown
		{100, true}, // unknown failure
	}

	for _, tt := range tests {
		got := lastMOStatusFailed(tt.moStatus)
		if got != tt.want {
			t.Errorf("lastMOStatusFailed(%d) = %v, want %v", tt.moStatus, got, tt.want)
		}
	}
}

func TestIridiumConfigParse(t *testing.T) {
	input := `{"forward_all":true,"poll_interval":300}`
	cfg, err := ParseIridiumConfig(input)
	if err != nil {
		t.Fatalf("ParseIridiumConfig: %v", err)
	}
	if !cfg.ForwardAll {
		t.Error("forward_all should be true")
	}
	if cfg.PollInterval != 300 {
		t.Errorf("poll_interval: got %d", cfg.PollInterval)
	}
	// Defaults
	if cfg.Compression != "compact" {
		t.Errorf("compression default: got %q", cfg.Compression)
	}
	if cfg.MaxTextLength != 320 {
		t.Errorf("max_text_length default: got %d", cfg.MaxTextLength)
	}
}

func TestGatewayStatusResponseJSON(t *testing.T) {
	resp := GatewayStatusResponse{
		Type:      "mqtt",
		Enabled:   true,
		Connected: true,
		Config:    json.RawMessage(`{"broker_url":"tcp://test:1883"}`),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 {
		t.Error("empty JSON")
	}
}
