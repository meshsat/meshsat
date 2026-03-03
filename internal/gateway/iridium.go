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

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// IridiumGateway bridges Meshtastic mesh messages to/from an Iridium satellite modem.
type IridiumGateway struct {
	config IridiumConfig
	sat    transport.SatTransport
	db     *database.DB
	inCh   chan InboundMessage

	outCh chan *transport.MeshMessage // buffered outbound queue

	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	dlqPending atomic.Int64
	lastActive atomic.Int64
	startTime  time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewIridiumGateway creates a new Iridium satellite gateway.
func NewIridiumGateway(cfg IridiumConfig, sat transport.SatTransport, db *database.DB) *IridiumGateway {
	return &IridiumGateway{
		config: cfg,
		sat:    sat,
		db:     db,
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

	// Load pending DLQ count
	if g.db != nil {
		if count, err := g.db.CountPendingDeadLetters(); err == nil {
			g.dlqPending.Store(int64(count))
			if count > 0 {
				log.Info().Int("pending", count).Msg("iridium: dead-letter queue has pending retries")
			}
		}
	}

	// Start outbound send worker
	g.wg.Add(1)
	go g.sendWorker(ctx)

	// Start DLQ retry worker
	if g.db != nil {
		g.wg.Add(1)
		go g.dlqRetryWorker(ctx)
	}

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

// Forward enqueues a message for satellite transmission.
// Filtering is handled by the rules engine before this is called.
func (g *IridiumGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
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
		DLQPending:  g.dlqPending.Load(),
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

			// Budget enforcement: calculate cost and check limits
			cost := creditCost(len(data))
			if !g.budgetAllows(cost, 1) { // default priority = normal
				log.Warn().Int("cost", cost).Uint32("packet_id", msg.ID).Msg("iridium: budget exceeded, queuing to DLQ")
				g.enqueueDeadLetter(msg.ID, data, "budget exceeded")
				continue
			}

			result, err := g.sat.Send(ctx, data)
			if err != nil {
				log.Error().Err(err).Uint32("packet_id", msg.ID).Msg("iridium: SBD send failed, queuing to DLQ")
				g.errors.Add(1)
				g.enqueueDeadLetter(msg.ID, data, err.Error())
				continue
			}

			g.msgsOut.Add(1)
			g.lastActive.Store(time.Now().Unix())
			log.Info().Int("mo_status", result.MOStatus).Uint32("packet_id", msg.ID).Msg("iridium: SBD sent")

			// Record credit usage
			if g.db != nil {
				g.db.InsertCreditUsage(nil, cost, nil)
			}

			// MT piggyback: check if there are queued MT messages from this session
			if result.MTQueued > 0 {
				log.Info().Int("mt_queued", result.MTQueued).Msg("iridium: MT messages queued, piggybacking receive")
				go g.handleRingAlert(ctx)
			}
		}
	}
}

// creditCost calculates SBD credits for a payload (1 credit per 50 bytes, minimum 1).
func creditCost(payloadLen int) int {
	if payloadLen <= 0 {
		return 1
	}
	cost := (payloadLen + 49) / 50
	if cost < 1 {
		return 1
	}
	return cost
}

// budgetAllows checks if a send is within daily/monthly credit limits.
// Priority 0 (critical) always passes, using the critical reserve.
func (g *IridiumGateway) budgetAllows(cost int, priority int) bool {
	if g.db == nil {
		return true
	}

	// Critical priority always allowed
	if priority == 0 {
		return true
	}

	// Check daily budget
	if g.config.DailyBudget > 0 {
		daily, err := g.db.GetDailyCreditTotal()
		if err == nil && daily+cost > g.config.DailyBudget {
			return false
		}
	}

	// Check monthly budget
	if g.config.MonthlyBudget > 0 {
		monthly, err := g.db.GetMonthlyCreditTotal()
		if err == nil && monthly+cost > g.config.MonthlyBudget {
			return false
		}
	}

	return true
}

// enqueueDeadLetter persists a failed send to the database for later retry.
func (g *IridiumGateway) enqueueDeadLetter(packetID uint32, payload []byte, errMsg string) {
	if g.db == nil {
		return
	}

	retryBase := g.config.DLQRetryBase
	if retryBase <= 0 {
		retryBase = 120
	}
	nextRetry := time.Now().Add(time.Duration(retryBase) * time.Second)

	maxRetries := g.config.DLQMaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	if err := g.db.InsertDeadLetter(packetID, payload, maxRetries, nextRetry, errMsg); err != nil {
		log.Error().Err(err).Uint32("packet_id", packetID).Msg("iridium: failed to enqueue dead letter")
		return
	}

	g.dlqPending.Add(1)
	log.Info().Uint32("packet_id", packetID).Time("next_retry", nextRetry).Msg("iridium: message queued in DLQ")
}

// dlqRetryWorker periodically retries failed sends from the dead-letter queue.
func (g *IridiumGateway) dlqRetryWorker(ctx context.Context) {
	defer g.wg.Done()

	retryBase := g.config.DLQRetryBase
	if retryBase <= 0 {
		retryBase = 120
	}

	// Check every 30s for pending retries
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.processDLQ(ctx, retryBase)
		}
	}
}

// processDLQ attempts to resend pending dead letters.
func (g *IridiumGateway) processDLQ(ctx context.Context, retryBase int) {
	pending, err := g.db.GetPendingDeadLetters(5)
	if err != nil {
		log.Error().Err(err).Msg("iridium: failed to query DLQ")
		return
	}

	for _, dl := range pending {
		select {
		case <-ctx.Done():
			return
		default:
		}

		result, err := g.sat.Send(ctx, dl.Payload)
		if err != nil {
			// Increment retry, check if exhausted
			if dl.Retries+1 >= dl.MaxRetries {
				if expErr := g.db.ExpireDeadLetter(dl.ID, err.Error()); expErr != nil {
					log.Error().Err(expErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to expire dead letter")
				}
				g.dlqPending.Add(-1)
				g.errors.Add(1)
				log.Warn().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("retries", dl.Retries+1).Msg("iridium: DLQ message expired after max retries")
			} else {
				// Exponential backoff: base * 2^retries
				backoff := time.Duration(retryBase) * time.Second * (1 << uint(dl.Retries+1))
				if backoff > 30*time.Minute {
					backoff = 30 * time.Minute
				}
				nextRetry := time.Now().Add(backoff)
				if updErr := g.db.UpdateDeadLetterRetry(dl.ID, nextRetry, err.Error()); updErr != nil {
					log.Error().Err(updErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to update DLQ retry")
				}
				log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("retry", dl.Retries+1).Time("next_retry", nextRetry).Msg("iridium: DLQ retry failed, rescheduled")
			}
			continue
		}

		// Success
		if markErr := g.db.MarkDeadLetterSent(dl.ID); markErr != nil {
			log.Error().Err(markErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to mark dead letter sent")
		}
		g.dlqPending.Add(-1)
		g.msgsOut.Add(1)
		g.lastActive.Store(time.Now().Unix())
		log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("mo_status", result.MOStatus).Int("retry", dl.Retries+1).Msg("iridium: DLQ message sent successfully")
	}
}

// ringAlertListener subscribes to HAL Iridium SSE for ring alert and signal events.
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
			switch event.Type {
			case "ring_alert":
				g.handleRingAlert(ctx)
			case "signal":
				// Opportunistic DLQ drain: if signal is sufficient and DLQ has pending entries,
				// drain them now rather than waiting for the periodic retry worker.
				minBars := g.config.MinSignalBars
				if minBars <= 0 {
					minBars = 1
				}
				if event.Signal >= minBars && g.dlqPending.Load() > 0 {
					go g.drainDLQ(ctx)
				}
			case "status", "connected", "reconnected":
				g.connected.Store(true)
			case "disconnected":
				g.connected.Store(false)
			}
		}
	}
}

// drainDLQ attempts to send all pending DLQ messages immediately.
// Called opportunistically when a good signal event arrives.
func (g *IridiumGateway) drainDLQ(ctx context.Context) {
	if g.db == nil {
		return
	}
	retryBase := g.config.DLQRetryBase
	if retryBase <= 0 {
		retryBase = 120
	}
	log.Info().Int64("pending", g.dlqPending.Load()).Msg("iridium: opportunistic DLQ drain triggered by signal event")
	g.processDLQ(ctx, retryBase)
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

	// Check for ACK message type (3 bytes: type + MOMSN)
	if len(data) >= 1 && data[0] == MsgTypeACK {
		g.handleACK(data)
		return
	}

	// Check for SOS message type
	if len(data) >= 1 && data[0] == MsgTypeSOS {
		log.Warn().Msg("iridium: received SOS message via satellite")
		// SOS is handled at the API level; this is just a relay
	}

	inbound, err := DecodeCompact(data)
	if err != nil {
		log.Error().Err(err).Msg("iridium: decode failed")
		g.errors.Add(1)
		return
	}

	if inbound.To == "" && g.config.DefaultDestination != "" {
		inbound.To = g.config.DefaultDestination
	}

	g.msgsIn.Add(1)
	g.lastActive.Store(time.Now().Unix())
	log.Info().Str("to", inbound.To).Str("text", inbound.Text).Msg("iridium: received MT message")

	select {
	case g.inCh <- *inbound:
	default:
		log.Warn().Msg("iridium: inbound channel full")
	}
}

// handleACK processes an app-level ACK message (3 bytes: type + MOMSN uint16 BE).
func (g *IridiumGateway) handleACK(data []byte) {
	if len(data) < 3 {
		log.Warn().Msg("iridium: ACK too short")
		return
	}
	momsn := binary.BigEndian.Uint16(data[1:3])
	log.Info().Uint16("momsn", momsn).Msg("iridium: received ACK for MOMSN")

	// Update delivery status to confirmed for the matching message
	if g.db != nil {
		g.db.UpdateDeliveryStatusByPacket(uint32(momsn), "confirmed")
	}
}

// EncodeACK creates a 3-byte ACK payload for a given MOMSN.
func EncodeACK(momsn uint16) []byte {
	buf := make([]byte, 3)
	buf[0] = MsgTypeACK
	binary.BigEndian.PutUint16(buf[1:3], momsn)
	return buf
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

	// Extended message types (byte 0 values > 0x0F)
	MsgTypeACK = 0x05 // App-level end-to-end ACK (3 bytes total)
	MsgTypeSOS = 0x06 // SOS emergency alert (16 bytes)
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
