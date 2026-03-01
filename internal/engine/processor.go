package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// Processor ingests mesh events, persists them, and routes to gateways.
type Processor struct {
	db       *database.DB
	mesh     transport.MeshTransport
	gateways []gateway.Gateway
	subs     []chan transport.MeshEvent // SSE re-broadcast subscribers
	subsMu   sync.RWMutex
}

// NewProcessor creates a new event processor.
func NewProcessor(db *database.DB, mesh transport.MeshTransport) *Processor {
	return &Processor{
		db:   db,
		mesh: mesh,
	}
}

// AddGateway registers a gateway for message forwarding.
func (p *Processor) AddGateway(gw gateway.Gateway) {
	p.gateways = append(p.gateways, gw)
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

	// Dedupe by packet ID
	exists, err := p.db.HasPacket(msg.ID)
	if err != nil {
		log.Error().Err(err).Msg("dedupe check failed")
	}
	if exists {
		log.Debug().Uint32("packet_id", msg.ID).Msg("duplicate packet, skipping")
		return
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

	// Forward to active gateways
	for _, gw := range p.gateways {
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

	// Persist telemetry if battery/voltage data present
	if node.BatteryLevel > 0 || node.Voltage > 0 {
		t := &database.Telemetry{
			NodeID:       node.UserID,
			BatteryLevel: node.BatteryLevel,
			Voltage:      node.Voltage,
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
