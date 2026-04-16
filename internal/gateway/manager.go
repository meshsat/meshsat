package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/certpin"
	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// ReceiverStartFunc is called when a gateway starts so inbound message receivers
// can be registered. This breaks the gateway→engine import cycle by using a callback.
type ReceiverStartFunc func(ctx context.Context, gw Gateway)

// Manager coordinates gateway lifecycle (start/stop/config).
type Manager struct {
	db              *database.DB
	sat             transport.SatTransport  // optional, for iridium SBD gateway (9603)
	imtSat          transport.SatTransport  // optional, for iridium IMT gateway (9704)
	cell            transport.CellTransport // optional, for cellular gateway
	predictor       PassPredictor           // optional, for pass scheduler
	onReceiverStart ReceiverStartFunc       // called when a gateway starts
	onEventEmit     EventEmitFunc           // SSE event emitter callback
	nodeNameFn      func(uint32) string     // resolves mesh node ID to name
	running         map[string]Gateway      // keyed by instance_id ("iridium_0", "iridium_1")
	runningByIface  map[string]Gateway      // v0.3.0: keyed by interface ID ("iridium_0")
	mu              sync.RWMutex
	cancelFn        context.CancelFunc
	supervisor      *transport.DeviceSupervisor // optional, for device event watching

	// Multi-instance transport registry: maps instance_id → transport
	satTransports  map[string]transport.SatTransport  // instance_id → SatTransport
	cellTransports map[string]transport.CellTransport // instance_id → CellTransport
	transportsMu   sync.RWMutex
}

// NewManager creates a new gateway manager.
func NewManager(db *database.DB, sat transport.SatTransport) *Manager {
	m := &Manager{
		db:             db,
		sat:            sat,
		running:        make(map[string]Gateway),
		runningByIface: make(map[string]Gateway),
		satTransports:  make(map[string]transport.SatTransport),
		cellTransports: make(map[string]transport.CellTransport),
	}
	// Register primary SBD transport as default instance
	if sat != nil {
		m.satTransports["iridium_0"] = sat
	}
	return m
}

// SetCellTransport sets the cellular transport for the cellular gateway.
func (m *Manager) SetCellTransport(cell transport.CellTransport) {
	m.cell = cell
	m.transportsMu.Lock()
	m.cellTransports["cellular_0"] = cell
	m.transportsMu.Unlock()
}

// SetIMTTransport sets the Iridium IMT (9704) transport for coexistence with SBD.
func (m *Manager) SetIMTTransport(imtSat transport.SatTransport) {
	m.imtSat = imtSat
	m.transportsMu.Lock()
	m.satTransports["iridium_imt_0"] = imtSat
	m.transportsMu.Unlock()
}

// RegisterSatTransport registers a satellite transport for a specific instance.
func (m *Manager) RegisterSatTransport(instanceID string, sat transport.SatTransport) {
	m.transportsMu.Lock()
	defer m.transportsMu.Unlock()
	m.satTransports[instanceID] = sat
}

// RegisterCellTransport registers a cellular transport for a specific instance.
func (m *Manager) RegisterCellTransport(instanceID string, cell transport.CellTransport) {
	m.transportsMu.Lock()
	defer m.transportsMu.Unlock()
	m.cellTransports[instanceID] = cell
}

// UnregisterTransport removes a transport instance.
func (m *Manager) UnregisterTransport(instanceID string) {
	m.transportsMu.Lock()
	defer m.transportsMu.Unlock()
	delete(m.satTransports, instanceID)
	delete(m.cellTransports, instanceID)
}

// getSatTransport returns the satellite transport for an instance, falling back to legacy.
func (m *Manager) getSatTransport(instanceID string) transport.SatTransport {
	m.transportsMu.RLock()
	defer m.transportsMu.RUnlock()
	if sat, ok := m.satTransports[instanceID]; ok {
		return sat
	}
	// Fallback: check legacy single-instance transports by type prefix
	if len(instanceID) > 4 && instanceID[:4] == "irid" {
		// iridium_imt_X → check imtSat, then sat
		if strings.Contains(instanceID, "imt") {
			if m.imtSat != nil {
				return m.imtSat
			}
			return m.sat
		}
		// iridium_X → check sat
		return m.sat
	}
	return m.sat
}

// getCellTransport returns the cellular transport for an instance, falling back to legacy.
func (m *Manager) getCellTransport(instanceID string) transport.CellTransport {
	m.transportsMu.RLock()
	defer m.transportsMu.RUnlock()
	if cell, ok := m.cellTransports[instanceID]; ok {
		return cell
	}
	return m.cell
}

// SetPassPredictor sets the pass predictor for pass-aware scheduling on Iridium gateways.
func (m *Manager) SetPassPredictor(p PassPredictor) {
	m.predictor = p
}

// SetReceiverStartFunc sets the callback invoked when a gateway starts,
// so the processor can register an inbound message receiver for it.
func (m *Manager) SetReceiverStartFunc(fn ReceiverStartFunc) {
	m.onReceiverStart = fn
}

// SetEventEmitFunc sets the callback for gateways to emit events to the SSE stream.
func (m *Manager) SetEventEmitFunc(fn EventEmitFunc) {
	m.onEventEmit = fn
}

// SetNodeNameResolver sets the function used to resolve mesh node IDs to names for SMS.
func (m *Manager) SetNodeNameResolver(fn func(uint32) string) {
	m.nodeNameFn = fn
}

// GetPassScheduler returns the pass scheduler from the running Iridium gateway, if any.
func (m *Manager) GetPassScheduler() *PassScheduler {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, gw := range m.running {
		if gw == nil {
			continue
		}
		if sgw, ok := gw.(*SBDGateway); ok {
			return sgw.PassSchedulerRef()
		}
		if igw, ok := gw.(*IMTGateway); ok {
			return igw.PassSchedulerRef()
		}
	}
	return nil
}

// Start loads enabled configs from DB and starts their gateways.
func (m *Manager) Start(ctx context.Context) error {
	ctx, m.cancelFn = context.WithCancel(ctx)

	configs, err := m.db.GetAllGatewayConfigs()
	if err != nil {
		log.Warn().Err(err).Msg("failed to load gateway configs, continuing without gateways")
		return nil
	}

	// Track how many transports are registered per type to avoid starting
	// more gateway instances than we have distinct hardware.
	m.transportsMu.RLock()
	transportCount := make(map[string]int)
	for id := range m.satTransports {
		// Map transport instance ID to gateway type
		if strings.Contains(id, "imt") {
			transportCount["iridium_imt"]++
		} else if strings.HasPrefix(id, "iridium") {
			transportCount["iridium"]++
		}
	}
	for range m.cellTransports {
		transportCount["cellular"]++
	}
	m.transportsMu.RUnlock()

	startedByType := make(map[string]int)
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		instanceID := cfg.InstanceID
		if instanceID == "" {
			instanceID = cfg.Type + "_0"
		}

		// For hardware-backed gateways, don't start more instances than
		// registered transports. This prevents stale DB configs from
		// spawning duplicate gateways that share the same transport.
		if role := gatewayTypeToRole(cfg.Type); role != transport.RoleNone {
			maxTransports := transportCount[cfg.Type]
			if maxTransports == 0 {
				maxTransports = 1 // at least try the default
			}
			if startedByType[cfg.Type] >= maxTransports {
				log.Info().Str("type", cfg.Type).Str("instance", instanceID).
					Msg("skipping gateway — no additional transport registered")
				continue
			}
		}

		gw, err := m.createGatewayForInstance(cfg.Type, instanceID, cfg.Config)
		if err != nil {
			log.Error().Err(err).Str("type", cfg.Type).Str("instance", instanceID).Msg("failed to create gateway")
			continue
		}
		if err := gw.Start(ctx); err != nil {
			log.Error().Err(err).Str("type", cfg.Type).Str("instance", instanceID).Msg("failed to start gateway")
			continue
		}
		m.mu.Lock()
		m.running[instanceID] = gw
		m.syncIfaceMap(cfg.Type, gw)
		m.mu.Unlock()
		if m.onReceiverStart != nil {
			m.onReceiverStart(ctx, gw)
		}
		startedByType[cfg.Type]++
		log.Info().Str("type", cfg.Type).Str("instance", instanceID).Msg("gateway started from saved config")
	}

	// v0.3.0 path: also bootstrap from interfaces table.
	m.bootstrapInterfaceGateways()

	return nil
}

// Stop stops all running gateways.
func (m *Manager) Stop() {
	if m.cancelFn != nil {
		m.cancelFn()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for instanceID, gw := range m.running {
		if err := gw.Stop(); err != nil {
			log.Error().Err(err).Str("instance", instanceID).Msg("failed to stop gateway")
		}
	}
	m.running = make(map[string]Gateway)
	m.runningByIface = make(map[string]Gateway)
}

// WatchDeviceEvents subscribes to DeviceSupervisor events and automatically
// stops/restarts gateways when hardware is disconnected, reconnected, or swapped.
// This bridges the gap where gateway configs were loaded from DB at startup but
// never reconciled with live hardware state.
func (m *Manager) WatchDeviceEvents(ctx context.Context, supervisor *transport.DeviceSupervisor) {
	m.supervisor = supervisor
	events, unsub := supervisor.SubscribeEvents()

	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				m.handleDeviceEvent(ctx, ev)
			}
		}
	}()

	log.Info().Msg("gwmgr: watching device supervisor events for hot-swap")
}

// handleDeviceEvent reacts to a single device supervisor event.
func (m *Manager) handleDeviceEvent(ctx context.Context, ev transport.DeviceEvent) {
	if ev.Device == nil {
		return
	}

	gwType := roleToGatewayType(ev.Device.Role)
	if gwType == "" {
		return // not a gateway-backed device (meshtastic, gps, unknown)
	}

	// Resolve which instance owns this port
	instanceID := ""
	if m.supervisor != nil {
		instanceID = m.supervisor.GetPortInstance(ev.Device.DevPath)
	}

	switch ev.Type {
	case "device_disconnected", "device_removed":
		if instanceID == "" {
			// Legacy fallback: find any running instance of this type
			instanceID = m.findRunningInstance(gwType)
		}
		if instanceID == "" {
			return
		}

		m.mu.RLock()
		_, running := m.running[instanceID]
		m.mu.RUnlock()
		if running {
			log.Info().Str("instance", instanceID).Str("port", ev.Device.DevPath).
				Str("event", ev.Type).Msg("gwmgr: device lost, stopping gateway")
			m.mu.Lock()
			if gw, ok := m.running[instanceID]; ok {
				gw.Stop()
				delete(m.running, instanceID)
				m.unsyncIfaceMap(gw)
			}
			m.mu.Unlock()
		}

	case "device_connected":
		// Determine instance ID for this new device
		if instanceID == "" {
			// Auto-assign: find or create an instance for this type
			instanceID = m.findOrCreateInstance(gwType)
		}

		m.mu.RLock()
		_, alreadyRunning := m.running[instanceID]
		m.mu.RUnlock()
		if alreadyRunning {
			return
		}

		cfg, err := m.db.GetGatewayConfigByInstance(instanceID)
		if err != nil {
			log.Info().Str("type", gwType).Str("instance", instanceID).Str("port", ev.Device.DevPath).
				Msg("gwmgr: device connected, auto-creating gateway config")
			if err := m.db.SaveGatewayConfigInstance(gwType, instanceID, true, "{}"); err != nil {
				log.Warn().Err(err).Str("instance", instanceID).Msg("gwmgr: failed to create default config")
				return
			}
		} else if !cfg.Enabled {
			log.Info().Str("instance", instanceID).Str("port", ev.Device.DevPath).
				Msg("gwmgr: device connected, re-enabling gateway")
			m.db.SaveGatewayConfigInstance(gwType, instanceID, true, cfg.Config)
		}

		log.Info().Str("type", gwType).Str("instance", instanceID).Str("port", ev.Device.DevPath).
			Msg("gwmgr: device connected, starting gateway")

		capturedInstance := instanceID
		time.AfterFunc(2*time.Second, func() {
			if err := m.StartGatewayInstance(ctx, capturedInstance); err != nil {
				log.Error().Err(err).Str("instance", capturedInstance).Msg("gwmgr: failed to restart gateway after device reconnect")
			}
		})
	}
}

// findRunningInstance returns the first running instance ID of a given gateway type.
func (m *Manager) findRunningInstance(gwType string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for instanceID, gw := range m.running {
		if gw != nil && gw.Type() == gwType {
			return instanceID
		}
	}
	return ""
}

// findOrCreateInstance returns an available instance ID for a gateway type.
// If an existing instance has no running gateway, reuse it. Otherwise allocate next.
func (m *Manager) findOrCreateInstance(gwType string) string {
	configs, _ := m.db.GetGatewayConfigsByType(gwType)
	m.mu.RLock()
	for _, cfg := range configs {
		if _, running := m.running[cfg.InstanceID]; !running {
			m.mu.RUnlock()
			return cfg.InstanceID
		}
	}
	m.mu.RUnlock()

	// All existing instances are running — allocate next
	nextID, _ := m.db.NextGatewayInstanceID(gwType)
	return nextID
}

// roleToGatewayType maps a DeviceSupervisor device role to a gateway type string.
// Returns "" for roles that don't correspond to a gateway (meshtastic, gps).
func roleToGatewayType(role transport.DeviceRole) string {
	switch role {
	case transport.RoleIridium9603:
		return "iridium"
	case transport.RoleIridium9704:
		return "iridium_imt"
	case transport.RoleCellular:
		return "cellular"
	case transport.RoleZigBee:
		return "zigbee"
	default:
		return ""
	}
}

// gatewayTypeToRole maps a gateway type to the device role that provides its hardware.
// Returns RoleNone for gateway types that don't need hardware (mqtt, webhook, tak, aprs).
func gatewayTypeToRole(gwType string) transport.DeviceRole {
	switch gwType {
	case "iridium":
		return transport.RoleIridium9603
	case "iridium_imt":
		return transport.RoleIridium9704
	case "cellular":
		return transport.RoleCellular
	case "zigbee":
		return transport.RoleZigBee
	default:
		return transport.RoleNone
	}
}

// ReconcileWithHardware cross-checks running gateways and DB configs against
// actual hardware detected by the device supervisor. This is the key to
// zero-config operation: plug hardware into any device and it just works.
//
// Phase 1: Disable and stop gateways whose hardware is missing.
// Phase 2: Auto-create default configs and start gateways for detected hardware.
//
// Should be called after Start() and WatchDeviceEvents().
func (m *Manager) ReconcileWithHardware(ctx context.Context) {
	if m.supervisor == nil {
		return
	}
	registry := m.supervisor.Registry()

	// Build set of hardware-backed gateway types actually present (with count).
	presentTypes := make(map[string]int) // gwType → count of devices
	for _, entry := range registry.ListAll() {
		if gwType := roleToGatewayType(entry.Role); gwType != "" {
			presentTypes[gwType]++
		}
	}

	// Phase 1: Disable DB configs and stop gateways for missing hardware.
	configs, _ := m.db.GetAllGatewayConfigs()
	for _, cfg := range configs {
		role := gatewayTypeToRole(cfg.Type)
		if role == transport.RoleNone {
			continue // software-only gateway (mqtt, webhook, tak, aprs)
		}
		if presentTypes[cfg.Type] > 0 {
			continue // hardware present for this type
		}

		instanceID := cfg.InstanceID
		if instanceID == "" {
			instanceID = cfg.Type + "_0"
		}

		if cfg.Enabled {
			log.Info().Str("instance", instanceID).Msg("gwmgr: reconcile — disabling gateway (hardware not present)")
			m.db.SaveGatewayConfigInstance(cfg.Type, instanceID, false, cfg.Config)
		}

		m.mu.Lock()
		if gw, ok := m.running[instanceID]; ok {
			gw.Stop()
			delete(m.running, instanceID)
			m.unsyncIfaceMap(gw)
		}
		m.mu.Unlock()
	}

	// Phase 2: Auto-create and start gateways for detected hardware.
	// Count running gateways per type to avoid creating more instances than hardware.
	m.mu.RLock()
	runningByType := make(map[string]int)
	for _, gw := range m.running {
		if gw != nil {
			runningByType[gw.Type()]++
		}
	}
	m.mu.RUnlock()

	for gwType, hwCount := range presentTypes {
		// Skip if we already have enough running instances for this type
		if runningByType[gwType] >= hwCount {
			continue
		}

		// Need to start (hwCount - runningByType[gwType]) more instances
		needed := hwCount - runningByType[gwType]
		for i := 0; i < needed; i++ {
			instanceID := m.findOrCreateInstance(gwType)

			m.mu.RLock()
			_, running := m.running[instanceID]
			m.mu.RUnlock()
			if running {
				continue
			}

			cfg, err := m.db.GetGatewayConfigByInstance(instanceID)
			if err != nil {
				log.Info().Str("type", gwType).Str("instance", instanceID).
					Msg("gwmgr: reconcile — auto-creating config for detected hardware")
				if err := m.db.SaveGatewayConfigInstance(gwType, instanceID, true, "{}"); err != nil {
					log.Warn().Err(err).Str("instance", instanceID).Msg("gwmgr: reconcile — failed to create default config")
					continue
				}
			} else if !cfg.Enabled {
				log.Info().Str("instance", instanceID).
					Msg("gwmgr: reconcile — re-enabling gateway (hardware detected)")
				m.db.SaveGatewayConfigInstance(gwType, instanceID, true, cfg.Config)
			}

			log.Info().Str("type", gwType).Str("instance", instanceID).
				Msg("gwmgr: reconcile — starting gateway for detected hardware")
			if err := m.StartGatewayInstance(ctx, instanceID); err != nil {
				log.Warn().Err(err).Str("instance", instanceID).Msg("gwmgr: reconcile — failed to start gateway")
			}
		}
	}
}

// Configure creates or updates a gateway configuration and optionally starts it.
func (m *Manager) Configure(ctx context.Context, gwType string, enabled bool, configJSON string) error {
	return m.ConfigureInstance(ctx, gwType, gwType+"_0", enabled, configJSON)
}

// ConfigureInstance creates or updates a specific gateway instance configuration.
func (m *Manager) ConfigureInstance(ctx context.Context, gwType, instanceID string, enabled bool, configJSON string) error {
	if _, err := m.createGatewayForInstance(gwType, instanceID, configJSON); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	if err := m.db.SaveGatewayConfigInstance(gwType, instanceID, enabled, configJSON); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	m.mu.Lock()
	if existing, ok := m.running[instanceID]; ok {
		existing.Stop()
		delete(m.running, instanceID)
		m.unsyncIfaceMap(existing)
	}
	m.mu.Unlock()

	if enabled {
		return m.StartGatewayInstance(ctx, instanceID)
	}
	return nil
}

// Delete stops and removes a gateway configuration (legacy — first instance).
func (m *Manager) Delete(gwType string) error {
	return m.DeleteInstance(gwType + "_0")
}

// DeleteInstance stops and removes a specific gateway instance.
func (m *Manager) DeleteInstance(instanceID string) error {
	m.mu.Lock()
	if gw, ok := m.running[instanceID]; ok {
		gw.Stop()
		delete(m.running, instanceID)
		m.unsyncIfaceMap(gw)
	}
	m.mu.Unlock()

	return m.db.DeleteGatewayConfigInstance(instanceID)
}

// StartGateway starts the first instance of a gateway type from its saved config.
func (m *Manager) StartGateway(ctx context.Context, gwType string) error {
	cfg, err := m.db.GetGatewayConfig(gwType)
	if err != nil {
		return fmt.Errorf("get config: %w", err)
	}
	instanceID := cfg.InstanceID
	if instanceID == "" {
		instanceID = gwType + "_0"
	}
	return m.StartGatewayInstance(ctx, instanceID)
}

// StartGatewayInstance starts a specific gateway instance from its saved config.
func (m *Manager) StartGatewayInstance(ctx context.Context, instanceID string) error {
	cfg, err := m.db.GetGatewayConfigByInstance(instanceID)
	if err != nil {
		return fmt.Errorf("get config for %s: %w", instanceID, err)
	}

	m.mu.Lock()
	if _, ok := m.running[instanceID]; ok {
		m.mu.Unlock()
		return fmt.Errorf("gateway %s is already running", instanceID)
	}
	m.running[instanceID] = nil // sentinel
	m.mu.Unlock()

	gw, err := m.createGatewayForInstance(cfg.Type, instanceID, cfg.Config)
	if err != nil {
		m.mu.Lock()
		delete(m.running, instanceID)
		m.mu.Unlock()
		return err
	}

	if err := gw.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.running, instanceID)
		m.mu.Unlock()
		return fmt.Errorf("start %s: %w", instanceID, err)
	}

	m.mu.Lock()
	m.running[instanceID] = gw
	m.syncIfaceMap(cfg.Type, gw)
	m.mu.Unlock()

	if m.onReceiverStart != nil {
		m.onReceiverStart(ctx, gw)
	}

	m.db.SaveGatewayConfigInstance(cfg.Type, instanceID, true, cfg.Config)

	return nil
}

// StopGateway stops the first running instance of a gateway type.
func (m *Manager) StopGateway(gwType string) error {
	instanceID := m.findRunningInstance(gwType)
	if instanceID == "" {
		return fmt.Errorf("gateway %s is not running", gwType)
	}
	return m.StopGatewayInstance(instanceID)
}

// StopGatewayInstance stops a specific running gateway instance.
func (m *Manager) StopGatewayInstance(instanceID string) error {
	m.mu.Lock()
	gw, ok := m.running[instanceID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("gateway %s is not running", instanceID)
	}
	delete(m.running, instanceID)
	m.unsyncIfaceMap(gw)
	m.mu.Unlock()

	if err := gw.Stop(); err != nil {
		return err
	}

	cfg, err := m.db.GetGatewayConfigByInstance(instanceID)
	if err == nil {
		m.db.SaveGatewayConfigInstance(cfg.Type, instanceID, false, cfg.Config)
	}

	return nil
}

// TestGateway performs a connectivity test for a gateway type.
func (m *Manager) TestGateway(gwType string) error {
	cfg, err := m.db.GetGatewayConfig(gwType)
	if err != nil {
		return fmt.Errorf("no config found for %s", gwType)
	}

	switch gwType {
	case "mqtt":
		mqttCfg, err := ParseMQTTConfig(cfg.Config)
		if err != nil {
			return err
		}
		gw := NewMQTTGateway(*mqttCfg)
		return gw.TestConnection()
	case "iridium", "iridium_imt":
		instanceID := cfg.InstanceID
		if instanceID == "" {
			instanceID = gwType + "_0"
		}
		sat := m.getSatTransport(instanceID)
		if sat == nil {
			return fmt.Errorf("satellite transport not available for %s", instanceID)
		}
		status, err := sat.GetStatus(context.Background())
		if err != nil {
			return err
		}
		if !status.Connected {
			return fmt.Errorf("modem not connected (%s)", instanceID)
		}
		return nil
	case "cellular":
		instanceID := cfg.InstanceID
		if instanceID == "" {
			instanceID = "cellular_0"
		}
		cell := m.getCellTransport(instanceID)
		if cell == nil {
			return fmt.Errorf("cellular transport not available")
		}
		status, err := cell.GetStatus(context.Background())
		if err != nil {
			return err
		}
		if !status.Connected {
			return fmt.Errorf("cellular modem not connected")
		}
		if status.SIMState != "READY" {
			return fmt.Errorf("SIM not ready: %s", status.SIMState)
		}
		return nil
	case "webhook":
		whCfg, err := ParseWebhookConfig(cfg.Config)
		if err != nil {
			return err
		}
		if whCfg.OutboundURL == "" {
			return fmt.Errorf("no outbound URL configured")
		}
		pin := certpin.FromEnv("MESHSAT_HUB_CERT_PIN", "MESHSAT_HUB_CERT_PIN_BACKUP")
		client := certpin.PinnedClient(pin, time.Duration(whCfg.TimeoutSec)*time.Second)
		req, err := http.NewRequest("HEAD", whCfg.OutboundURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("webhook endpoint unreachable: %w", err)
		}
		resp.Body.Close()
		return nil
	case "zigbee":
		zigbeeID := m.findRunningInstance("zigbee")
		if zigbeeID == "" {
			return fmt.Errorf("zigbee gateway not running")
		}
		m.mu.RLock()
		zgw := m.running[zigbeeID]
		m.mu.RUnlock()
		if zgw == nil || !zgw.Status().Connected {
			return fmt.Errorf("zigbee coordinator not connected")
		}
		return nil
	default:
		return fmt.Errorf("unknown gateway type: %s", gwType)
	}
}

// Gateways returns all running gateways for processor registration.
func (m *Manager) Gateways() []Gateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	gws := make([]Gateway, 0, len(m.running))
	for _, gw := range m.running {
		gws = append(gws, gw)
	}
	return gws
}

// GatewayByInterfaceID returns the running gateway bound to a specific interface ID.
// Returns nil if no gateway is running for that interface.
func (m *Manager) GatewayByInterfaceID(id string) Gateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runningByIface[id]
}

// ResolveGatewayInterface maps a gateway type (e.g. "iridium_imt", "iridium", "mqtt")
// to the interface ID of the first running gateway of that type.
func (m *Manager) ResolveGatewayInterface(gwType string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Check interface-keyed map first
	for ifaceID, gw := range m.runningByIface {
		if gw != nil && gw.Type() == gwType {
			return ifaceID
		}
	}
	// Check instance-keyed running map
	for _, gw := range m.running {
		if gw != nil && gw.Type() == gwType {
			if m.db != nil {
				if ifaces, err := m.db.GetInterfacesByType(gwType); err == nil && len(ifaces) > 0 {
					return ifaces[0].ID
				}
			}
		}
	}
	return ""
}

// StartInterfaceGateway starts a gateway for an interface, using its channel_type and config.
func (m *Manager) StartInterfaceGateway(ctx context.Context, ifaceID, channelType, configJSON string) error {
	m.mu.Lock()
	if _, ok := m.runningByIface[ifaceID]; ok {
		m.mu.Unlock()
		return fmt.Errorf("interface %s gateway already running", ifaceID)
	}
	m.mu.Unlock()

	gw, err := m.createGatewayForInstance(channelType, ifaceID, configJSON)
	if err != nil {
		return fmt.Errorf("create gateway for interface %s: %w", ifaceID, err)
	}

	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("start gateway for interface %s: %w", ifaceID, err)
	}

	m.mu.Lock()
	m.runningByIface[ifaceID] = gw
	// Also register in instance map
	m.running[ifaceID] = gw
	m.mu.Unlock()

	if m.onReceiverStart != nil {
		m.onReceiverStart(ctx, gw)
	}

	log.Info().Str("interface", ifaceID).Str("type", channelType).Msg("interface gateway started")
	return nil
}

// StopInterfaceGateway stops the gateway bound to a specific interface ID.
func (m *Manager) StopInterfaceGateway(ifaceID string) error {
	m.mu.Lock()
	gw, ok := m.runningByIface[ifaceID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no gateway running for interface %s", ifaceID)
	}
	delete(m.runningByIface, ifaceID)
	// Remove from legacy map if it points to the same gateway
	for k, v := range m.running {
		if v == gw {
			delete(m.running, k)
			break
		}
	}
	m.mu.Unlock()

	if err := gw.Stop(); err != nil {
		return fmt.Errorf("stop gateway for interface %s: %w", ifaceID, err)
	}

	log.Info().Str("interface", ifaceID).Msg("interface gateway stopped")
	return nil
}

// bootstrapInterfaceGateways maps existing running gateways to their interface IDs.
// This bridges the gateway_config start path with the interface model.
func (m *Manager) bootstrapInterfaceGateways() {
	ifaces, err := m.db.GetAllInterfaces()
	if err != nil {
		log.Warn().Err(err).Msg("gwmgr: failed to load interfaces for gateway mapping")
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	mapped := 0
	for _, iface := range ifaces {
		if !iface.Enabled {
			continue
		}
		// Find a running gateway of matching channel_type
		for _, gw := range m.running {
			if gw != nil && gw.Type() == iface.ChannelType {
				if _, already := m.runningByIface[iface.ID]; !already {
					m.runningByIface[iface.ID] = gw
					mapped++
				}
				break
			}
		}
	}

	if mapped > 0 {
		log.Info().Int("count", mapped).Msg("gwmgr: mapped running gateways to interface IDs")
	}
}

// syncIfaceMap registers a gateway under all matching interface IDs.
// Must be called with m.mu held.
func (m *Manager) syncIfaceMap(gwType string, gw Gateway) {
	ifaces, err := m.db.GetAllInterfaces()
	if err != nil {
		return
	}
	for _, iface := range ifaces {
		if iface.Enabled && iface.ChannelType == gwType {
			m.runningByIface[iface.ID] = gw
		}
	}
}

// unsyncIfaceMap removes all interface map entries pointing to a gateway.
// Must be called with m.mu held.
func (m *Manager) unsyncIfaceMap(gw Gateway) {
	for id, g := range m.runningByIface {
		if g == gw {
			delete(m.runningByIface, id)
		}
	}
}

// GetStatus returns status info for all known gateway instances (configured or running).
func (m *Manager) GetStatus() []GatewayStatusResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []GatewayStatusResponse

	configs, err := m.db.GetAllGatewayConfigs()
	if err != nil {
		return results
	}

	for _, cfg := range configs {
		instanceID := cfg.InstanceID
		if instanceID == "" {
			instanceID = cfg.Type + "_0"
		}
		resp := GatewayStatusResponse{
			Type:       cfg.Type,
			InstanceID: instanceID,
			Enabled:    cfg.Enabled,
		}

		if gw, ok := m.running[instanceID]; ok && gw != nil {
			status := gw.Status()
			resp.Connected = status.Connected
			resp.MessagesIn = status.MessagesIn
			resp.MessagesOut = status.MessagesOut
			resp.Errors = status.Errors
			resp.DLQPending = status.DLQPending
			resp.LastActivity = status.LastActivity
			resp.ConnectionUptime = status.ConnectionUptime
		}

		resp.Config = m.redactConfig(cfg.Type, cfg.Config)

		results = append(results, resp)
	}

	return results
}

// GetSingleStatus returns status for a specific gateway type (first instance).
func (m *Manager) GetSingleStatus(gwType string) (*GatewayStatusResponse, error) {
	cfg, err := m.db.GetGatewayConfig(gwType)
	if err != nil {
		return nil, fmt.Errorf("gateway %s not configured", gwType)
	}

	instanceID := cfg.InstanceID
	if instanceID == "" {
		instanceID = gwType + "_0"
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	resp := &GatewayStatusResponse{
		Type:       cfg.Type,
		InstanceID: instanceID,
		Enabled:    cfg.Enabled,
		Config:     m.redactConfig(cfg.Type, cfg.Config),
	}

	if gw, ok := m.running[instanceID]; ok && gw != nil {
		status := gw.Status()
		resp.Connected = status.Connected
		resp.MessagesIn = status.MessagesIn
		resp.MessagesOut = status.MessagesOut
		resp.Errors = status.Errors
		resp.DLQPending = status.DLQPending
		resp.LastActivity = status.LastActivity
		resp.ConnectionUptime = status.ConnectionUptime
	}

	return resp, nil
}

// createGateway is a legacy wrapper that creates a gateway for the default instance.
func (m *Manager) createGateway(gwType, configJSON string) (Gateway, error) {
	return m.createGatewayForInstance(gwType, gwType+"_0", configJSON)
}

// createGatewayForInstance creates a gateway bound to a specific transport instance.
func (m *Manager) createGatewayForInstance(gwType, instanceID, configJSON string) (Gateway, error) {
	switch gwType {
	case "mqtt":
		cfg, err := ParseMQTTConfig(configJSON)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return NewMQTTGateway(*cfg), nil
	case "iridium":
		sat := m.getSatTransport(instanceID)
		if sat == nil {
			return nil, fmt.Errorf("satellite transport not available for %s", instanceID)
		}
		cfg, err := ParseIridiumConfig(configJSON)
		if err != nil {
			return nil, err
		}
		gw := NewSBDGateway(*cfg, sat, m.db, m.predictor)
		if m.onEventEmit != nil {
			gw.SetEventEmitter(m.onEventEmit)
		}
		return gw, nil
	case "iridium_imt":
		sat := m.getSatTransport(instanceID)
		if sat == nil {
			return nil, fmt.Errorf("IMT satellite transport not available for %s", instanceID)
		}
		cfg, err := ParseIMTConfig(configJSON)
		if err != nil {
			return nil, err
		}
		gw := NewIMTGateway(*cfg, sat, m.db, m.predictor)
		if m.onEventEmit != nil {
			gw.SetEventEmitter(m.onEventEmit)
		}
		return gw, nil
	case "cellular":
		cell := m.getCellTransport(instanceID)
		if cell == nil {
			return nil, fmt.Errorf("cellular transport not available for %s", instanceID)
		}
		cfg, err := ParseCellularConfig(configJSON)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		gw := NewCellularGateway(*cfg, cell, m.db)
		if m.onEventEmit != nil {
			gw.SetEventEmitter(m.onEventEmit)
		}
		if m.nodeNameFn != nil {
			gw.SetNodeNameResolver(m.nodeNameFn)
		}
		return gw, nil
	case "webhook":
		cfg, err := ParseWebhookConfig(configJSON)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return NewWebhookGateway(*cfg, m.db), nil
	case "zigbee":
		cfg, err := ParseZigBeeConfig(configJSON)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return NewZigBeeGateway(*cfg), nil
	case "tak":
		cfg, err := ParseTAKConfig(configJSON)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return NewTAKGateway(*cfg, m.db), nil
	case "aprs":
		cfg, err := ParseAPRSConfig(configJSON)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return NewAPRSGateway(*cfg, m.db), nil
	default:
		return nil, fmt.Errorf("unknown gateway type: %s", gwType)
	}
}

func (m *Manager) redactConfig(gwType, configJSON string) json.RawMessage {
	switch gwType {
	case "mqtt":
		cfg, err := ParseMQTTConfig(configJSON)
		if err != nil {
			return json.RawMessage(configJSON)
		}
		redacted := cfg.Redacted()
		data, _ := json.Marshal(redacted)
		return data
	case "cellular":
		cfg, err := ParseCellularConfig(configJSON)
		if err != nil {
			return json.RawMessage(configJSON)
		}
		redacted := cfg.Redacted()
		data, _ := json.Marshal(redacted)
		return data
	case "webhook":
		cfg, err := ParseWebhookConfig(configJSON)
		if err != nil {
			return json.RawMessage(configJSON)
		}
		redacted := cfg.Redacted()
		data, _ := json.Marshal(redacted)
		return data
	case "zigbee":
		cfg, err := ParseZigBeeConfig(configJSON)
		if err != nil {
			return json.RawMessage(configJSON)
		}
		data, _ := json.Marshal(cfg.Redacted())
		return data
	case "tak":
		cfg, err := ParseTAKConfig(configJSON)
		if err != nil {
			return json.RawMessage(configJSON)
		}
		data, _ := json.Marshal(cfg.Redacted())
		return data
	case "aprs":
		cfg, err := ParseAPRSConfig(configJSON)
		if err != nil {
			return json.RawMessage(configJSON)
		}
		data, _ := json.Marshal(cfg.Redacted())
		return data
	default:
		return json.RawMessage(configJSON)
	}
}

// GetCellularSignal returns the current cellular signal.
func (m *Manager) GetCellularSignal(ctx context.Context) (*transport.CellSignalInfo, error) {
	if m.cell == nil {
		return nil, fmt.Errorf("cellular transport not available")
	}
	return m.cell.GetSignal(ctx)
}

// GetCellularStatus returns the cellular modem status.
func (m *Manager) GetCellularStatus(ctx context.Context) (*transport.CellStatus, error) {
	if m.cell == nil {
		return nil, fmt.Errorf("cellular transport not available")
	}
	return m.cell.GetStatus(ctx)
}

// GetCellularDataStatus returns the LTE data connection status.
func (m *Manager) GetCellularDataStatus(ctx context.Context) (*transport.CellDataStatus, error) {
	if m.cell == nil {
		return nil, fmt.Errorf("cellular transport not available")
	}
	return m.cell.GetDataStatus(ctx)
}

// ConnectCellularData brings up the LTE data connection.
func (m *Manager) ConnectCellularData(ctx context.Context, apn string) error {
	if m.cell == nil {
		return fmt.Errorf("cellular transport not available")
	}
	return m.cell.ConnectData(ctx, apn)
}

// DisconnectCellularData tears down the LTE data connection.
func (m *Manager) DisconnectCellularData(ctx context.Context) error {
	if m.cell == nil {
		return fmt.Errorf("cellular transport not available")
	}
	return m.cell.DisconnectData(ctx)
}

// GetCellularGateway returns the first running cellular gateway (for webhook forwarding).
func (m *Manager) GetCellularGateway() *CellularGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, gw := range m.running {
		if gw == nil {
			continue
		}
		if cgw, ok := gw.(*CellularGateway); ok {
			return cgw
		}
	}
	return nil
}

// GetWebhookGateway returns the running webhook gateway, if any.
func (m *Manager) GetWebhookGateway() *WebhookGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, gw := range m.running {
		if gw == nil {
			continue
		}
		if wgw, ok := gw.(*WebhookGateway); ok {
			return wgw
		}
	}
	return nil
}

// GetMQTTGateway returns the running MQTT gateway, if any.
func (m *Manager) GetMQTTGateway() *MQTTGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, gw := range m.running {
		if gw == nil {
			continue
		}
		if mgw, ok := gw.(*MQTTGateway); ok {
			return mgw
		}
	}
	return nil
}

// GetZigBeeGateway returns the running ZigBee gateway, if any.
func (m *Manager) GetZigBeeGateway() *ZigBeeGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, gw := range m.running {
		if gw == nil {
			continue
		}
		if zgw, ok := gw.(*ZigBeeGateway); ok {
			return zgw
		}
	}
	return nil
}

// GetAPRSGateway returns the running APRS gateway, if any.
func (m *Manager) GetAPRSGateway() *APRSGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, gw := range m.running {
		if gw == nil {
			continue
		}
		if agw, ok := gw.(*APRSGateway); ok {
			return agw
		}
	}
	return nil
}

// GetDynDNSUpdater returns the DynDNS updater from the running cellular gateway, if any.
func (m *Manager) GetDynDNSUpdater() *DynDNSUpdater {
	if cgw := m.GetCellularGateway(); cgw != nil {
		return cgw.dyndns
	}
	return nil
}

// activeSatTransport returns whichever satellite transport is currently connected.
// Checks IMT (9704) first since it's the newer modem, then SBD (9603).
// Returns nil if no satellite transport is connected.
func (m *Manager) activeSatTransport(ctx context.Context) transport.SatTransport {
	if m.imtSat != nil {
		if st, err := m.imtSat.GetStatus(ctx); err == nil && st.Connected {
			return m.imtSat
		}
	}
	if m.sat != nil {
		if st, err := m.sat.GetStatus(ctx); err == nil && st.Connected {
			return m.sat
		}
	}
	// Neither connected — return whichever is configured (prefer IMT)
	if m.imtSat != nil {
		return m.imtSat
	}
	return m.sat
}

// GetSatModemInfo returns the satellite modem connection status (model, IMEI, firmware).
// Automatically selects the active transport (9603 SBD or 9704 IMT).
func (m *Manager) GetSatModemInfo(ctx context.Context) (*transport.SatStatus, error) {
	sat := m.activeSatTransport(ctx)
	if sat == nil {
		return nil, fmt.Errorf("satellite transport not available")
	}
	return sat.GetStatus(ctx)
}

// GetIMTModemInfo returns the IMT (9704) modem connection status.
func (m *Manager) GetIMTModemInfo(ctx context.Context) (*transport.SatStatus, error) {
	if m.imtSat == nil {
		return nil, fmt.Errorf("IMT transport not available")
	}
	return m.imtSat.GetStatus(ctx)
}

// GetSBDModemInfo returns the SBD (9603) modem connection status.
func (m *Manager) GetSBDModemInfo(ctx context.Context) (*transport.SatStatus, error) {
	if m.sat == nil {
		return nil, fmt.Errorf("SBD transport not available")
	}
	return m.sat.GetStatus(ctx)
}

// CheckIMTProvisioning queries the 9704 modem for its provisioned topics.
// An empty list means the modem hasn't completed provisioning with the Iridium network.
func (m *Manager) CheckIMTProvisioning() ([]transport.ProvisioningTopic, error) {
	if m.imtSat == nil {
		return nil, fmt.Errorf("IMT transport not available")
	}
	imt, ok := m.imtSat.(*transport.DirectIMTTransport)
	if !ok {
		return nil, fmt.Errorf("IMT transport does not support provisioning check")
	}
	return imt.CheckProvisioning()
}

// GetIMTSignalFast returns a cached IMT signal reading.
func (m *Manager) GetIMTSignalFast(ctx context.Context) (*transport.SignalInfo, error) {
	if m.imtSat == nil {
		return nil, fmt.Errorf("IMT transport not available")
	}
	return m.imtSat.GetSignalFast(ctx)
}

// HasIMTTransport returns true if an IMT (9704) transport is available.
func (m *Manager) HasIMTTransport() bool {
	return m.imtSat != nil
}

// GetIridiumSignal returns the current satellite signal (blocking query).
// Automatically selects the active transport.
func (m *Manager) GetIridiumSignal(ctx context.Context) (*transport.SignalInfo, error) {
	sat := m.activeSatTransport(ctx)
	if sat == nil {
		return nil, fmt.Errorf("satellite transport not available")
	}
	return sat.GetSignal(ctx)
}

// GetSignalFast implements engine.SignalProvider for the signal recorder.
func (m *Manager) GetSignalFast(ctx context.Context) (*transport.SignalInfo, error) {
	return m.GetIridiumSignalFast(ctx)
}

// GetIridiumSignalFast returns a cached satellite signal reading (non-blocking).
// Automatically selects the active transport.
func (m *Manager) GetIridiumSignalFast(ctx context.Context) (*transport.SignalInfo, error) {
	sat := m.activeSatTransport(ctx)
	if sat == nil {
		return nil, fmt.Errorf("satellite transport not available")
	}
	return sat.GetSignalFast(ctx)
}

// GetIridiumGeolocation returns Iridium-derived geolocation (AT-MSGEO).
// Only available on SBD (9603). Returns error for IMT (9704).
func (m *Manager) GetIridiumGeolocation(ctx context.Context) (*transport.GeolocationInfo, error) {
	if m.sat == nil {
		return nil, fmt.Errorf("satellite transport not available")
	}
	sbd, ok := m.sat.(transport.SBDTransport)
	if !ok {
		return nil, fmt.Errorf("geolocation requires SBD (9603) modem")
	}
	return sbd.GetGeolocation(ctx)
}

// GetIridiumTime returns the Iridium network system time (AT-MSSTM).
// Only available on SBD (9603). IMT (9704) uses JSPR and has no AT-MSSTM.
func (m *Manager) GetIridiumTime(ctx context.Context) (*transport.IridiumTime, error) {
	if m.sat == nil {
		return nil, fmt.Errorf("system time requires SBD (9603) modem — not available with IMT (9704)")
	}
	sbd, ok := m.sat.(transport.SBDTransport)
	if !ok {
		return nil, fmt.Errorf("system time requires SBD (9603) modem — not available with IMT (9704)")
	}
	return sbd.GetSystemTime(ctx)
}

// ManualMailboxCheck triggers a one-shot mailbox check on the first running Iridium gateway.
func (m *Manager) ManualMailboxCheck(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, gw := range m.running {
		if gw == nil {
			continue
		}
		if sgw, ok := gw.(*SBDGateway); ok {
			sgw.ManualMailboxCheck(ctx)
			return nil
		}
		if igw, ok := gw.(*IMTGateway); ok {
			igw.ManualMailboxCheck(ctx)
			return nil
		}
	}
	return fmt.Errorf("iridium gateway not running")
}

// GatewayStatusResponse is the API response for gateway status.
type GatewayStatusResponse struct {
	Type             string          `json:"type"`
	InstanceID       string          `json:"instance_id,omitempty"`
	Enabled          bool            `json:"enabled"`
	Connected        bool            `json:"connected"`
	MessagesIn       int64           `json:"messages_in"`
	MessagesOut      int64           `json:"messages_out"`
	Errors           int64           `json:"errors"`
	DLQPending       int64           `json:"dlq_pending,omitempty"`
	LastActivity     interface{}     `json:"last_activity,omitempty"`
	ConnectionUptime string          `json:"connection_uptime,omitempty"`
	Config           json.RawMessage `json:"config,omitempty"`
}
