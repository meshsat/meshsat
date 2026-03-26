package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// IMTGateway bridges Meshtastic mesh messages to/from an Iridium 9704 IMT modem.
// Uses JSPR protocol with 100KB message capacity. MT is push-based — no ring alerts
// or polling needed. No compact encoding, no credit budgets.
type IMTGateway struct {
	IridiumGateway
}

// NewIMTGateway creates a new Iridium IMT satellite gateway (RockBLOCK 9704).
func NewIMTGateway(cfg IridiumConfig, sat transport.SatTransport, db *database.DB, predictor PassPredictor) *IMTGateway {
	gw := &IMTGateway{
		IridiumGateway: IridiumGateway{
			config:    cfg,
			sat:       sat,
			db:        db,
			inCh:      make(chan InboundMessage, 32),
			outCh:     make(chan *transport.MeshMessage, 10),
			gwLabel:   "IMT",
			gwType:    "iridium_imt",
			gssSource: "imt_gss",
		},
	}
	gw.forwardFn = gw.sendIMT
	if cfg.SchedulerEnabled && predictor != nil {
		gw.scheduler = NewPassScheduler(predictor, db, cfg)
		gw.scheduler.SetCounterSource(gw)
	}
	return gw
}

// Start connects to the IMT modem and starts the send and DLQ workers.
// IMT does NOT start ring alert listener or poll worker — MT is push-based.
func (g *IMTGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()

	// Check modem status
	status, err := g.sat.GetStatus(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("imt: could not get modem status")
	} else {
		g.connected.Store(status.Connected)
	}

	// Load pending DLQ count
	if g.db != nil {
		if count, err := g.db.CountPendingDeadLetters(); err == nil {
			g.dlqPending.Store(int64(count))
			if count > 0 {
				log.Info().Int("pending", count).Msg("imt: dead-letter queue has pending retries")
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

	// IMT: NO ring alert listener — MT is push-based via transport events
	// IMT: NO poll worker — no SBDSX to poll
	// IMT: NO credit budgets — sendIMT() doesn't call budgetAllows()
	// IMT: DLQ skips MOBufferEmpty — moBufferEmpty() returns error for non-SBD

	// Start pass scheduler if configured.
	// Both SBD and IMT use the same Iridium NEXT LEO constellation — pass scheduling
	// is valuable for both in canyon/restricted-sky environments.
	// GSS success rate tracking uses per-modem source keys (imt_gss) and shared "gss".
	if g.scheduler != nil {
		g.scheduler.Start(ctx)
	}

	// Subscribe to transport events for push-based MT reception
	g.wg.Add(1)
	go g.mtEventListener(ctx)

	schedulerMode := "disabled"
	if g.scheduler != nil {
		schedulerMode = "enabled"
	}
	log.Info().Str("scheduler", schedulerMode).Msg("IMT gateway started")
	return nil
}

// Forward sends a message via IMT synchronously. No compact encoding needed.
func (g *IMTGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	return g.sendIMT(ctx, msg)
}

// Type returns the gateway type identifier.
func (g *IMTGateway) Type() string {
	return "iridium_imt"
}

// sendIMT sends a message via IMT. 100KB capacity — send as plaintext or raw binary.
func (g *IMTGateway) sendIMT(ctx context.Context, msg *transport.MeshMessage) error {
	var data []byte
	var result *transport.SatResult
	var err error

	text := msg.DecodedText
	if text == "" && len(msg.RawPayload) > 0 {
		data = msg.RawPayload
		result, err = g.sat.Send(ctx, data)
	} else {
		data = []byte(text)
		result, err = g.sat.SendText(ctx, text)
	}

	if err != nil {
		g.errors.Add(1)
		g.recordGSSRegistration(false, 0)
		return fmt.Errorf("imt: send failed: %w", err)
	}

	g.recordGSSRegistration(result.MOSuccess(), result.MOStatus)

	if !result.MOSuccess() {
		g.errors.Add(1)
		return fmt.Errorf("imt: session failed (mo_status=%d)", result.MOStatus)
	}

	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	log.Info().Int("mo_status", result.MOStatus).Uint32("packet_id", msg.ID).Msg("imt: message sent")
	g.emit("forward", fmt.Sprintf("IMT sent (mo_status=%d, packet=%d)", result.MOStatus, msg.ID))

	if g.db != nil {
		g.db.InsertSentRecord(msg.ID, data, msg.DecodedText)
	}

	return nil
}

// mtEventListener subscribes to transport events and handles push-based MT reception.
func (g *IMTGateway) mtEventListener(ctx context.Context) {
	defer g.wg.Done()

	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		err := g.imtListenOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}

		if err != nil {
			log.Warn().Err(err).Dur("backoff", backoff).Msg("imt: event listener disconnected, reconnecting")
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// imtListenOnce subscribes to events and processes them until disconnected.
func (g *IMTGateway) imtListenOnce(ctx context.Context) error {
	events, err := g.sat.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	if status, err := g.sat.GetStatus(ctx); err == nil && status.Connected {
		g.connected.Store(true)
		log.Info().Msg("imt: modem connected (post-subscribe check)")
		g.emit("iridium", "IMT modem connected")
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("event channel closed")
			}
			switch event.Type {
			case "mt_received":
				// IMT MT is push-based — transport already received and buffered it
				log.Info().Str("detail", event.Message).Msg("imt: MT push received")
				g.handleRingAlert(ctx)
			case "signal":
				// Opportunistic DLQ drain on good signal
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
				return fmt.Errorf("modem disconnected")
			}
		}
	}
}
