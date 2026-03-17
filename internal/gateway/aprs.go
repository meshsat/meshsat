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

// APRSGateway bridges MeshSat messages to/from APRS via Direwolf KISS TCP.
type APRSGateway struct {
	config APRSConfig
	db     *database.DB
	kiss   *KISSConn
	inCh   chan InboundMessage
	outCh  chan *transport.MeshMessage

	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	lastActive atomic.Int64
	startTime  time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewAPRSGateway creates a new APRS gateway.
func NewAPRSGateway(cfg APRSConfig, db *database.DB) *APRSGateway {
	addr := fmt.Sprintf("%s:%d", cfg.KISSHost, cfg.KISSPort)
	return &APRSGateway{
		config: cfg,
		db:     db,
		kiss:   NewKISSConn(addr),
		inCh:   make(chan InboundMessage, 32),
		outCh:  make(chan *transport.MeshMessage, 10),
	}
}

// Start connects to Direwolf and begins read/write workers.
func (g *APRSGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()

	if err := g.kiss.Dial(); err != nil {
		return fmt.Errorf("aprs: %w", err)
	}
	g.connected.Store(true)

	g.wg.Add(2)
	go g.readWorker(ctx)
	go g.writeWorker(ctx)

	log.Info().
		Str("kiss_addr", fmt.Sprintf("%s:%d", g.config.KISSHost, g.config.KISSPort)).
		Str("callsign", FormatCallsign(AX25Address{Call: g.config.Callsign, SSID: g.config.SSID})).
		Float64("freq_mhz", g.config.FrequencyMHz).
		Msg("aprs gateway started")
	return nil
}

// Stop shuts down the APRS gateway.
func (g *APRSGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.kiss.Close()
	g.wg.Wait()
	g.connected.Store(false)
	log.Info().Msg("aprs gateway stopped")
	return nil
}

// Forward enqueues a MeshSat message for APRS transmission.
func (g *APRSGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	select {
	case g.outCh <- msg:
		return nil
	default:
		g.errors.Add(1)
		return fmt.Errorf("aprs outbound queue full")
	}
}

// Receive returns the inbound message channel.
func (g *APRSGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *APRSGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "aprs",
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
func (g *APRSGateway) Type() string {
	return "aprs"
}

// readWorker reads APRS packets from Direwolf via KISS.
func (g *APRSGateway) readWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		payload, err := g.kiss.ReadFrame()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Timeout is normal — just retry
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				continue
			}
			log.Warn().Err(err).Msg("aprs: read frame error")
			g.errors.Add(1)
			g.connected.Store(false)
			g.reconnect(ctx)
			continue
		}

		frame, err := DecodeAX25Frame(payload)
		if err != nil {
			log.Debug().Err(err).Msg("aprs: decode AX.25")
			continue
		}

		pkt, err := ParseAPRSPacket(frame)
		if err != nil {
			log.Debug().Err(err).Msg("aprs: parse APRS")
			continue
		}

		text := g.formatInboundText(pkt)
		msg := InboundMessage{
			Text:   text,
			Source: "aprs",
		}

		select {
		case g.inCh <- msg:
			g.msgsIn.Add(1)
			g.lastActive.Store(time.Now().Unix())
		default:
			log.Warn().Msg("aprs: inbound channel full")
		}
	}
}

// writeWorker sends MeshSat messages as APRS packets via KISS.
func (g *APRSGateway) writeWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			g.sendMessage(msg)
		}
	}
}

func (g *APRSGateway) sendMessage(msg *transport.MeshMessage) {
	src := AX25Address{Call: g.config.Callsign, SSID: g.config.SSID}
	dst := AX25Address{Call: "APMSHT", SSID: 0} // APMSxx = MeshSat tocall
	path := []AX25Address{{Call: "WIDE1", SSID: 1}, {Call: "WIDE2", SSID: 1}}

	// Default: send as position-less message via the APRS bulletin/message system
	var info []byte
	if msg.DecodedText != "" {
		// Send as third-party traffic with attribution
		comment := fmt.Sprintf("[MeshSat !%08x] %s", msg.From, msg.DecodedText)
		info = EncodeAPRSPosition(0, 0, '/', '-', comment) // 0,0 = no position
	} else {
		info = []byte(fmt.Sprintf(">MeshSat bridge: packet from !%08x", msg.From))
	}

	frame := EncodeAX25Frame(dst, src, path, info)
	if err := g.kiss.SendFrame(frame); err != nil {
		log.Warn().Err(err).Msg("aprs: send frame")
		g.errors.Add(1)
		return
	}

	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	log.Debug().Str("callsign", FormatCallsign(src)).Msg("aprs: sent packet")
}

func (g *APRSGateway) formatInboundText(pkt *APRSPacket) string {
	switch pkt.DataType {
	case '!', '=', '/', '@': // Position
		return fmt.Sprintf("[APRS:%s] %.4f,%.4f %s", pkt.Source, pkt.Lat, pkt.Lon, pkt.Comment)
	case ':': // Message
		return fmt.Sprintf("[APRS:%s→%s] %s", pkt.Source, pkt.MsgTo, pkt.Message)
	default:
		return fmt.Sprintf("[APRS:%s] %s", pkt.Source, pkt.Raw)
	}
}

func (g *APRSGateway) reconnect(ctx context.Context) {
	wait := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		if err := g.kiss.Dial(); err != nil {
			log.Warn().Err(err).Dur("retry_in", wait).Msg("aprs: reconnect failed")
			wait *= 2
			if wait > 5*time.Minute {
				wait = 5 * time.Minute
			}
			continue
		}

		g.connected.Store(true)
		log.Info().Msg("aprs: reconnected to Direwolf")
		return
	}
}
