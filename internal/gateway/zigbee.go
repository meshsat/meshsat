package gateway

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// ZigBeeGateway bridges Meshtastic mesh messages to/from a ZigBee 3.0 coordinator.
type ZigBeeGateway struct {
	config    ZigBeeConfig
	transport *transport.DirectZigBeeTransport
	store     transport.ZigBeeStore // optional persistent backend, wired before Start
	inCh      chan InboundMessage
	outCh     chan *transport.MeshMessage

	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	lastActive atomic.Int64
	startTime  time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Sensor fan-out — looked up per event. Nil-safe: when targets are
	// unwired (e.g. TAK gateway disabled) the corresponding hop is skipped
	// silently. [MESHSAT-509]
	sensorRouter zigbeeSensorRouter
}

// zigbeeSensorRouter is the dependency-injected target set for sensor
// events. main.go builds one with concrete handles to TAK, the hub reporter,
// and a GPS provider; tests can build a no-op variant.
type zigbeeSensorRouter struct {
	// db is the routing-config lookup (per-device to_tak/to_mesh/...).
	db zigbeeRoutingStore
	// takSend forwards a fully-built CoT event to the TAK server. May be nil.
	takSend func(ev CotEvent) error
	// hubPublish publishes a sensor telemetry payload to the hub. May be nil.
	hubPublish func(ieeeAddr string, kind string, value float64, unit string) error
	// gps returns the bridge's current position (lat, lon, ok). May be nil.
	gps func() (float64, float64, bool)
	// callsignPrefix is prepended to the synthesized CoT callsign.
	callsignPrefix string
	// staleSec controls the CoT marker lifetime.
	staleSec int
	// minIntervalDefault is the per-device rate-limit fallback (seconds).
	minIntervalDefault int
}

// zigbeeRoutingStore is the subset of *database.DB the router needs. Kept
// as an interface so the gateway package doesn't take a hard import on
// the database package (it already does for the transport store, but this
// keeps the seam clean for future tests).
type zigbeeRoutingStore interface {
	GetZigBeeRouting(ieeeAddr string) (*databaseRouting, error)
}

type databaseRouting = databasePackageRouting // alias declared in zigbee_router_aliases.go

// SetStore wires the persistence backend (typically *database.DB). Must be
// called before Start() — the gateway forwards it to the transport just
// after construction so device hydration runs at coordinator startup.
func (g *ZigBeeGateway) SetStore(s transport.ZigBeeStore) {
	g.store = s
}

// SensorRoutingDeps is the dependency bundle main.go provides so the zigbee
// gateway can fan out sensor readings to TAK / hub / log according to the
// per-device routing config. Any field may be nil — the corresponding hop
// is skipped silently when its handle isn't wired.
type SensorRoutingDeps struct {
	DB                 *database.DB
	TAK                *TAKGateway
	HubPublish         func(ieeeAddr string, kind string, value float64, unit string) error
	GPS                func() (float64, float64, bool)
	CallsignPrefix     string
	CoTStaleSec        int
	MinIntervalDefault int
}

// SetSensorRouter wires the sensor fan-out targets. Safe to call before or
// after Start() — the receiveWorker reads g.sensorRouter under no lock,
// but the SensorRoutingDeps struct is small (only function pointers and a
// couple of ints) and Go's atomicity for word-sized writes is sufficient
// for our "best-effort delivery" semantics here.
func (g *ZigBeeGateway) SetSensorRouter(deps SensorRoutingDeps) {
	r := zigbeeSensorRouter{
		callsignPrefix:     deps.CallsignPrefix,
		staleSec:           deps.CoTStaleSec,
		minIntervalDefault: deps.MinIntervalDefault,
		gps:                deps.GPS,
		hubPublish:         deps.HubPublish,
	}
	if deps.DB != nil {
		r.db = deps.DB
	}
	if deps.TAK != nil {
		r.takSend = deps.TAK.SendCotEvent
	}
	if r.staleSec <= 0 {
		r.staleSec = 600 // 10 min default — sensors usually report every 1-5 min
	}
	if r.callsignPrefix == "" {
		r.callsignPrefix = "MESHSAT"
	}
	g.sensorRouter = r
}

// Transport exposes the underlying transport for handlers that need to
// drive ZNP-level operations (alias updates, OnOff commands, etc).
// Returns nil before Start has succeeded.
func (g *ZigBeeGateway) Transport() *transport.DirectZigBeeTransport {
	return g.transport
}

// NewZigBeeGateway creates a new ZigBee gateway.
func NewZigBeeGateway(cfg ZigBeeConfig) *ZigBeeGateway {
	return &ZigBeeGateway{
		config: cfg,
		inCh:   make(chan InboundMessage, 64),
		outCh:  make(chan *transport.MeshMessage, 10),
	}
}

// Start initializes the Z-Stack coordinator and starts message workers.
func (g *ZigBeeGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()

	// Resolve serial port
	portName := g.config.SerialPort
	if portName == "" || portName == "auto" {
		portName = transport.FindZigBeePort()
		if portName == "" {
			return fmt.Errorf("zigbee: no coordinator found (auto-detect failed)")
		}
	}

	// Initialize transport — wire the store BEFORE Start so the device
	// cache hydrates from DB during coordinator init [MESHSAT-509].
	g.transport = transport.NewDirectZigBeeTransport()
	if g.store != nil {
		g.transport.SetStore(g.store)
	}
	if err := g.transport.Start(ctx, portName); err != nil {
		return fmt.Errorf("zigbee transport: %w", err)
	}

	g.connected.Store(true)

	// Start workers
	g.wg.Add(2)
	go g.receiveWorker(ctx)
	go g.sendWorker(ctx)

	log.Info().Str("port", portName).Msg("zigbee gateway started")
	return nil
}

// Stop shuts down the gateway and transport.
func (g *ZigBeeGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	if g.transport != nil {
		g.transport.Stop()
	}
	g.connected.Store(false)
	log.Info().Msg("zigbee gateway stopped")
	return nil
}

// Forward enqueues a message for ZigBee transmission.
func (g *ZigBeeGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	select {
	case g.outCh <- msg:
		return nil
	default:
		g.errors.Add(1)
		return fmt.Errorf("zigbee outbound queue full")
	}
}

// Enqueue submits a message for outbound delivery via the gateway.
func (g *ZigBeeGateway) Enqueue(msg *transport.MeshMessage) error {
	return g.Forward(context.Background(), msg)
}

// Receive returns the inbound message channel.
func (g *ZigBeeGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *ZigBeeGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "zigbee",
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
func (g *ZigBeeGateway) Type() string {
	return "zigbee"
}

// GetTransport returns the underlying ZigBee transport (for device list, etc.).
func (g *ZigBeeGateway) GetTransport() *transport.DirectZigBeeTransport {
	return g.transport
}

// receiveWorker subscribes to ZigBee events and converts them to InboundMessages.
func (g *ZigBeeGateway) receiveWorker(ctx context.Context) {
	defer g.wg.Done()

	events := g.transport.Subscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-events:
			if !ok {
				return
			}

			switch evt.Type {
			case "data":
				// Convert ZigBee data payload to text for mesh injection.
				// Binary data is hex-encoded; printable ASCII is passed as-is.
				text := formatZigBeeData(evt)
				if text == "" {
					continue
				}

				g.msgsIn.Add(1)
				g.lastActive.Store(time.Now().Unix())

				select {
				case g.inCh <- InboundMessage{
					Text:    text,
					To:      g.config.InboundDest,
					Channel: g.config.InboundChannel,
					Source:  "zigbee",
				}:
				default:
					log.Warn().Msg("zigbee: inbound channel full, dropping message")
				}

			case "temperature", "humidity", "battery", "onoff":
				// Sensor reading — fan out per device routing config.
				g.msgsIn.Add(1)
				g.lastActive.Store(time.Now().Unix())
				g.routeSensorEvent(evt)

			case "join":
				log.Info().
					Uint16("addr", evt.Device.ShortAddr).
					Str("ieee", evt.Device.IEEEAddr).
					Msg("zigbee: device joined network")

			case "leave":
				log.Info().
					Uint16("addr", evt.Device.ShortAddr).
					Msg("zigbee: device left network")
			}
		}
	}
}

// sendWorker dequeues mesh messages and sends them to ZigBee devices.
func (g *ZigBeeGateway) sendWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			data := []byte(msg.DecodedText)
			if len(data) == 0 {
				continue
			}

			// Truncate to ZigBee payload limit (~100 bytes after APS/NWK headers)
			if len(data) > 100 {
				data = data[:100]
			}

			dstAddr := g.config.DefaultDstAddr
			dstEP := g.config.DefaultDstEP
			cluster := g.config.DefaultCluster

			if err := g.transport.Send(dstAddr, dstEP, cluster, data); err != nil {
				log.Error().Err(err).Uint16("dst", dstAddr).Msg("zigbee: send failed")
				g.errors.Add(1)
				continue
			}

			g.msgsOut.Add(1)
			g.lastActive.Store(time.Now().Unix())
			log.Debug().Uint16("dst", dstAddr).Int("len", len(data)).Msg("zigbee: data sent")
		}
	}
}

// formatZigBeeData converts a ZigBee data event into a human-readable string.
func formatZigBeeData(evt transport.ZigBeeEvent) string {
	if len(evt.Data) == 0 {
		return ""
	}

	// Check if data is printable ASCII
	printable := true
	for _, b := range evt.Data {
		if b < 0x20 || b > 0x7E {
			printable = false
			break
		}
	}

	if printable {
		return fmt.Sprintf("[ZB:%04x] %s", evt.Device.ShortAddr, string(evt.Data))
	}

	// Hex-encode binary data
	return fmt.Sprintf("[ZB:%04x] hex:%x", evt.Device.ShortAddr, evt.Data)
}
