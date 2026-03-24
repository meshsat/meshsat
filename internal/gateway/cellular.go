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

	// Node name resolver (set by engine wiring)
	nodeNameFn func(uint32) string

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	emitEvent EventEmitFunc
}

// SetEventEmitter sets the SSE event emitter callback.
func (g *CellularGateway) SetEventEmitter(fn EventEmitFunc) {
	g.emitEvent = fn
}

// SetNodeNameResolver sets a function that resolves mesh node IDs to human-readable names.
func (g *CellularGateway) SetNodeNameResolver(fn func(uint32) string) {
	g.nodeNameFn = fn
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

// Forward sends a message as SMS synchronously. The caller (delivery worker)
// gets the actual send result so the delivery ledger reflects reality.
func (g *CellularGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	return g.sendSMSSync(ctx, msg)
}

// Enqueue submits a message for outbound delivery via the gateway.
func (g *CellularGateway) Enqueue(msg *transport.MeshMessage) error {
	return g.Forward(context.Background(), msg)
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

// sendSMSSync formats and sends the message as SMS synchronously.
// Returns error if ANY destination fails (first error).
func (g *CellularGateway) sendSMSSync(ctx context.Context, msg *transport.MeshMessage) error {
	var text string

	if msg.Encrypted {
		// Encrypted: send raw base64 ciphertext only — no prefix, no metadata.
		// The MeshSat Android app expects pure base64 for decryption.
		// GSM safety was already validated by the dispatcher (re-encrypt loop).
		text = msg.DecodedText
	} else {
		// Plain text: human-readable format with sender name
		sender := g.resolveNodeName(msg.From)
		if msg.Channel > 0 {
			text = fmt.Sprintf("%s %s ch%d: %s", g.config.SMSPrefix, sender, msg.Channel, msg.DecodedText)
		} else {
			text = fmt.Sprintf("%s %s: %s", g.config.SMSPrefix, sender, msg.DecodedText)
		}

		// Sanitize to GSM 7-bit basic charset — some modems (Huawei E220)
		// fail with CMS ERROR 305 on extension table characters like [ ] { } | \ ^ ~
		text = SanitizeSMSText(text)
	}

	// Truncate to SMS limit
	maxLen := 160 * g.config.MaxSMSSegments
	if len(text) > maxLen {
		text = text[:maxLen]
	}

	// Use per-rule SMS destinations if set, otherwise fall back to gateway config
	destinations := msg.SMSDestinations
	if len(destinations) == 0 {
		destinations = g.config.DestinationNumbers
	}

	if len(destinations) == 0 {
		return fmt.Errorf("no SMS destinations configured")
	}

	var firstErr error
	for _, number := range destinations {
		if err := g.cell.SendSMS(ctx, number, text); err != nil {
			log.Error().Err(err).Str("to", number).Msg("cellular: SMS send failed")
			g.errors.Add(1)
			if g.db != nil {
				g.db.InsertSMSMessage("tx", number, text, "failed", time.Now().Unix())
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("SMS to %s: %w", number, err)
			}
			continue
		}
		if g.db != nil {
			g.db.InsertSMSMessage("tx", number, text, "sent", time.Now().Unix())
		}
	}

	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	if firstErr == nil {
		log.Info().Uint32("from", msg.From).Int("destinations", len(destinations)).Msg("cellular: SMS sent")
		g.emit("cellular", fmt.Sprintf("SMS sent to %d destinations", len(destinations)))
	}
	return firstErr
}

// sendWorker dequeues messages from outCh (legacy/non-dispatcher callers).
func (g *CellularGateway) sendWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			if err := g.sendSMSSync(ctx, msg); err != nil {
				log.Error().Err(err).Msg("cellular: sendWorker SMS failed")
			}
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

// resolveNodeName returns a human-readable name for a mesh node ID.
// Uses the node name resolver if set, falls back to short hex ID.
func (g *CellularGateway) resolveNodeName(nodeID uint32) string {
	if g.nodeNameFn != nil {
		if name := g.nodeNameFn(nodeID); name != "" {
			return name
		}
	}
	return fmt.Sprintf("!%08x", nodeID)
}

func isAllowedSender(sender string, allowed []string) bool {
	for _, a := range allowed {
		if a == sender {
			return true
		}
	}
	return false
}
