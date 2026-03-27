// Package hubreporter connects the bridge to the Hub MQTT broker for
// lifecycle management (birth/death/health) and device telemetry uplinking.
package hubreporter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"
)

// ReporterConfig holds the connection parameters for the Hub MQTT broker.
type ReporterConfig struct {
	HubURL         string
	BridgeID       string
	Username       string
	Password       string
	TLSCert        string
	TLSKey         string
	TLSCA          string // path to CA certificate for server verification
	TLSInsecure    bool   // skip server certificate verification (dev only)
	HealthInterval time.Duration
}

// Validate checks that the minimum required config is present.
func (c ReporterConfig) Validate() error {
	if c.HubURL == "" {
		return fmt.Errorf("hub URL is required")
	}
	if c.BridgeID == "" {
		return fmt.Errorf("bridge ID is required")
	}
	if c.HealthInterval <= 0 {
		c.HealthInterval = 30 * time.Second
	}
	return nil
}

// HubReporter manages the MQTT connection to the Hub and publishes
// bridge lifecycle events, device births/deaths, and telemetry.
type HubReporter struct {
	cfg        ReporterConfig
	client     mqtt.Client
	birthData  func() BridgeBirth
	healthData func() BridgeHealth
	mu         sync.Mutex
	connected  bool
	stopCh     chan struct{}
	stopped    bool
	cmdHandler *CommandHandler
	outbox     *Outbox
}

// NewHubReporter creates a new HubReporter. It does not connect until Start is called.
// birthFn is called on connect to collect the birth certificate data.
// healthFn is called periodically to collect health metrics.
func NewHubReporter(cfg ReporterConfig, birthFn func() BridgeBirth, healthFn func() BridgeHealth) *HubReporter {
	return &HubReporter{
		cfg:        cfg,
		birthData:  birthFn,
		healthData: healthFn,
		stopCh:     make(chan struct{}),
	}
}

// Start connects to the Hub MQTT broker, publishes the birth certificate,
// subscribes to the command topic, and starts the health ticker.
func (r *HubReporter) Start(ctx context.Context) error {
	if err := r.cfg.Validate(); err != nil {
		return fmt.Errorf("hubreporter config: %w", err)
	}

	opts := mqtt.NewClientOptions().
		AddBroker(r.cfg.HubURL).
		SetClientID(fmt.Sprintf("meshsat-bridge-%s", r.cfg.BridgeID)).
		SetKeepAlive(60 * time.Second).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(30 * time.Second).
		SetCleanSession(true)

	if r.cfg.Username != "" {
		opts.SetUsername(r.cfg.Username)
	}
	if r.cfg.Password != "" {
		opts.SetPassword(r.cfg.Password)
	}

	// TLS configuration — needed for ssl://, wss://, or explicit mTLS
	if tlsCfg := r.buildTLSConfig(); tlsCfg != nil {
		opts.SetTLSConfig(tlsCfg)
	}

	// LWT: publish death with reason "lwt" on unexpected disconnect
	lwtDeath := BridgeDeath{
		Protocol:  ProtocolVersion,
		BridgeID:  r.cfg.BridgeID,
		Reason:    "lwt",
		Timestamp: time.Now().UTC(),
	}
	lwtPayload, _ := json.Marshal(lwtDeath)
	opts.SetWill(TopicBridgeDeath(r.cfg.BridgeID), string(lwtPayload), 1, false)

	opts.SetOnConnectHandler(func(_ mqtt.Client) {
		r.mu.Lock()
		r.connected = true
		ob := r.outbox
		r.mu.Unlock()
		log.Info().Str("hub", r.cfg.HubURL).Str("bridge_id", r.cfg.BridgeID).Msg("hubreporter connected to hub")

		// Publish birth certificate on every (re)connect (never queued)
		r.publishBirth()

		// Subscribe to command topic
		r.subscribeCmd()

		// Replay queued outbox messages
		if ob != nil {
			go func() {
				n, err := ob.Replay(context.Background(), func(topic string, payload []byte, qos byte) error {
					token := r.client.Publish(topic, qos, false, payload)
					if !token.WaitTimeout(5 * time.Second) {
						return fmt.Errorf("replay publish timeout on %s", topic)
					}
					return token.Error()
				})
				if err != nil {
					log.Warn().Err(err).Int("replayed", n).Msg("hubreporter: outbox replay error")
				} else if n > 0 {
					log.Info().Int("replayed", n).Msg("hubreporter: outbox replay complete")
				}
				if cleanErr := ob.Cleanup(); cleanErr != nil {
					log.Warn().Err(cleanErr).Msg("hubreporter: outbox cleanup error")
				}
			}()
		}
	})

	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		r.mu.Lock()
		r.connected = false
		r.mu.Unlock()
		log.Warn().Err(err).Msg("hubreporter connection lost")
	})

	r.client = mqtt.NewClient(opts)
	token := r.client.Connect()
	if !token.WaitTimeout(15 * time.Second) {
		return fmt.Errorf("hubreporter connect timeout")
	}
	if token.Error() != nil {
		return fmt.Errorf("hubreporter connect: %w", token.Error())
	}

	// Start health ticker
	go r.healthLoop(ctx)

	log.Info().Str("hub", r.cfg.HubURL).Str("bridge_id", r.cfg.BridgeID).Msg("hubreporter started")
	return nil
}

// buildTLSConfig returns a *tls.Config when TLS is needed (ssl://, wss://, mTLS,
// custom CA, or insecure-skip). Returns nil when no TLS settings apply.
func (r *HubReporter) buildTLSConfig() *tls.Config {
	scheme := strings.SplitN(r.cfg.HubURL, "://", 2)[0]
	needsTLS := scheme == "ssl" || scheme == "tls" || scheme == "wss"
	hasCert := r.cfg.TLSCert != "" && r.cfg.TLSKey != ""
	hasCA := r.cfg.TLSCA != ""

	if !needsTLS && !hasCert && !hasCA && !r.cfg.TLSInsecure {
		return nil
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if hasCert {
		cert, err := tls.LoadX509KeyPair(r.cfg.TLSCert, r.cfg.TLSKey)
		if err != nil {
			log.Error().Err(err).Msg("hubreporter: failed to load TLS client certificate")
		} else {
			cfg.Certificates = []tls.Certificate{cert}
		}
	}

	if hasCA {
		caCert, err := os.ReadFile(r.cfg.TLSCA)
		if err != nil {
			log.Error().Err(err).Str("ca", r.cfg.TLSCA).Msg("hubreporter: failed to read CA certificate")
		} else {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(caCert) {
				cfg.RootCAs = pool
			} else {
				log.Warn().Str("ca", r.cfg.TLSCA).Msg("hubreporter: CA file contains no valid certificates")
			}
		}
	}

	if r.cfg.TLSInsecure {
		cfg.InsecureSkipVerify = true //nolint:gosec // user-configured for dev/testing
	}

	return cfg
}

// Stop publishes a graceful death message and disconnects from the broker.
func (r *HubReporter) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.mu.Unlock()

	close(r.stopCh)

	// Publish graceful death
	if r.client != nil && r.client.IsConnected() {
		death := BridgeDeath{
			Protocol:  ProtocolVersion,
			BridgeID:  r.cfg.BridgeID,
			Reason:    "shutdown",
			Timestamp: time.Now().UTC(),
		}
		payload, err := json.Marshal(death)
		if err == nil {
			token := r.client.Publish(TopicBridgeDeath(r.cfg.BridgeID), 1, false, payload)
			token.WaitTimeout(500 * time.Millisecond)
		}

		r.client.Disconnect(500)
	}

	r.mu.Lock()
	r.connected = false
	r.mu.Unlock()

	log.Info().Msg("hubreporter stopped")
}

// IsConnected returns whether the MQTT client is currently connected.
func (r *HubReporter) IsConnected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected
}

// PublishDeviceBirth publishes a device birth certificate to the Hub.
func (r *HubReporter) PublishDeviceBirth(device DeviceBirth) error {
	device.Protocol = ProtocolVersion
	device.BridgeID = r.cfg.BridgeID
	return r.publishOrQueue(TopicDeviceBirth(r.cfg.BridgeID, device.DeviceID), 1, false, device)
}

// PublishDeviceDeath publishes a device death notice to the Hub.
func (r *HubReporter) PublishDeviceDeath(device DeviceDeath) error {
	device.Protocol = ProtocolVersion
	device.BridgeID = r.cfg.BridgeID
	return r.publishOrQueue(TopicDeviceDeath(r.cfg.BridgeID, device.DeviceID), 1, false, device)
}

// PublishDevicePosition publishes a device position update to the Hub.
func (r *HubReporter) PublishDevicePosition(deviceID string, pos DevicePosition) error {
	pos.BridgeID = r.cfg.BridgeID
	return r.publishOrQueue(TopicDevicePosition(deviceID), 0, false, pos)
}

// PublishDeviceTelemetry publishes device telemetry to the Hub.
func (r *HubReporter) PublishDeviceTelemetry(deviceID string, tel DeviceTelemetry) error {
	tel.BridgeID = r.cfg.BridgeID
	return r.publishOrQueue(TopicDeviceTelemetry(deviceID), 0, false, tel)
}

// PublishDeviceSOS publishes a device SOS event to the Hub.
func (r *HubReporter) PublishDeviceSOS(sos DeviceSOS) error {
	sos.BridgeID = r.cfg.BridgeID
	return r.publishOrQueue(TopicDeviceSOS(sos.DeviceID), 1, false, sos)
}

// publishBirth collects and publishes the bridge birth certificate.
func (r *HubReporter) publishBirth() {
	birth := r.birthData()
	birth.Protocol = ProtocolVersion
	birth.BridgeID = r.cfg.BridgeID
	birth.Timestamp = time.Now().UTC()

	if err := r.publish(TopicBridgeBirth(r.cfg.BridgeID), 1, true, birth); err != nil {
		log.Error().Err(err).Msg("hubreporter: failed to publish birth")
	} else {
		log.Info().Str("bridge_id", r.cfg.BridgeID).Msg("hubreporter: birth certificate published")
	}
}

// subscribeCmd subscribes to the command topic for this bridge.
func (r *HubReporter) subscribeCmd() {
	topic := TopicBridgeCmd(r.cfg.BridgeID)
	token := r.client.Subscribe(topic, 1, r.onCommand)
	if !token.WaitTimeout(10 * time.Second) {
		log.Error().Str("topic", topic).Msg("hubreporter: cmd subscribe timeout")
		return
	}
	if token.Error() != nil {
		log.Error().Err(token.Error()).Str("topic", topic).Msg("hubreporter: cmd subscribe failed")
		return
	}
	log.Info().Str("topic", topic).Msg("hubreporter: subscribed to commands")
}

// SetOutbox sets the offline message queue for store-and-forward.
// When set, hub-bound messages are queued locally if the broker is unreachable
// and replayed in FIFO order on reconnect.
func (r *HubReporter) SetOutbox(outbox *Outbox) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outbox = outbox
}

// SetCommandHandler sets the command handler that processes incoming Hub commands.
func (r *HubReporter) SetCommandHandler(handler *CommandHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmdHandler = handler
}

// onCommand handles incoming commands from the Hub.
func (r *HubReporter) onCommand(_ mqtt.Client, msg mqtt.Message) {
	r.mu.Lock()
	handler := r.cmdHandler
	r.mu.Unlock()

	if handler != nil {
		handler.HandleCommand(msg.Payload())
		return
	}

	// Fallback: log only if no handler is set.
	var cmd Command
	if err := json.Unmarshal(msg.Payload(), &cmd); err != nil {
		log.Warn().Err(err).Str("topic", msg.Topic()).Msg("hubreporter: invalid command JSON")
		return
	}
	log.Info().
		Str("cmd", cmd.Cmd).
		Str("request_id", cmd.RequestID).
		Str("target", cmd.TargetDevice).
		Msg("hubreporter: received command (no handler set)")
}

// healthLoop periodically publishes health metrics to the Hub and runs
// outbox cleanup every hour.
func (r *HubReporter) healthLoop(ctx context.Context) {
	interval := r.cfg.HealthInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-cleanupTicker.C:
			r.mu.Lock()
			ob := r.outbox
			r.mu.Unlock()
			if ob != nil {
				if err := ob.Cleanup(); err != nil {
					log.Warn().Err(err).Msg("hubreporter: periodic outbox cleanup error")
				}
			}
		case <-ticker.C:
			if !r.IsConnected() {
				continue
			}
			health := r.healthData()
			health.Protocol = ProtocolVersion
			health.BridgeID = r.cfg.BridgeID
			health.Timestamp = time.Now().UTC()

			if err := r.publish(TopicBridgeHealth(r.cfg.BridgeID), 0, false, health); err != nil {
				log.Debug().Err(err).Msg("hubreporter: health publish failed (will retry)")
			}
		}
	}
}

// publishOrQueue attempts to publish directly if connected, otherwise queues
// to the outbox for later replay. If no outbox is set, drops silently (legacy behavior).
func (r *HubReporter) publishOrQueue(topic string, qos byte, retained bool, v interface{}) error {
	if r.IsConnected() {
		return r.publish(topic, qos, retained, v)
	}
	r.mu.Lock()
	ob := r.outbox
	r.mu.Unlock()
	if ob == nil {
		return nil
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("outbox marshal: %w", err)
	}
	return ob.Enqueue(topic, payload, qos)
}

// publish marshals a value to JSON and publishes it to the given MQTT topic.
func (r *HubReporter) publish(topic string, qos byte, retained bool, v interface{}) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	token := r.client.Publish(topic, qos, retained, payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("publish timeout on %s", topic)
	}
	if token.Error() != nil {
		return fmt.Errorf("publish %s: %w", topic, token.Error())
	}
	return nil
}
