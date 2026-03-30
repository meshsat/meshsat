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
	"meshsat/internal/hemb"
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
	announceRelay   *routing.AnnounceRelay
	linkMgr         *routing.LinkManager
	keepalive       *routing.LinkKeepalive
	destTable       *routing.DestinationTable
	transportNode   *routing.TransportNode
	pathFinder      *routing.PathFinder
	routingIdentity *routing.Identity
	resourceXfer    *routing.ResourceTransfer

	// Packet sender: sends a Reticulum packet to a specific interface.
	// Set by main.go to route responses (link proofs, data) to TCP or mesh.
	packetSenders   map[string]func(ctx context.Context, data []byte) error
	packetSendersMu sync.RWMutex

	// Gateway injection dedup (text hash → timestamp, 5min TTL)
	relayDedupMu sync.Mutex
	relayDedup   map[string]time.Time

	// Protocol enhancements (MESHSAT-407)
	timeSyncHandler   func(data []byte, sourceIface string) // handles 0x14/0x15
	custodyHandler    func(data []byte, sourceIface string) // handles 0x16 (custody offer)
	custodyACKHandler func(data []byte)                     // handles 0x17 (custody ACK)

	// HeMB (MESHSAT-415)
	hembBonder hemb.Bonder // nil = HeMB disabled
}

// NewProcessor creates a new event processor.
func NewProcessor(db *database.DB, mesh transport.MeshTransport) *Processor {
	return &Processor{
		db:            db,
		mesh:          mesh,
		relayDedup:    make(map[string]time.Time),
		packetSenders: make(map[string]func(ctx context.Context, data []byte) error),
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

// SetPathFinder enables path discovery for unknown destinations.
func (p *Processor) SetPathFinder(pf *routing.PathFinder) {
	p.pathFinder = pf
}

// SetRoutingIdentity sets the local routing identity for packet filtering.
func (p *Processor) SetRoutingIdentity(id *routing.Identity) {
	p.routingIdentity = id
}

// SetResourceTransfer sets the resource transfer manager for chunked file delivery.
func (p *Processor) SetResourceTransfer(rt *routing.ResourceTransfer) {
	p.resourceXfer = rt
}

// SetTimeSyncHandler registers the callback for time sync packets (0x14/0x15).
func (p *Processor) SetTimeSyncHandler(fn func(data []byte, sourceIface string)) {
	p.timeSyncHandler = fn
}

// SetCustodyHandlers registers callbacks for DTN custody packets (0x16/0x17).
func (p *Processor) SetCustodyHandlers(offerFn func(data []byte, sourceIface string), ackFn func(data []byte)) {
	p.custodyHandler = offerFn
	p.custodyACKHandler = ackFn
}

// SetHeMBBonder registers the HeMB bonder for inbound symbol reassembly.
func (p *Processor) SetHeMBBonder(b hemb.Bonder) {
	p.hembBonder = b
}

// HeMBStats returns current HeMB bonding metrics, or zeros if no bonder is registered.
func (p *Processor) HeMBStats() hemb.BondStats {
	if p.hembBonder != nil {
		return p.hembBonder.Stats()
	}
	return hemb.BondStats{}
}

// HeMBActiveStreams returns active reassembly stream info.
func (p *Processor) HeMBActiveStreams() []hemb.StreamInfo {
	if p.hembBonder != nil {
		return p.hembBonder.ActiveStreams()
	}
	return nil
}

// HeMBStreamDetail returns per-generation info for a specific stream.
func (p *Processor) HeMBStreamDetail(streamID uint8) ([]hemb.GenerationInfo, bool) {
	if p.hembBonder != nil {
		return p.hembBonder.StreamDetail(streamID)
	}
	return nil, false
}

// HeMBInspectGeneration returns detailed RLNC matrix data for debugging.
func (p *Processor) HeMBInspectGeneration(streamID uint8, genID uint16) (*hemb.GenerationInspection, bool) {
	if p.hembBonder != nil {
		return p.hembBonder.InspectGeneration(streamID, genID)
	}
	return nil, false
}

// GetPacketSender returns the registered send function for a Reticulum interface.
// Implements PacketSenderProvider for HeMB bearer resolution.
func (p *Processor) GetPacketSender(ifaceID string) func(ctx context.Context, data []byte) error {
	p.packetSendersMu.RLock()
	defer p.packetSendersMu.RUnlock()
	return p.packetSenders[ifaceID]
}

// bearerIndexForIface maps a Reticulum interface name to its bearer index
// within the active bond groups. Falls back to a deterministic hash if the
// interface is not found in any bond group.
func (p *Processor) bearerIndexForIface(ifaceName string) uint8 {
	groups, err := p.db.GetAllBondGroups()
	if err == nil {
		for _, g := range groups {
			members, merr := p.db.GetBondMembers(g.ID)
			if merr != nil {
				continue
			}
			for i, m := range members {
				if m.InterfaceID == ifaceName {
					return uint8(i)
				}
			}
		}
	}
	// Fallback: deterministic index from interface name.
	var h uint8
	for _, c := range ifaceName {
		h = h*31 + uint8(c)
	}
	return h
}

// RegisterPacketSender registers a function that sends Reticulum packets to a
// specific interface (e.g. "tcp_0", "mesh_0"). Used to route link proofs and
// data packets back to the interface they were received on.
func (p *Processor) RegisterPacketSender(ifaceID string, fn func(ctx context.Context, data []byte) error) {
	p.packetSendersMu.Lock()
	defer p.packetSendersMu.Unlock()
	p.packetSenders[ifaceID] = fn
}

// SendReticulumPacketTo sends a Reticulum packet to the specified interface.
// Exported for use by ResourceTransfer send callback.
func (p *Processor) SendReticulumPacketTo(ifaceID string, data []byte) error {
	p.sendReticulumPacket(data, ifaceID)
	return nil
}

// sendReticulumPacket sends a Reticulum packet to the specified interface.
// Falls back to mesh broadcast if no specific sender is registered.
func (p *Processor) sendReticulumPacket(data []byte, ifaceID string) {
	p.packetSendersMu.RLock()
	sender, ok := p.packetSenders[ifaceID]
	p.packetSendersMu.RUnlock()

	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := sender(ctx, data); err != nil {
			log.Warn().Err(err).Str("iface", ifaceID).Msg("routing: failed to send packet to interface")
		}
		return
	}
	// Fallback to mesh broadcast
	p.sendRoutingPacket(data)
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

	// Bridge HeMB bond_stats events to the main /api/events SSE stream.
	// This dual-emits HEMB_BOND_STATS so dashboard clients on /api/events
	// see bonding metrics without subscribing to the HeMB-specific bus.
	bondStatsCh, bondStatsUnsub := hemb.GlobalEventBus.SubscribeFiltered(hemb.EventBondStats)
	go func() {
		defer bondStatsUnsub()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-bondStatsCh:
				if !ok {
					return
				}
				p.broadcast(transport.MeshEvent{
					Type:    string(evt.Type),
					Data:    evt.Payload,
					Time:    evt.Timestamp.Format(time.RFC3339),
					Message: "hemb bond stats",
				})
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
		p.handleRoutingPacket(event, msg.RawPayload, "mesh_0")
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
// type byte detection (0x10-0x13). The sourceIface parameter identifies
// which interface the packet was received on (e.g. "mesh_0", "tcp_0").
func (p *Processor) handleRoutingPacket(event transport.MeshEvent, payload []byte, sourceIface string) {
	if len(payload) == 0 {
		return
	}

	// HeMB frames: detect before any other protocol parsing.
	if len(payload) >= 2 {
		log.Debug().Str("iface", sourceIface).Int("len", len(payload)).
			Hex("head2", payload[:2]).Bool("is_hemb", hemb.IsHeMBFrame(payload)).
			Msg("hemb: handleRoutingPacket entry")
	}
	if hemb.IsHeMBFrame(payload) {
		if p.hembBonder != nil {
			bearerIdx := p.bearerIndexForIface(sourceIface)
			decoded, err := p.hembBonder.ReceiveSymbol(bearerIdx, payload)
			if err != nil {
				log.Debug().Err(err).Str("iface", sourceIface).Msg("hemb: receive symbol failed")
			}
			if decoded != nil {
				log.Info().Str("iface", sourceIface).Int("bytes", len(decoded)).Msg("hemb: cross-bearer reassembly complete")
			}
		} else {
			log.Debug().Str("iface", sourceIface).Int("len", len(payload)).Msg("hemb: frame detected but no bonder registered")
		}
		return
	}
	// Debug: log first 4 bytes of PRIVATE_APP payloads for HeMB frame analysis
	if sourceIface == "mesh_0" && len(payload) >= 4 {
		log.Debug().Str("iface", sourceIface).Int("len", len(payload)).
			Hex("head", payload[:4]).
			Bool("is_hemb", hemb.IsHeMBFrame(payload)).
			Msg("hemb: mesh PRIVATE_APP payload inspection")
	}

	// Bridge protocol extension type bytes (0x14-0x17) must be checked BEFORE
	// Reticulum header parsing. These bytes are valid Reticulum flags values
	// (e.g. 0x14 = PacketData+DestGroup+Transport), so UnmarshalHeader would
	// succeed and misinterpret the packet as a data packet with a garbage dest hash.
	firstByte := payload[0]
	switch firstByte {
	case 0x14, 0x15: // BridgeTimeSyncReq, BridgeTimeSyncResp
		if p.timeSyncHandler != nil {
			log.Info().Int("type", int(firstByte)).Str("iface", sourceIface).Int("size", len(payload)).
				Msg("routing: time sync packet received")
			p.timeSyncHandler(payload, sourceIface)
		}
		return
	case 0x16: // BridgeCustodyOffer — another node is offering us custody of a payload.
		if p.custodyHandler != nil {
			log.Info().Str("iface", sourceIface).Int("size", len(payload)).
				Msg("routing: custody offer received")
			p.custodyHandler(payload, sourceIface)
		}
		return
	case 0x17: // BridgeCustodyACK — a relay accepted our custody offer.
		if p.custodyACKHandler != nil {
			log.Info().Str("iface", sourceIface).Int("size", len(payload)).
				Msg("routing: custody ack received")
			p.custodyACKHandler(payload)
		}
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
					if p.announceRelay.HandleAnnounce(ctx, payload, sourceIface) {
						log.Debug().Int("size", len(payload)).Str("iface", sourceIface).Msg("routing: Reticulum announce processed")
						// Feed to transport node routing table
						if p.transportNode != nil {
							ann, err := routing.UnmarshalAnnounce(payload)
							if err == nil {
								p.transportNode.ProcessAnnounce(ann, sourceIface)
							}
						}
					}
				}
				return

			case reticulum.PacketLinkRequest:
				// Try transport forwarding first (relay to dest if not for us)
				if p.transportNode != nil && p.transportNode.ForwardPacket(payload, sourceIface) {
					log.Debug().Msg("routing: Reticulum link request forwarded via transport")
					return
				}
				// Local handling: if addressed to us, process the link request
				if p.linkMgr != nil && p.isAddressedToUs(hdr) {
					respData, err := p.linkMgr.HandleLinkRequest(hdr.Data)
					if err != nil {
						log.Debug().Err(err).Msg("routing: Reticulum link request handling failed")
					} else {
						log.Info().Str("iface", sourceIface).Msg("routing: Reticulum link request accepted, sending proof")
						// Wrap proof in Reticulum header and send back
						proofPkt := &reticulum.Header{
							PacketType: reticulum.PacketProof,
							DestType:   hdr.DestType,
							DestHash:   hdr.DestHash,
						}
						proofPkt.Data = respData
						p.sendReticulumPacket(proofPkt.Marshal(), sourceIface)
					}
				} else {
					log.Debug().Msg("routing: Reticulum link request not for us, no transport route")
				}
				return

			case reticulum.PacketProof:
				if p.transportNode != nil && p.transportNode.ForwardPacket(payload, sourceIface) {
					log.Debug().Msg("routing: Reticulum proof forwarded via transport")
					return
				}
				// Local handling: if we have a pending link, process the proof
				if p.linkMgr != nil && len(hdr.Data) > 0 {
					var signingPub []byte
					if p.destTable != nil {
						proof, err := routing.UnmarshalLinkProof(hdr.Data)
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
					if err := p.linkMgr.HandleLinkProof(hdr.Data, signingPub); err != nil {
						log.Debug().Err(err).Msg("routing: Reticulum proof handling failed")
					} else {
						log.Info().Str("iface", sourceIface).Msg("routing: Reticulum proof processed, link established")
					}
				}
				return

			case reticulum.PacketData:
				// Check context for path discovery and resource transfer packets
				switch hdr.Context {
				case reticulum.ContextRequest:
					if p.pathFinder != nil {
						p.pathFinder.HandlePathRequest(hdr.Data, sourceIface)
						return
					}
				case reticulum.ContextPathResponse:
					if p.pathFinder != nil {
						p.pathFinder.HandlePathResponse(hdr.Data, sourceIface)
						return
					}
				case reticulum.ContextResourceAdv:
					if p.resourceXfer != nil {
						p.resourceXfer.HandleAdvertisement(hdr.Data, sourceIface)
						return
					}
				case reticulum.ContextResourceReq:
					if p.resourceXfer != nil {
						p.resourceXfer.HandleRequest(hdr.Data, sourceIface)
						return
					}
				case reticulum.ContextResource:
					if p.resourceXfer != nil {
						p.resourceXfer.HandleSegment(hdr.Data, sourceIface)
						return
					}
				case reticulum.ContextResourcePRF:
					if p.resourceXfer != nil {
						p.resourceXfer.HandleProof(hdr.Data)
						return
					}
				case reticulum.ContextResourceRLNC:
					if p.resourceXfer != nil {
						log.Info().Int("size", len(hdr.Data)).Str("iface", sourceIface).
							Msg("routing: RLNC coded resource segment received")
						p.resourceXfer.HandleCodedSegment(hdr.Data, sourceIface)
						return
					}
				}
				// HeMB frame inside Reticulum data packet — route to reassembler.
				if len(hdr.Data) > 0 && hemb.IsHeMBFrame(hdr.Data) {
					if p.hembBonder != nil {
						bearerIdx := p.bearerIndexForIface(sourceIface)
						decoded, err := p.hembBonder.ReceiveSymbol(bearerIdx, hdr.Data)
						if err != nil {
							log.Debug().Err(err).Str("iface", sourceIface).Msg("hemb: receive symbol failed (Reticulum-wrapped)")
						}
						if decoded != nil {
							log.Info().Str("iface", sourceIface).Int("bytes", len(decoded)).Msg("hemb: cross-bearer reassembly complete")
						}
					}
					return
				}
				if p.transportNode != nil && p.transportNode.ForwardPacket(payload, sourceIface) {
					log.Debug().Msg("routing: Reticulum data packet forwarded via transport")
					return
				}
				// Local handling: if addressed to us and we have an established link, decrypt
				if p.isAddressedToUs(hdr) && p.linkMgr != nil && len(hdr.Data) > 0 {
					log.Info().Int("size", len(hdr.Data)).Str("iface", sourceIface).
						Msg("routing: Reticulum data packet received for local identity")
				} else {
					log.Debug().Msg("routing: Reticulum data packet not for us, no transport route")
				}
				return
			}
		}
	}

	// Fallback: Bridge-legacy packet type bytes (0x10-0x13).
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

// isAddressedToUs checks if a Reticulum packet's destination hash matches our identity.
func (p *Processor) isAddressedToUs(hdr *reticulum.Header) bool {
	if p.routingIdentity == nil {
		return false
	}
	ourHash := p.routingIdentity.DestHash()
	return hdr.DestHash == ourHash
}

// InjectReticulumPacket feeds a raw Reticulum packet into the routing subsystem
// as if it arrived from the named interface. This is used by non-mesh transports
// (e.g. TCPInterface) that deliver already-unframed Reticulum packets.
func (p *Processor) InjectReticulumPacket(packet []byte, sourceIface string) {
	if len(packet) == 0 {
		return
	}
	p.handleRoutingPacket(transport.MeshEvent{}, packet, sourceIface)
}

// isPaidInterface returns true for satellite and cellular interfaces that cost
// money per message. Protocol overhead (time sync, keepalive, announces) must
// NEVER be sent over these interfaces automatically.
func isPaidInterface(id string) bool {
	switch {
	case len(id) >= 7 && id[:7] == "iridium":
		return true // iridium_0, iridium_imt_0
	case len(id) >= 9 && id[:9] == "astrocast":
		return true // astrocast_0
	case len(id) >= 8 && id[:8] == "cellular":
		return true // cellular_0
	}
	return false
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

// BroadcastRoutingPacket sends a protocol packet to FREE interfaces only
// (mesh, TCP, MQTT, AX.25). NEVER sends to paid satellite or cellular
// interfaces — those cost real money per message.
func (p *Processor) BroadcastRoutingPacket(data []byte) {
	// Send to mesh (free, LoRa)
	p.sendRoutingPacket(data)
	// Send to free registered packet senders (TCP, MQTT, AX.25) — skip paid interfaces.
	p.packetSendersMu.RLock()
	senders := make(map[string]func(ctx context.Context, data []byte) error, len(p.packetSenders))
	for k, v := range p.packetSenders {
		// Skip ALL paid satellite and cellular interfaces.
		if isPaidInterface(k) {
			continue
		}
		senders[k] = v
	}
	p.packetSendersMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for id, sender := range senders {
		if err := sender(ctx, data); err != nil {
			log.Debug().Err(err).Str("iface", id).Msg("routing: broadcast packet send failed")
		}
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
