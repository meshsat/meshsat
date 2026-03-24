package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

const (
	mqttDedupTTL     = 5 * time.Minute
	mqttDedupCleanup = time.Minute
)

// MQTTGateway bridges Meshtastic mesh messages to/from an MQTT broker.
type MQTTGateway struct {
	config MQTTConfig
	client mqtt.Client
	inCh   chan InboundMessage

	// dedup tracks packet IDs we published to avoid re-ingesting our own echoes
	dedup   map[uint32]time.Time
	dedupMu sync.Mutex

	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	lastActive atomic.Int64 // unix timestamp
	startTime  time.Time

	cancel context.CancelFunc
	done   chan struct{}
}

// NewMQTTGateway creates a new MQTT gateway with the given config.
func NewMQTTGateway(cfg MQTTConfig) *MQTTGateway {
	return &MQTTGateway{
		config: cfg,
		inCh:   make(chan InboundMessage, 64),
		dedup:  make(map[uint32]time.Time),
		done:   make(chan struct{}),
	}
}

// Start connects to the MQTT broker and subscribes to mesh topics.
func (g *MQTTGateway) Start(ctx context.Context) error {
	if err := g.config.Validate(); err != nil {
		return fmt.Errorf("mqtt config invalid: %w", err)
	}

	opts := mqtt.NewClientOptions().
		AddBroker(g.config.BrokerURL).
		SetClientID(g.config.ClientID).
		SetKeepAlive(time.Duration(g.config.KeepAlive) * time.Second).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(30 * time.Second).
		SetCleanSession(true)

	if g.config.Username != "" {
		opts.SetUsername(g.config.Username)
	}
	if g.config.Password != "" {
		opts.SetPassword(g.config.Password)
	}

	// TLS client certificates and/or CA verification
	if tlsCfg, err := g.config.buildTLSConfig(); err != nil {
		return fmt.Errorf("mqtt tls config: %w", err)
	} else if tlsCfg != nil {
		opts.SetTLSConfig(tlsCfg)
	}

	opts.SetOnConnectHandler(func(_ mqtt.Client) {
		g.connected.Store(true)
		g.lastActive.Store(time.Now().Unix())
		log.Info().Str("broker", g.config.BrokerURL).Msg("mqtt connected")
		g.subscribe()
	})

	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		g.connected.Store(false)
		g.errors.Add(1)
		log.Warn().Err(err).Msg("mqtt connection lost")
	})

	g.client = mqtt.NewClient(opts)
	token := g.client.Connect()
	if !token.WaitTimeout(15 * time.Second) {
		return fmt.Errorf("mqtt connect timeout")
	}
	if token.Error() != nil {
		return fmt.Errorf("mqtt connect: %w", token.Error())
	}

	g.startTime = time.Now()

	ctx, g.cancel = context.WithCancel(ctx)
	go g.dedupCleaner(ctx)

	log.Info().Str("broker", g.config.BrokerURL).Msg("mqtt gateway started")
	return nil
}

// subscribe sets up the MQTT topic subscription with retry on failure.
func (g *MQTTGateway) subscribe() {
	topic := fmt.Sprintf("%s/%s/+", g.config.TopicPrefix, g.config.ChannelName)

	const maxRetries = 5
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := g.client.Subscribe(topic, byte(g.config.QoS), g.onMessage)
		if !token.WaitTimeout(10 * time.Second) {
			log.Error().Str("topic", topic).Int("attempt", attempt).Msg("mqtt subscribe timeout")
			g.errors.Add(1)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			log.Error().Str("topic", topic).Msg("mqtt subscribe failed after all retries — messages will be lost")
			return
		}
		if token.Error() != nil {
			log.Error().Err(token.Error()).Str("topic", topic).Int("attempt", attempt).Msg("mqtt subscribe failed")
			g.errors.Add(1)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			log.Error().Str("topic", topic).Msg("mqtt subscribe failed after all retries — messages will be lost")
			return
		}
		log.Info().Str("topic", topic).Int("attempt", attempt).Msg("mqtt subscribed")
		return
	}
}

// onMessage handles incoming MQTT messages.
func (g *MQTTGateway) onMessage(_ mqtt.Client, msg mqtt.Message) {
	var payload struct {
		From        uint32  `json:"from"`
		To          uint32  `json:"to"`
		Channel     int     `json:"channel"`
		PortNum     int     `json:"portnum"`
		PortNumName string  `json:"portnum_name"`
		Text        string  `json:"text"`
		SNR         float32 `json:"snr"`
		Timestamp   int64   `json:"timestamp"`
		PacketID    uint32  `json:"packet_id"`
	}

	if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
		log.Debug().Err(err).Str("topic", msg.Topic()).Msg("mqtt: ignoring non-JSON message")
		return
	}

	// Skip if this is our own echo
	g.dedupMu.Lock()
	if _, ours := g.dedup[payload.PacketID]; ours {
		g.dedupMu.Unlock()
		return
	}
	g.dedupMu.Unlock()

	// Only bridge text messages
	if payload.Text == "" {
		return
	}

	g.msgsIn.Add(1)
	g.lastActive.Store(time.Now().Unix())

	select {
	case g.inCh <- InboundMessage{
		Text:    payload.Text,
		Channel: payload.Channel,
		Source:  "mqtt",
	}:
	default:
		log.Warn().Msg("mqtt inbound channel full, dropping message")
	}
}

// Stop disconnects from the MQTT broker.
func (g *MQTTGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	if g.client != nil && g.client.IsConnected() {
		g.client.Disconnect(1000)
	}
	g.connected.Store(false)
	log.Info().Msg("mqtt gateway stopped")
	return nil
}

// Forward publishes a mesh message to MQTT.
func (g *MQTTGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	if !g.connected.Load() {
		return nil // silently skip if disconnected
	}

	topic := fmt.Sprintf("%s/%s/%08x", g.config.TopicPrefix, g.config.ChannelName, msg.From)

	payload, err := json.Marshal(map[string]interface{}{
		"from":         msg.From,
		"to":           msg.To,
		"channel":      msg.Channel,
		"portnum":      msg.PortNum,
		"portnum_name": msg.PortNumName,
		"text":         msg.DecodedText,
		"snr":          msg.RxSNR,
		"timestamp":    msg.RxTime,
		"packet_id":    msg.ID,
	})
	if err != nil {
		g.errors.Add(1)
		return fmt.Errorf("marshal mqtt payload: %w", err)
	}

	// Record packet ID in dedup map before publishing
	g.dedupMu.Lock()
	g.dedup[msg.ID] = time.Now()
	g.dedupMu.Unlock()

	token := g.client.Publish(topic, byte(g.config.QoS), false, payload)
	if !token.WaitTimeout(5 * time.Second) {
		g.errors.Add(1)
		return fmt.Errorf("mqtt publish timeout")
	}
	if token.Error() != nil {
		g.errors.Add(1)
		return fmt.Errorf("mqtt publish: %w", token.Error())
	}

	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	return nil
}

// Enqueue submits a message for outbound delivery via the gateway.
func (g *MQTTGateway) Enqueue(msg *transport.MeshMessage) error {
	return g.Forward(context.Background(), msg)
}

// Receive returns the inbound message channel.
func (g *MQTTGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *MQTTGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "mqtt",
		Connected:   g.connected.Load(),
		MessagesIn:  g.msgsIn.Load(),
		MessagesOut: g.msgsOut.Load(),
		Errors:      g.errors.Load(),
	}
	if ts := g.lastActive.Load(); ts > 0 {
		s.LastActivity = time.Unix(ts, 0)
	}
	if s.Connected && !g.startTime.IsZero() {
		s.ConnectionUptime = time.Since(g.startTime).Truncate(time.Second).String()
	}
	return s
}

// Type returns the gateway type identifier.
func (g *MQTTGateway) Type() string {
	return "mqtt"
}

// dedupCleaner periodically removes expired entries from the dedup map.
func (g *MQTTGateway) dedupCleaner(ctx context.Context) {
	ticker := time.NewTicker(mqttDedupCleanup)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			g.dedupMu.Lock()
			for id, t := range g.dedup {
				if now.Sub(t) > mqttDedupTTL {
					delete(g.dedup, id)
				}
			}
			g.dedupMu.Unlock()
		}
	}
}

// PublishRaw publishes a raw payload to an arbitrary MQTT topic.
// Used by the hub namespace (e.g., mo/raw, mo/decoded) which differs from
// the bridge's Forward() method that uses the msh/ topic prefix.
func (g *MQTTGateway) PublishRaw(topic string, qos byte, retain bool, payload []byte) error {
	if !g.connected.Load() {
		return fmt.Errorf("mqtt not connected")
	}

	token := g.client.Publish(topic, qos, retain, payload)
	if !token.WaitTimeout(5 * time.Second) {
		g.errors.Add(1)
		return fmt.Errorf("mqtt publish timeout")
	}
	if token.Error() != nil {
		g.errors.Add(1)
		return fmt.Errorf("mqtt publish: %w", token.Error())
	}

	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	return nil
}

// TestConnection attempts a temporary connection to validate config.
func (g *MQTTGateway) TestConnection() error {
	opts := mqtt.NewClientOptions().
		AddBroker(g.config.BrokerURL).
		SetClientID(g.config.ClientID + "-test").
		SetAutoReconnect(false)

	if g.config.Username != "" {
		opts.SetUsername(g.config.Username)
	}
	if g.config.Password != "" {
		opts.SetPassword(g.config.Password)
	}

	if tlsCfg, err := g.config.buildTLSConfig(); err != nil {
		return fmt.Errorf("mqtt tls config: %w", err)
	} else if tlsCfg != nil {
		opts.SetTLSConfig(tlsCfg)
	}

	c := mqtt.NewClient(opts)
	token := c.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("connection timeout")
	}
	if token.Error() != nil {
		return token.Error()
	}
	c.Disconnect(500)
	return nil
}
