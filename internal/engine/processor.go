package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/dedup"
	"meshsat/internal/gateway"
	"meshsat/internal/ratelimit"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// GatewayProvider returns the currently running gateways.
// The Manager implements this so the Processor always forwards to live instances.
type GatewayProvider interface {
	Gateways() []gateway.Gateway
}

// Processor ingests mesh events, persists them, and routes to gateways.
type Processor struct {
	db       *database.DB
	mesh     transport.MeshTransport
	gateways []gateway.Gateway          // static list (boot-time only, kept for backward compat)
	gwProv   GatewayProvider            // dynamic provider — takes precedence when set
	dedup    *dedup.Deduplicator        // composite-key deduplication
	rules    *rules.Engine              // forwarding rules engine
	meshLim  *ratelimit.TokenBucket     // rate limiter for mesh injection
	subs     []chan transport.MeshEvent // SSE re-broadcast subscribers
	subsMu   sync.RWMutex
}

// NewProcessor creates a new event processor.
func NewProcessor(db *database.DB, mesh transport.MeshTransport) *Processor {
	return &Processor{
		db:      db,
		mesh:    mesh,
		meshLim: ratelimit.NewMeshInjectionLimiter(),
	}
}

// SetDeduplicator sets the in-memory deduplicator.
func (p *Processor) SetDeduplicator(d *dedup.Deduplicator) {
	p.dedup = d
}

// SetRuleEngine sets the forwarding rules engine.
func (p *Processor) SetRuleEngine(e *rules.Engine) {
	p.rules = e
}

// AddGateway registers a static gateway for message forwarding.
// Prefer SetGatewayProvider for dynamic gateway lifecycle.
func (p *Processor) AddGateway(gw gateway.Gateway) {
	p.gateways = append(p.gateways, gw)
}

// SetGatewayProvider sets a dynamic gateway source (e.g. the Manager).
// When set, Forward() queries this instead of the static gateways list,
// so gateway stop/start/reconfigure via the API is reflected immediately.
func (p *Processor) SetGatewayProvider(prov GatewayProvider) {
	p.gwProv = prov
}

// Subscribe adds an SSE re-broadcast subscriber. Returns a channel and unsubscribe func.
func (p *Processor) Subscribe() (<-chan transport.MeshEvent, func()) {
	ch := make(chan transport.MeshEvent, 32)
	p.subsMu.Lock()
	p.subs = append(p.subs, ch)
	p.subsMu.Unlock()

	unsub := func() {
		p.subsMu.Lock()
		defer p.subsMu.Unlock()
		for i, s := range p.subs {
			if s == ch {
				p.subs = append(p.subs[:i], p.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, unsub
}

// Run starts the event processing loop. Blocks until ctx is cancelled.
func (p *Processor) Run(ctx context.Context) error {
	events, err := p.mesh.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to mesh: %w", err)
	}

	log.Info().Msg("event processor started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("event processor stopped")
			return nil
		case event, ok := <-events:
			if !ok {
				log.Warn().Msg("mesh event channel closed")
				return nil
			}
			p.handleEvent(ctx, event)
			p.broadcast(event)
		}
	}
}

func (p *Processor) handleEvent(ctx context.Context, event transport.MeshEvent) {
	switch event.Type {
	case "message":
		p.handleMessage(event)
	case "node_update":
		p.handleNodeUpdate(event)
	case "position":
		p.handlePosition(event)
	case "connected", "disconnected", "config_complete":
		log.Info().Str("type", event.Type).Str("message", event.Message).Msg("mesh status event")
	default:
		log.Debug().Str("type", event.Type).Msg("unhandled event type")
	}
}

func (p *Processor) handleMessage(event transport.MeshEvent) {
	var msg transport.MeshMessage
	if err := json.Unmarshal(event.Data, &msg); err != nil {
		log.Warn().Err(err).Msg("failed to parse message event")
		return
	}

	// Dedupe: prefer in-memory composite-key check, fall back to DB
	if p.dedup != nil {
		if p.dedup.IsDuplicate(msg.From, msg.ID) {
			log.Debug().Uint32("packet_id", msg.ID).Msg("duplicate packet (dedup), skipping")
			return
		}
	} else {
		exists, err := p.db.HasPacket(msg.ID)
		if err != nil {
			log.Error().Err(err).Msg("dedupe check failed")
		}
		if exists {
			log.Debug().Uint32("packet_id", msg.ID).Msg("duplicate packet (db), skipping")
			return
		}
	}

	dbMsg := &database.Message{
		PacketID:    msg.ID,
		FromNode:    fmt.Sprintf("!%08x", msg.From),
		ToNode:      fmt.Sprintf("!%08x", msg.To),
		Channel:     int(msg.Channel),
		PortNum:     msg.PortNum,
		PortNumName: msg.PortNumName,
		DecodedText: msg.DecodedText,
		RxSNR:       msg.RxSNR,
		RxTime:      msg.RxTime,
		HopLimit:    msg.HopLimit,
		HopStart:    msg.HopStart,
		Direction:   "rx",
		Transport:   "radio",
	}

	if err := p.db.InsertMessage(dbMsg); err != nil {
		log.Error().Err(err).Uint32("packet_id", msg.ID).Msg("failed to persist message")
		return
	}

	log.Debug().Uint32("packet_id", msg.ID).Str("portnum", msg.PortNumName).Msg("message persisted")

	// Rule engine evaluation (if configured)
	if p.rules != nil && p.rules.RuleCount() > 0 {
		match := p.rules.Evaluate(&msg)
		if match != nil {
			// Check keyword filter
			if !rules.MatchesKeyword(match.Rule, msg.DecodedText) {
				return
			}

			log.Info().Int("rule_id", match.Rule.ID).Str("rule", match.Rule.Name).Str("dest", match.Rule.DestType).Msg("rule matched, forwarding")

			activeGateways := p.gateways
			if p.gwProv != nil {
				activeGateways = p.gwProv.Gateways()
			}

			for _, gw := range activeGateways {
				// Route based on dest_type
				switch match.Rule.DestType {
				case "both":
					if err := gw.Forward(context.Background(), &msg); err != nil {
						log.Warn().Err(err).Str("gateway", gw.Type()).Msg("rule forward failed")
					}
				case "iridium":
					if gw.Type() == "iridium" {
						if err := gw.Forward(context.Background(), &msg); err != nil {
							log.Warn().Err(err).Msg("iridium rule forward failed")
						}
					}
				case "mqtt":
					if gw.Type() == "mqtt" {
						if err := gw.Forward(context.Background(), &msg); err != nil {
							log.Warn().Err(err).Msg("mqtt rule forward failed")
						}
					}
				}
			}
		}
		return // rule engine handled (or no match = local-only)
	}

	// Fallback: forward to all active gateways (legacy behavior when no rules configured)
	activeGateways := p.gateways
	if p.gwProv != nil {
		activeGateways = p.gwProv.Gateways()
	}
	for _, gw := range activeGateways {
		if err := gw.Forward(context.Background(), &msg); err != nil {
			log.Warn().Err(err).Str("gateway", gw.Type()).Msg("gateway forward failed")
		}
	}
}

func (p *Processor) handleNodeUpdate(event transport.MeshEvent) {
	var node transport.MeshNode
	if err := json.Unmarshal(event.Data, &node); err != nil {
		log.Warn().Err(err).Msg("failed to parse node_update event")
		return
	}

	// Persist telemetry if any telemetry data is present
	hasTelemetry := node.BatteryLevel > 0 || node.Voltage > 0 ||
		node.ChannelUtil > 0 || node.AirUtilTx > 0 ||
		node.Temperature != nil || node.Humidity != nil || node.Pressure != nil ||
		node.UptimeSeconds > 0
	if hasTelemetry {
		t := &database.Telemetry{
			NodeID:        node.UserID,
			BatteryLevel:  node.BatteryLevel,
			Voltage:       node.Voltage,
			ChannelUtil:   node.ChannelUtil,
			AirUtilTx:     node.AirUtilTx,
			Temperature:   node.Temperature,
			Humidity:      node.Humidity,
			Pressure:      node.Pressure,
			UptimeSeconds: node.UptimeSeconds,
		}
		if err := p.db.InsertTelemetry(t); err != nil {
			log.Error().Err(err).Str("node", node.UserID).Msg("failed to persist telemetry")
		}
	}

	// Persist position if GPS data present
	if node.Latitude != 0 || node.Longitude != 0 {
		pos := &database.Position{
			NodeID:     node.UserID,
			Latitude:   node.Latitude,
			Longitude:  node.Longitude,
			Altitude:   int(node.Altitude),
			SatsInView: node.Sats,
		}
		if err := p.db.InsertPosition(pos); err != nil {
			log.Error().Err(err).Str("node", node.UserID).Msg("failed to persist position")
		}
	}
}

func (p *Processor) handlePosition(event transport.MeshEvent) {
	// Position events may come as standalone (not wrapped in node_update)
	var pos struct {
		NodeID    string  `json:"node_id"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Altitude  int     `json:"altitude"`
		Sats      int     `json:"sats"`
	}
	if err := json.Unmarshal(event.Data, &pos); err != nil {
		log.Warn().Err(err).Msg("failed to parse position event")
		return
	}

	if pos.Latitude == 0 && pos.Longitude == 0 {
		return
	}

	dbPos := &database.Position{
		NodeID:     pos.NodeID,
		Latitude:   pos.Latitude,
		Longitude:  pos.Longitude,
		Altitude:   pos.Altitude,
		SatsInView: pos.Sats,
	}
	if err := p.db.InsertPosition(dbPos); err != nil {
		log.Error().Err(err).Str("node", pos.NodeID).Msg("failed to persist position")
	}
}

// StartGatewayReceiver drains inbound messages from a gateway and sends them to the mesh.
func (p *Processor) StartGatewayReceiver(ctx context.Context, gw gateway.Gateway) {
	go func() {
		ch := gw.Receive()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				log.Info().Str("source", msg.Source).Str("text", msg.Text).Msg("gateway inbound message")

				// Rate limit mesh injection from external sources
				if p.meshLim != nil && !p.meshLim.Allow() {
					log.Warn().Str("source", msg.Source).Msg("mesh injection rate limited, dropping")
					continue
				}

				// Send to mesh
				req := transport.SendRequest{
					Text:    msg.Text,
					To:      msg.To,
					Channel: msg.Channel,
				}
				if err := p.mesh.SendMessage(ctx, req); err != nil {
					log.Error().Err(err).Str("source", msg.Source).Msg("failed to send gateway message to mesh")
					continue
				}

				// Persist as inbound message
				dbMsg := &database.Message{
					FromNode:    msg.Source,
					ToNode:      "mesh",
					Channel:     msg.Channel,
					PortNum:     1, // TEXT_MESSAGE
					PortNumName: "TEXT_MESSAGE_APP",
					DecodedText: msg.Text,
					Direction:   "tx",
					Transport:   msg.Source,
				}
				if err := p.db.InsertMessage(dbMsg); err != nil {
					log.Error().Err(err).Msg("failed to persist gateway inbound message")
				}
			}
		}
	}()
}

func (p *Processor) broadcast(event transport.MeshEvent) {
	p.subsMu.RLock()
	defer p.subsMu.RUnlock()

	for _, ch := range p.subs {
		select {
		case ch <- event:
		default:
			// Slow subscriber, drop event
		}
	}
}
