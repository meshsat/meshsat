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

// AstrocastGateway bridges Meshtastic mesh messages to/from an Astrocast Astronode S module.
type AstrocastGateway struct {
	config     AstrocastConfig
	astro      transport.AstrocastTransport
	db         *database.DB
	inCh       chan InboundMessage
	outCh      chan *transport.MeshMessage
	reassembly *transport.ReassemblyBuffer

	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	lastActive atomic.Int64
	startTime  time.Time
	nextMsgID  atomic.Uint32

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewAstrocastGateway creates a new Astrocast satellite gateway.
func NewAstrocastGateway(cfg AstrocastConfig, astro transport.AstrocastTransport, db *database.DB) *AstrocastGateway {
	return &AstrocastGateway{
		config:     cfg,
		astro:      astro,
		db:         db,
		inCh:       make(chan InboundMessage, 32),
		outCh:      make(chan *transport.MeshMessage, 10),
		reassembly: transport.NewReassemblyBuffer(),
	}
}

// Start subscribes to Astrocast events and starts workers.
func (g *AstrocastGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()

	status, err := g.astro.GetStatus(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("astrocast: could not get module status")
	} else {
		g.connected.Store(status.Connected)
	}

	g.wg.Add(1)
	go g.sendWorker(ctx)

	g.wg.Add(1)
	go g.receiveWorker(ctx)

	log.Info().
		Bool("fragment", g.config.FragmentEnabled).
		Str("power_mode", g.config.PowerMode).
		Int("poll_sec", g.config.PollIntervalSec).
		Msg("astrocast gateway started")
	return nil
}

// Stop shuts down the gateway.
func (g *AstrocastGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	g.connected.Store(false)
	log.Info().Msg("astrocast gateway stopped")
	return nil
}

// Forward enqueues a message for Astrocast uplink.
func (g *AstrocastGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	select {
	case g.outCh <- msg:
		return nil
	default:
		g.errors.Add(1)
		return fmt.Errorf("astrocast outbound queue full")
	}
}

// Enqueue submits a message for outbound delivery via the gateway.
func (g *AstrocastGateway) Enqueue(msg *transport.MeshMessage) error {
	return g.Forward(context.Background(), msg)
}

// Receive returns the inbound message channel.
func (g *AstrocastGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *AstrocastGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "astrocast",
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
func (g *AstrocastGateway) Type() string {
	return "astrocast"
}

// sendWorker dequeues messages, fragments if needed, and enqueues uplinks.
func (g *AstrocastGateway) sendWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			g.sendMessage(ctx, msg)
		}
	}
}

func (g *AstrocastGateway) sendMessage(ctx context.Context, msg *transport.MeshMessage) {
	data := []byte(msg.DecodedText)

	// Fragment if needed and enabled
	if g.config.FragmentEnabled && len(data) > transport.AstroMaxUplink {
		msgID := uint8(g.nextMsgID.Add(1) & 0x0F)
		fragments := transport.FragmentMessage(msgID, data)
		if fragments != nil {
			for i, frag := range fragments {
				if _, err := g.astro.Send(ctx, frag); err != nil {
					log.Error().Err(err).Int("frag", i).Int("total", len(fragments)).Msg("astrocast: fragment send failed")
					g.errors.Add(1)
					return
				}
				log.Debug().Int("frag", i+1).Int("total", len(fragments)).Int("bytes", len(frag)).Msg("astrocast: fragment enqueued")
			}
			g.msgsOut.Add(1)
			g.lastActive.Store(time.Now().Unix())
			log.Info().Int("fragments", len(fragments)).Int("total_bytes", len(data)).Msg("astrocast: fragmented message enqueued")
			return
		}
	}

	// Truncate to max uplink if fragmentation disabled
	if len(data) > g.config.MaxUplinkBytes {
		data = data[:g.config.MaxUplinkBytes]
	}

	if _, err := g.astro.Send(ctx, data); err != nil {
		log.Error().Err(err).Msg("astrocast: send failed")
		g.errors.Add(1)
		return
	}

	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	log.Info().Int("bytes", len(data)).Msg("astrocast: message enqueued for uplink")
}

// receiveWorker polls for downlink events and reassembles fragmented messages.
func (g *AstrocastGateway) receiveWorker(ctx context.Context) {
	defer g.wg.Done()

	ticker := time.NewTicker(time.Duration(g.config.PollIntervalSec) * time.Second)
	defer ticker.Stop()

	// Also subscribe to events for push-based notification
	events, err := g.astro.Subscribe(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("astrocast: subscribe failed, falling back to polling only")
		events = nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.pollDownlink(ctx)
		case evt, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if evt.Type == "downlink" {
				g.pollDownlink(ctx)
			}
		}

		// Expire stale reassembly entries (5 minute timeout)
		g.reassembly.Expire(time.Now().Unix(), 300)
	}
}

func (g *AstrocastGateway) pollDownlink(ctx context.Context) {
	data, err := g.astro.Receive(ctx)
	if err != nil {
		// No downlink available is not an error
		return
	}
	if len(data) == 0 {
		return
	}

	// Check if this is a fragment (has fragment header)
	if len(data) > 1 {
		msgID, fragNum, fragTotal := transport.DecodeFragmentHeader(data[0])
		if fragTotal > 1 && fragTotal <= 4 {
			// This is a fragment
			frag := transport.AstroFragment{
				MsgID:     msgID,
				FragNum:   fragNum,
				FragTotal: fragTotal,
				Payload:   data[1:],
			}
			reassembled := g.reassembly.AddFragment(frag, time.Now().Unix())
			if reassembled == nil {
				log.Debug().Uint8("msg_id", msgID).Uint8("frag", fragNum+1).Uint8("total", fragTotal).Msg("astrocast: fragment received, waiting for more")
				return
			}
			data = reassembled
			log.Info().Uint8("msg_id", msgID).Int("bytes", len(data)).Msg("astrocast: message reassembled from fragments")
		}
	}

	inbound := InboundMessage{
		Text:   string(data),
		Source: "astrocast",
	}

	g.msgsIn.Add(1)
	g.lastActive.Store(time.Now().Unix())

	select {
	case g.inCh <- inbound:
	default:
		log.Warn().Msg("astrocast: inbound channel full")
	}
}
