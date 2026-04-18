package engine

import (
	"context"
	"crypto/sha256"
	encoding_base64 "encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/channel"
	"meshsat/internal/codec"
	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/hemb"
	"meshsat/internal/routing"
	"meshsat/internal/rules"
	"meshsat/internal/transport"
)

// DefaultMaxHops is the maximum number of interfaces a message may traverse
// before being dropped. Prevents unbounded forwarding chains in misconfigured
// rule sets. Override with MESHSAT_MAX_HOPS environment variable.
const DefaultMaxHops = 8

// DefaultDeliveryDedupTTL is the time window for content-hash dedup at the
// delivery level. If the same payload is dispatched to the same interface
// within this window, the duplicate is suppressed.
const DefaultDeliveryDedupTTL = 5 * time.Minute

// DefaultMaxQueueDepth is the default max deliveries per interface queue.
// Override with MESHSAT_MAX_QUEUE_DEPTH environment variable.
const DefaultMaxQueueDepth = 500

// DefaultMaxQueueBytes is the default max payload bytes per interface queue.
// Override with MESHSAT_MAX_QUEUE_BYTES environment variable.
const DefaultMaxQueueBytes = 1 * 1024 * 1024 // 1 MB

// PassStateProvider reports the current satellite pass scheduling mode.
// Implemented by gateway.PassScheduler; defined here to avoid import cycle.
type PassStateProvider interface {
	// PassMode returns the current pass scheduling mode:
	// 0=Idle, 1=PreWake, 2=Active, 3=PostPass
	PassMode() int
}

// LoopMetrics tracks loop prevention statistics for monitoring.
type LoopMetrics struct {
	HopLimitDrops   atomic.Int64 // messages dropped due to max hop count
	VisitedSetDrops atomic.Int64 // deliveries skipped by visited-set check
	SelfLoopDrops   atomic.Int64 // deliveries skipped by self-loop check
	DeliveryDedups  atomic.Int64 // deliveries suppressed by content-hash dedup
}

// Snapshot returns a point-in-time copy of the counters.
func (m *LoopMetrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"hop_limit_drops":   m.HopLimitDrops.Load(),
		"visited_set_drops": m.VisitedSetDrops.Load(),
		"self_loop_drops":   m.SelfLoopDrops.Load(),
		"delivery_dedups":   m.DeliveryDedups.Load(),
	}
}

// Dispatcher evaluates access rules and fans out messages to per-channel delivery workers.
// PacketSenderProvider returns a send function for a Reticulum interface ID.
type PacketSenderProvider interface {
	GetPacketSender(ifaceID string) func(ctx context.Context, data []byte) error
}

type Dispatcher struct {
	db         *database.DB
	access     *rules.AccessEvaluator // v0.3.0 access rule evaluation
	failover   *FailoverResolver      // v0.3.0 failover group resolution
	signing    *SigningService        // v0.3.0 non-repudiation signing + audit
	transforms *TransformPipeline     // v0.3.0 per-interface transform pipeline
	registry   *channel.Registry
	gwProv     GatewayProvider
	mesh       transport.MeshTransport
	pktSender  PacketSenderProvider // Reticulum packet senders (tcp_0, etc.)
	workers    map[string]*DeliveryWorker
	emit       func(transport.MeshEvent) // SSE broadcast callback
	passSched  PassStateProvider         // satellite pass scheduler (nil if no satellite interfaces)

	// Loop prevention
	maxHops          int                  // max interfaces a message may traverse
	loopMetrics      LoopMetrics          // counters for monitoring
	deliveryDedup    map[string]time.Time // content-hash → timestamp for delivery-level dedup
	deliveryDedupMu  sync.Mutex
	deliveryDedupTTL time.Duration

	// Queue limits
	maxQueueDepth int   // max deliveries per interface queue
	maxQueueBytes int64 // max payload bytes per interface queue

	// Routing identity for delivery confirmations
	routingIdentity *routing.Identity

	// DTN (MESHSAT-408)
	fragmentMgr *ReassemblyBuffer
	custodyMgr  *CustodyManager

	// Directory-backed recipient resolution (MESHSAT-544 / S2-01).
	// Nil until [Dispatcher.SetRecipientResolver] is called; when nil,
	// [Dispatcher.SendToRecipient] accepts only RawRecipient targets.
	recipientResolver RecipientResolver

	// wg tracks every goroutine spawned by Start / StartWorker so
	// callers (tests, graceful shutdown) can Wait() for all workers to
	// drain before closing the underlying database. Without this,
	// cancel→db.Close() races the in-flight delivery write and emits
	// "disk I/O error (5898)" from SQLite plus a non-empty TempDir at
	// the end of test cleanup.
	wg sync.WaitGroup

	mu sync.RWMutex
}

// Wait blocks until every goroutine started by this dispatcher has
// exited. Callers must cancel the context passed to Start first,
// otherwise Wait blocks forever. Used by tests to drain before
// db.Close; safe in production shutdown paths too.
func (d *Dispatcher) Wait() {
	d.wg.Wait()
}

// forwardOptions holds parsed forward_options JSON from access rules.
type forwardOptions struct {
	TTLSeconds  int     `json:"ttl_seconds"`
	SMSContacts []int64 `json:"sms_contacts"` // SMS contact IDs → resolved to phone numbers at delivery time
}

// NewDispatcher creates a dispatcher wired to the channel registry and gateways.
func NewDispatcher(db *database.DB, reg *channel.Registry, gwProv GatewayProvider, mesh transport.MeshTransport) *Dispatcher {
	maxHops := DefaultMaxHops
	if v := os.Getenv("MESHSAT_MAX_HOPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxHops = n
		}
	}
	maxQueueDepth := DefaultMaxQueueDepth
	if v := os.Getenv("MESHSAT_MAX_QUEUE_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxQueueDepth = n
		}
	}
	maxQueueBytes := int64(DefaultMaxQueueBytes)
	if v := os.Getenv("MESHSAT_MAX_QUEUE_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			maxQueueBytes = n
		}
	}
	return &Dispatcher{
		db:               db,
		registry:         reg,
		gwProv:           gwProv,
		mesh:             mesh,
		workers:          make(map[string]*DeliveryWorker),
		maxHops:          maxHops,
		maxQueueDepth:    maxQueueDepth,
		maxQueueBytes:    maxQueueBytes,
		deliveryDedup:    make(map[string]time.Time),
		deliveryDedupTTL: DefaultDeliveryDedupTTL,
	}
}

// LoopMetrics returns a reference to the loop prevention metrics.
func (d *Dispatcher) LoopMetrics() *LoopMetrics {
	return &d.loopMetrics
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

// FailoverResolver returns the failover resolver, or nil.
func (d *Dispatcher) FailoverResolver() *FailoverResolver {
	return d.failover
}

// SetSigningService sets the v0.3.0 signing service for non-repudiation.
func (d *Dispatcher) SetSigningService(ss *SigningService) {
	d.signing = ss
}

// SetTransformPipeline sets the v0.3.0 per-interface transform pipeline.
func (d *Dispatcher) SetTransformPipeline(tp *TransformPipeline) {
	d.transforms = tp
}

// TransformPipeline returns the transform pipeline for external callers. [MESHSAT-447]
func (d *Dispatcher) TransformPipeline() *TransformPipeline {
	return d.transforms
}

// SetPassStateProvider sets the satellite pass scheduler for pass-aware delivery.
func (d *Dispatcher) SetPassStateProvider(ps PassStateProvider) {
	d.passSched = ps
}

// SetRoutingIdentity sets the routing identity for delivery confirmations.
func (d *Dispatcher) SetRoutingIdentity(id *routing.Identity) {
	d.routingIdentity = id
}

// SetFragmentManager registers the DTN reassembly buffer for bundle fragmentation.
func (d *Dispatcher) SetFragmentManager(fm *ReassemblyBuffer) {
	d.fragmentMgr = fm
}

// SetCustodyManager registers the DTN custody transfer manager.
func (d *Dispatcher) SetCustodyManager(cm *CustodyManager) {
	d.custodyMgr = cm
}

func (d *Dispatcher) SetPacketSenderProvider(p PacketSenderProvider) {
	d.pktSender = p
}

// parseCustodyID converts a hex-encoded custody ID string to a [16]byte array.
func parseCustodyID(hexStr string) ([16]byte, error) {
	var id [16]byte
	b, err := hex.DecodeString(hexStr)
	if err != nil || len(b) < 16 {
		return id, fmt.Errorf("invalid custody ID: %s", hexStr)
	}
	copy(id[:], b[:16])
	return id, nil
}

// satellitePassSched returns the pass scheduler for satellite channels, nil otherwise.
func (d *Dispatcher) satellitePassSched(desc channel.ChannelDescriptor) PassStateProvider {
	if desc.IsSatellite && d.passSched != nil {
		return d.passSched
	}
	return nil
}

// Start launches per-channel delivery workers.
func (d *Dispatcher) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Cancel runaway deliveries with excessive retries (safety net for past bugs).
	// Deliveries with max_retries=0 (infinite) are capped at 15 retries.
	if n, err := d.db.CancelRunawayDeliveries(15); err != nil {
		log.Error().Err(err).Msg("dispatcher: failed to cancel runaway deliveries")
	} else if n > 0 {
		log.Warn().Int64("cancelled", n).Msg("dispatcher: cancelled runaway deliveries exceeding retry limits")
	}

	// Recover stale "sending" deliveries from previous crash/restart.
	// These were mid-delivery when the process died and are now stuck.
	if n, err := d.db.RecoverStaleDeliveries(); err != nil {
		log.Error().Err(err).Msg("dispatcher: failed to recover stale deliveries")
	} else if n > 0 {
		log.Info().Int64("recovered", n).Msg("dispatcher: recovered stale 'sending' deliveries to 'retry'")
	}

	// Per-interface delivery workers: one worker per enabled interface instance.
	// All delivery routing uses interface IDs (e.g. "mqtt_0", "iridium_0").
	d.startInterfaceWorkers(ctx)

	// Prune delivery dedup cache every 2 minutes
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.pruneDeliveryDedup()
			}
		}
	}()

	// Start ACK timeout reaper — marks timed-out pending ACKs every 60 seconds
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.reapAckTimeouts()
			}
		}
	}()

	// Start TTL reaper — expires deliveries past their TTL every 60 seconds
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
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
			channelID:       iface.ID,
			desc:            desc,
			db:              d.db,
			gwProv:          d.gwProv,
			mesh:            d.mesh,
			emit:            d.emit,
			signing:         d.signing,
			transforms:      d.transforms,
			access:          d.access,
			passSched:       d.satellitePassSched(desc),
			routingIdentity: d.routingIdentity,
			custodyMgr:      d.custodyMgr,
			cancel:          workerCancel,
		}
		d.workers[iface.ID] = w
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			w.Run(workerCtx)
		}()
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
		channelID:       ifaceID,
		desc:            desc,
		db:              d.db,
		gwProv:          d.gwProv,
		mesh:            d.mesh,
		emit:            d.emit,
		signing:         d.signing,
		transforms:      d.transforms,
		access:          d.access,
		passSched:       d.satellitePassSched(desc),
		routingIdentity: d.routingIdentity,
		custodyMgr:      d.custodyMgr,
		cancel:          workerCancel,
	}
	d.workers[ifaceID] = w

	// Unhold any deliveries that were held while interface was offline
	if n, err := d.db.UnholdDeliveriesForChannel(ifaceID); err != nil {
		log.Error().Err(err).Str("interface", ifaceID).Msg("failed to unhold deliveries")
	} else if n > 0 {
		log.Info().Str("interface", ifaceID).Int64("count", n).Msg("unheld deliveries on interface online")
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		w.Run(workerCtx)
	}()
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

	// Max hop count: drop messages that have traversed too many interfaces
	if len(msg.Visited) >= d.maxHops {
		d.loopMetrics.HopLimitDrops.Add(1)
		log.Warn().Int("hops", len(msg.Visited)).Int("max", d.maxHops).
			Strs("visited", msg.Visited).Str("source", sourceInterface).
			Msg("loop prevention: max hop count exceeded, dropping message")
		return 0
	}

	// Strip protocol version byte before applying ingress transforms.
	if len(payload) > 0 {
		protoVer, stripped := codec.StripVersionByte(payload)
		codec.LogVersionInfo(protoVer, sourceInterface)
		payload = stripped
	}

	// Apply ingress transforms to decrypt/decompress incoming payload
	if d.transforms != nil && len(payload) > 0 {
		iface, err := d.db.GetInterface(sourceInterface)
		if err == nil && iface.IngressTransforms != "" && iface.IngressTransforms != "[]" {
			decoded, err := d.transforms.ApplyIngress(payload, iface.IngressTransforms)
			if err != nil {
				log.Warn().Err(err).Str("source", sourceInterface).
					Msg("ingress transform failed, forwarding raw payload")
			} else {
				payload = decoded
				msg.Text = string(decoded)
				log.Debug().Str("source", sourceInterface).
					Int("raw", len(payload)).Int("decoded", len(decoded)).
					Msg("ingress transforms applied")
			}
		}
	}

	matches := d.access.EvaluateIngress(sourceInterface, msg)
	if len(matches) == 0 {
		return 0
	}

	msgRef := fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102-150405"), time.Now().UnixNano()%100000)

	count := 0
	for _, m := range matches {
		// Check if the forwarding target is a HeMB bond group.
		if d.db.IsBondGroup(m.ForwardTo) {
			if d.failover != nil {
				sendFnProvider := func(ifaceID string) func(ctx context.Context, data []byte) error {
					// Try gateway first (satellite, cellular, MQTT, etc.).
					gw := d.gwProv.GatewayByInterfaceID(ifaceID)
					if gw != nil {
						return func(ctx context.Context, data []byte) error {
							return gw.Forward(ctx, &transport.MeshMessage{
								RawPayload: data,
							})
						}
					}
					// Fallback: Reticulum packet sender for any interface (mesh_0, tcp_0, etc.).
					// Uses the RNS transport node which relays PRIVATE_APP between bridges.
					// Direct mesh.SendRaw(PortNum:256) doesn't cross Meshtastic nodes.
					if d.pktSender != nil {
						if fn := d.pktSender.GetPacketSender(ifaceID); fn != nil {
							return fn
						}
					}
					// Last resort: raw mesh transport (local delivery only).
					if d.mesh != nil && strings.HasPrefix(ifaceID, "mesh") {
						return func(ctx context.Context, data []byte) error {
							return d.mesh.SendRaw(ctx, transport.RawRequest{
								PortNum: 256, // PRIVATE_APP
								Payload: encoding_base64.StdEncoding.EncodeToString(data),
							})
						}
					}
					return nil
				}
				bearers := d.failover.SelectBearers(m.ForwardTo, sendFnProvider)
				if len(bearers) > 0 {
					bdr := hemb.NewBonder(hemb.Options{
						Bearers:   bearers,
						DeliverFn: nil,                           // send-only path
						EventCh:   hemb.GlobalEventBus.Channel(), // fan-out to SSE subscribers
					})
					if err := bdr.Send(context.Background(), payload); err != nil {
						log.Error().Err(err).Str("bond_group", m.ForwardTo).
							Msg("hemb: bonded send failed")
					} else {
						count++
						log.Info().Str("bond_group", m.ForwardTo).Int("bearers", len(bearers)).
							Msg("hemb: payload sent via bond group")
					}
					continue
				}
			}
			log.Warn().Str("bond_group", m.ForwardTo).Msg("hemb: no online bearers in bond group")
			continue
		}

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
			d.loopMetrics.SelfLoopDrops.Add(1)
			log.Debug().Str("target", destInterface).Str("source", sourceInterface).
				Msg("failover: resolved target is source interface, skipping")
			continue
		}
		if len(msg.Visited) > 0 {
			skip := false
			for _, v := range msg.Visited {
				if v == destInterface {
					d.loopMetrics.VisitedSetDrops.Add(1)
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

		// Apply per-channel-type default TTL when rule doesn't specify one
		if ttlSeconds == 0 && ok && desc.DefaultTTL > 0 {
			ttlSeconds = int(desc.DefaultTTL / time.Second)
		}

		// Content-hash dedup: suppress duplicate payload→interface deliveries within TTL window
		if d.isDeliveryDuplicate(destInterface, payload) {
			d.loopMetrics.DeliveryDedups.Add(1)
			log.Debug().Str("dest", destInterface).Msg("delivery dedup: same payload recently delivered to this interface, skipping")
			continue
		}

		// Queue limit check: higher-precedence / higher-priority arrivals
		// evict weaker queued deliveries. STANAG 4406 precedence is the
		// primary key (Flash > Routine regardless of priority int), with
		// the legacy P0/P1/P2 priority used as tiebreaker within the
		// same precedence rank. P0 critical deliveries are never
		// evicted. Access-rule-driven dispatches don't yet carry
		// precedence on the wire (that plumbing lands in MESHSAT-544);
		// for now we default to Routine so operators still get the
		// Deferred-evicted-by-Routine win from the DB-level ordering.
		// [MESHSAT-546 / S2-03]
		if d.maxQueueDepth > 0 {
			depth, dErr := d.db.QueueDepth(destInterface)
			if dErr == nil && depth >= d.maxQueueDepth {
				weakest, wErr := d.db.WeakestEvictable(destInterface)
				newPrec := routineDefault("") // no precedence on RouteMessage yet
				newPrecRank := precedenceRankFromName(newPrec)
				if wErr != nil || weakest == nil || !isStrongerThanQueued(newPrecRank, m.Rule.Priority, weakest.PrecedenceRank, weakest.Priority) {
					log.Warn().Str("dest", destInterface).
						Int("depth", depth).Int("max", d.maxQueueDepth).
						Int("new_priority", m.Rule.Priority).
						Str("new_precedence", newPrec).
						Msg("queue depth limit reached, rejecting delivery")
					continue
				}
				reason := fmt.Sprintf("evicted: queue full, preempted by %s/%d", newPrec, m.Rule.Priority)
				if n, eErr := d.db.EvictDelivery(weakest.ID, reason); eErr != nil || n == 0 {
					log.Warn().Str("dest", destInterface).Err(eErr).
						Msg("queue depth limit: eviction failed, rejecting delivery")
					continue
				}
				log.Info().Str("dest", destInterface).
					Int64("evicted_id", weakest.ID).
					Str("evicted_precedence", weakest.Precedence).
					Int("evicted_priority", weakest.Priority).
					Str("new_precedence", newPrec).
					Int("new_priority", m.Rule.Priority).
					Msg("queue full: evicted weaker delivery to admit higher-precedence arrival")
				if d.signing != nil {
					d.signing.AuditEvent("delivery_preempt",
						&destInterface, nil, &weakest.ID, nil,
						fmt.Sprintf("preempted %s/%d by %s/%d",
							weakest.Precedence, weakest.Priority,
							newPrec, m.Rule.Priority))
				}
			}
		}
		if d.maxQueueBytes > 0 {
			qBytes, bErr := d.db.QueueBytes(destInterface)
			if bErr == nil && qBytes+int64(len(payload)) > d.maxQueueBytes {
				log.Warn().Str("dest", destInterface).Int64("bytes", qBytes).Int64("max", d.maxQueueBytes).
					Msg("queue bytes limit reached, rejecting delivery")
				continue
			}
		}

		ruleID := m.Rule.ID
		// QoS 0 (best-effort): no retries
		if m.Rule.QoSLevel == 0 {
			maxRetries = 0
		}

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

		// Set TTL expiry if configured (P0 critical messages exempt — never expire)
		if ttlSeconds > 0 && m.Rule.Priority > 0 {
			del.TTLSeconds = ttlSeconds
			exp := time.Now().Add(time.Duration(ttlSeconds) * time.Second).UTC().Format("2006-01-02 15:04:05")
			del.ExpiresAt = &exp
		}

		// Assign egress sequence number
		if seq, seqErr := d.db.IncrementEgressSeq(destInterface); seqErr == nil {
			del.SeqNum = seq
		}

		// Sign the payload for non-repudiation
		if d.signing != nil && len(payload) > 0 {
			del.Signature = d.signing.Sign(payload)
			del.SignerID = d.signing.SignerID()
		}

		// DTN bundle fragmentation (MESHSAT-408): if payload exceeds interface MTU,
		// split into fragments and create one delivery per fragment.
		if d.fragmentMgr != nil && desc.MaxPayload > 0 && len(payload) > desc.MaxPayload {
			bundleID, fragments, fragErr := Fragment(payload, desc.MaxPayload)
			if fragErr == nil && len(fragments) > 1 {
				log.Info().
					Int("fragments", len(fragments)).
					Int("mtu", desc.MaxPayload).
					Int("payload", len(payload)).
					Str("bundle_id", fmt.Sprintf("%x", bundleID[:8])).
					Str("dest", destInterface).
					Msg("DTN: payload exceeds MTU, fragmenting")

				for fi, frag := range fragments {
					fragDel := del // copy base delivery
					fragDel.Payload = frag
					fragDel.TextPreview = fmt.Sprintf("[frag %d/%d] %s", fi+1, len(fragments), preview)
					if seq, seqErr := d.db.IncrementEgressSeq(destInterface); seqErr == nil {
						fragDel.SeqNum = seq
					}
					if _, fragInsertErr := d.db.InsertDelivery(fragDel); fragInsertErr != nil {
						log.Error().Err(fragInsertErr).Int("fragment", fi).Msg("DTN: failed to insert fragment delivery")
					} else {
						count++
					}
				}
				continue // skip single-delivery insertion below
			}
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

// QueueDirectSend inserts a delivery record for a direct API send (no access rule).
// The DeliveryWorker for the target interface picks it up within 2 seconds.
// Returns the delivery ID and msg_ref for status tracking.
//
// precedence is the STANAG 4406 Edition 2 level carried on the delivery row
// ([MESHSAT-543]). Pass the canonical value (e.g. "Flash") or empty for the
// schema default ("Routine"). Callers that receive user input should
// normalise via [types.ParsePrecedence] before calling. Queue behaviour is
// unchanged in Phase 1; queue-by-precedence lands in MESHSAT-546 / S2-03.
func (d *Dispatcher) QueueDirectSend(interfaceID, text, precedence string) (int64, string, error) {
	msgRef := time.Now().UTC().Format("20060102-150405") + "-" + fmt.Sprintf("%05d", time.Now().Nanosecond()/10000)

	payload := []byte(text)
	preview := text
	if len(preview) > 200 {
		preview = preview[:200]
	}

	del := database.MessageDelivery{
		MsgRef:      msgRef,
		Channel:     interfaceID,
		Status:      "queued",
		Priority:    1,
		Payload:     payload,
		TextPreview: preview,
		MaxRetries:  3,
		QoSLevel:    1,
		Precedence:  precedence,
	}

	// Assign egress sequence number
	if seq, err := d.db.IncrementEgressSeq(interfaceID); err == nil {
		del.SeqNum = seq
	}

	// Sign for non-repudiation
	if d.signing != nil {
		del.Signature = d.signing.Sign(payload)
		del.SignerID = d.signing.SignerID()
	}

	delID, err := d.db.InsertDelivery(del)
	if err != nil {
		return 0, "", fmt.Errorf("queue delivery: %w", err)
	}

	log.Info().Int64("id", delID).Str("channel", interfaceID).Str("msg_ref", msgRef).Msg("direct send queued via delivery ledger")

	if d.emit != nil {
		d.emit(transport.MeshEvent{
			Type:    "delivery_queued",
			Message: fmt.Sprintf("Direct send queued: %s → %s", preview, interfaceID),
		})
	}

	return delID, msgRef, nil
}

// isDeliveryDuplicate checks whether the same payload was recently dispatched to
// the same interface. Uses a SHA-256 content hash with a configurable TTL window.
func (d *Dispatcher) isDeliveryDuplicate(destInterface string, payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	h := sha256.Sum256(append([]byte(destInterface+"|"), payload...))
	key := fmt.Sprintf("%x", h[:12])

	d.deliveryDedupMu.Lock()
	defer d.deliveryDedupMu.Unlock()

	if ts, ok := d.deliveryDedup[key]; ok && time.Since(ts) < d.deliveryDedupTTL {
		return true
	}
	d.deliveryDedup[key] = time.Now()
	return false
}

// pruneDeliveryDedup removes expired entries from the delivery dedup cache.
func (d *Dispatcher) pruneDeliveryDedup() {
	d.deliveryDedupMu.Lock()
	defer d.deliveryDedupMu.Unlock()
	now := time.Now()
	for k, ts := range d.deliveryDedup {
		if now.Sub(ts) > d.deliveryDedupTTL {
			delete(d.deliveryDedup, k)
		}
	}
}

// reapAckTimeouts checks all interface workers for pending ACKs that have timed out (5 minutes).
func (d *Dispatcher) reapAckTimeouts() {
	d.mu.RLock()
	channels := make([]string, 0, len(d.workers))
	for ch := range d.workers {
		channels = append(channels, ch)
	}
	d.mu.RUnlock()

	for _, ch := range channels {
		timedOut, err := d.db.GetPendingAcks(ch, 300) // 5-minute timeout
		if err != nil {
			log.Error().Err(err).Str("channel", ch).Msg("ack reaper: failed to query pending acks")
			continue
		}
		for _, del := range timedOut {
			if err := d.db.SetDeliveryAck(del.ID, "timeout"); err != nil {
				log.Error().Err(err).Int64("id", del.ID).Msg("ack reaper: failed to set timeout")
				continue
			}
			log.Warn().Int64("id", del.ID).Str("channel", ch).Msg("ack reaper: delivery ACK timed out")
			if d.emit != nil {
				d.emit(transport.MeshEvent{
					Type:    "delivery_ack_timeout",
					Message: fmt.Sprintf("ACK timeout for delivery %d on %s", del.ID, ch),
				})
			}
		}
	}
}

// SendViaBondGroup sends a payload through a HeMB bond group.
// Returns the number of bearers used or an error.
func (d *Dispatcher) SendViaBondGroup(groupID string, payload []byte) (int, error) {
	if d.failover == nil {
		return 0, fmt.Errorf("failover resolver not initialized")
	}
	if !d.db.IsBondGroup(groupID) {
		return 0, fmt.Errorf("unknown bond group: %s", groupID)
	}

	sendFnProvider := func(ifaceID string) func(ctx context.Context, data []byte) error {
		gw := d.gwProv.GatewayByInterfaceID(ifaceID)
		if gw != nil {
			log.Debug().Str("iface", ifaceID).Msg("hemb: sendFn resolved via gateway")
			return func(ctx context.Context, data []byte) error {
				return gw.Forward(ctx, &transport.MeshMessage{RawPayload: data})
			}
		}
		// Reticulum packet sender: crosses bridges via TCP/HDLC.
		if d.pktSender != nil {
			if fn := d.pktSender.GetPacketSender(ifaceID); fn != nil {
				log.Debug().Str("iface", ifaceID).Msg("hemb: sendFn resolved via packet sender")
				return fn
			}
		}
		// Last resort: raw mesh (local only — doesn't cross Meshtastic nodes).
		if d.mesh != nil && strings.HasPrefix(ifaceID, "mesh") {
			log.Debug().Str("iface", ifaceID).Msg("hemb: sendFn resolved via raw mesh")
			return func(ctx context.Context, data []byte) error {
				return d.mesh.SendRaw(ctx, transport.RawRequest{
					PortNum: 256,
					Payload: encoding_base64.StdEncoding.EncodeToString(data),
				})
			}
		}
		log.Warn().Str("iface", ifaceID).Msg("hemb: no sendFn for bearer — skipped")
		return nil
	}

	bearers := d.failover.SelectBearers(groupID, sendFnProvider)
	if len(bearers) == 0 {
		return 0, fmt.Errorf("no online bearers in bond group %s", groupID)
	}

	bdr := hemb.NewBonder(hemb.Options{
		Bearers:   bearers,
		DeliverFn: nil,
		EventCh:   hemb.GlobalEventBus.Channel(),
	})
	if err := bdr.Send(context.Background(), payload); err != nil {
		return 0, fmt.Errorf("bonded send failed: %w", err)
	}
	return len(bearers), nil
}

// ForwardHeMBFrame sends a raw HeMB frame to a specific interface via its gateway.
// Used by the TUN adapter to route coded symbols to physical transports.
func (d *Dispatcher) ForwardHeMBFrame(ifaceID string, data []byte) error {
	gw := d.gwProv.GatewayByInterfaceID(ifaceID)
	if gw == nil {
		return fmt.Errorf("no gateway for interface %q", ifaceID)
	}
	return gw.Forward(context.Background(), &transport.MeshMessage{
		RawPayload: data,
	})
}

// DeliveryWorker polls the delivery queue for a single channel and attempts delivery.
type DeliveryWorker struct {
	channelID       string
	desc            channel.ChannelDescriptor
	db              *database.DB
	gwProv          GatewayProvider
	mesh            transport.MeshTransport
	emit            func(transport.MeshEvent)
	signing         *SigningService
	transforms      *TransformPipeline
	access          *rules.AccessEvaluator // egress rule check before send
	passSched       PassStateProvider      // satellite pass scheduler (nil for non-satellite)
	routingIdentity *routing.Identity      // routing identity for delivery confirmations
	custodyMgr      *CustodyManager        // DTN custody transfer (MESHSAT-408)
	cancel          context.CancelFunc     // per-worker cancellation
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
	// Pass-aware scheduling: for satellite interfaces, only drain during Active/PostPass passes.
	if w.passSched != nil {
		mode := w.passSched.PassMode()
		if mode == 0 { // Idle — no pass within pre-wake window
			return
		}
		if mode == 1 { // PreWake — pass approaching, prepare queue but don't drain
			w.prepareQueue()
			return
		}
		// Mode 2 (Active) or 3 (PostPass): proceed with drain
	}

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

// prepareQueue runs during PreWake: expires stale deliveries and logs queue
// readiness so that when the pass becomes Active the queue is clean and ordered.
func (w *DeliveryWorker) prepareQueue() {
	// Expire any deliveries whose TTL has lapsed while waiting for a pass
	if n, err := w.db.ExpireDeliveriesForChannel(w.channelID); err != nil {
		log.Error().Err(err).Str("channel", w.channelID).Msg("pre-wake: TTL expiry check failed")
	} else if n > 0 {
		log.Info().Str("channel", w.channelID).Int64("expired", n).Msg("pre-wake: expired stale deliveries")
	}

	// Log queue state for observability
	depth, _ := w.db.QueueDepth(w.channelID)
	if depth > 0 {
		log.Debug().Str("channel", w.channelID).Int("queued", depth).Msg("pre-wake: queue ready for active pass")
	}
}

func (w *DeliveryWorker) deliver(ctx context.Context, del database.MessageDelivery) {
	// Safety check: re-read delivery status before processing. Between GetPendingDeliveries
	// and now, the delivery may have been completed by another path (e.g. MailboxCheck piggyback)
	// or the process may have restarted and RecoverStaleDeliveries re-queued it.
	if fresh, err := w.db.GetDelivery(del.ID); err == nil {
		if fresh.Status == "sent" || fresh.Status == "dead" || fresh.Status == "cancelled" {
			log.Debug().Int64("id", del.ID).Str("status", fresh.Status).Msg("delivery already terminal, skipping")
			return
		}
	}

	// Egress rule check: evaluate egress rules on the destination interface before sending.
	// Only applies when egress rules are configured for this interface (no rules = implicit allow).
	// If egress denies, mark the delivery as 'denied' and skip sending.
	if w.access != nil && w.access.HasEgressRules(w.channelID) {
		egressMsg := rules.RouteMessage{
			Text: del.TextPreview,
		}
		// Parse visited set for loop prevention context
		if del.Visited != "" && del.Visited != "[]" {
			var visited []string
			if err := json.Unmarshal([]byte(del.Visited), &visited); err == nil {
				egressMsg.Visited = visited
			}
		}
		matches := w.access.EvaluateEgress(w.channelID, egressMsg)
		if len(matches) == 0 {
			// Implicit deny — egress rules exist but none matched
			if err := w.db.SetDeliveryStatus(del.ID, "denied", "egress rules denied", ""); err != nil {
				log.Error().Err(err).Int64("id", del.ID).Msg("failed to mark delivery denied")
			}
			log.Info().Int64("id", del.ID).Str("channel", w.channelID).Msg("delivery denied by egress rules")
			if w.signing != nil {
				ifacePtr := &w.channelID
				dir := "egress"
				delID := del.ID
				w.signing.AuditEvent("drop", ifacePtr, &dir, &delID, del.RuleID, "egress rules denied")
			}
			return
		}
	}

	// TTL expiry check before send: verify delivery hasn't expired (P0 exempt)
	if del.Priority > 0 && del.ExpiresAt != nil && *del.ExpiresAt != "" {
		if expT, err := time.Parse("2006-01-02 15:04:05", *del.ExpiresAt); err == nil {
			if time.Now().UTC().After(expT) {
				if err := w.db.SetDeliveryStatus(del.ID, "expired", "TTL expired before send", ""); err != nil {
					log.Error().Err(err).Int64("id", del.ID).Msg("failed to mark delivery expired")
				}
				log.Info().Int64("id", del.ID).Str("channel", w.channelID).Msg("delivery expired before send attempt")
				return
			}
		}
	}

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
			if isCellular && encrypted && del.TextPreview != "" {
				// For cellular SMS with encryption: encrypt ONLY the text preview
				// (human-readable message), not the full JSON payload.
				// The MeshSat Android app decrypts to get plain text, not JSON.
				inputData = []byte(del.TextPreview)
			} else if len(del.Payload) > 0 {
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
					// Prepend protocol version byte after all transforms.
					transformed = codec.PrependVersionByte(transformed)
					del.Payload = transformed
					del.TextPreview = string(transformed)
				}
			}
		}
	}

	// DTN custody transfer (MESHSAT-408): if custody is enabled for this delivery,
	// wrap the payload with a custody offer header. The receiving node extracts
	// the payload, creates a local delivery, and sends back an ACK on the same
	// interface. This is point-to-point — NOT broadcast.
	if w.custodyMgr != nil && del.CustodyID != "" && w.routingIdentity != nil {
		custodyID, _ := parseCustodyID(del.CustodyID)
		offer := &CustodyOffer{
			CustodyID:  custodyID,
			SourceHash: w.routingIdentity.DestHash(),
			DeliveryID: uint32(del.ID),
			Payload:    del.Payload,
		}
		// Replace the payload with the custody-wrapped version.
		del.Payload = MarshalCustodyOffer(offer)
		del.TextPreview = fmt.Sprintf("[custody:%x] %s", custodyID[:4], del.TextPreview)

		ackCh := w.custodyMgr.RegisterOffer(offer)
		log.Info().
			Str("custody_id", fmt.Sprintf("%x", custodyID[:8])).
			Str("channel", w.channelID).
			Int64("delivery_id", del.ID).
			Msg("DTN: custody offer sent with delivery")

		// After send completes, check for ACK asynchronously.
		defer func() {
			select {
			case ack := <-ackCh:
				if ack != nil {
					log.Info().
						Str("custody_id", fmt.Sprintf("%x", custodyID[:8])).
						Str("acceptor", fmt.Sprintf("%x", ack.AcceptorHash[:8])).
						Msg("DTN: custody accepted by relay")
					_ = w.db.SetDeliveryStatus(del.ID, "custody_transferred",
						fmt.Sprintf("custody accepted by %x", ack.AcceptorHash[:8]), "")
				}
			case <-time.After(60 * time.Second):
				log.Debug().Str("custody_id", fmt.Sprintf("%x", custodyID[:8])).
					Msg("DTN: custody ACK timeout — delivery continues as normal send")
			}
		}()
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

	// QoS 1+: transport success IS the ACK (Iridium mo_status=0, MQTT PUBACK, HTTP 2xx).
	// Immediately mark as acked → delivered.
	if del.QoSLevel >= 1 {
		if err := w.db.SetDeliveryAck(del.ID, "acked"); err != nil {
			log.Error().Err(err).Int64("id", del.ID).Msg("failed to set delivery ack")
		}
		log.Info().Int64("id", del.ID).Str("channel", w.channelID).Int("qos", del.QoSLevel).Msg("delivery sent + acked")
	} else {
		log.Info().Int64("id", del.ID).Str("channel", w.channelID).Msg("delivery sent (QoS 0, no ack)")
	}

	// Audit the successful delivery
	if w.signing != nil {
		ifacePtr := &w.channelID
		dir := "egress"
		delID := del.ID
		w.signing.AuditEvent("deliver", ifacePtr, &dir, &delID, del.RuleID, del.TextPreview)
	}

	// Generate delivery confirmation (unforgeable proof of delivery)
	if w.routingIdentity != nil && len(del.Payload) > 0 {
		conf := routing.NewDeliveryConfirmation(w.routingIdentity, del.Payload)
		confData := conf.Marshal()
		log.Debug().Int64("id", del.ID).Int("conf_size", len(confData)).
			Str("channel", w.channelID).Msg("delivery confirmation generated")
	}

	if w.emit != nil {
		evtType := "delivery_sent"
		if del.QoSLevel >= 1 {
			evtType = "delivery_acked"
		}
		w.emit(transport.MeshEvent{
			Type:    evtType,
			Message: fmt.Sprintf("Delivered to %s", w.channelID),
		})
	}
}

func (w *DeliveryWorker) handleFailure(del database.MessageDelivery, deliveryErr error) {
	errMsg := deliveryErr.Error()

	// QoS 0 (best-effort): mark dead immediately, no retry
	if del.QoSLevel == 0 {
		if err := w.db.SetDeliveryStatus(del.ID, "dead", errMsg, ""); err != nil {
			log.Error().Err(err).Int64("id", del.ID).Msg("failed to mark QoS 0 delivery dead")
		}
		log.Info().Int64("id", del.ID).Str("channel", w.channelID).Msg("QoS 0 delivery failed, no retry (best-effort)")
		if w.emit != nil {
			w.emit(transport.MeshEvent{
				Type:    "delivery_dead",
				Message: fmt.Sprintf("QoS 0 delivery to %s failed (best-effort, no retry): %s", w.channelID, errMsg),
			})
		}
		return
	}

	newRetries := del.Retries + 1

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
