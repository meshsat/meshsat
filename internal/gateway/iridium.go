package gateway

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// IridiumGateway bridges Meshtastic mesh messages to/from an Iridium satellite modem.
type IridiumGateway struct {
	config IridiumConfig
	sat    transport.SatTransport
	inCh   chan InboundMessage

	outCh chan *transport.MeshMessage // buffered outbound queue

	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	lastActive atomic.Int64
	startTime  time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewIridiumGateway creates a new Iridium satellite gateway.
func NewIridiumGateway(cfg IridiumConfig, sat transport.SatTransport) *IridiumGateway {
	return &IridiumGateway{
		config: cfg,
		sat:    sat,
		inCh:   make(chan InboundMessage, 32),
		outCh:  make(chan *transport.MeshMessage, 10),
	}
}

// Start subscribes to HAL Iridium SSE for ring alerts and starts the send worker.
func (g *IridiumGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()

	// Check modem status
	status, err := g.sat.GetStatus(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("iridium: could not get modem status")
	} else {
		g.connected.Store(status.Connected)
	}

	// Start outbound send worker
	g.wg.Add(1)
	go g.sendWorker(ctx)

	// Start SSE listener for ring alerts
	if g.config.AutoReceive {
		g.wg.Add(1)
		go g.ringAlertListener(ctx)
	}

	// Start optional poll worker
	if g.config.PollInterval > 0 {
		g.wg.Add(1)
		go g.pollWorker(ctx)
	}

	log.Info().Bool("auto_receive", g.config.AutoReceive).Int("poll_interval", g.config.PollInterval).Msg("iridium gateway started")
	return nil
}

// Stop shuts down the gateway.
func (g *IridiumGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	g.connected.Store(false)
	log.Info().Msg("iridium gateway stopped")
	return nil
}

// Forward checks forwarding rules and enqueues a message for satellite transmission.
func (g *IridiumGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	if !g.shouldForward(msg) {
		return nil
	}

	select {
	case g.outCh <- msg:
		return nil
	default:
		g.errors.Add(1)
		return fmt.Errorf("iridium outbound queue full")
	}
}

// Receive returns the inbound message channel.
func (g *IridiumGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *IridiumGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "iridium",
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
func (g *IridiumGateway) Type() string {
	return "iridium"
}

func (g *IridiumGateway) shouldForward(msg *transport.MeshMessage) bool {
	if g.config.ForwardAll {
		return true
	}
	for _, pn := range g.config.ForwardPortnums {
		if msg.PortNum == pn {
			return true
		}
	}
	return false
}

// sendWorker dequeues messages and sends them via SBD (slow, 10-60s per message).
func (g *IridiumGateway) sendWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			data, err := EncodeCompact(msg, g.config.IncludePosition)
			if err != nil {
				log.Error().Err(err).Msg("iridium: encode failed")
				g.errors.Add(1)
				continue
			}

			result, err := g.sat.Send(ctx, data)
			if err != nil {
				log.Error().Err(err).Msg("iridium: SBD send failed")
				g.errors.Add(1)
				continue
			}

			g.msgsOut.Add(1)
			g.lastActive.Store(time.Now().Unix())
			log.Info().Int("mo_status", result.MOStatus).Uint32("packet_id", msg.ID).Msg("iridium: SBD sent")
		}
	}
}

// ringAlertListener subscribes to HAL Iridium SSE for ring alert events.
func (g *IridiumGateway) ringAlertListener(ctx context.Context) {
	defer g.wg.Done()

	events, err := g.sat.Subscribe(ctx)
	if err != nil {
		log.Error().Err(err).Msg("iridium: failed to subscribe to SSE")
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
			if event.Type == "ring_alert" {
				g.handleRingAlert(ctx)
			}
			if event.Type == "status" {
				// Update connected state from SSE status events
				g.connected.Store(true)
			}
		}
	}
}

func (g *IridiumGateway) handleRingAlert(ctx context.Context) {
	log.Info().Msg("iridium: ring alert, checking mailbox")

	result, err := g.sat.MailboxCheck(ctx)
	if err != nil {
		log.Error().Err(err).Msg("iridium: mailbox check failed")
		g.errors.Add(1)
		return
	}

	if result.MTStatus != 1 || result.MTLength == 0 {
		return // no message waiting
	}

	data, err := g.sat.Receive(ctx)
	if err != nil {
		log.Error().Err(err).Msg("iridium: receive failed")
		g.errors.Add(1)
		return
	}

	inbound, err := DecodeCompact(data)
	if err != nil {
		log.Error().Err(err).Msg("iridium: decode failed")
		g.errors.Add(1)
		return
	}

	g.msgsIn.Add(1)
	g.lastActive.Store(time.Now().Unix())

	select {
	case g.inCh <- *inbound:
	default:
		log.Warn().Msg("iridium: inbound channel full")
	}
}

// pollWorker periodically checks for MT messages.
func (g *IridiumGateway) pollWorker(ctx context.Context) {
	defer g.wg.Done()
	ticker := time.NewTicker(time.Duration(g.config.PollInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.handleRingAlert(ctx) // reuse the same logic
		}
	}
}

// --- Compact binary codec for 340-byte SBD ---

const (
	compactVersion    = 1
	flagHasPosition   = 0x04
	flagHasSender     = 0x08
	maxSBDPayload     = 340
	positionFieldSize = 10 // lat(4) + lon(4) + alt(2)
)

// EncodeCompact encodes a mesh message into compact binary for SBD transmission.
func EncodeCompact(msg *transport.MeshMessage, includePosition bool) ([]byte, error) {
	buf := make([]byte, 0, maxSBDPayload)

	// Byte 0: flags
	flags := byte(compactVersion & 0x03)
	flags |= flagHasSender // always include sender
	if includePosition {
		flags |= flagHasPosition
	}
	buf = append(buf, flags)

	// Byte 1: portnum
	buf = append(buf, byte(msg.PortNum))

	// Bytes 2-5: from node (uint32 BE)
	fromBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(fromBytes, msg.From)
	buf = append(buf, fromBytes...)

	// Bytes 6-9: timestamp (uint32 BE)
	tsBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(tsBytes, uint32(msg.RxTime))
	buf = append(buf, tsBytes...)

	// Optional position (10 bytes)
	if includePosition {
		// We don't have position data on MeshMessage, encode zeros
		posBytes := make([]byte, positionFieldSize)
		buf = append(buf, posBytes...)
	}

	// Text
	text := []byte(msg.DecodedText)
	maxText := maxSBDPayload - len(buf) - 1 // -1 for length byte
	if len(text) > maxText {
		text = text[:maxText]
	}
	buf = append(buf, byte(len(text)))
	buf = append(buf, text...)

	return buf, nil
}

// DecodeCompact decodes compact binary from an SBD MT message.
func DecodeCompact(data []byte) (*InboundMessage, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("data too short")
	}

	flags := data[0]
	offset := 1

	// Portnum
	_ = data[offset] // portnum, not needed for inbound
	offset++

	// From node (optional)
	if flags&flagHasSender != 0 {
		if len(data) < offset+4 {
			return nil, fmt.Errorf("data too short for sender")
		}
		offset += 4
	}

	// Timestamp
	if len(data) < offset+4 {
		return nil, fmt.Errorf("data too short for timestamp")
	}
	offset += 4

	// Position (optional)
	if flags&flagHasPosition != 0 {
		if len(data) < offset+positionFieldSize {
			return nil, fmt.Errorf("data too short for position")
		}
		offset += positionFieldSize
	}

	// Text
	if len(data) < offset+1 {
		return nil, fmt.Errorf("data too short for text length")
	}
	textLen := int(data[offset])
	offset++

	if len(data) < offset+textLen {
		return nil, fmt.Errorf("data too short for text")
	}
	text := string(data[offset : offset+textLen])

	return &InboundMessage{
		Text:   text,
		Source: "iridium",
	}, nil
}

// EncodePosition encodes lat/lon/alt into the position bytes of a compact message.
func EncodePosition(lat, lon float64, alt int16) []byte {
	buf := make([]byte, positionFieldSize)
	binary.BigEndian.PutUint32(buf[0:4], uint32(int32(math.Round(lat*1e7))))
	binary.BigEndian.PutUint32(buf[4:8], uint32(int32(math.Round(lon*1e7))))
	binary.BigEndian.PutUint16(buf[8:10], uint16(alt))
	return buf
}
