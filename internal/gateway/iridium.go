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
	config    IridiumConfig
	sat       transport.SatTransport
	db        *database.DB
	scheduler *PassScheduler
	inCh      chan InboundMessage

	outCh chan *transport.MeshMessage // buffered outbound queue

	connected       atomic.Bool
	ringAlertActive atomic.Bool // prevents concurrent handleRingAlert goroutines
	drainActive     atomic.Bool // prevents concurrent drainDLQ goroutines
	msgsIn          atomic.Int64
	msgsOut         atomic.Int64
	errors          atomic.Int64
	dlqPending      atomic.Int64
	lastActive      atomic.Int64
	passAttempts    atomic.Int64 // SBDIX attempts during current Active pass
	passSuccesses   atomic.Int64 // successful SBDIX sessions during current Active pass
	startTime       time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewIridiumGateway creates a new Iridium satellite gateway.
// If predictor is non-nil and cfg.SchedulerEnabled is true, a PassScheduler is created.
func NewIridiumGateway(cfg IridiumConfig, sat transport.SatTransport, db *database.DB, predictor PassPredictor) *IridiumGateway {
	gw := &IridiumGateway{
		config: cfg,
		sat:    sat,
		db:     db,
		inCh:   make(chan InboundMessage, 32),
		outCh:  make(chan *transport.MeshMessage, 10),
	}

	if cfg.SchedulerEnabled && predictor != nil {
		gw.scheduler = NewPassScheduler(predictor, db, cfg)
		gw.scheduler.SetCounterSource(gw)
	}

	return gw
}

// PassSchedulerRef returns the pass scheduler (may be nil).
func (g *IridiumGateway) PassSchedulerRef() *PassScheduler {
	return g.scheduler
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

	// Start pass scheduler if configured
	if g.scheduler != nil {
		g.scheduler.Start(ctx)
	}

	// Start poll worker for MT message retrieval.
	// If poll_interval is 0 (legacy config), use 1800s as fallback.
	// The SBDSX pre-check in MailboxCheck prevents credit waste on each poll.
	if g.config.PollInterval <= 0 {
		g.config.PollInterval = 1800
	}
	g.wg.Add(1)
	go g.pollWorker(ctx)

	schedulerMode := "disabled"
	if g.scheduler != nil {
		schedulerMode = "enabled"
	}
	log.Info().Bool("auto_receive", g.config.AutoReceive).Int("poll_interval", g.config.PollInterval).Str("scheduler", schedulerMode).Msg("iridium gateway started")
	return nil
}

// Stop shuts down the gateway.
func (g *IridiumGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	if g.scheduler != nil {
		g.scheduler.Stop()
	}
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
// Uses plaintext (AT+SBDWT) for short text messages so they're human-readable on
// the RockBLOCK portal, and compact binary (AT+SBDWB) for everything else.
func (g *IridiumGateway) sendWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			// Decide encoding: plaintext for short ASCII text messages, binary for the rest
			usePlaintext := canSendPlaintext(msg)

			var data []byte
			var cost int
			var result *transport.SBDResult
			var err error

			if usePlaintext {
				text := msg.DecodedText
				cost = creditCost(len(text))
				if !g.budgetAllows(cost, 1) {
					log.Warn().Int("cost", cost).Uint32("packet_id", msg.ID).Msg("iridium: budget exceeded, queuing to DLQ")
					g.enqueueDeadLetter(msg.ID, []byte(text), "budget exceeded", text)
					continue
				}

				result, err = g.sat.SendText(ctx, text)
				data = []byte(text) // for DLQ/record
			} else {
				data, err = EncodeCompact(msg, g.config.IncludePosition)
				if err != nil {
					log.Error().Err(err).Msg("iridium: encode failed")
					g.errors.Add(1)
					continue
				}

				cost = creditCost(len(data))
				if !g.budgetAllows(cost, 1) {
					log.Warn().Int("cost", cost).Uint32("packet_id", msg.ID).Msg("iridium: budget exceeded, queuing to DLQ")
					g.enqueueDeadLetter(msg.ID, data, "budget exceeded", msg.DecodedText)
					continue
				}

				result, err = g.sat.Send(ctx, data)
			}

			if err != nil {
				log.Error().Err(err).Uint32("packet_id", msg.ID).Bool("plaintext", usePlaintext).Msg("iridium: SBD send failed, queuing to DLQ")
				g.errors.Add(1)
				g.recordGSSRegistration(false, 0)
				g.enqueueDeadLetter(msg.ID, data, err.Error(), msg.DecodedText)
				continue
			}

			g.recordGSSRegistration(result.MOSuccess(), result.MOStatus)

			// Check if SBD session actually succeeded (mo_status 0-4).
			// HTTP request can succeed but SBD transfer fail (e.g. mo_status=32 = no network).
			if !result.MOSuccess() {
				log.Warn().Int("mo_status", result.MOStatus).Uint32("packet_id", msg.ID).
					Bool("plaintext", usePlaintext).Msg("iridium: SBD session failed, queuing to DLQ")
				g.errors.Add(1)
				g.enqueueDeadLetter(msg.ID, data, fmt.Sprintf("mo_status=%d", result.MOStatus), msg.DecodedText)
				continue
			}

			g.msgsOut.Add(1)
			g.lastActive.Store(time.Now().Unix())
			log.Info().Int("mo_status", result.MOStatus).Uint32("packet_id", msg.ID).Bool("plaintext", usePlaintext).Msg("iridium: SBD sent")

			// Record credit usage and successful send for queue visibility
			if g.db != nil {
				g.db.InsertCreditUsage(nil, cost, nil)
				g.db.InsertSentRecord(msg.ID, data, msg.DecodedText)
			}

			// MT piggyback: if this SBDIX session received an MT or there are more queued, read them
			if result.MTReceived || result.MTStatus == 1 || result.MTQueued > 0 {
				log.Info().Bool("mt_received", result.MTReceived).Int("mt_status", result.MTStatus).
					Int("mt_queued", result.MTQueued).Msg("iridium: MT available, piggybacking receive")
				go g.handleRingAlert(ctx)
			}

			// Opportunistic geolocation: after successful SBD session, query AT-MSGEO
			go g.captureGeolocation(ctx)
		}
	}
}

// captureGeolocation fetches the Iridium-derived geolocation after a successful SBD session
// and stores it in the database. AT-MSGEO is only valid after a recent SBD session.
func (g *IridiumGateway) captureGeolocation(ctx context.Context) {
	if g.db == nil {
		return
	}
	geo, err := g.sat.GetGeolocation(ctx)
	if err != nil {
		log.Debug().Err(err).Msg("iridium: geolocation unavailable")
		return
	}
	if geo.Lat == 0 && geo.Lon == 0 {
		return // no fix
	}
	ts := time.Now().Unix()
	if geo.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, geo.Timestamp); err == nil {
			ts = t.Unix()
		}
	}
	// Sanitize altitude: AT-MSGEO can return satellite altitude (~780-5000km)
	altKm := geo.AltKm
	if altKm > 10.0 || altKm < -1.0 {
		altKm = 0.0
	}
	if err := g.db.InsertGeolocation("iridium", geo.Lat, geo.Lon, altKm, geo.Accuracy, ts); err != nil {
		log.Warn().Err(err).Msg("iridium: failed to store geolocation")
		return
	}
	log.Info().Float64("lat", geo.Lat).Float64("lon", geo.Lon).Float64("accuracy_km", geo.Accuracy).Msg("iridium: geolocation captured")
}

// canSendPlaintext returns true if a message can be sent as readable ASCII text
// via AT+SBDWT (max 120 chars, no control characters). This makes messages
// human-readable on the RockBLOCK portal instead of appearing as hex.
func canSendPlaintext(msg *transport.MeshMessage) bool {
	// Only text messages qualify
	if msg.PortNum != 1 { // TEXT_MESSAGE
		return false
	}
	text := msg.DecodedText
	if len(text) == 0 || len(text) > 120 {
		return false
	}
	// Must be printable ASCII (no control chars, no high bytes)
	for _, b := range []byte(text) {
		if b < 0x20 || b > 0x7E {
			return false
		}
	}
	return true
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
func (g *IridiumGateway) enqueueDeadLetter(packetID uint32, payload []byte, errMsg string, textPreview string) {
	if g.db == nil {
		return
	}

	retryBase := g.config.DLQRetryBase
	if retryBase <= 0 {
		retryBase = 120
	}
	nextRetry := time.Now().Add(time.Duration(retryBase) * time.Second)

	maxRetries := g.config.DLQMaxRetries // 0 = infinite retries

	if err := g.db.InsertDeadLetter(packetID, payload, maxRetries, nextRetry, errMsg, textPreview); err != nil {
		log.Error().Err(err).Uint32("packet_id", packetID).Msg("iridium: failed to enqueue dead letter")
		return
	}

	g.dlqPending.Add(1)
	log.Info().Uint32("packet_id", packetID).Time("next_retry", nextRetry).Msg("iridium: message queued in DLQ")
}

// dlqBackoff computes the retry backoff for a failed DLQ send based on the
// mo_status code from the ISU AT Command Reference (MAN0009 v2).
//
// mo_status=32 ("no network service"): the modem hasn't registered and needs
// idle radio time to reacquire satellites. Minimum 3 minutes between attempts.
// mo_status=36 ("must wait 3 minutes since last registration"): explicit 3min wait.
// mo_status=35 ("ISU is busy"): short backoff, retry soon.
// mo_status=17 ("gateway not responding"): local timeout, retry soon.
// mo_status=18 ("connection lost / RF drop"): moderate backoff.
// All others: exponential backoff with mode-aware cap.
func dlqBackoff(retryBase int, retries int, moStatus int, params TimingParams) time.Duration {
	switch moStatus {
	case 32, 36:
		// No network / registration cooldown — give modem idle time to reacquire.
		// ISU needs 3+ minutes between registration attempts.
		return 3 * time.Minute
	case 35:
		// ISU is busy — short wait
		return 30 * time.Second
	case 17:
		// Local session timeout — retry after moderate wait
		return time.Minute
	}

	// Default exponential backoff
	backoff := time.Duration(retryBase) * time.Second * (1 << uint(retries+1))
	maxBackoff := 30 * time.Minute
	if params.Mode == ModeActive || params.Mode == ModePostPass {
		maxBackoff = 2 * time.Minute
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}

// getTimingParams returns dynamic timing parameters from the scheduler,
// or legacy hardcoded intervals if no scheduler is active.
func (g *IridiumGateway) getTimingParams() TimingParams {
	if g.scheduler != nil {
		return g.scheduler.GetTimingParams()
	}
	// Legacy fallback — exact same behavior as before
	return TimingParams{
		PollInterval:     time.Duration(g.config.PollInterval) * time.Second,
		DLQCheckInterval: 30 * time.Second,
		DLQRetryBase:     time.Duration(g.config.DLQRetryBase) * time.Second,
		Mode:             ModeActive,
		ModeName:         "legacy",
	}
}

// dlqRetryWorker periodically retries failed sends from the dead-letter queue.
// Uses dynamic timing from the pass scheduler when available.
func (g *IridiumGateway) dlqRetryWorker(ctx context.Context) {
	defer g.wg.Done()

	retryBase := g.config.DLQRetryBase
	if retryBase <= 0 {
		retryBase = 120
	}

	// Dynamic timing: use Timer instead of Ticker
	params := g.getTimingParams()
	timer := time.NewTimer(params.DLQCheckInterval)
	defer timer.Stop()

	// Channel for mode transitions (nil if no scheduler)
	var modeCh <-chan ScheduleMode
	if g.scheduler != nil {
		modeCh = g.scheduler.ModeCh()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-timer.C:
			params = g.getTimingParams()
			log.Debug().Str("mode", params.ModeName).Dur("interval", params.DLQCheckInterval).Int64("pending", g.dlqPending.Load()).Msg("iridium: DLQ timer tick")
			// Timer always respects next_retry. This ensures mo_status=32 backoff
			// (3min) is honored — the modem needs idle time to re-register.
			// Only signal-event-triggered drainDLQ bypasses next_retry (signal just appeared).
			g.processDLQ(ctx, retryBase)
			timer.Reset(params.DLQCheckInterval)

		case newMode := <-modeCh:
			// Instant mode transition — adjust timing immediately
			params = g.getTimingParams()
			timer.Reset(params.DLQCheckInterval)

			// On Active entry, trigger immediate DLQ drain
			if newMode == ModeActive && g.dlqPending.Load() > 0 {
				log.Info().Msg("iridium: scheduler active mode — triggering immediate DLQ drain")
				go g.drainDLQ(ctx)
			}
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
		// Treat successful HTTP but failed SBD session as a send error
		moStatus := -1
		if err == nil && !result.MOSuccess() {
			moStatus = result.MOStatus
			err = fmt.Errorf("mo_status=%d", result.MOStatus)
		}
		if err != nil {
			g.recordGSSRegistration(false, moStatus)
			backoff := dlqBackoff(retryBase, dl.Retries, moStatus, g.getTimingParams())
			nextRetry := time.Now().Add(backoff)
			if updErr := g.db.UpdateDeadLetterRetry(dl.ID, nextRetry, err.Error()); updErr != nil {
				log.Error().Err(updErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to update DLQ retry")
			}
			log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("retry", dl.Retries+1).
				Int("mo_status", moStatus).Dur("backoff", backoff).Time("next_retry", nextRetry).
				Msg("iridium: DLQ retry failed, rescheduled")
			continue
		}

		g.recordGSSRegistration(true, result.MOStatus)

		// Success
		if markErr := g.db.MarkDeadLetterSent(dl.ID); markErr != nil {
			log.Error().Err(markErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to mark dead letter sent")
		}
		g.dlqPending.Add(-1)
		g.msgsOut.Add(1)
		g.lastActive.Store(time.Now().Unix())
		log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("mo_status", result.MOStatus).Int("retry", dl.Retries+1).Msg("iridium: DLQ message sent successfully")

		// MT piggyback: if this SBDIX session received an MT or there are more queued, read them
		if result.MTReceived || result.MTStatus == 1 || result.MTQueued > 0 {
			log.Info().Bool("mt_received", result.MTReceived).Int("mt_status", result.MTStatus).
				Int("mt_queued", result.MTQueued).Msg("iridium: MT available during DLQ retry, piggybacking receive")
			go g.handleRingAlert(ctx)
		}
	}
}

// ringAlertListener subscribes to Iridium SSE for ring alert and signal events.
// Retries Subscribe with exponential backoff (matches signal recorder pattern).
func (g *IridiumGateway) ringAlertListener(ctx context.Context) {
	defer g.wg.Done()

	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		err := g.ringAlertListenOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		// Reset backoff if connection lasted > 10s
		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}

		if err != nil {
			log.Warn().Err(err).Dur("backoff", backoff).Msg("iridium: SSE disconnected, reconnecting")
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

// ringAlertListenOnce subscribes once and processes events until disconnected.
func (g *IridiumGateway) ringAlertListenOnce(ctx context.Context) error {
	events, err := g.sat.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// Re-check modem status after Subscribe (which triggers connect for direct transport).
	if status, err := g.sat.GetStatus(ctx); err == nil && status.Connected {
		g.connected.Store(true)
		log.Info().Msg("iridium: modem connected (post-subscribe check)")
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
				return fmt.Errorf("modem disconnected")
			}
		}
	}
}

// drainDLQ attempts to send all pending DLQ messages immediately, bypassing backoff timers.
// Called opportunistically when a good signal event arrives.
func (g *IridiumGateway) drainDLQ(ctx context.Context) {
	if g.db == nil {
		return
	}
	// Prevent concurrent drain goroutines from piling up on the modem mutex.
	// Each signal event spawns a goroutine; without this guard, multiple
	// goroutines queue on the serial lock and each increments retry counters.
	if !g.drainActive.CompareAndSwap(false, true) {
		log.Debug().Msg("iridium: DLQ drain already active, skipping")
		return
	}
	defer g.drainActive.Store(false)

	retryBase := g.config.DLQRetryBase
	if retryBase <= 0 {
		retryBase = 120
	}
	log.Info().Int64("pending", g.dlqPending.Load()).Msg("iridium: opportunistic DLQ drain triggered by signal event")
	g.processDLQImmediate(ctx, retryBase)
}

// processDLQImmediate sends all pending dead letters regardless of next_retry time.
// Used when we know signal is available — no point waiting for a backoff timer.
func (g *IridiumGateway) processDLQImmediate(ctx context.Context, retryBase int) {
	pending, err := g.db.GetPendingDeadLettersAll(5)
	if err != nil {
		log.Error().Err(err).Msg("iridium: failed to query DLQ (immediate)")
		return
	}
	log.Debug().Int("count", len(pending)).Msg("iridium: processDLQImmediate queried")

	for _, dl := range pending {
		select {
		case <-ctx.Done():
			return
		default:
		}

		result, err := g.sat.Send(ctx, dl.Payload)
		// Treat successful HTTP but failed SBD session as a send error
		moStatus := -1
		if err == nil && !result.MOSuccess() {
			moStatus = result.MOStatus
			err = fmt.Errorf("mo_status=%d", result.MOStatus)
		}
		if err != nil {
			g.recordGSSRegistration(false, moStatus)
			backoff := dlqBackoff(retryBase, dl.Retries, moStatus, g.getTimingParams())
			nextRetry := time.Now().Add(backoff)
			if updErr := g.db.UpdateDeadLetterRetry(dl.ID, nextRetry, err.Error()); updErr != nil {
				log.Error().Err(updErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to update DLQ retry")
			}
			log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("retry", dl.Retries+1).
				Int("mo_status", moStatus).Dur("backoff", backoff).Time("next_retry", nextRetry).
				Str("mode", g.getTimingParams().ModeName).Msg("iridium: DLQ drain retry failed, rescheduled")
			continue
		}

		g.recordGSSRegistration(true, result.MOStatus)

		// Success
		if markErr := g.db.MarkDeadLetterSent(dl.ID); markErr != nil {
			log.Error().Err(markErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to mark dead letter sent")
		}
		g.dlqPending.Add(-1)
		g.msgsOut.Add(1)
		g.lastActive.Store(time.Now().Unix())
		log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("mo_status", result.MOStatus).Int("retry", dl.Retries+1).Msg("iridium: DLQ message sent successfully via drain")

		// MT piggyback: if this SBDIX session received an MT or there are more queued, read them
		if result.MTReceived || result.MTStatus == 1 || result.MTQueued > 0 {
			log.Info().Bool("mt_received", result.MTReceived).Int("mt_status", result.MTStatus).
				Int("mt_queued", result.MTQueued).Msg("iridium: MT available during DLQ drain, piggybacking receive")
			go g.handleRingAlert(ctx)
		}
	}
}

func (g *IridiumGateway) handleRingAlert(ctx context.Context) {
	if !g.ringAlertActive.CompareAndSwap(false, true) {
		log.Debug().Msg("iridium: ring alert already in progress, skipping")
		return
	}
	defer g.ringAlertActive.Store(false)
	g.handleRingAlertWithRetry(ctx, 0)
}

func (g *IridiumGateway) handleRingAlertWithRetry(ctx context.Context, attempt int) {
	log.Info().Int("attempt", attempt).Msg("iridium: ring alert, checking mailbox")

	result, err := g.sat.MailboxCheck(ctx)
	if err != nil {
		log.Error().Err(err).Int("attempt", attempt).Msg("iridium: mailbox check failed")
		g.errors.Add(1)
		g.recordGSSRegistration(false, 0)
		// Retry after 30s if this was a ring-alert-triggered check (max 3 retries)
		if attempt < 3 {
			go func() {
				select {
				case <-ctx.Done():
				case <-time.After(30 * time.Second):
					g.handleRingAlertWithRetry(ctx, attempt+1)
				}
			}()
		}
		return
	}

	// Record GSS registration result
	g.recordGSSRegistration(result.MOSuccess(), result.MOStatus)

	log.Info().Int("mt_status", result.MTStatus).Int("mt_length", result.MTLength).
		Int("mt_queued", result.MTQueued).Bool("mt_received", result.MTReceived).
		Int("attempt", attempt).Msg("iridium: mailbox check result")

	if result.MTStatus != 1 || result.MTLength == 0 {
		// SBDIX succeeded but no MT message delivered. If the modem received a ring alert
		// (attempt 0) and the gateway didn't deliver the MT, retry — the satellite may
		// have moved out of range momentarily.
		if attempt == 0 && !result.MOSuccess() {
			log.Warn().Int("mo_status", result.MOStatus).Msg("iridium: SBD session failed during mailbox check, retrying in 30s")
			go func() {
				select {
				case <-ctx.Done():
				case <-time.After(30 * time.Second):
					g.handleRingAlertWithRetry(ctx, attempt+1)
				}
			}()
		} else {
			log.Info().Msg("iridium: no MT message in this session")
		}
		return // no message waiting (or retry scheduled)
	}

	data, err := g.sat.Receive(ctx)
	if err != nil {
		log.Error().Err(err).Msg("iridium: receive failed")
		g.errors.Add(1)
		return
	}

	if len(data) == 0 {
		log.Warn().Msg("iridium: received empty MT buffer")
		return
	}

	// Check for ACK message type (3 bytes: type + MOMSN)
	if data[0] == MsgTypeACK {
		g.handleACK(data)
		return
	}

	// Check for SOS message type
	if data[0] == MsgTypeSOS {
		log.Warn().Msg("iridium: received SOS message via satellite")
		// SOS is handled at the API level; this is just a relay
	}

	// Try compact binary decode first (MeshSat-to-MeshSat messages).
	// If that fails, treat the raw data as plain text (RockBLOCK web, email-to-SBD, etc.).
	inbound, err := DecodeCompact(data)
	if err != nil {
		log.Info().Int("bytes", len(data)).Msg("iridium: not compact binary, treating as plain text")
		inbound = &InboundMessage{
			Text:   string(data),
			Source: "iridium",
		}
	}

	if inbound.To == "" && g.config.DefaultDestination != "" {
		inbound.To = g.config.DefaultDestination
	}

	g.msgsIn.Add(1)
	g.lastActive.Store(time.Now().Unix())
	log.Info().Str("to", inbound.To).Str("text", inbound.Text).Msg("iridium: received MT message")

	// Record inbound receive for queue visibility
	if g.db != nil {
		g.db.InsertInboundReceiveRecord(data, inbound.Text)
	}

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
// Behavior depends on mailbox_mode config:
//   - "off": no polling, no ring alert response (worker exits immediately)
//   - "ring_alert_only": only responds to scheduler mode transitions (Active entry)
//   - "scheduled": periodic polling with pass-aware dynamic intervals
func (g *IridiumGateway) pollWorker(ctx context.Context) {
	defer g.wg.Done()

	mode := g.config.MailboxMode
	if mode == "off" {
		log.Info().Msg("iridium: mailbox polling disabled (mode=off)")
		return
	}

	// Channel for mode transitions (nil if no scheduler)
	var modeCh <-chan ScheduleMode
	if g.scheduler != nil {
		modeCh = g.scheduler.ModeCh()
	}

	if mode == "ring_alert_only" {
		log.Info().Msg("iridium: mailbox mode=ring_alert_only — no periodic polling, waiting for ring alerts/pass events")
		for {
			select {
			case <-ctx.Done():
				return
			case newMode := <-modeCh:
				if newMode == ModeActive {
					log.Info().Msg("iridium: scheduler active mode — triggering mailbox check (ring_alert_only)")
					go g.handleRingAlert(ctx)
				}
			}
		}
	}

	// mode == "scheduled": periodic polling with pass-aware intervals
	log.Info().Msg("iridium: mailbox mode=scheduled — periodic polling enabled")
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-timer.C:
			g.handleRingAlert(ctx)
			params := g.getTimingParams()
			timer.Reset(params.PollInterval)

		case newMode := <-modeCh:
			params := g.getTimingParams()
			timer.Reset(params.PollInterval)

			if newMode == ModeActive {
				log.Info().Msg("iridium: scheduler active mode — triggering immediate mailbox check")
				go g.handleRingAlert(ctx)
			}
		}
	}
}

// ResetPassCounters resets and returns the per-pass MO attempt/success counters.
func (g *IridiumGateway) ResetPassCounters() (attempts, successes int64) {
	attempts = g.passAttempts.Swap(0)
	successes = g.passSuccesses.Swap(0)
	return
}

// ManualMailboxCheck triggers a one-shot mailbox check (for "Check Mailbox Now" button).
func (g *IridiumGateway) ManualMailboxCheck(ctx context.Context) {
	go g.handleRingAlert(ctx)
}

// recordGSSRegistration persists an SBDIX session outcome to signal_history (source="gss").
// value=1 for successful GSS registration (mo_status 0-4), value=0 for failure.
// Also tracks per-pass attempt/success counters for pass quality logging.
func (g *IridiumGateway) recordGSSRegistration(success bool, moStatus int) {
	// Track per-pass MO attempt/success counters
	g.passAttempts.Add(1)
	if success {
		g.passSuccesses.Add(1)
	}

	if g.db == nil {
		return
	}
	val := float64(0)
	if success {
		val = 1
	}
	ts := time.Now().Unix()
	if err := g.db.InsertSignalHistory("gss", ts, val); err != nil {
		log.Debug().Err(err).Msg("iridium: failed to record GSS registration")
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
