package routing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// MQTTInterfaceConfig configures an MQTT Reticulum interface.
type MQTTInterfaceConfig struct {
	Name      string // e.g. "mqtt_0"
	BrokerURL string // e.g. "tcp://broker:1883" or "wss://mqtt-hub.meshsat.net/mqtt"
	ClientID  string // MQTT client ID (must be unique)
	// Topic is the shared MQTT topic for Reticulum packets.
	// Both publish and subscribe use the same topic (broadcast pattern).
	// Must match the Hub's ReticulumMQTTTopic (default: "meshsat/reticulum/packet").
	Topic    string // e.g. "meshsat/reticulum/packet"
	Username string
	Password string
	QoS      byte // 0, 1, or 2

	// TLS/mTLS fields — inline PEM from Hub connection config.
	// Required when connecting to Hub's NATS broker via wss:// or ssl://.
	TLSCertPEM  []byte // client certificate PEM (mTLS)
	TLSKeyPEM   []byte // client private key PEM (mTLS)
	TLSCAPEM    []byte // CA certificate PEM (server verification)
	TLSInsecure bool   // skip server cert verification (dev only)
}

// MQTTInterface is a bidirectional Reticulum interface over MQTT.
// Reticulum packets are published as raw binary payloads on dedicated topics,
// separate from the Meshtastic mesh bridging topics.
type MQTTInterface struct {
	config   MQTTInterfaceConfig
	callback func(packet []byte)
	client   mqtt.Client

	mu      sync.Mutex
	online  bool
	stopCh  chan struct{}
	stopped bool
}

// NewMQTTInterface creates a new MQTT Reticulum interface.
func NewMQTTInterface(config MQTTInterfaceConfig, callback func(packet []byte)) *MQTTInterface {
	if config.QoS > 2 {
		config.QoS = 1
	}
	if config.Topic == "" {
		config.Topic = "meshsat/reticulum/packet"
	}
	return &MQTTInterface{
		config:   config,
		callback: callback,
		stopCh:   make(chan struct{}),
	}
}

// buildTLSConfig creates a *tls.Config from inline PEM fields.
// Returns nil if no TLS is needed.
func (m *MQTTInterface) buildTLSConfig() *tls.Config {
	scheme := strings.SplitN(m.config.BrokerURL, "://", 2)[0]
	needsTLS := scheme == "ssl" || scheme == "tls" || scheme == "wss"
	hasCert := len(m.config.TLSCertPEM) > 0 && len(m.config.TLSKeyPEM) > 0
	hasCA := len(m.config.TLSCAPEM) > 0

	if !needsTLS && !hasCert && !hasCA && !m.config.TLSInsecure {
		return nil
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if hasCert {
		cert, err := tls.X509KeyPair(m.config.TLSCertPEM, m.config.TLSKeyPEM)
		if err != nil {
			log.Error().Err(err).Str("iface", m.config.Name).Msg("mqtt rns: failed to parse mTLS client certificate")
		} else {
			cfg.Certificates = []tls.Certificate{cert}
			log.Info().Str("iface", m.config.Name).Msg("mqtt rns: mTLS client certificate loaded")
		}
	}

	if hasCA {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(m.config.TLSCAPEM) {
			cfg.RootCAs = pool
		} else {
			log.Warn().Str("iface", m.config.Name).Msg("mqtt rns: CA PEM contains no valid certificates")
		}
	}

	if m.config.TLSInsecure {
		cfg.InsecureSkipVerify = true
	}

	return cfg
}

// Start connects to the MQTT broker and subscribes to the receive topic.
func (m *MQTTInterface) Start(ctx context.Context) error {
	opts := mqtt.NewClientOptions().
		AddBroker(m.config.BrokerURL).
		SetClientID(m.config.ClientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(10 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetCleanSession(false)

	if m.config.Username != "" {
		opts.SetUsername(m.config.Username)
		opts.SetPassword(m.config.Password)
	}

	if tlsCfg := m.buildTLSConfig(); tlsCfg != nil {
		opts.SetTLSConfig(tlsCfg)
	}

	opts.SetOnConnectHandler(func(_ mqtt.Client) {
		m.mu.Lock()
		m.online = true
		m.mu.Unlock()
		log.Info().Str("iface", m.config.Name).Str("broker", m.config.BrokerURL).
			Msg("mqtt reticulum interface connected")
		m.subscribe()
	})

	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		m.mu.Lock()
		m.online = false
		m.mu.Unlock()
		log.Warn().Err(err).Str("iface", m.config.Name).Msg("mqtt reticulum interface disconnected")
	})

	m.client = mqtt.NewClient(opts)
	token := m.client.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("mqtt connect timeout")
	}
	if token.Error() != nil {
		return fmt.Errorf("mqtt connect: %w", token.Error())
	}

	log.Info().Str("iface", m.config.Name).Str("broker", m.config.BrokerURL).
		Str("topic", m.config.Topic).Msg("mqtt reticulum interface started")
	return nil
}

// Send publishes a Reticulum packet to the MQTT transmit topic.
func (m *MQTTInterface) Send(ctx context.Context, packet []byte) error {
	m.mu.Lock()
	online := m.online
	m.mu.Unlock()

	if !online {
		return fmt.Errorf("mqtt interface %s is offline", m.config.Name)
	}

	topic := m.config.Topic
	token := m.client.Publish(topic, m.config.QoS, false, packet)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt publish timeout")
	}
	if token.Error() != nil {
		return fmt.Errorf("mqtt publish: %w", token.Error())
	}

	log.Debug().Str("iface", m.config.Name).Int("size", len(packet)).
		Str("topic", topic).Msg("mqtt iface: packet sent")
	return nil
}

// Stop disconnects from the broker.
func (m *MQTTInterface) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return
	}
	m.stopped = true
	m.online = false
	if m.client != nil {
		m.client.Disconnect(1000)
	}
	close(m.stopCh)
	log.Info().Str("iface", m.config.Name).Msg("mqtt reticulum interface stopped")
}

// IsOnline returns whether the MQTT broker is connected.
func (m *MQTTInterface) IsOnline() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.online
}

// subscribe to the receive topic for inbound Reticulum packets.
func (m *MQTTInterface) subscribe() {
	topic := m.config.Topic
	handler := func(_ mqtt.Client, msg mqtt.Message) {
		data := msg.Payload()
		if len(data) < 2 {
			return // too short for Reticulum packet
		}
		log.Debug().Str("iface", m.config.Name).Int("size", len(data)).
			Str("topic", msg.Topic()).Msg("mqtt iface: received Reticulum packet")
		m.callback(data)
	}

	const maxRetries = 5
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := m.client.Subscribe(topic, m.config.QoS, handler)
		if token.WaitTimeout(5*time.Second) && token.Error() == nil {
			log.Info().Str("iface", m.config.Name).Str("topic", topic).
				Int("attempt", attempt).Msg("mqtt iface: subscribed")
			return
		}
		log.Warn().Str("iface", m.config.Name).Str("topic", topic).
			Int("attempt", attempt).Msg("mqtt iface: subscribe failed, retrying")
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	log.Error().Str("iface", m.config.Name).Str("topic", topic).
		Msg("mqtt iface: subscribe failed after all retries — packets will be lost")
}

// Ensure MQTTInterface satisfies the needed usage pattern (not a formal interface,
// but used via NewReticulumInterface wrapping).
var _ interface {
	Send(ctx context.Context, packet []byte) error
	IsOnline() bool
} = (*MQTTInterface)(nil)

// RegisterMQTTInterface is a convenience to create the ReticulumInterface wrapper,
// register send, and return the wrapped interface for lifecycle management.
func RegisterMQTTInterface(config MQTTInterfaceConfig, callback func(packet []byte)) (*MQTTInterface, *ReticulumInterface) {
	mqttIface := NewMQTTInterface(config, callback)
	ri := NewReticulumInterface(
		config.Name,
		reticulum.IfaceMQTT,
		65535, // MQTT has no practical MTU limit
		mqttIface.Send,
	)
	return mqttIface, ri
}
