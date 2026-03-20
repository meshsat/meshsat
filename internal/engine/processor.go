package engine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/dedup"
	"meshsat/internal/gateway"
	"meshsat/internal/reticulum"
	"meshsat/internal/routing"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// GatewayProvider returns the currently running gateways.
// The Manager implements this so the Processor always forwards to live instances.
type GatewayProvider interface {
	Gateways() []gateway.Gateway
	GatewayByInterfaceID(id string) gateway.Gateway // v0.3.0: lookup by interface ID
}

// Processor ingests mesh events, persists them, and routes to gateways.
type Processor struct {
	db         *database.DB
	mesh       transport.MeshTransport
	gwProv     GatewayProvider            // dynamic provider (gateway manager)
	dedup      *dedup.Deduplicator        // composite-key deduplication
	dispatcher *Dispatcher                // v0.2.0 delivery fan-out
	subs       []chan transport.MeshEvent // SSE re-broadcast subscribers
	subsMu     sync.RWMutex

	// Routing subsystem (v0.2.0 — Reticulum-inspired)
	announceRelay *routing.AnnounceRelay
	linkMgr       *routing.LinkManager
	keepalive     *routing.LinkKeepalive
	destTable     *routing.DestinationTable
	transportNode *routing.TransportNode

	// Gateway injection dedup (text hash → timestamp, 5min TTL)
	relayDedupMu sync.Mutex
	relayDedup   map[string]time.Time
}

// NewProcessor creates a new event processor.
func NewProcessor(db *database.DB, mesh transport.MeshTransport) *Processor {
	return &Processor{
		db:         db,
		mesh:       mesh,
		relayDedup: make(map[string]time.Time),
	}
}

// SetDeduplicator sets the in-memory deduplicator.
func (p *Processor) SetDeduplicator(d *dedup.Deduplicator) {
	p.dedup = d
}

// SetDispatcher sets the v0.2.0 dispatcher for structured delivery fan-out.
func (p *Processor) SetDispatcher(d *Dispatcher) {
	p.dispatcher = d
}

// SetGatewayProvider sets a dynamic gateway source (e.g. the Manager).
// When set, Forward() queries this instead of the static gateways list,
// so gateway stop/start/reconfigure via the API is reflected immediately.
func (p *Processor) SetGatewayProvider(prov GatewayProvider) {
	p.gwProv = prov
}

// SetRouting wires the routing subsystem into the processor event loop.
// When set, incoming PRIVATE_APP packets are dispatched to the announce relay,
// link manager, and keepalive handler.
func (p *Processor) SetRouting(relay *routing.AnnounceRelay, linkMgr *routing.LinkManager, keepalive *routing.LinkKeepalive, destTable *routing.DestinationTable) {
	p.announceRelay = relay
	p.linkMgr = linkMgr
	p.keepalive = keepalive
	p.destTable = destTable
}

// SetTransportNode enables Transport Node packet forwarding. When set,
// non-local Reticulum packets are forwarded via the routing table.
func (p *Processor) SetTransportNode(tn *routing.TransportNode) {
	p.transportNode = tn
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
// Automatically reconnects to the mesh transport with exponential backoff
// if the serial connection is lost (matching the Iridium gateway pattern).
func (p *Processor) Run(ctx context.Context) error {
	log.Info().Msg("event processor started")

	// Periodically prune the relay dedup map to prevent unbounded growth
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.pruneRelayDedup()
			}
		}
	}()

	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("event processor stopped")
			return nil
		default:
		}

		start := time.Now()
		err := p.processEvents(ctx)
		if ctx.Err() != nil {
			return nil
		}

		// Reset backoff if connection lasted > 10s
		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}

		if err != nil {
			log.Warn().Err(err).Dur("backoff", backoff).Msg("mesh transport disconnected, reconnecting")
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 2*time.Minute {
			backoff = 2 * time.Minute
		}
	}
}

// processEvents subscribes once and processes events until disconnected or ctx cancelled.
func (p *Processor) processEvents(ctx context.Context) error {
	events, err := p.mesh.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to mesh: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("event channel closed")
			}
			if event.Type == "disconnected" {
				return fmt.Errorf("mesh transport disconnected")
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
	case "neighbor_info":
		p.handleNeighborInfoEvent(event)
	case "range_test":
		p.handleRangeTestEvent(event)
	case "store_forward":
		log.Info().Str("message", event.Message).Msg("store_forward event")
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

	// Route PRIVATE_APP packets to the routing subsystem (announces, links, keepalives)
	if msg.PortNum == transport.PortNumPrivate && len(msg.RawPayload) > 0 {
		p.handleRoutingPacket(event, msg.RawPayload)
		return // routing packets are not forwarded through the rules engine
	}

	// Prevent gateway→mesh→gateway feedback loops:
	// If this message text was recently injected from a gateway, don't forward it back.
	if p.isRecentGatewayInjection(msg.DecodedText) {
		log.Debug().Uint32("packet_id", msg.ID).Msg("skipping forward: message originated from gateway (loop prevention)")
		return
	}

	// Dispatch via rules engine + delivery ledger
	fromNode := fmt.Sprintf("!%08x", msg.From)
	routeMsg := rules.RouteMessage{
		Text:    msg.DecodedText,
		From:    fromNode,
		Channel: int(msg.Channel),
		PortNum: msg.PortNum,
	}

	if p.dispatcher != nil {
		// Increment ingress sequence number for the source interface
		if _, err := p.db.IncrementIngressSeq("mesh_0"); err != nil {
			log.Warn().Err(err).Msg("failed to increment ingress seq for mesh_0")
		}

		// Pass the full message JSON as payload so delivery workers have
		// access to all metadata (From, ID, Channel, RxTime, etc.).
		payload, _ := json.Marshal(msg)
		if n := p.dispatcher.DispatchAccess("mesh_0", routeMsg, payload); n > 0 {
			log.Info().Int("deliveries", n).Uint32("packet_id", msg.ID).Msg("dispatched via access rules")
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

func (p *Processor) handleNeighborInfoEvent(event transport.MeshEvent) {
	var ni transport.NeighborInfo
	if err := json.Unmarshal(event.Data, &ni); err != nil {
		log.Warn().Err(err).Msg("failed to parse neighbor_info event")
		return
	}
	for _, n := range ni.Neighbors {
		if err := p.db.InsertNeighborInfo(ni.NodeID, n.NodeID, n.SNR, n.LastRxTime, n.NodeBroadcastIntervalSec); err != nil {
			log.Error().Err(err).Uint32("node", ni.NodeID).Msg("failed to persist neighbor info")
		}
	}
	log.Debug().Uint32("node", ni.NodeID).Int("neighbors", len(ni.Neighbors)).Msg("neighbor info persisted")
}

func (p *Processor) handleRangeTestEvent(event transport.MeshEvent) {
	var rt struct {
		From   uint32  `json:"from"`
		Text   string  `json:"text"`
		RxSNR  float32 `json:"rx_snr"`
		RxRSSI int     `json:"rx_rssi"`
	}
	if err := json.Unmarshal(event.Data, &rt); err != nil {
		log.Warn().Err(err).Msg("failed to parse range_test event")
		return
	}
	fromNode := fmt.Sprintf("!%08x", rt.From)
	if err := p.db.InsertRangeTest(fromNode, "", rt.Text, rt.RxSNR, rt.RxRSSI, 0, 0, "rx"); err != nil {
		log.Error().Err(err).Str("from", fromNode).Msg("failed to persist range test")
	}
}

// handleRoutingPacket dispatches a PRIVATE_APP payload to the appropriate
// routing subsystem handler. It first tries to parse the payload as a
// Reticulum packet (header + payload). If the packet type is recognized,
// it's handled accordingly. Otherwise, it falls back to Bridge-legacy
// type byte detection (0x10-0x13).
func (p *Processor) handleRoutingPacket(event transport.MeshEvent, payload []byte) {
	if len(payload) == 0 {
		return
	}

	// Try Reticulum header parsing first (requires at least HeaderMinSize bytes).
	if len(payload) >= reticulum.HeaderMinSize {
		hdr, err := reticulum.UnmarshalHeader(payload)
		if err == nil {
			switch hdr.PacketType {
			case reticulum.PacketAnnounce:
				if p.announceRelay != nil {
					ctx := context.Background()
					if p.announceRelay.HandleAnnounce(ctx, payload, "mesh_0") {
						log.Debug().Int("size", len(payload)).Msg("routing: Reticulum announce processed")
						// Feed to transport node routing table
						if p.transportNode != nil {
							ann, err := routing.UnmarshalAnnounce(payload)
							if err == nil {
								p.transportNode.ProcessAnnounce(ann, "mesh_0")
							}
						}
					}
				}
				return

			case reticulum.PacketLinkRequest:
				// Try transport forwarding first (relay to dest if not for us)
				if p.transportNode != nil && p.transportNode.ForwardPacket(payload, "mesh_0") {
					log.Debug().Msg("routing: Reticulum link request forwarded via transport")
					return
				}
				log.Debug().Msg("routing: Reticulum link request (local handling not yet wired)")
				return

			case reticulum.PacketProof:
				if p.transportNode != nil && p.transportNode.ForwardPacket(payload, "mesh_0") {
					log.Debug().Msg("routing: Reticulum proof forwarded via transport")
					return
				}
				log.Debug().Msg("routing: Reticulum proof (local handling not yet wired)")
				return

			case reticulum.PacketData:
				if p.transportNode != nil && p.transportNode.ForwardPacket(payload, "mesh_0") {
					log.Debug().Msg("routing: Reticulum data packet forwarded via transport")
					return
				}
				log.Debug().Msg("routing: Reticulum data packet (local handling not yet wired)")
				return
			}
		}
	}

	// Fallback: Bridge-legacy packet type bytes (0x10-0x13).
	firstByte := payload[0]
	switch firstByte {
	case routing.PacketLinkRequest:
		if p.linkMgr != nil {
			respData, err := p.linkMgr.HandleLinkRequest(payload)
			if err != nil {
				log.Debug().Err(err).Msg("routing: link request handling failed")
			} else {
				log.Debug().Msg("routing: link request processed")
				p.sendRoutingPacket(respData)
			}
		}

	case routing.PacketLinkProof:
		if p.linkMgr != nil {
			var signingPub []byte
			if p.destTable != nil {
				proof, err := routing.UnmarshalLinkProof(payload)
				if err == nil {
					link := p.linkMgr.GetPendingLink(proof.LinkID)
					if link != nil {
						dest := p.destTable.Lookup(link.DestHash)
						if dest != nil {
							signingPub = dest.SigningPub
						}
					}
				}
			}
			if err := p.linkMgr.HandleLinkProof(payload, signingPub); err != nil {
				log.Debug().Err(err).Msg("routing: link proof handling failed")
			} else {
				log.Debug().Msg("routing: link proof processed, link established")
			}
		}

	case routing.PacketKeepalive:
		if p.keepalive != nil {
			if err := p.keepalive.HandleKeepalive(payload); err != nil {
				log.Debug().Err(err).Msg("routing: keepalive handling failed")
			}
		}

	default:
		log.Debug().Int("type", int(firstByte)).Msg("routing: unknown packet type")
	}
}

// sendRoutingPacket transmits a routing protocol packet via mesh as a PRIVATE_APP raw payload.
func (p *Processor) sendRoutingPacket(data []byte) {
	if len(data) == 0 || p.mesh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := transport.RawRequest{
		PortNum: transport.PortNumPrivate,
		Payload: base64Encode(data),
	}
	if err := p.mesh.SendRaw(ctx, req); err != nil {
		log.Warn().Err(err).Int("size", len(data)).Msg("routing: failed to send packet")
	}
}

// StartGatewayReceiver drains inbound messages from a gateway and dispatches
// them through the v0.2.0 rules engine + delivery ledger.
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

				// Dispatch through rules engine
				if p.dispatcher != nil {
					routeMsg := rules.RouteMessage{
						Text: msg.Text,
						From: msg.Source,
					}
					sourceIface := msg.Source + "_0"
					// Increment ingress sequence number for the source interface
					if _, err := p.db.IncrementIngressSeq(sourceIface); err != nil {
						log.Warn().Err(err).Str("interface", sourceIface).Msg("failed to increment ingress seq")
					}
					if n := p.dispatcher.DispatchAccess(sourceIface, routeMsg, []byte(msg.Text)); n > 0 {
						log.Info().Int("deliveries", n).Str("interface", sourceIface).Msg("inbound dispatched via access rules")
					}
				}

				// Persist inbound message
				dbMsg := &database.Message{
					FromNode:    msg.Source,
					ToNode:      msg.To,
					Channel:     msg.Channel,
					PortNum:     1,
					PortNumName: "TEXT_MESSAGE_APP",
					DecodedText: msg.Text,
					Direction:   "rx",
					Transport:   msg.Source,
				}
				if err := p.db.InsertMessage(dbMsg); err != nil {
					log.Error().Err(err).Msg("failed to persist gateway inbound message")
				}

				// Mark for loop prevention
				p.markGatewayInjection(msg.Text)
			}
		}
	}()
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

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
