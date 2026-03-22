package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	sat             transport.SatTransport       // optional, for iridium SBD gateway (9603)
	imtSat          transport.SatTransport       // optional, for iridium IMT gateway (9704)
	cell            transport.CellTransport      // optional, for cellular gateway
	astro           transport.AstrocastTransport // optional, for astrocast gateway
	predictor       PassPredictor                // optional, for pass scheduler
	onReceiverStart ReceiverStartFunc            // called when a gateway starts
	onEventEmit     EventEmitFunc                // SSE event emitter callback
	nodeNameFn      func(uint32) string          // resolves mesh node ID to name
	running         map[string]Gateway           // legacy: keyed by type ("iridium")
	runningByIface  map[string]Gateway           // v0.3.0: keyed by interface ID ("iridium_0")
	mu              sync.RWMutex
	cancelFn        context.CancelFunc
	supervisor      *transport.DeviceSupervisor // optional, for device event watching
}

// NewManager creates a new gateway manager.
func NewManager(db *database.DB, sat transport.SatTransport) *Manager {
	return &Manager{
		db:             db,
		sat:            sat,
		running:        make(map[string]Gateway),
		runningByIface: make(map[string]Gateway),
	}
}

// SetCellTransport sets the cellular transport for the cellular gateway.
func (m *Manager) SetCellTransport(cell transport.CellTransport) {
	m.cell = cell
}

// SetIMTTransport sets the Iridium IMT (9704) transport for coexistence with SBD.
func (m *Manager) SetIMTTransport(imtSat transport.SatTransport) {
	m.imtSat = imtSat
}

// SetAstrocastTransport sets the Astrocast transport for the astrocast gateway.
func (m *Manager) SetAstrocastTransport(astro transport.AstrocastTransport) {
	m.astro = astro
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

	// Check SBD gateway first, then IMT
	for _, gwType := range []string{"iridium", "iridium_imt"} {
		if gw, ok := m.running[gwType]; ok {
			if igw, ok := gw.(*IridiumGateway); ok {
				return igw.PassSchedulerRef()
			}
		}
	}
	return nil
}

// Start loads enabled configs from DB and starts their gateways.
func (m *Manager) Start(ctx context.Context) error {
	ctx, m.cancelFn = context.WithCancel(ctx)

	// Legacy path: load from gateway_config table
	configs, err := m.db.GetAllGatewayConfigs()
	if err != nil {
		log.Warn().Err(err).Msg("failed to load gateway configs, continuing without gateways")
		return nil
	}

	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		gw, err := m.createGateway(cfg.Type, cfg.Config)
		if err != nil {
			log.Error().Err(err).Str("type", cfg.Type).Msg("failed to create gateway")
			continue
		}
		if err := gw.Start(ctx); err != nil {
			log.Error().Err(err).Str("type", cfg.Type).Msg("failed to start gateway")
			continue
		}
		m.mu.Lock()
		m.running[cfg.Type] = gw
		m.mu.Unlock()
		if m.onReceiverStart != nil {
			m.onReceiverStart(ctx, gw)
		}
		log.Info().Str("type", cfg.Type).Msg("gateway started from saved config")
	}

	// v0.3.0 path: also bootstrap from interfaces table.
	// For each enabled interface with a known gateway channel_type, register
	// the running gateway under the interface ID so GatewayByInterfaceID works.
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
	for gwType, gw := range m.running {
		if err := gw.Stop(); err != nil {
			log.Error().Err(err).Str("type", gwType).Msg("failed to stop gateway")
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

	switch ev.Type {
	case "device_disconnected", "device_removed":
		m.mu.RLock()
		_, running := m.running[gwType]
		m.mu.RUnlock()
		if running {
			log.Info().Str("type", gwType).Str("port", ev.Device.DevPath).
				Str("event", ev.Type).Msg("gwmgr: device lost, stopping gateway")
			// Stop gateway but don't mark disabled in DB — we want to restart on reconnect
			m.mu.Lock()
			if gw, ok := m.running[gwType]; ok {
				gw.Stop()
				delete(m.running, gwType)
				m.unsyncIfaceMap(gw)
			}
			m.mu.Unlock()
		}

	case "device_connected":
		// Device reconnected or newly identified — restart gateway if enabled in DB
		m.mu.RLock()
		_, alreadyRunning := m.running[gwType]
		m.mu.RUnlock()
		if alreadyRunning {
			return // already running, no action needed
		}

		cfg, err := m.db.GetGatewayConfig(gwType)
		if err != nil {
			// No config exists — auto-create a default enabled config
			log.Info().Str("type", gwType).Str("port", ev.Device.DevPath).
				Msg("gwmgr: device connected, auto-creating gateway config")
			if err := m.db.SaveGatewayConfig(gwType, true, "{}"); err != nil {
				log.Warn().Err(err).Str("type", gwType).Msg("gwmgr: failed to create default config")
				return
			}
		} else if !cfg.Enabled {
			// Config exists but disabled — re-enable since hardware is back
			log.Info().Str("type", gwType).Str("port", ev.Device.DevPath).
				Msg("gwmgr: device connected, re-enabling gateway")
			m.db.SaveGatewayConfig(gwType, true, cfg.Config)
		}

		log.Info().Str("type", gwType).Str("port", ev.Device.DevPath).
			Msg("gwmgr: device connected, starting gateway")

		// Small delay to let the transport layer finish connecting
		time.AfterFunc(2*time.Second, func() {
			if err := m.StartGateway(ctx, gwType); err != nil {
				log.Error().Err(err).Str("type", gwType).Msg("gwmgr: failed to restart gateway after device reconnect")
			}
		})
	}
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
	case transport.RoleAstrocast:
		return "astrocast"
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
	case "astrocast":
		return transport.RoleAstrocast
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

	// Build set of hardware-backed gateway types actually present.
	presentTypes := make(map[string]bool)
	for _, entry := range registry.ListAll() {
		if gwType := roleToGatewayType(entry.Role); gwType != "" {
			presentTypes[gwType] = true
		}
	}

	// Phase 1: Disable DB configs and stop gateways for missing hardware.
	configs, _ := m.db.GetAllGatewayConfigs()
	for _, cfg := range configs {
		role := gatewayTypeToRole(cfg.Type)
		if role == transport.RoleNone {
			continue // software-only gateway (mqtt, webhook, tak, aprs)
		}
		if presentTypes[cfg.Type] {
			continue // hardware present
		}

		// Hardware gone — disable in DB so it doesn't start next boot
		if cfg.Enabled {
			log.Info().Str("type", cfg.Type).Msg("gwmgr: reconcile — disabling gateway (hardware not present)")
			m.db.SaveGatewayConfig(cfg.Type, false, cfg.Config)
		}

		// Stop if running
		m.mu.Lock()
		if gw, ok := m.running[cfg.Type]; ok {
			gw.Stop()
			delete(m.running, cfg.Type)
			m.unsyncIfaceMap(gw)
		}
		m.mu.Unlock()
	}

	// Phase 2: Auto-create and start gateways for detected hardware.
	for _, entry := range registry.ListAll() {
		gwType := roleToGatewayType(entry.Role)
		if gwType == "" {
			continue
		}

		m.mu.RLock()
		_, running := m.running[gwType]
		m.mu.RUnlock()
		if running {
			continue
		}

		// Check if config exists — if not, auto-create a default one
		cfg, err := m.db.GetGatewayConfig(gwType)
		if err != nil {
			log.Info().Str("type", gwType).Str("port", entry.DevPath).
				Msg("gwmgr: reconcile — auto-creating config for detected hardware")
			if err := m.db.SaveGatewayConfig(gwType, true, "{}"); err != nil {
				log.Warn().Err(err).Str("type", gwType).Msg("gwmgr: reconcile — failed to create default config")
				continue
			}
		} else if !cfg.Enabled {
			// Hardware is back — re-enable
			log.Info().Str("type", gwType).Str("port", entry.DevPath).
				Msg("gwmgr: reconcile — re-enabling gateway (hardware detected)")
			m.db.SaveGatewayConfig(gwType, true, cfg.Config)
		}

		log.Info().Str("type", gwType).Str("port", entry.DevPath).
			Msg("gwmgr: reconcile — starting gateway for detected hardware")
		if err := m.StartGateway(ctx, gwType); err != nil {
			log.Warn().Err(err).Str("type", gwType).Msg("gwmgr: reconcile — failed to start gateway")
		}
	}
}

// Configure creates or updates a gateway configuration and optionally starts it.
func (m *Manager) Configure(ctx context.Context, gwType string, enabled bool, configJSON string) error {
	// Validate by trying to create
	if _, err := m.createGateway(gwType, configJSON); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	if err := m.db.SaveGatewayConfig(gwType, enabled, configJSON); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// Stop existing if running
	m.mu.Lock()
	if existing, ok := m.running[gwType]; ok {
		existing.Stop()
		delete(m.running, gwType)
	}
	m.mu.Unlock()

	// Start if enabled
	if enabled {
		return m.StartGateway(ctx, gwType)
	}
	return nil
}

// Delete stops and removes a gateway configuration.
func (m *Manager) Delete(gwType string) error {
	m.mu.Lock()
	if gw, ok := m.running[gwType]; ok {
		gw.Stop()
		delete(m.running, gwType)
	}
	m.mu.Unlock()

	return m.db.DeleteGatewayConfig(gwType)
}

// StartGateway starts a specific gateway from its saved config.
func (m *Manager) StartGateway(ctx context.Context, gwType string) error {
	cfg, err := m.db.GetGatewayConfig(gwType)
	if err != nil {
		return fmt.Errorf("get config: %w", err)
	}

	m.mu.Lock()
	if _, ok := m.running[gwType]; ok {
		m.mu.Unlock()
		return fmt.Errorf("gateway %s is already running", gwType)
	}
	// Place sentinel to prevent concurrent starts
	m.running[gwType] = nil
	m.mu.Unlock()

	gw, err := m.createGateway(cfg.Type, cfg.Config)
	if err != nil {
		m.mu.Lock()
		delete(m.running, gwType)
		m.mu.Unlock()
		return err
	}

	if err := gw.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.running, gwType)
		m.mu.Unlock()
		return fmt.Errorf("start %s: %w", gwType, err)
	}

	m.mu.Lock()
	m.running[gwType] = gw
	// Sync v0.3.0 interface map: register under matching interface IDs
	m.syncIfaceMap(gwType, gw)
	m.mu.Unlock()

	if m.onReceiverStart != nil {
		m.onReceiverStart(ctx, gw)
	}

	// Mark enabled in DB
	m.db.SaveGatewayConfig(gwType, true, cfg.Config)

	return nil
}

// StopGateway stops a specific running gateway.
func (m *Manager) StopGateway(gwType string) error {
	m.mu.Lock()
	gw, ok := m.running[gwType]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("gateway %s is not running", gwType)
	}
	delete(m.running, gwType)
	// Clean interface map entries pointing to this gateway
	m.unsyncIfaceMap(gw)
	m.mu.Unlock()

	if err := gw.Stop(); err != nil {
		return err
	}

	// Mark disabled in DB
	cfg, err := m.db.GetGatewayConfig(gwType)
	if err == nil {
		m.db.SaveGatewayConfig(gwType, false, cfg.Config)
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
	case "iridium":
		if m.sat == nil {
			return fmt.Errorf("satellite transport not available")
		}
		status, err := m.sat.GetStatus(context.Background())
		if err != nil {
			return err
		}
		if !status.Connected {
			return fmt.Errorf("iridium modem not connected")
		}
		return nil
	case "iridium_imt":
		sat := m.imtSat
		if sat == nil {
			sat = m.sat
		}
		if sat == nil {
			return fmt.Errorf("IMT satellite transport not available")
		}
		status, err := sat.GetStatus(context.Background())
		if err != nil {
			return err
		}
		if !status.Connected {
			return fmt.Errorf("iridium IMT modem not connected")
		}
		return nil
	case "cellular":
		if m.cell == nil {
			return fmt.Errorf("cellular transport not available")
		}
		status, err := m.cell.GetStatus(context.Background())
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
	case "astrocast":
		if m.astro == nil {
			return fmt.Errorf("astrocast transport not available")
		}
		status, err := m.astro.GetStatus(context.Background())
		if err != nil {
			return err
		}
		if !status.Connected {
			return fmt.Errorf("astrocast module not connected")
		}
		return nil
	case "zigbee":
		m.mu.RLock()
		gw, ok := m.running["zigbee"]
		m.mu.RUnlock()
		if !ok {
			return fmt.Errorf("zigbee gateway not running")
		}
		if !gw.Status().Connected {
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

// StartInterfaceGateway starts a gateway for an interface, using its channel_type and config.
func (m *Manager) StartInterfaceGateway(ctx context.Context, ifaceID, channelType, configJSON string) error {
	m.mu.Lock()
	if _, ok := m.runningByIface[ifaceID]; ok {
		m.mu.Unlock()
		return fmt.Errorf("interface %s gateway already running", ifaceID)
	}
	m.mu.Unlock()

	gw, err := m.createGateway(channelType, configJSON)
	if err != nil {
		return fmt.Errorf("create gateway for interface %s: %w", ifaceID, err)
	}

	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("start gateway for interface %s: %w", ifaceID, err)
	}

	m.mu.Lock()
	m.runningByIface[ifaceID] = gw
	// Also register in legacy map if not already present (backwards compat)
	if _, ok := m.running[channelType]; !ok {
		m.running[channelType] = gw
	}
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
// This bridges the legacy gateway_config start path with the v0.3.0 interface model.
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
		// If a legacy gateway is running for this channel_type, also index it by interface ID
		if gw, ok := m.running[iface.ChannelType]; ok {
			if _, already := m.runningByIface[iface.ID]; !already {
				m.runningByIface[iface.ID] = gw
				mapped++
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

// GetStatus returns status info for all known gateway types (configured or running).
func (m *Manager) GetStatus() []GatewayStatusResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []GatewayStatusResponse

	configs, err := m.db.GetAllGatewayConfigs()
	if err != nil {
		return results
	}

	for _, cfg := range configs {
		resp := GatewayStatusResponse{
			Type:    cfg.Type,
			Enabled: cfg.Enabled,
		}

		if gw, ok := m.running[cfg.Type]; ok {
			status := gw.Status()
			resp.Connected = status.Connected
			resp.MessagesIn = status.MessagesIn
			resp.MessagesOut = status.MessagesOut
			resp.Errors = status.Errors
			resp.DLQPending = status.DLQPending
			resp.LastActivity = status.LastActivity
			resp.ConnectionUptime = status.ConnectionUptime
		}

		// Include redacted config
		resp.Config = m.redactConfig(cfg.Type, cfg.Config)

		results = append(results, resp)
	}

	return results
}

// GetSingleStatus returns status for a specific gateway type.
func (m *Manager) GetSingleStatus(gwType string) (*GatewayStatusResponse, error) {
	cfg, err := m.db.GetGatewayConfig(gwType)
	if err != nil {
		return nil, fmt.Errorf("gateway %s not configured", gwType)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	resp := &GatewayStatusResponse{
		Type:    cfg.Type,
		Enabled: cfg.Enabled,
		Config:  m.redactConfig(cfg.Type, cfg.Config),
	}

	if gw, ok := m.running[cfg.Type]; ok {
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

// createGateway is a factory method that creates a gateway from type and JSON config.
func (m *Manager) createGateway(gwType, configJSON string) (Gateway, error) {
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
		if m.sat == nil {
			return nil, fmt.Errorf("satellite transport not available")
		}
		cfg, err := ParseIridiumConfig(configJSON)
		if err != nil {
			return nil, err
		}
		gw := NewIridiumGateway(*cfg, m.sat, m.db, m.predictor)
		if m.onEventEmit != nil {
			gw.SetEventEmitter(m.onEventEmit)
		}
		return gw, nil
	case "iridium_imt":
		sat := m.imtSat
		if sat == nil {
			// Fall back to primary sat transport if no dedicated IMT transport
			sat = m.sat
		}
		if sat == nil {
			return nil, fmt.Errorf("IMT satellite transport not available")
		}
		cfg, err := ParseIridiumConfig(configJSON)
		if err != nil {
			return nil, err
		}
		gw := NewIridiumIMTGateway(*cfg, sat, m.db, m.predictor)
		if m.onEventEmit != nil {
			gw.SetEventEmitter(m.onEventEmit)
		}
		return gw, nil
	case "cellular":
		if m.cell == nil {
			return nil, fmt.Errorf("cellular transport not available")
		}
		cfg, err := ParseCellularConfig(configJSON)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		gw := NewCellularGateway(*cfg, m.cell, m.db)
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
	case "astrocast":
		if m.astro == nil {
			return nil, fmt.Errorf("astrocast transport not available")
		}
		cfg, err := ParseAstrocastConfig(configJSON)
		if err != nil {
			return nil, err
		}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return NewAstrocastGateway(*cfg, m.astro, m.db), nil
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

// GetCellularGateway returns the running cellular gateway (for webhook forwarding).
func (m *Manager) GetCellularGateway() *CellularGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if gw, ok := m.running["cellular"]; ok {
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
	if gw, ok := m.running["webhook"]; ok {
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
	if gw, ok := m.running["mqtt"]; ok {
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
	if gw, ok := m.running["zigbee"]; ok {
		if zgw, ok := gw.(*ZigBeeGateway); ok {
			return zgw
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
	return m.sat.GetGeolocation(ctx)
}

// GetIridiumTime returns the Iridium network system time (AT-MSSTM).
func (m *Manager) GetIridiumTime(ctx context.Context) (*transport.IridiumTime, error) {
	if m.sat == nil {
		return nil, fmt.Errorf("satellite transport not available")
	}
	return m.sat.GetSystemTime(ctx)
}

// ManualMailboxCheck triggers a one-shot mailbox check on the running Iridium gateway.
func (m *Manager) ManualMailboxCheck(ctx context.Context) error {
	m.mu.RLock()
	// Try SBD first, then IMT
	gw, ok := m.running["iridium"]
	if !ok {
		gw, ok = m.running["iridium_imt"]
	}
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("iridium gateway not running")
	}
	iGw, ok := gw.(*IridiumGateway)
	if !ok {
		return fmt.Errorf("iridium gateway has unexpected type")
	}
	iGw.ManualMailboxCheck(ctx)
	return nil
}

// GatewayStatusResponse is the API response for gateway status.
type GatewayStatusResponse struct {
	Type             string          `json:"type"`
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
