package vpn

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// Manager handles VPN peer lifecycle tied to device registration.
type Manager struct {
	db     *database.DB
	client *Client
	mu     sync.RWMutex
	// cache of device_id → vpn peer ID for fast lookups
	peerMap map[int64]string
}

// NewManager creates a VPN manager.
func NewManager(db *database.DB, client *Client) *Manager {
	return &Manager{
		db:      db,
		client:  client,
		peerMap: make(map[int64]string),
	}
}

// Start loads the peer cache from DB and begins the tunnel status monitor.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.loadCache(); err != nil {
		return fmt.Errorf("vpn cache load: %w", err)
	}
	go m.statusLoop(ctx)
	log.Info().Int("peers", len(m.peerMap)).Msg("VPN manager started")
	return nil
}

// loadCache populates the in-memory device→peer mapping from DB.
func (m *Manager) loadCache() error {
	rows, err := m.db.Query("SELECT device_id, wg_peer_id FROM vpn_peers WHERE wg_peer_id != ''")
	if err != nil {
		return err
	}
	defer rows.Close()

	m.mu.Lock()
	defer m.mu.Unlock()
	for rows.Next() {
		var deviceID int64
		var peerID string
		if err := rows.Scan(&deviceID, &peerID); err != nil {
			return err
		}
		m.peerMap[deviceID] = peerID
	}
	return rows.Err()
}

// ProvisionPeer creates a WireGuard peer for a registered device.
// Called automatically when a device is registered.
func (m *Manager) ProvisionPeer(deviceID int64, deviceLabel string) error {
	m.mu.RLock()
	if _, exists := m.peerMap[deviceID]; exists {
		m.mu.RUnlock()
		return nil // already provisioned
	}
	m.mu.RUnlock()

	peerName := fmt.Sprintf("meshsat-%s-%d", deviceLabel, deviceID)
	peer, err := m.client.CreatePeer(peerName)
	if err != nil {
		return fmt.Errorf("provision vpn peer: %w", err)
	}

	allocatedIP := ""
	if len(peer.AllocatedIPs) > 0 {
		allocatedIP = peer.AllocatedIPs[0]
	}

	_, err = m.db.Exec(
		`INSERT INTO vpn_peers (device_id, wg_peer_id, public_key, allocated_ip, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, datetime('now'), datetime('now'))
		 ON CONFLICT(device_id) DO UPDATE SET
		   wg_peer_id = excluded.wg_peer_id,
		   public_key = excluded.public_key,
		   allocated_ip = excluded.allocated_ip,
		   enabled = excluded.enabled,
		   updated_at = datetime('now')`,
		deviceID, peer.ID, peer.PublicKey, allocatedIP,
	)
	if err != nil {
		return fmt.Errorf("save vpn peer: %w", err)
	}

	m.mu.Lock()
	m.peerMap[deviceID] = peer.ID
	m.mu.Unlock()

	log.Info().Int64("device_id", deviceID).Str("peer_id", peer.ID).Str("ip", allocatedIP).Msg("VPN peer provisioned")
	return nil
}

// RemovePeer removes a WireGuard peer for a device.
func (m *Manager) RemovePeer(deviceID int64) error {
	m.mu.RLock()
	peerID, exists := m.peerMap[deviceID]
	m.mu.RUnlock()
	if !exists {
		return nil
	}

	if err := m.client.DeletePeer(peerID); err != nil {
		return fmt.Errorf("remove vpn peer: %w", err)
	}

	if _, err := m.db.Exec("DELETE FROM vpn_peers WHERE device_id = ?", deviceID); err != nil {
		return fmt.Errorf("delete vpn peer row: %w", err)
	}

	m.mu.Lock()
	delete(m.peerMap, deviceID)
	m.mu.Unlock()

	log.Info().Int64("device_id", deviceID).Msg("VPN peer removed")
	return nil
}

// PeerInfo holds the combined DB + runtime status for a device's VPN peer.
type PeerInfo struct {
	DeviceID      int64  `json:"device_id"`
	WGPeerID      string `json:"wg_peer_id"`
	PublicKey     string `json:"public_key"`
	AllocatedIP   string `json:"allocated_ip"`
	Enabled       bool   `json:"enabled"`
	Connected     bool   `json:"connected"`
	LastHandshake string `json:"last_handshake,omitempty"`
	TransferRx    int64  `json:"transfer_rx"`
	TransferTx    int64  `json:"transfer_tx"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// GetPeerInfo returns VPN peer info for a device, including tunnel status.
func (m *Manager) GetPeerInfo(deviceID int64) (*PeerInfo, error) {
	var info PeerInfo
	err := m.db.QueryRow(
		`SELECT device_id, wg_peer_id, public_key, allocated_ip, enabled,
		        last_handshake, transfer_rx, transfer_tx, created_at, updated_at
		 FROM vpn_peers WHERE device_id = ?`, deviceID,
	).Scan(&info.DeviceID, &info.WGPeerID, &info.PublicKey, &info.AllocatedIP,
		&info.Enabled, &info.LastHandshake, &info.TransferRx, &info.TransferTx,
		&info.CreatedAt, &info.UpdatedAt)
	if err != nil {
		return nil, err
	}

	// Determine connection status from last handshake
	if info.LastHandshake != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", info.LastHandshake); err == nil {
			info.Connected = time.Since(t) < 3*time.Minute
		}
	}
	return &info, nil
}

// ListPeers returns VPN peer info for all devices.
func (m *Manager) ListPeers() ([]PeerInfo, error) {
	rows, err := m.db.Query(
		`SELECT device_id, wg_peer_id, public_key, allocated_ip, enabled,
		        last_handshake, transfer_rx, transfer_tx, created_at, updated_at
		 FROM vpn_peers ORDER BY device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []PeerInfo
	for rows.Next() {
		var p PeerInfo
		if err := rows.Scan(&p.DeviceID, &p.WGPeerID, &p.PublicKey, &p.AllocatedIP,
			&p.Enabled, &p.LastHandshake, &p.TransferRx, &p.TransferTx,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if p.LastHandshake != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", p.LastHandshake); err == nil {
				p.Connected = time.Since(t) < 3*time.Minute
			}
		}
		peers = append(peers, p)
	}
	return peers, rows.Err()
}

// GetPeerConfig returns the WireGuard client config file for a device's peer.
func (m *Manager) GetPeerConfig(deviceID int64) (string, error) {
	m.mu.RLock()
	peerID, exists := m.peerMap[deviceID]
	m.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("no VPN peer for device %d", deviceID)
	}
	return m.client.GetPeerConfig(peerID)
}

// EnablePeer enables a VPN peer.
func (m *Manager) EnablePeer(deviceID int64) error {
	m.mu.RLock()
	peerID, exists := m.peerMap[deviceID]
	m.mu.RUnlock()
	if !exists {
		return fmt.Errorf("no VPN peer for device %d", deviceID)
	}
	if err := m.client.EnablePeer(peerID); err != nil {
		return err
	}
	_, err := m.db.Exec("UPDATE vpn_peers SET enabled = 1, updated_at = datetime('now') WHERE device_id = ?", deviceID)
	return err
}

// DisablePeer disables a VPN peer.
func (m *Manager) DisablePeer(deviceID int64) error {
	m.mu.RLock()
	peerID, exists := m.peerMap[deviceID]
	m.mu.RUnlock()
	if !exists {
		return fmt.Errorf("no VPN peer for device %d", deviceID)
	}
	if err := m.client.DisablePeer(peerID); err != nil {
		return err
	}
	_, err := m.db.Exec("UPDATE vpn_peers SET enabled = 0, updated_at = datetime('now') WHERE device_id = ?", deviceID)
	return err
}

// statusLoop periodically polls wireguard-ui for tunnel status and updates the DB.
func (m *Manager) statusLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshStatus()
		}
	}
}

// refreshStatus fetches current peer status from wireguard-ui and updates the DB.
func (m *Manager) refreshStatus() {
	peers, err := m.client.ListPeers()
	if err != nil {
		log.Warn().Err(err).Msg("VPN status refresh failed")
		return
	}

	// Build lookup by peer ID
	peerByID := make(map[string]*Peer, len(peers))
	for i := range peers {
		peerByID[peers[i].ID] = &peers[i]
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for deviceID, peerID := range m.peerMap {
		p, ok := peerByID[peerID]
		if !ok {
			continue
		}
		_, err := m.db.Exec(
			`UPDATE vpn_peers SET
				last_handshake = CASE WHEN ? != '' THEN ? ELSE last_handshake END,
				transfer_rx = ?, transfer_tx = ?,
				updated_at = datetime('now')
			 WHERE device_id = ?`,
			p.LastHandshakeTime, p.LastHandshakeTime,
			p.TransferRx, p.TransferTx,
			deviceID,
		)
		if err != nil {
			log.Warn().Err(err).Int64("device_id", deviceID).Msg("VPN status update failed")
		}
	}
}

// Healthy returns true if the VPN backend API is reachable.
func (m *Manager) Healthy() bool {
	return m.client.Healthy()
}
