package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// Manager coordinates gateway lifecycle (start/stop/config).
type Manager struct {
	db       *database.DB
	sat      transport.SatTransport // optional, for iridium gateway
	running  map[string]Gateway
	mu       sync.RWMutex
	cancelFn context.CancelFunc
}

// NewManager creates a new gateway manager.
func NewManager(db *database.DB, sat transport.SatTransport) *Manager {
	return &Manager{
		db:      db,
		sat:     sat,
		running: make(map[string]Gateway),
	}
}

// Start loads enabled configs from DB and starts their gateways.
func (m *Manager) Start(ctx context.Context) error {
	ctx, m.cancelFn = context.WithCancel(ctx)

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
		log.Info().Str("type", cfg.Type).Msg("gateway started from saved config")
	}
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
	m.mu.Unlock()

	gw, err := m.createGateway(cfg.Type, cfg.Config)
	if err != nil {
		return err
	}

	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("start %s: %w", gwType, err)
	}

	m.mu.Lock()
	m.running[gwType] = gw
	m.mu.Unlock()

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
		return NewIridiumGateway(*cfg, m.sat, m.db), nil
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
	default:
		return json.RawMessage(configJSON)
	}
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
