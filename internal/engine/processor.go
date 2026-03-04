package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

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

// paidTransportRateLimit is the global hourly rate limit for paid gateways.
var paidTransportRateLimit = 60

func init() {
	if v := os.Getenv("MESHSAT_PAID_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			paidTransportRateLimit = n
		}
	}
}

// SetPaidRateLimit sets the global hourly rate limit for paid transports.
func SetPaidRateLimit(limit int) {
	if limit > 0 {
		paidTransportRateLimit = limit
	}
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

	// Cross-gateway relay deduplication (source+text hash → timestamp, 5min TTL)
	relayDedupMu sync.Mutex
	relayDedup   map[string]time.Time

	// Per-gateway paid transport rate limiters (iridium, cellular)
	paidLimiters   map[string]*ratelimit.TokenBucket
	paidLimitersMu sync.RWMutex
}

// NewProcessor creates a new event processor.
func NewProcessor(db *database.DB, mesh transport.MeshTransport) *Processor {
	return &Processor{
		db:           db,
		mesh:         mesh,
		meshLim:      ratelimit.NewMeshInjectionLimiter(),
		relayDedup:   make(map[string]time.Time),
		paidLimiters: make(map[string]*ratelimit.TokenBucket),
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

	// Prevent gateway→mesh→gateway feedback loops:
	// If this message text was recently injected from a gateway, don't forward it back.
	if p.isRecentGatewayInjection(msg.DecodedText) {
		log.Debug().Uint32("packet_id", msg.ID).Msg("skipping forward: message originated from gateway (loop prevention)")
		return
	}

	// Rule engine evaluation (if configured)
	if p.rules != nil && p.rules.RuleCount() > 0 {
		match := p.rules.Evaluate(&msg)
		if match != nil {
			// Check keyword filter
			if !rules.MatchesKeyword(match.Rule, msg.DecodedText) {
				return
			}

			log.Info().Int("rule_id", match.Rule.ID).Str("rule", match.Rule.Name).Str("dest", match.Rule.DestType).Msg("rule matched, forwarding")

			p.Emit(transport.MeshEvent{
				Type:    "rule_match",
				Message: fmt.Sprintf("Rule '%s' matched: mesh→%s", match.Rule.Name, match.Rule.DestType),
			})

			activeGateways := p.gateways
			if p.gwProv != nil {
				activeGateways = p.gwProv.Gateways()
			}

			for _, gw := range activeGateways {
				gwType := gw.Type()
				destType := match.Rule.DestType

				// Check if this gateway should receive based on dest_type
				shouldForward := false
				switch destType {
				case "both":
					shouldForward = gwType == "iridium" || gwType == "mqtt"
				case "all":
					shouldForward = true
				case "iridium":
					shouldForward = gwType == "iridium"
				case "mqtt":
					shouldForward = gwType == "mqtt"
				case "cellular":
					shouldForward = gwType == "cellular"
				}

				if !shouldForward {
					continue
				}

				// Paid transport safety: exclude telemetry/traceroute unless explicitly opted in
				if isPaidTransport(gwType) && !isPortnumExplicitlyIncluded(match.Rule, msg.PortNum) {
					if msg.PortNum == 67 || msg.PortNum == 70 { // TELEMETRY_APP, TRACEROUTE_APP
						log.Debug().Str("gateway", gwType).Int("portnum", msg.PortNum).Msg("telemetry excluded from paid transport")
						continue
					}
				}

				// Global paid transport rate limit
				if isPaidTransport(gwType) && !p.allowPaidTransport(gwType) {
					log.Warn().Str("gateway", gwType).Msg("paid transport global rate limit exceeded")
					continue
				}

				if err := gw.Forward(context.Background(), &msg); err != nil {
					log.Warn().Err(err).Str("gateway", gwType).Msg("rule forward failed")
					p.Emit(transport.MeshEvent{
						Type:    "forward_error",
						Message: fmt.Sprintf("Failed to forward to %s: %s", gwType, err.Error()),
					})
				} else {
					p.Emit(transport.MeshEvent{
						Type:    "forward",
						Message: fmt.Sprintf("Forwarded to %s gateway", gwType),
					})
				}
			}
		}
		return // rule engine handled (or no match = local-only)
	}

	// No rules configured: messages stay local (no forwarding)
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
// Also handles cross-gateway relay: before mesh injection, check if the message should
// be forwarded to other gateways (e.g., iridium→cellular relay).
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

				p.Emit(transport.MeshEvent{
					Type:    "inbound",
					Message: fmt.Sprintf("Received from %s: %s", msg.Source, truncateText(msg.Text, 80)),
				})

				// Cross-gateway relay: check if this message should be forwarded
				// to other gateways WITHOUT touching the mesh.
				p.handleCrossGatewayRelay(ctx, msg)

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

				// Rule engine evaluation for inbound messages.
				// If a matching inbound rule exists, use its dest_channel/dest_node.
				// If no inbound rule matches, fall through to inject as-is (broadcast).
				// This ensures outbound-only rules don't block inbound message delivery.
				if p.rules != nil {
					match := p.rules.EvaluateInbound(msg.Source, msg.Text)
					if match != nil {
						log.Info().Int("rule_id", match.Rule.ID).Str("rule", match.Rule.Name).Msg("inbound rule matched")
						req.Channel = match.Rule.DestChannel
						if match.Rule.DestNode != nil && *match.Rule.DestNode != "" {
							req.To = *match.Rule.DestNode
						}
					} else {
						log.Info().Str("source", msg.Source).Msg("no inbound rule matched, injecting as broadcast")
					}
				}
				if err := p.mesh.SendMessage(ctx, req); err != nil {
					log.Error().Err(err).Str("source", msg.Source).Msg("failed to send gateway message to mesh")
					continue
				}

				// Mark this text as gateway-injected so the mesh echo doesn't
				// get forwarded back to gateways (prevents feedback loops).
				p.markGatewayInjection(msg.Text)

				// Persist as inbound message (received from external, relayed to mesh)
				dbMsg := &database.Message{
					FromNode:    msg.Source,
					ToNode:      msg.To,
					Channel:     req.Channel,
					PortNum:     1, // TEXT_MESSAGE
					PortNumName: "TEXT_MESSAGE_APP",
					DecodedText: msg.Text,
					Direction:   "rx",
					Transport:   msg.Source, // "iridium", "mqtt", or "cellular"
				}
				if err := p.db.InsertMessage(dbMsg); err != nil {
					log.Error().Err(err).Msg("failed to persist gateway inbound message")
				}
			}
		}
	}()
}

// handleCrossGatewayRelay checks if an inbound gateway message should be forwarded
// to other gateways (cross-gateway relay). This enables gateway-to-gateway forwarding
// without touching the mesh (e.g., iridium→cellular, mqtt→iridium).
func (p *Processor) handleCrossGatewayRelay(ctx context.Context, msg gateway.InboundMessage) {
	if p.rules == nil {
		return
	}

	// Check relay dedup (prevent loops)
	dedupKey := relayDedupKey(msg.Source, msg.Text)
	p.relayDedupMu.Lock()
	if ts, ok := p.relayDedup[dedupKey]; ok && time.Since(ts) < 5*time.Minute {
		p.relayDedupMu.Unlock()
		log.Debug().Str("source", msg.Source).Msg("relay dedup: skipping duplicate")
		return
	}
	p.relayDedup[dedupKey] = time.Now()
	p.relayDedupMu.Unlock()

	// Prune old relay dedup entries periodically
	go p.pruneRelayDedup()

	match := p.rules.EvaluateRelay(msg.Source, msg.Text)
	if match == nil {
		return
	}

	log.Info().Int("rule_id", match.Rule.ID).Str("rule", match.Rule.Name).
		Str("source", msg.Source).Str("dest", match.Rule.DestType).Msg("cross-gateway relay matched")

	activeGateways := p.gateways
	if p.gwProv != nil {
		activeGateways = p.gwProv.Gateways()
	}

	// Build a synthetic MeshMessage for the Forward() interface
	relayMsg := &transport.MeshMessage{
		PortNum:     1, // TEXT_MESSAGE
		PortNumName: "TEXT_MESSAGE_APP",
		DecodedText: msg.Text,
	}

	for _, gw := range activeGateways {
		gwType := gw.Type()

		// Skip source gateway (prevent self-relay)
		if gwType == msg.Source {
			continue
		}

		// Check dest_type matching
		shouldRelay := false
		switch match.Rule.DestType {
		case "all":
			shouldRelay = true
		case "both":
			shouldRelay = gwType == "iridium" || gwType == "mqtt"
		case "iridium":
			shouldRelay = gwType == "iridium"
		case "mqtt":
			shouldRelay = gwType == "mqtt"
		case "cellular":
			shouldRelay = gwType == "cellular"
		}

		if !shouldRelay {
			continue
		}

		// Global paid transport rate limit
		if isPaidTransport(gwType) && !p.allowPaidTransport(gwType) {
			log.Warn().Str("gateway", gwType).Msg("relay: paid transport global rate limit exceeded")
			continue
		}

		if err := gw.Forward(ctx, relayMsg); err != nil {
			log.Warn().Err(err).Str("gateway", gwType).Msg("cross-gateway relay forward failed")
			p.Emit(transport.MeshEvent{
				Type:    "forward_error",
				Message: fmt.Sprintf("Relay %s→%s failed: %s", msg.Source, gwType, err.Error()),
			})
		} else {
			log.Info().Str("from", msg.Source).Str("to", gwType).Msg("cross-gateway relay delivered")
			p.Emit(transport.MeshEvent{
				Type:    "relay",
				Message: fmt.Sprintf("Relayed %s→%s", msg.Source, gwType),
			})

			// Persist relay message
			dbMsg := &database.Message{
				FromNode:    msg.Source,
				ToNode:      gwType,
				PortNum:     1,
				PortNumName: "TEXT_MESSAGE_APP",
				DecodedText: msg.Text,
				Direction:   "relay",
				Transport:   gwType,
			}
			if err := p.db.InsertMessage(dbMsg); err != nil {
				log.Error().Err(err).Msg("failed to persist relay message")
			}
		}
	}
}

func relayDedupKey(source, text string) string {
	h := sha256.Sum256([]byte(source + "|" + text))
	return fmt.Sprintf("%x", h[:8])
}

// markGatewayInjection records that a message text was injected from a gateway
// into the mesh, so the resulting mesh echo won't be forwarded back.
func (p *Processor) markGatewayInjection(text string) {
	key := gwInjectDedupKey(text)
	p.relayDedupMu.Lock()
	p.relayDedup[key] = time.Now()
	p.relayDedupMu.Unlock()
}

// isRecentGatewayInjection returns true if this text was recently injected
// from a gateway into the mesh (within 5 minutes).
func (p *Processor) isRecentGatewayInjection(text string) bool {
	if text == "" {
		return false
	}
	key := gwInjectDedupKey(text)
	p.relayDedupMu.Lock()
	ts, ok := p.relayDedup[key]
	p.relayDedupMu.Unlock()
	return ok && time.Since(ts) < 5*time.Minute
}

func gwInjectDedupKey(text string) string {
	h := sha256.Sum256([]byte("gw_inject|" + text))
	return fmt.Sprintf("%x", h[:8])
}

func (p *Processor) pruneRelayDedup() {
	p.relayDedupMu.Lock()
	defer p.relayDedupMu.Unlock()
	now := time.Now()
	for k, ts := range p.relayDedup {
		if now.Sub(ts) > 5*time.Minute {
			delete(p.relayDedup, k)
		}
	}
}

// isPaidTransport returns true if the gateway type is a paid transport.
func isPaidTransport(gwType string) bool {
	return gwType == "iridium" || gwType == "cellular"
}

// isPortnumExplicitlyIncluded checks if a portnum is explicitly listed in the rule's source_portnums.
func isPortnumExplicitlyIncluded(rule database.ForwardingRule, portnum int) bool {
	if rule.SourcePortnums == nil {
		return false // not explicitly included = use default exclusion
	}
	var portnums []int
	if err := json.Unmarshal([]byte(*rule.SourcePortnums), &portnums); err != nil {
		return false
	}
	for _, pn := range portnums {
		if pn == portnum {
			return true
		}
	}
	return false
}

// allowPaidTransport enforces the global hourly rate limit for paid gateways.
func (p *Processor) allowPaidTransport(gwType string) bool {
	p.paidLimitersMu.Lock()
	limiter, ok := p.paidLimiters[gwType]
	if !ok {
		// Create a limiter: paidTransportRateLimit messages per hour → rate per minute
		ratePerMin := paidTransportRateLimit / 60
		if ratePerMin < 1 {
			ratePerMin = 1
		}
		limiter = ratelimit.NewRuleLimiter(ratePerMin, 60)
		p.paidLimiters[gwType] = limiter
	}
	p.paidLimitersMu.Unlock()
	return limiter.Allow()
}

// Emit broadcasts a synthetic event to all SSE subscribers.
// Used by the processor to emit gateway operation events (rule matches, forwards, relays).
func (p *Processor) Emit(event transport.MeshEvent) {
	p.broadcast(event)
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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
