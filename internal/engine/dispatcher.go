package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// Dispatcher evaluates rules and fans out messages to per-channel delivery workers.
type Dispatcher struct {
	db       *database.DB
	rules    *rules.Engine
	registry *channel.Registry
	gwProv   GatewayProvider
	mesh     transport.MeshTransport
	workers  map[string]*DeliveryWorker
	emit     func(transport.MeshEvent) // SSE broadcast callback

	mu sync.RWMutex
}

// NewDispatcher creates a dispatcher wired to the rules engine, channel registry, and gateways.
func NewDispatcher(db *database.DB, rulesEngine *rules.Engine, reg *channel.Registry, gwProv GatewayProvider, mesh transport.MeshTransport) *Dispatcher {
	return &Dispatcher{
		db:       db,
		rules:    rulesEngine,
		registry: reg,
		gwProv:   gwProv,
		mesh:     mesh,
		workers:  make(map[string]*DeliveryWorker),
	}
}

// SetEmitter sets the SSE broadcast callback.
func (d *Dispatcher) SetEmitter(fn func(transport.MeshEvent)) {
	d.emit = fn
}

// Start launches per-channel delivery workers.
func (d *Dispatcher) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, desc := range d.registry.List() {
		if !desc.CanSend {
			continue
		}
		w := &DeliveryWorker{
			channelID: desc.ID,
			desc:      desc,
			db:        d.db,
			gwProv:    d.gwProv,
			mesh:      d.mesh,
			emit:      d.emit,
		}
		d.workers[desc.ID] = w
		go w.Run(ctx)
		log.Info().Str("channel", desc.ID).Msg("delivery worker started")
	}
}

// Dispatch evaluates rules for a message from a source channel and creates delivery rows.
// Returns the number of deliveries created.
func (d *Dispatcher) Dispatch(source string, msg rules.RouteMessage, payload []byte) int {
	if d.rules == nil {
		return 0
	}

	matches := d.rules.EvaluateRoute(source, msg)
	if len(matches) == 0 {
		return 0
	}

	// Generate a message reference for grouping deliveries
	msgRef := fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102-150405"), time.Now().UnixNano()%100000)

	count := 0
	for _, m := range matches {
		desc, ok := d.registry.Get(m.Rule.DestType)
		maxRetries := 3
		if ok && desc.RetryConfig.Enabled {
			maxRetries = desc.RetryConfig.MaxRetries
		}

		preview := msg.Text
		if len(preview) > 200 {
			preview = preview[:200]
		}

		ruleID := int64(m.Rule.ID)
		del := database.MessageDelivery{
			MsgRef:      msgRef,
			RuleID:      &ruleID,
			Channel:     m.Rule.DestType,
			Status:      "queued",
			Priority:    m.Rule.Priority,
			Payload:     payload,
			TextPreview: preview,
			MaxRetries:  maxRetries,
		}

		if _, err := d.db.InsertDelivery(del); err != nil {
			log.Error().Err(err).Int("rule_id", m.Rule.ID).Str("dest", m.Rule.DestType).Msg("failed to create delivery")
			continue
		}

		count++
		log.Info().Int("rule_id", m.Rule.ID).Str("dest", m.Rule.DestType).Str("msg_ref", msgRef).Msg("delivery queued")

		if d.emit != nil {
			d.emit(transport.MeshEvent{
				Type:    "delivery_queued",
				Message: fmt.Sprintf("Rule '%s': %s→%s queued", m.Rule.Name, source, m.Rule.DestType),
			})
		}
	}

	return count
}

// DeliveryWorker polls the delivery queue for a single channel and attempts delivery.
type DeliveryWorker struct {
	channelID string
	desc      channel.ChannelDescriptor
	db        *database.DB
	gwProv    GatewayProvider
	mesh      transport.MeshTransport
	emit      func(transport.MeshEvent)
}

// Run polls the delivery queue and processes pending deliveries.
func (w *DeliveryWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

func (w *DeliveryWorker) processBatch(ctx context.Context) {
	deliveries, err := w.db.GetPendingDeliveries(w.channelID, 10)
	if err != nil {
		log.Error().Err(err).Str("channel", w.channelID).Msg("failed to fetch pending deliveries")
		return
	}

	for _, del := range deliveries {
		select {
		case <-ctx.Done():
			return
		default:
		}
		w.deliver(ctx, del)
	}
}

func (w *DeliveryWorker) deliver(ctx context.Context, del database.MessageDelivery) {
	// Mark as sending
	if err := w.db.SetDeliveryStatus(del.ID, "sending", "", ""); err != nil {
		log.Error().Err(err).Int64("id", del.ID).Msg("failed to set delivery sending")
		return
	}

	var deliveryErr error

	if w.channelID == "mesh" {
		// Mesh delivery: inject into mesh transport
		deliveryErr = w.mesh.SendMessage(ctx, transport.SendRequest{
			Text: del.TextPreview,
		})
	} else {
		// Gateway delivery: find the gateway and forward
		deliveryErr = w.forwardToGateway(ctx, del)
	}

	if deliveryErr != nil {
		w.handleFailure(del, deliveryErr)
	} else {
		w.handleSuccess(del)
	}
}

func (w *DeliveryWorker) forwardToGateway(ctx context.Context, del database.MessageDelivery) error {
	if w.gwProv == nil {
		return fmt.Errorf("no gateway provider")
	}

	for _, gw := range w.gwProv.Gateways() {
		if gw.Type() == w.channelID {
			msg := &transport.MeshMessage{
				PortNum:     1,
				PortNumName: "TEXT_MESSAGE_APP",
				DecodedText: del.TextPreview,
			}
			return gw.Forward(ctx, msg)
		}
	}

	return fmt.Errorf("gateway %s not found or not running", w.channelID)
}

func (w *DeliveryWorker) handleSuccess(del database.MessageDelivery) {
	if err := w.db.SetDeliveryStatus(del.ID, "sent", "", ""); err != nil {
		log.Error().Err(err).Int64("id", del.ID).Msg("failed to mark delivery sent")
	}

	log.Info().Int64("id", del.ID).Str("channel", w.channelID).Msg("delivery sent")

	if w.emit != nil {
		w.emit(transport.MeshEvent{
			Type:    "delivery_sent",
			Message: fmt.Sprintf("Delivered to %s", w.channelID),
		})
	}
}

func (w *DeliveryWorker) handleFailure(del database.MessageDelivery, deliveryErr error) {
	newRetries := del.Retries + 1
	errMsg := deliveryErr.Error()

	// Check if retries exhausted
	if del.MaxRetries > 0 && newRetries >= del.MaxRetries {
		if err := w.db.SetDeliveryStatus(del.ID, "dead", errMsg, ""); err != nil {
			log.Error().Err(err).Int64("id", del.ID).Msg("failed to mark delivery dead")
		}
		log.Warn().Int64("id", del.ID).Str("channel", w.channelID).Int("retries", newRetries).Msg("delivery exhausted retries, moved to dead")

		if w.emit != nil {
			w.emit(transport.MeshEvent{
				Type:    "delivery_dead",
				Message: fmt.Sprintf("Delivery to %s failed after %d retries: %s", w.channelID, newRetries, errMsg),
			})
		}
		return
	}

	// Schedule retry with backoff from channel descriptor
	nextRetry := w.calculateNextRetry(newRetries)
	if err := w.db.UpdateDeliveryRetry(del.ID, nextRetry, newRetries, errMsg); err != nil {
		log.Error().Err(err).Int64("id", del.ID).Msg("failed to schedule delivery retry")
	}

	log.Warn().Int64("id", del.ID).Str("channel", w.channelID).Int("retry", newRetries).
		Time("next_retry", nextRetry).Str("error", errMsg).Msg("delivery failed, retry scheduled")

	if w.emit != nil {
		w.emit(transport.MeshEvent{
			Type:    "delivery_retry",
			Message: fmt.Sprintf("Delivery to %s failed, retry %d scheduled", w.channelID, newRetries),
		})
	}
}

func (w *DeliveryWorker) calculateNextRetry(retries int) time.Time {
	initial := w.desc.RetryConfig.InitialWait
	if initial == 0 {
		initial = 5 * time.Second
	}
	maxWait := w.desc.RetryConfig.MaxWait
	if maxWait == 0 {
		maxWait = 5 * time.Minute
	}

	wait := initial
	switch w.desc.RetryConfig.BackoffFunc {
	case "isu":
		// ISU modem: 3-minute minimum for all retries
		wait = initial
		if wait < 3*time.Minute {
			wait = 3 * time.Minute
		}
	case "exponential":
		for i := 1; i < retries; i++ {
			wait *= 2
		}
	default:
		// linear
		wait = initial * time.Duration(retries)
	}

	if wait > maxWait {
		wait = maxWait
	}

	return time.Now().Add(wait)
}
