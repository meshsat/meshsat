package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	access   *rules.AccessEvaluator // v0.3.0 access rule evaluation
	failover *FailoverResolver      // v0.3.0 failover group resolution
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

// SetAccessEvaluator sets the v0.3.0 access rule evaluator.
func (d *Dispatcher) SetAccessEvaluator(ae *rules.AccessEvaluator) {
	d.access = ae
}

// SetFailoverResolver sets the v0.3.0 failover group resolver.
func (d *Dispatcher) SetFailoverResolver(fr *FailoverResolver) {
	d.failover = fr
}

// Start launches per-channel delivery workers.
func (d *Dispatcher) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Legacy workers: one per channel registry entry
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

	// v0.3.0 workers: one per interface ID (for DispatchAccess deliveries)
	d.startInterfaceWorkers(ctx)

	// Start TTL reaper — expires deliveries past their TTL every 60 seconds
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := d.db.ExpireDeliveries(); err != nil {
					log.Error().Err(err).Msg("delivery reaper error")
				} else if n > 0 {
					log.Info().Int64("expired", n).Msg("delivery reaper: expired deliveries")
				}
			}
		}
	}()
}

// startInterfaceWorkers launches delivery workers for each enabled interface.
// These workers pick up deliveries created by DispatchAccess (keyed by interface ID).
func (d *Dispatcher) startInterfaceWorkers(ctx context.Context) {
	ifaces, err := d.db.GetAllInterfaces()
	if err != nil {
		log.Warn().Err(err).Msg("dispatcher: failed to load interfaces for workers")
		return
	}

	started := 0
	for _, iface := range ifaces {
		if !iface.Enabled {
			continue
		}
		// Skip if a worker already exists (legacy or duplicate)
		if _, exists := d.workers[iface.ID]; exists {
			continue
		}

		// Resolve channel descriptor for retry config
		desc, ok := d.registry.Get(iface.ChannelType)
		if !ok {
			// Use a default descriptor for unknown types
			desc = channel.ChannelDescriptor{
				ID:      iface.ChannelType,
				CanSend: true,
			}
		}

		w := &DeliveryWorker{
			channelID: iface.ID,
			desc:      desc,
			db:        d.db,
			gwProv:    d.gwProv,
			mesh:      d.mesh,
			emit:      d.emit,
		}
		d.workers[iface.ID] = w
		go w.Run(ctx)
		started++
		log.Info().Str("interface", iface.ID).Str("type", iface.ChannelType).Msg("interface delivery worker started")
	}

	if started > 0 {
		log.Info().Int("count", started).Msg("dispatcher: interface delivery workers started")
	}
}

// StartWorker starts a delivery worker for a specific interface ID.
// Called when an interface transitions to ONLINE.
func (d *Dispatcher) StartWorker(ctx context.Context, ifaceID string, channelType string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workers[ifaceID]; exists {
		return // already running
	}

	desc, ok := d.registry.Get(channelType)
	if !ok {
		desc = channel.ChannelDescriptor{
			ID:      channelType,
			CanSend: true,
		}
	}

	w := &DeliveryWorker{
		channelID: ifaceID,
		desc:      desc,
		db:        d.db,
		gwProv:    d.gwProv,
		mesh:      d.mesh,
		emit:      d.emit,
	}
	d.workers[ifaceID] = w

	// Unhold any deliveries that were held while interface was offline
	if n, err := d.db.UnholdDeliveriesForChannel(ifaceID); err != nil {
		log.Error().Err(err).Str("interface", ifaceID).Msg("failed to unhold deliveries")
	} else if n > 0 {
		log.Info().Str("interface", ifaceID).Int64("count", n).Msg("unheld deliveries on interface online")
	}

	go w.Run(ctx)
	log.Info().Str("interface", ifaceID).Str("type", channelType).Msg("delivery worker started (state change)")
}

// StopWorker stops the delivery worker for a specific interface ID and holds pending deliveries.
// Called when an interface transitions to OFFLINE or ERROR.
func (d *Dispatcher) StopWorker(ifaceID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workers[ifaceID]; !exists {
		return
	}
	// Note: the worker will stop on its next tick when ctx is cancelled.
	// We remove it from the map so it won't be found by new dispatches.
	delete(d.workers, ifaceID)

	// Hold pending deliveries so they aren't lost
	if n, err := d.db.HoldDeliveriesForChannel(ifaceID); err != nil {
		log.Error().Err(err).Str("interface", ifaceID).Msg("failed to hold deliveries")
	} else if n > 0 {
		log.Info().Str("interface", ifaceID).Int64("count", n).Msg("held deliveries on interface offline")
	}

	log.Info().Str("interface", ifaceID).Msg("delivery worker stopped (state change)")
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

// DispatchAccess evaluates v0.3.0 access rules for a message arriving on an interface.
// Returns the number of deliveries created. Uses interface IDs for routing.
func (d *Dispatcher) DispatchAccess(sourceInterface string, msg rules.RouteMessage, payload []byte) int {
	if d.access == nil {
		return 0
	}

	matches := d.access.EvaluateIngress(sourceInterface, msg)
	if len(matches) == 0 {
		return 0
	}

	msgRef := fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102-150405"), time.Now().UnixNano()%100000)

	count := 0
	for _, m := range matches {
		// Resolve failover groups to concrete interface ID
		destInterface := m.ForwardTo
		if d.failover != nil {
			resolved := d.failover.Resolve(m.ForwardTo)
			if resolved == "" {
				log.Warn().Str("target", m.ForwardTo).Msg("failover: no available interface, dropping delivery")
				continue
			}
			destInterface = resolved
		}

		// Post-resolution loop prevention: check resolved target against visited set
		if destInterface == sourceInterface {
			log.Debug().Str("target", destInterface).Str("source", sourceInterface).
				Msg("failover: resolved target is source interface, skipping")
			continue
		}
		if len(msg.Visited) > 0 {
			skip := false
			for _, v := range msg.Visited {
				if v == destInterface {
					log.Debug().Str("target", destInterface).Strs("visited", msg.Visited).
						Msg("failover: resolved target already in visited set, skipping")
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		}

		// Resolve retry config from channel registry using the channel_type portion
		channelType := destInterface
		if idx := strings.LastIndex(destInterface, "_"); idx > 0 {
			channelType = destInterface[:idx]
		}
		desc, ok := d.registry.Get(channelType)
		maxRetries := 3
		if ok && desc.RetryConfig.Enabled {
			maxRetries = desc.RetryConfig.MaxRetries
		}

		preview := msg.Text
		if len(preview) > 200 {
			preview = preview[:200]
		}

		// Build visited set: start with source interface, append any previously visited
		visited := fmt.Sprintf(`["%s"]`, sourceInterface)
		if len(msg.Visited) > 0 {
			// Merge: source + previous visited (deduplicated)
			seen := map[string]bool{sourceInterface: true}
			all := []string{sourceInterface}
			for _, v := range msg.Visited {
				if !seen[v] {
					seen[v] = true
					all = append(all, v)
				}
			}
			parts := make([]string, len(all))
			for i, v := range all {
				parts[i] = fmt.Sprintf(`"%s"`, v)
			}
			visited = "[" + strings.Join(parts, ",") + "]"
		}

		// Parse TTL from forward_options
		var ttlSeconds int
		if m.Rule.ForwardOptions != "" && m.Rule.ForwardOptions != "{}" {
			var fwdOpts struct {
				TTLSeconds int `json:"ttl_seconds"`
			}
			if err := json.Unmarshal([]byte(m.Rule.ForwardOptions), &fwdOpts); err == nil && fwdOpts.TTLSeconds > 0 {
				ttlSeconds = fwdOpts.TTLSeconds
			}
		}

		ruleID := m.Rule.ID
		del := database.MessageDelivery{
			MsgRef:      msgRef,
			RuleID:      &ruleID,
			Channel:     destInterface, // resolved interface ID as delivery target
			Status:      "queued",
			Priority:    m.Rule.Priority,
			Payload:     payload,
			TextPreview: preview,
			MaxRetries:  maxRetries,
			Visited:     visited,
			QoSLevel:    m.Rule.QoSLevel,
		}

		// Set TTL expiry if configured
		if ttlSeconds > 0 {
			del.TTLSeconds = ttlSeconds
			exp := time.Now().Add(time.Duration(ttlSeconds) * time.Second).UTC().Format("2006-01-02 15:04:05")
			del.ExpiresAt = &exp
		}

		if _, err := d.db.InsertDelivery(del); err != nil {
			log.Error().Err(err).Int64("rule_id", m.Rule.ID).Str("dest", destInterface).Msg("failed to create access delivery")
			continue
		}

		count++
		log.Info().Int64("rule_id", m.Rule.ID).Str("dest", destInterface).Str("msg_ref", msgRef).Msg("access delivery queued")

		if d.emit != nil {
			d.emit(transport.MeshEvent{
				Type:    "delivery_queued",
				Message: fmt.Sprintf("AccessRule '%s': %s→%s queued", m.Rule.Name, sourceInterface, destInterface),
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

	if w.channelID == "mesh" || w.channelID == "mesh_0" {
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

	msg := &transport.MeshMessage{
		PortNum:     1,
		PortNumName: "TEXT_MESSAGE_APP",
		DecodedText: del.TextPreview,
	}

	// v0.3.0: try interface ID-based lookup first (e.g. "iridium_0", "mqtt_0")
	if gw := w.gwProv.GatewayByInterfaceID(w.channelID); gw != nil {
		return gw.Forward(ctx, msg)
	}

	// Legacy: match by gateway type string
	for _, gw := range w.gwProv.Gateways() {
		if gw.Type() == w.channelID {
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
