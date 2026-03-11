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
	"meshsat/internal/gateway"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// Dispatcher evaluates access rules and fans out messages to per-channel delivery workers.
type Dispatcher struct {
	db         *database.DB
	access     *rules.AccessEvaluator // v0.3.0 access rule evaluation
	failover   *FailoverResolver      // v0.3.0 failover group resolution
	signing    *SigningService        // v0.3.0 non-repudiation signing + audit
	transforms *TransformPipeline     // v0.3.0 per-interface transform pipeline
	registry   *channel.Registry
	gwProv     GatewayProvider
	mesh       transport.MeshTransport
	workers    map[string]*DeliveryWorker
	emit       func(transport.MeshEvent) // SSE broadcast callback

	mu sync.RWMutex
}

// forwardOptions holds parsed forward_options JSON from access rules.
type forwardOptions struct {
	TTLSeconds  int     `json:"ttl_seconds"`
	SMSContacts []int64 `json:"sms_contacts"` // SMS contact IDs → resolved to phone numbers at delivery time
}

// NewDispatcher creates a dispatcher wired to the channel registry and gateways.
func NewDispatcher(db *database.DB, reg *channel.Registry, gwProv GatewayProvider, mesh transport.MeshTransport) *Dispatcher {
	return &Dispatcher{
		db:       db,
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

// SetSigningService sets the v0.3.0 signing service for non-repudiation.
func (d *Dispatcher) SetSigningService(ss *SigningService) {
	d.signing = ss
}

// SetTransformPipeline sets the v0.3.0 per-interface transform pipeline.
func (d *Dispatcher) SetTransformPipeline(tp *TransformPipeline) {
	d.transforms = tp
}

// Start launches per-channel delivery workers.
func (d *Dispatcher) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Recover stale "sending" deliveries from previous crash/restart.
	// These were mid-delivery when the process died and are now stuck.
	if n, err := d.db.RecoverStaleDeliveries(); err != nil {
		log.Error().Err(err).Msg("dispatcher: failed to recover stale deliveries")
	} else if n > 0 {
		log.Info().Int64("recovered", n).Msg("dispatcher: recovered stale 'sending' deliveries to 'retry'")
	}

	// Legacy workers: one per channel registry entry
	for _, desc := range d.registry.List() {
		if !desc.CanSend {
			continue
		}
		w := &DeliveryWorker{
			channelID:  desc.ID,
			desc:       desc,
			db:         d.db,
			gwProv:     d.gwProv,
			mesh:       d.mesh,
			emit:       d.emit,
			signing:    d.signing,
			transforms: d.transforms,
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

		workerCtx, workerCancel := context.WithCancel(ctx)
		w := &DeliveryWorker{
			channelID:  iface.ID,
			desc:       desc,
			db:         d.db,
			gwProv:     d.gwProv,
			mesh:       d.mesh,
			emit:       d.emit,
			signing:    d.signing,
			transforms: d.transforms,
			cancel:     workerCancel,
		}
		d.workers[iface.ID] = w
		go w.Run(workerCtx)
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

	workerCtx, workerCancel := context.WithCancel(ctx)
	w := &DeliveryWorker{
		channelID:  ifaceID,
		desc:       desc,
		db:         d.db,
		gwProv:     d.gwProv,
		mesh:       d.mesh,
		emit:       d.emit,
		signing:    d.signing,
		transforms: d.transforms,
		cancel:     workerCancel,
	}
	d.workers[ifaceID] = w

	// Unhold any deliveries that were held while interface was offline
	if n, err := d.db.UnholdDeliveriesForChannel(ifaceID); err != nil {
		log.Error().Err(err).Str("interface", ifaceID).Msg("failed to unhold deliveries")
	} else if n > 0 {
		log.Info().Str("interface", ifaceID).Int64("count", n).Msg("unheld deliveries on interface online")
	}

	go w.Run(workerCtx)
	log.Info().Str("interface", ifaceID).Str("type", channelType).Msg("delivery worker started (state change)")
}

// StopWorker stops the delivery worker for a specific interface ID and holds pending deliveries.
// Called when an interface transitions to OFFLINE or ERROR.
func (d *Dispatcher) StopWorker(ifaceID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	w, exists := d.workers[ifaceID]
	if !exists {
		return
	}
	// Cancel the worker's context so its goroutine exits promptly
	if w.cancel != nil {
		w.cancel()
	}
	delete(d.workers, ifaceID)

	// Hold pending deliveries so they aren't lost
	if n, err := d.db.HoldDeliveriesForChannel(ifaceID); err != nil {
		log.Error().Err(err).Str("interface", ifaceID).Msg("failed to hold deliveries")
	} else if n > 0 {
		log.Info().Str("interface", ifaceID).Int64("count", n).Msg("held deliveries on interface offline")
	}

	log.Info().Str("interface", ifaceID).Msg("delivery worker stopped (state change)")
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

		// Parse forward_options
		var fwdOpts forwardOptions
		if m.Rule.ForwardOptions != "" && m.Rule.ForwardOptions != "{}" {
			json.Unmarshal([]byte(m.Rule.ForwardOptions), &fwdOpts)
		}
		ttlSeconds := fwdOpts.TTLSeconds

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

		// Sign the payload for non-repudiation
		if d.signing != nil && len(payload) > 0 {
			del.Signature = d.signing.Sign(payload)
			del.SignerID = d.signing.SignerID()
		}

		delID, err := d.db.InsertDelivery(del)
		if err != nil {
			log.Error().Err(err).Int64("rule_id", m.Rule.ID).Str("dest", destInterface).Msg("failed to create access delivery")
			continue
		}

		// Audit the dispatch event
		if d.signing != nil {
			ifacePtr := &sourceInterface
			dir := "egress"
			d.signing.AuditEvent("dispatch", ifacePtr, &dir, &delID, &ruleID, preview)
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
	channelID  string
	desc       channel.ChannelDescriptor
	db         *database.DB
	gwProv     GatewayProvider
	mesh       transport.MeshTransport
	emit       func(transport.MeshEvent)
	signing    *SigningService
	transforms *TransformPipeline
	cancel     context.CancelFunc // per-worker cancellation
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

	// Apply egress transforms from the destination interface config.
	// For cellular (SMS), the transform pipeline produces base64 ciphertext
	// that the MeshSat Android app decrypts. The gateway detects encrypted
	// payloads and sends them as raw base64 (no prefix/metadata).
	//
	// GSM safety: AES-GCM uses a random nonce, so base64 output varies per
	// attempt. Standard base64 (A-Z a-z 0-9 + / =) is GSM 7-bit safe, but
	// as defense-in-depth for cellular we validate and retry (new nonce) if
	// any non-GSM character appears.
	encrypted := false
	if w.transforms != nil {
		iface, err := w.db.GetInterface(w.channelID)
		if err == nil && iface.EgressTransforms != "" && iface.EgressTransforms != "[]" {
			encrypted = strings.Contains(iface.EgressTransforms, "encrypt")
			isCellular := strings.HasPrefix(w.channelID, "cellular")

			applyToData := func(data []byte) ([]byte, error) {
				return w.transforms.ApplyEgress(data, iface.EgressTransforms)
			}

			var inputData []byte
			if len(del.Payload) > 0 {
				inputData = del.Payload
			} else if del.TextPreview != "" {
				inputData = []byte(del.TextPreview)
			}

			if len(inputData) > 0 {
				transformed, tErr := applyToData(inputData)
				// For cellular with encryption: retry up to 5 times if output
				// is not GSM-safe (new AES-GCM nonce = different base64 output)
				if tErr == nil && isCellular && encrypted {
					for retry := 0; retry < 5 && !gateway.IsGSMSafe(string(transformed)); retry++ {
						log.Warn().Int("retry", retry+1).Msg("cellular: encrypted output not GSM-safe, re-encrypting with new nonce")
						transformed, tErr = applyToData(inputData)
						if tErr != nil {
							break
						}
					}
				}
				if tErr != nil {
					log.Error().Err(tErr).Str("interface", w.channelID).Msg("egress transform failed, sending untransformed")
				} else {
					del.Payload = transformed
					del.TextPreview = string(transformed)
				}
			}
		}
	}

	var deliveryErr error

	if w.channelID == "mesh" || w.channelID == "mesh_0" {
		// Mesh delivery: inject into mesh transport
		deliveryErr = w.mesh.SendMessage(ctx, transport.SendRequest{
			Text: del.TextPreview,
		})
	} else {
		// Gateway delivery: find the gateway and forward
		deliveryErr = w.forwardToGateway(ctx, del, encrypted)
	}

	if deliveryErr != nil {
		w.handleFailure(del, deliveryErr)
	} else {
		w.handleSuccess(del)
	}
}

func (w *DeliveryWorker) forwardToGateway(ctx context.Context, del database.MessageDelivery, encrypted bool) error {
	if w.gwProv == nil {
		return fmt.Errorf("no gateway provider")
	}

	// Reconstruct MeshMessage from payload (full JSON) or fall back to text-only
	msg := &transport.MeshMessage{
		PortNum:     1,
		PortNumName: "TEXT_MESSAGE_APP",
		DecodedText: del.TextPreview,
	}
	if len(del.Payload) > 0 {
		var fullMsg transport.MeshMessage
		if err := json.Unmarshal(del.Payload, &fullMsg); err == nil {
			msg = &fullMsg
			// Ensure text is current (may have been transformed)
			if del.TextPreview != "" {
				msg.DecodedText = del.TextPreview
			}
		}
	}
	msg.Encrypted = encrypted

	// Resolve per-rule SMS destinations from forward_options
	if del.RuleID != nil && w.db != nil {
		if rule, err := w.db.GetAccessRule(*del.RuleID); err == nil && rule != nil {
			if rule.ForwardOptions != "" && rule.ForwardOptions != "{}" {
				var opts forwardOptions
				if err := json.Unmarshal([]byte(rule.ForwardOptions), &opts); err == nil && len(opts.SMSContacts) > 0 {
					contacts, _ := w.db.GetSMSContacts()
					contactMap := make(map[int64]string)
					for _, c := range contacts {
						contactMap[c.ID] = c.Phone
					}
					for _, cid := range opts.SMSContacts {
						if phone, ok := contactMap[cid]; ok {
							msg.SMSDestinations = append(msg.SMSDestinations, phone)
						}
					}
					if len(msg.SMSDestinations) > 0 {
						log.Debug().Strs("sms_to", msg.SMSDestinations).Int64("rule_id", *del.RuleID).
							Msg("resolved per-rule SMS destinations from contacts")
					}
				}
			}
		}
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

	// Audit the successful delivery
	if w.signing != nil {
		ifacePtr := &w.channelID
		dir := "egress"
		delID := del.ID
		w.signing.AuditEvent("deliver", ifacePtr, &dir, &delID, del.RuleID, del.TextPreview)
	}

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

		// Audit the drop event
		if w.signing != nil {
			ifacePtr := &w.channelID
			dir := "egress"
			delID := del.ID
			detail := fmt.Sprintf("retries exhausted (%d): %s", newRetries, errMsg)
			w.signing.AuditEvent("drop", ifacePtr, &dir, &delID, del.RuleID, detail)
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
