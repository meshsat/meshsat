package api

// BLE peer manager — owns BLEClientInterface instances that live behind
// each paired-and-connected MeshSat kit. Wired into the bluetooth
// handlers so that when an operator pairs another kit via Settings >
// Routing > Bluetooth Peers, a Reticulum link is auto-started over
// BLE. Teardown on disconnect / remove.  [MESHSAT-633]

import (
	"context"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/engine"
	"meshsat/internal/routing"
)

// BLEPeerManager tracks one BLEClientInterface per paired MeshSat kit.
// Concurrency: all public methods are safe from any goroutine.
type BLEPeerManager struct {
	mu        sync.Mutex
	adapterID string
	proc      *engine.Processor
	reg       *routing.InterfaceRegistry
	// Map MAC → live client. Only populated for kits we've actively
	// brought up a link to.
	peers map[string]*bleManagedPeer
	// seq lets us allocate unique ble_peer_N names.
	seq int
}

type bleManagedPeer struct {
	name   string // e.g. "ble_peer_0"
	client *routing.BLEClientInterface
}

// NewBLEPeerManager wires the manager to the live Processor + registry.
// Both may legally be nil during early boot / tests — in that case the
// manager becomes a no-op so the BT REST handlers still work.
func NewBLEPeerManager(adapter string, proc *engine.Processor, reg *routing.InterfaceRegistry) *BLEPeerManager {
	if adapter == "" {
		adapter = "hci0"
	}
	return &BLEPeerManager{
		adapterID: adapter,
		proc:      proc,
		reg:       reg,
		peers:     make(map[string]*bleManagedPeer),
	}
}

// EnsurePeer starts a BLEClientInterface for `address` if none is live.
// Called from handleBluetoothConnect after we've verified the device
// advertises the MeshSat Reticulum GATT service UUID.
func (m *BLEPeerManager) EnsurePeer(ctx context.Context, address string) error {
	if m == nil || m.proc == nil || m.reg == nil {
		return nil // dormant
	}
	key := normalizeMAC(address)
	m.mu.Lock()
	if existing, ok := m.peers[key]; ok {
		m.mu.Unlock()
		if existing.client.IsOnline() {
			return nil
		}
		// Stale entry — drop and rebuild.
		m.removePeerLocked(key)
		m.mu.Lock()
	}
	name := m.allocNameLocked()
	m.mu.Unlock()

	callback := func(packet []byte) {
		log.Debug().Str("iface", name).Int("size", len(packet)).Msg("ble-peer: inbound packet")
		m.proc.InjectReticulumPacket(packet, name)
	}
	client, ri := routing.RegisterBLEClientInterface(routing.BLEClientConfig{
		Name:        name,
		AdapterID:   m.adapterID,
		PeerAddress: address,
	}, callback)
	if err := client.Start(ctx); err != nil {
		return err
	}
	m.proc.RegisterPacketSender(name, client.Send)
	m.reg.Register(ri)

	m.mu.Lock()
	m.peers[key] = &bleManagedPeer{name: name, client: client}
	m.mu.Unlock()
	log.Info().Str("iface", name).Str("peer", address).Msg("ble-peer: link established")
	return nil
}

// RemovePeer tears the BLE client link down and deregisters the
// interface. Safe to call for an unknown address (no-op).
func (m *BLEPeerManager) RemovePeer(address string) {
	if m == nil {
		return
	}
	key := normalizeMAC(address)
	m.mu.Lock()
	m.removePeerLocked(key)
	m.mu.Unlock()
}

// removePeerLocked — caller holds m.mu.
func (m *BLEPeerManager) removePeerLocked(key string) {
	peer, ok := m.peers[key]
	if !ok {
		return
	}
	delete(m.peers, key)
	if peer.client != nil {
		peer.client.Stop()
	}
	if m.reg != nil {
		m.reg.Unregister(peer.name)
	}
	log.Info().Str("iface", peer.name).Str("peer", key).Msg("ble-peer: link torn down")
}

// Names returns live ble_peer_N interface IDs, for introspection.
func (m *BLEPeerManager) Names() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.peers))
	for _, p := range m.peers {
		out = append(out, p.name)
	}
	return out
}

func (m *BLEPeerManager) allocNameLocked() string {
	for {
		name := blePeerName(m.seq)
		m.seq++
		// Collisions between generations — skip any name already used
		// by an active peer.
		inUse := false
		for _, p := range m.peers {
			if p.name == name {
				inUse = true
				break
			}
		}
		if !inUse {
			return name
		}
	}
}

func blePeerName(seq int) string {
	// e.g. "ble_peer_0"
	return "ble_peer_" + itoa(seq)
}

// itoa is a tiny no-alloc int→string for positive seq numbers. Avoids
// pulling in strconv here.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// normalizeMAC uppercases the MAC for map keying (BlueZ is consistent,
// but inbound MACs from the REST API may vary).
func normalizeMAC(mac string) string {
	return strings.ToUpper(strings.TrimSpace(mac))
}
