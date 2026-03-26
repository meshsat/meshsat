package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// SBDGateway bridges Meshtastic mesh messages to/from an Iridium 9603 SBD modem.
// Uses AT commands, compact binary encoding (340-byte limit), ring alerts, and
// periodic MT polling. Supports credit budgets and MO buffer pre-checks.
type SBDGateway struct {
	IridiumGateway
}

// NewSBDGateway creates a new Iridium SBD satellite gateway (RockBLOCK 9603).
func NewSBDGateway(cfg IridiumConfig, sat transport.SatTransport, db *database.DB, predictor PassPredictor) *SBDGateway {
	gw := &SBDGateway{
		IridiumGateway: IridiumGateway{
			config:    cfg,
			sat:       sat,
			db:        db,
			inCh:      make(chan InboundMessage, 32),
			outCh:     make(chan *transport.MeshMessage, 10),
			gwLabel:   "SBD",
			gwType:    "iridium",
			gssSource: "sbd_gss",
		},
	}
	gw.forwardFn = gw.sendSBD
	if cfg.SchedulerEnabled && predictor != nil {
		gw.scheduler = NewPassScheduler(predictor, db, cfg)
		gw.scheduler.SetCounterSource(gw)
	}
	return gw
}

// Start subscribes to Iridium SSE for ring alerts and starts send, DLQ, poll workers.
func (g *SBDGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()

	// Check modem status
	status, err := g.sat.GetStatus(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("sbd: could not get modem status")
	} else {
		g.connected.Store(status.Connected)
	}

	// Load pending DLQ count
	if g.db != nil {
		if count, err := g.db.CountPendingDeadLetters(); err == nil {
			g.dlqPending.Store(int64(count))
			if count > 0 {
				log.Info().Int("pending", count).Msg("sbd: dead-letter queue has pending retries")
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

	// Start SSE listener for ring alerts (SBD-specific)
	if g.config.AutoReceive {
		g.wg.Add(1)
		go g.ringAlertListener(ctx)
	}

	// Start pass scheduler if configured
	if g.scheduler != nil {
		g.scheduler.Start(ctx)
	}

	// Start poll worker for MT message retrieval (SBD-specific)
	if g.config.PollInterval <= 0 {
		g.config.PollInterval = 1800
	}
	g.wg.Add(1)
	go g.pollWorker(ctx)

	schedulerMode := "disabled"
	if g.scheduler != nil {
		schedulerMode = "enabled"
	}
	log.Info().Bool("auto_receive", g.config.AutoReceive).Int("poll_interval", g.config.PollInterval).
		Str("scheduler", schedulerMode).Msg("SBD gateway started")
	return nil
}

// Forward sends a message via SBD synchronously using compact binary encoding.
func (g *SBDGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	return g.sendSBD(ctx, msg)
}

// Type returns the gateway type identifier.
func (g *SBDGateway) Type() string {
	return "iridium"
}

// sendSBD sends a message via SBD with compact encoding or plaintext.
func (g *SBDGateway) sendSBD(ctx context.Context, msg *transport.MeshMessage) error {
	var data []byte
	var cost int
	var result *transport.SatResult
	var err error

	if canSendPlaintext(msg) {
		// Short ASCII text — send as readable plaintext
		text := msg.DecodedText
		cost = creditCost(len(text))
		if !g.budgetAllows(cost, 1) {
			return fmt.Errorf("sbd: budget exceeded (cost=%d)", cost)
		}
		result, err = g.sat.SendText(ctx, text)
		data = []byte(text)
	} else {
		// Non-text or long message — use compact binary encoding (340-byte limit)
		data, err = EncodeCompact(msg, g.config.IncludePosition)
		if err != nil {
			g.errors.Add(1)
			return fmt.Errorf("sbd: encode failed: %w", err)
		}
		cost = creditCost(len(data))
		if !g.budgetAllows(cost, 1) {
			return fmt.Errorf("sbd: budget exceeded (cost=%d)", cost)
		}
		result, err = g.sat.Send(ctx, data)
	}

	if err != nil {
		g.errors.Add(1)
		g.recordGSSRegistration(false, 0)
		return fmt.Errorf("sbd: send failed: %w", err)
	}

	g.recordGSSRegistration(result.MOSuccess(), result.MOStatus)

	if !result.MOSuccess() {
		g.errors.Add(1)
		return fmt.Errorf("sbd: session failed (mo_status=%d)", result.MOStatus)
	}

	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	log.Info().Int("mo_status", result.MOStatus).Uint32("packet_id", msg.ID).Msg("sbd: message sent")
	g.emit("forward", fmt.Sprintf("SBD sent (mo_status=%d, packet=%d)", result.MOStatus, msg.ID))

	if g.db != nil {
		g.db.InsertCreditUsage(nil, cost, nil)
		g.db.InsertSentRecord(msg.ID, data, msg.DecodedText)
	}

	// MT piggyback (SBD-specific: SBDIX can carry MT in same session)
	if result.MTReceived || result.MTStatus == 1 || result.MTQueued > 0 {
		log.Info().Bool("mt_received", result.MTReceived).Int("mt_status", result.MTStatus).
			Int("mt_queued", result.MTQueued).Msg("sbd: MT available, piggybacking receive")
		go g.handleRingAlert(ctx)
	}

	return nil
}
