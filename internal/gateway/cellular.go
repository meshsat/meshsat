package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// CellularGateway bridges Meshtastic mesh messages to/from a cellular modem (SMS + webhooks).
type CellularGateway struct {
	config CellularConfig
	cell   transport.CellTransport
	db     *database.DB
	inCh   chan InboundMessage
	outCh  chan *transport.MeshMessage

	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	lastActive atomic.Int64
	startTime  time.Time

	// DynDNS updater
	dyndns *DynDNSUpdater

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	emitEvent EventEmitFunc
}

// SetEventEmitter sets the SSE event emitter callback.
func (g *CellularGateway) SetEventEmitter(fn EventEmitFunc) {
	g.emitEvent = fn
}

func (g *CellularGateway) emit(eventType, message string) {
	if g.emitEvent != nil {
		g.emitEvent(eventType, message)
	}
}

// NewCellularGateway creates a new cellular gateway.
func NewCellularGateway(cfg CellularConfig, cell transport.CellTransport, db *database.DB) *CellularGateway {
	return &CellularGateway{
		config: cfg,
		cell:   cell,
		db:     db,
		inCh:   make(chan InboundMessage, 32),
		outCh:  make(chan *transport.MeshMessage, 10),
	}
}

// Start subscribes to cellular events and starts workers.
func (g *CellularGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()

	// Check modem status
	status, err := g.cell.GetStatus(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("cellular: could not get modem status")
	} else {
		g.connected.Store(status.Connected)
	}

	// Configure multi-APN failover and auto-reconnect (direct mode only)
	if dt, ok := g.cell.(*transport.DirectCellTransport); ok {
		if len(g.config.APNFailoverList) > 0 {
			dt.SetAPNList(g.config.APNFailoverList)
		}
		if g.config.AutoReconnect {
			dt.SetDataAutoReconnect(true)
		}
	}

	// Auto-connect LTE data if configured
	if g.config.AutoConnectData && g.config.APN != "" {
		if err := g.cell.ConnectData(ctx, g.config.APN); err != nil {
			log.Warn().Err(err).Str("apn", g.config.APN).Msg("cellular: auto-connect data failed")
		}
	}

	// Start outbound SMS worker
	g.wg.Add(1)
	go g.sendWorker(ctx)

	// Start SMS listener (subscribes to cell events)
	g.wg.Add(1)
	go g.smsListener(ctx)

	// Start DynDNS updater if configured
	if g.config.DynDNS.Enabled {
		g.dyndns = NewDynDNSUpdater(g.config.DynDNS)
		g.dyndns.Start(ctx)
	}

	log.Info().Int("destinations", len(g.config.DestinationNumbers)).
		Bool("dyndns", g.config.DynDNS.Enabled).
		Msg("cellular gateway started")
	return nil
}

// Stop shuts down the gateway.
func (g *CellularGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	if g.dyndns != nil {
		g.dyndns.Stop()
	}
	g.connected.Store(false)
	log.Info().Msg("cellular gateway stopped")
	return nil
}

// Forward enqueues a message for cellular transmission.
func (g *CellularGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	select {
	case g.outCh <- msg:
		return nil
	default:
		g.errors.Add(1)
		return fmt.Errorf("cellular outbound queue full")
	}
}

// Receive returns the inbound message channel.
func (g *CellularGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *CellularGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "cellular",
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
func (g *CellularGateway) Type() string {
	return "cellular"
}

// Config returns the gateway configuration (for webhook secret validation).
func (g *CellularGateway) Config() CellularConfig {
	return g.config
}

// ForwardWebhookInbound pushes an externally-received webhook message into the gateway.
func (g *CellularGateway) ForwardWebhookInbound(msg InboundMessage) {
	select {
	case g.inCh <- msg:
		g.msgsIn.Add(1)
		g.lastActive.Store(time.Now().Unix())
	default:
		log.Warn().Msg("cellular: inbound channel full (webhook)")
	}
}

// sendWorker dequeues messages and sends them as SMS to configured numbers.
func (g *CellularGateway) sendWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			// Format: [MeshSat] !nodeID ch0: text
			fromNode := fmt.Sprintf("!%08x", msg.From)
			text := fmt.Sprintf("%s %s ch%d: %s", g.config.SMSPrefix, fromNode, msg.Channel, msg.DecodedText)

			// Truncate to SMS limit
			maxLen := 160 * g.config.MaxSMSSegments
			if len(text) > maxLen {
				text = text[:maxLen]
			}

			for _, number := range g.config.DestinationNumbers {
				if err := g.cell.SendSMS(ctx, number, text); err != nil {
					log.Error().Err(err).Str("to", number).Msg("cellular: SMS send failed")
					g.errors.Add(1)
					if g.db != nil {
						g.db.InsertSMSMessage("tx", number, text, "failed", time.Now().Unix())
					}
					continue
				}
				if g.db != nil {
					g.db.InsertSMSMessage("tx", number, text, "sent", time.Now().Unix())
				}
			}

			g.msgsOut.Add(1)
			g.lastActive.Store(time.Now().Unix())
			log.Info().Str("from", fromNode).Int("destinations", len(g.config.DestinationNumbers)).Msg("cellular: SMS sent")
			g.emit("cellular", fmt.Sprintf("SMS sent to %d destinations", len(g.config.DestinationNumbers)))
		}
	}
}

// smsListener subscribes to CellEvent channel and processes incoming SMS.
func (g *CellularGateway) smsListener(ctx context.Context) {
	defer g.wg.Done()

	events, err := g.cell.Subscribe(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cellular: failed to subscribe to cell events")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			switch event.Type {
			case "sms_received":
				// DB persistence is handled by CellSignalRecorder (engine layer).
				// Gateway only handles forwarding to mesh.
				sender := ""
				if event.Data != nil {
					var sms transport.SMSMessage
					if err := json.Unmarshal(event.Data, &sms); err == nil {
						sender = sms.Sender
					}
				}

				// Check allowed senders
				if len(g.config.AllowedSenders) > 0 && !isAllowedSender(sender, g.config.AllowedSenders) {
					log.Info().Str("sender", sender).Msg("cellular: SMS from non-allowed sender, ignoring")
					continue
				}

				inbound := InboundMessage{
					Text:    event.Message,
					To:      g.config.InboundDestNode,
					Channel: g.config.InboundChannel,
					Source:  "cellular",
				}

				g.msgsIn.Add(1)
				g.lastActive.Store(time.Now().Unix())
				log.Info().Str("sender", sender).Str("text", event.Message).Msg("cellular: SMS received, forwarding to mesh")
				g.emit("cellular", fmt.Sprintf("SMS received from %s, forwarding to mesh", sender))

				select {
				case g.inCh <- inbound:
				default:
					log.Warn().Msg("cellular: inbound channel full")
				}

			case "cbs_received", "cell_info_update":
				// DB persistence handled by CellSignalRecorder (engine layer)

			case "connected":
				g.connected.Store(true)
			case "disconnected":
				g.connected.Store(false)
			case "signal":
				// Signal events tracked by CellSignalRecorder
			}
		}
	}
}

func isAllowedSender(sender string, allowed []string) bool {
	for _, a := range allowed {
		if a == sender {
			return true
		}
	}
	return false
}
