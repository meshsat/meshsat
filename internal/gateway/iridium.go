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

// IridiumGateway is the shared base for SBDGateway and IMTGateway.
// Contains common fields and methods for Iridium satellite communication.
// Do not instantiate directly — use NewSBDGateway or NewIMTGateway.
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
	passAttempts    atomic.Int64 // satellite session attempts during current Active pass
	passSuccesses   atomic.Int64 // successful sessions during current Active pass
	startTime       time.Time

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	emitEvent EventEmitFunc

	gwLabel   string // "SBD" or "IMT" — for log messages
	gwType    string // "iridium" or "iridium_imt" — gateway type ID
	gssSource string // "sbd_gss" or "imt_gss" — GSS recording source key

	// forwardFn is the concrete send function set by SBDGateway or IMTGateway.
	// Used by the shared sendWorker.
	forwardFn func(ctx context.Context, msg *transport.MeshMessage) error
}

// NewIridiumGateway creates a legacy SBD gateway. Use NewSBDGateway instead.
func NewIridiumGateway(cfg IridiumConfig, sat transport.SatTransport, db *database.DB, predictor PassPredictor) *SBDGateway {
	return NewSBDGateway(cfg, sat, db, predictor)
}

// NewIridiumIMTGateway creates a legacy IMT gateway. Use NewIMTGateway instead.
func NewIridiumIMTGateway(cfg IridiumConfig, sat transport.SatTransport, db *database.DB, predictor PassPredictor) *IMTGateway {
	return NewIMTGateway(cfg, sat, db, predictor)
}

// SetEventEmitter sets the SSE event emitter callback.
func (g *IridiumGateway) SetEventEmitter(fn EventEmitFunc) {
	g.emitEvent = fn
}

// emit sends an event to the SSE stream if an emitter is configured.
func (g *IridiumGateway) emit(eventType, message string) {
	if g.emitEvent != nil {
		g.emitEvent(eventType, message)
	}
}

// PassSchedulerRef returns the pass scheduler (may be nil).
func (g *IridiumGateway) PassSchedulerRef() *PassScheduler {
	return g.scheduler
}

// Start is not implemented on the base — use SBDGateway.Start() or IMTGateway.Start().
func (g *IridiumGateway) Start(ctx context.Context) error {
	return fmt.Errorf("IridiumGateway.Start() called on base — use SBDGateway or IMTGateway")
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

// Forward is not implemented on the base — use SBDGateway.Forward() or IMTGateway.Forward().
func (g *IridiumGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	return fmt.Errorf("IridiumGateway.Forward() called on base — use SBDGateway or IMTGateway")
}

// Enqueue accepts a message for async send via the outbound queue.
// Returns immediately — the sendWorker processes it in the background.
func (g *IridiumGateway) Enqueue(msg *transport.MeshMessage) error {
	select {
	case g.outCh <- msg:
		return nil
	default:
		return fmt.Errorf("outbound queue full")
	}
}

// Receive returns the inbound message channel.
func (g *IridiumGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *IridiumGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        g.gwType,
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
	return g.gwType
}

// sendWorker dequeues messages from outCh (legacy/non-dispatcher callers).
func (g *IridiumGateway) sendWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			if g.forwardFn != nil {
				if err := g.forwardFn(ctx, msg); err != nil {
					log.Error().Err(err).Uint32("packet_id", msg.ID).Str("gw", g.gwLabel).Msg("sendWorker failed")
				}
			}
		}
	}
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

// moBufferEmpty checks if the MO buffer is empty (SBD only).
// Returns false for IMT transports since they don't have an MO buffer.
func (g *IridiumGateway) moBufferEmpty(ctx context.Context) (bool, error) {
	if sbd, ok := g.sat.(transport.SBDTransport); ok {
		return sbd.MOBufferEmpty(ctx)
	}
	return false, fmt.Errorf("MOBufferEmpty not supported on this transport")
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
	g.emit("forward_error", fmt.Sprintf("Iridium send failed, queued for retry: %s", errMsg))
}

// lastMOStatusFailed returns true if the DLQ entry's last SBDIX mo_status was a
// failure code (anything outside 0-4). When the MO buffer is empty after a failure,
// it's because the bridge cleared it with AT+SBDD0 — NOT because the ISU
// transmitted autonomously. -1 means no SBDIX was attempted yet (first send used
// a different error path); treat as failed to be safe.
func lastMOStatusFailed(moStatus int) bool {
	return moStatus < 0 || moStatus > 4
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

		// Enforce expiry policy: use live config per-priority max_retries.
		// This allows operators to change expiry at runtime for all pending entries.
		// MaxRetries=0 means infinite retries (never expire).
		maxRetries := g.config.ExpiryPolicy.MaxRetriesForPriority(dl.Priority)
		if maxRetries > 0 && dl.Retries >= maxRetries {
			if expErr := g.db.ExpireDeadLetter(dl.ID, fmt.Sprintf("max retries exhausted (%d/%d)", dl.Retries, maxRetries)); expErr != nil {
				log.Error().Err(expErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to expire dead letter")
			} else {
				g.dlqPending.Add(-1)
				log.Warn().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("retries", dl.Retries).
					Int("max_retries", maxRetries).Int("priority", dl.Priority).Str("text", dl.TextPreview).
					Msg("iridium: DLQ entry expired — max retries exhausted")
			}
			continue
		}

		// Pre-check: if MO buffer is empty AND we previously loaded it (retries > 0),
		// the ISU MAY have transmitted this message autonomously. SBDSX is free
		// (no satellite session, no credits).
		// Skip this check on first attempt — the buffer is naturally empty before we load it.
		//
		// IMPORTANT: After a failed SBDIX (mo_status=32/35/36 etc), the bridge clears the
		// MO buffer with AT+SBDD0. An empty buffer after failure does NOT mean the ISU
		// transmitted — it means WE cleared it. Only trust "already transmitted" when the
		// last error does NOT indicate a failed mo_status.
		if dl.Retries > 0 {
			if empty, err := g.moBufferEmpty(ctx); err == nil && empty {
				if lastMOStatusFailed(dl.LastMOStatus) {
					// Buffer is empty because we cleared it after failure — must re-send
					log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).
						Int("last_mo_status", dl.LastMOStatus).
						Msg("iridium: MO buffer empty after failed send — re-sending (not marking sent)")
				} else {
					// Buffer is empty and last attempt wasn't a known failure code —
					// ISU likely transmitted autonomously
					if markErr := g.db.MarkDeadLetterSent(dl.ID); markErr != nil {
						log.Error().Err(markErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to mark dead letter sent (MO empty)")
					}
					g.dlqPending.Add(-1)
					g.msgsOut.Add(1)
					g.lastActive.Store(time.Now().Unix())
					log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).
						Msg("iridium: MO buffer empty — ISU already transmitted, marking sent")
					continue
				}
			}
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
			if updErr := g.db.UpdateDeadLetterRetry(dl.ID, nextRetry, err.Error(), moStatus); updErr != nil {
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
		g.emit("iridium", "Iridium modem connected")
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
			case "mt_received":
				// IMT (9704): MT is push-based — the transport already received
				// the full message and buffered it in mtPending. Trigger the same
				// handleRingAlert path which calls MailboxCheck → Receive.
				log.Info().Str("detail", event.Message).Msg("iridium: MT push received from IMT transport")
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

		// Enforce expiry policy (same as processDLQ — live config per-priority).
		maxRetries := g.config.ExpiryPolicy.MaxRetriesForPriority(dl.Priority)
		if maxRetries > 0 && dl.Retries >= maxRetries {
			if expErr := g.db.ExpireDeadLetter(dl.ID, fmt.Sprintf("max retries exhausted (%d/%d)", dl.Retries, maxRetries)); expErr != nil {
				log.Error().Err(expErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to expire dead letter")
			} else {
				g.dlqPending.Add(-1)
				log.Warn().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).Int("retries", dl.Retries).
					Int("max_retries", maxRetries).Int("priority", dl.Priority).Str("text", dl.TextPreview).
					Msg("iridium: DLQ entry expired — max retries exhausted")
			}
			continue
		}

		// Pre-check: if MO buffer is empty AND we previously loaded it (retries > 0),
		// the ISU MAY have transmitted autonomously. Only trust this when last SBDIX
		// was a success (mo_status 0-4). [MESHSAT-341]
		if dl.Retries > 0 {
			if empty, err := g.moBufferEmpty(ctx); err == nil && empty {
				if lastMOStatusFailed(dl.LastMOStatus) {
					log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).
						Int("last_mo_status", dl.LastMOStatus).
						Msg("iridium: MO buffer empty after failed send — re-sending (not marking sent)")
				} else {
					if markErr := g.db.MarkDeadLetterSent(dl.ID); markErr != nil {
						log.Error().Err(markErr).Int64("dlq_id", dl.ID).Msg("iridium: failed to mark dead letter sent (MO empty)")
					}
					g.dlqPending.Add(-1)
					g.msgsOut.Add(1)
					g.lastActive.Store(time.Now().Unix())
					log.Info().Int64("dlq_id", dl.ID).Uint32("packet_id", dl.PacketID).
						Str("mode", g.getTimingParams().ModeName).
						Msg("iridium: MO buffer empty — ISU already transmitted, marking sent")
					continue
				}
			}
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
			if updErr := g.db.UpdateDeadLetterRetry(dl.ID, nextRetry, err.Error(), moStatus); updErr != nil {
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
	g.emit("mailbox", fmt.Sprintf("Mailbox check started (attempt %d)", attempt+1))

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
			log.Warn().Int("mo_status", result.MOStatus).Msg("iridium: session failed during mailbox check, retrying in 30s")
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
	g.emit("inbound", fmt.Sprintf("Iridium MT received: %s", inbound.Text))

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

// recordGSSRegistration persists a satellite session outcome to signal_history.
// Uses modem-specific source key: "sbd_gss" for 9603, "imt_gss" for 9704.
// Also writes to "gss" for backward compatibility with combined views.
// value=1 for successful registration (mo_status 0-4), value=0 for failure.
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
	if err := g.db.InsertSignalHistory(g.gssSource, ts, val); err != nil {
		log.Debug().Err(err).Msg("iridium: failed to record GSS registration")
	}
	// Also write combined "gss" for backward compat
	if err := g.db.InsertSignalHistory("gss", ts, val); err != nil {
		log.Debug().Err(err).Msg("iridium: failed to record combined GSS registration")
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

	// Text — length is 1 byte (max 255; SBD payload is 340 so this is sufficient)
	text := []byte(msg.DecodedText)
	maxText := maxSBDPayload - len(buf) - 1 // -1 for length byte
	if maxText > 255 {
		maxText = 255
	}
	if maxText < 0 {
		maxText = 0
	}
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
