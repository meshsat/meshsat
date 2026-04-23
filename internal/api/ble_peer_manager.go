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
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/federation"
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
	// Auto-federation hooks — optional. When all knobs are set, the
	// manager exchanges a signed CapabilityManifest over the new BLE
	// link immediately after it comes up, verifies the peer's, and
	// persists to trusted_peers. [MESHSAT-635 Phase 3]
	db             *database.DB
	signerID       string
	signFn         func([]byte) []byte // opaque — SigningService.Sign
	localAlias     string
	localRouting   string
	bearerSnapshot func() []federation.Bearer
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

// EnableAutoFederation arms the manifest-exchange path. All knobs are
// required: a DB handle for the trusted_peers upsert, the bridge's
// signer_id + Sign callback (the SigningService surface), and a
// snapshot callback that returns the current bearer list on demand.
// Idempotent — calling twice with different inputs replaces the
// prior wiring. [MESHSAT-635 Phase 3]
func (m *BLEPeerManager) EnableAutoFederation(
	db *database.DB,
	signerIDHex string,
	signFn func([]byte) []byte,
	alias string,
	routingIdentity string,
	bearerSnapshot func() []federation.Bearer,
) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.db = db
	m.signerID = signerIDHex
	m.signFn = signFn
	m.localAlias = alias
	m.localRouting = routingIdentity
	m.bearerSnapshot = bearerSnapshot
}

// EnsurePeer starts a BLEClientInterface for `address` if none is live.
// Called from handleBluetoothConnect after we've verified the device
// advertises the MeshSat Reticulum GATT service UUID, and from the
// direct POST /api/system/bluetooth/ble-peer/{address} handler.
// Idempotent + thread-safe: concurrent callers for the same address
// don't race, duplicate a client, or leak one. [MESHSAT-675]
func (m *BLEPeerManager) EnsurePeer(ctx context.Context, address string) error {
	if m == nil || m.proc == nil || m.reg == nil {
		return nil // dormant
	}
	key := normalizeMAC(address)
	m.mu.Lock()
	if existing, ok := m.peers[key]; ok {
		// client == nil means another goroutine is currently inside
		// Start() for this address; treat as idempotent success —
		// caller can poll Names()/status to observe the eventual
		// outcome. A live online client is also a success no-op.
		if existing.client == nil || existing.client.IsOnline() {
			m.mu.Unlock()
			return nil
		}
		// Stale entry — drop and rebuild. removePeerLocked requires
		// m.mu held; must stay inside this critical section.
		m.removePeerLocked(key)
	}
	name := m.allocNameLocked()
	// Claim the slot with a client-less placeholder so a concurrent
	// EnsurePeer call for the same address hits the idempotent branch
	// above instead of starting a second client.
	m.peers[key] = &bleManagedPeer{name: name}
	// Snapshot auto-federation state while we still hold the lock so
	// the callback goroutine doesn't race with EnableAutoFederation.
	db := m.db
	signerID := m.signerID
	signFn := m.signFn
	bearerSnapshot := m.bearerSnapshot
	m.mu.Unlock()

	// Packet callback: manifest frames go to the federation-persist
	// path; everything else into the Reticulum processor. We use a
	// closure so each ble_peer_N sees its own iface name in the
	// RNS inject.
	callback := func(packet []byte) {
		federation.DispatchPacket(packet,
			func(mf *federation.CapabilityManifest) {
				onManifestReceived(name, mf, db)
			},
			func(b []byte) {
				log.Debug().Str("iface", name).Int("size", len(b)).Msg("ble-peer: inbound packet")
				m.proc.InjectReticulumPacket(b, name)
			},
		)
	}
	client, ri := routing.RegisterBLEClientInterface(routing.BLEClientConfig{
		Name:        name,
		AdapterID:   m.adapterID,
		PeerAddress: address,
	}, callback)
	if err := client.Start(ctx); err != nil {
		// Drop our placeholder so the next EnsurePeer call can retry
		// with a clean slot. Only remove if the slot still holds our
		// placeholder (name matches, client still nil) — if another
		// goroutine has already replaced it we leave it alone.
		m.mu.Lock()
		if p, ok := m.peers[key]; ok && p.client == nil && p.name == name {
			delete(m.peers, key)
		}
		m.mu.Unlock()
		return err
	}
	m.proc.RegisterPacketSender(name, client.Send)
	m.reg.Register(ri)

	m.mu.Lock()
	m.peers[key] = &bleManagedPeer{name: name, client: client}
	m.mu.Unlock()
	log.Info().Str("iface", name).Str("peer", address).Msg("ble-peer: link established")

	// Best-effort manifest send — the peer may not yet have wired
	// its own federation side, in which case our frame is dropped on
	// their end and nothing breaks. We retry opportunistically on
	// future link re-establishments.
	if db != nil && signFn != nil && signerID != "" && bearerSnapshot != nil {
		go m.sendOurManifest(client, signerID, signFn, bearerSnapshot)
	}
	return nil
}

// sendOurManifest marshals the local capability manifest and writes
// it over the freshly-established BLE client link. Runs in a goroutine
// because BLE WriteValue can block behind the peer's slow ACK; we
// don't want to hold the pair/connect REST request on it.
func (m *BLEPeerManager) sendOurManifest(
	client *routing.BLEClientInterface,
	signerID string,
	signFn func([]byte) []byte,
	bearerSnapshot func() []federation.Bearer,
) {
	// Tiny settle time — lets the peer's GATT subscription finish
	// before we start writing. Observed ~200 ms on Pi 5 brcmfmac.
	time.Sleep(300 * time.Millisecond)
	bearers := bearerSnapshot()
	wire, err := federation.BuildManifestBytes(signerID, m.localRouting, m.localAlias, bearers, signFn)
	if err != nil {
		log.Warn().Err(err).Msg("ble-peer: build manifest failed")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Send(ctx, wire); err != nil {
		log.Warn().Err(err).Msg("ble-peer: manifest send failed")
		return
	}
	log.Info().Int("size", len(wire)).Msg("ble-peer: sent capability manifest")
}

// onManifestReceived verifies + persists a manifest that arrived over
// a BLE peer link. Package-level so the peripheral side (ble_0 in
// main.go) can share it. [MESHSAT-635 Phase 3]
func onManifestReceived(ifaceName string, mf *federation.CapabilityManifest, db *database.DB) {
	if mf == nil || db == nil {
		return
	}
	vr := mf.Verify(time.Now(), federation.DefaultMaxAge)
	if !vr.OK {
		log.Warn().Str("iface", ifaceName).Str("reason", vr.Reason).Msg("ble-peer: manifest verify failed")
		return
	}
	summary, err := federation.SummariseForPersist(mf)
	if err != nil {
		log.Warn().Err(err).Str("iface", ifaceName).Msg("ble-peer: summarise manifest failed")
		return
	}
	if err := db.UpsertTrustedPeer(summary.SignerID, summary.RoutingIdentity, summary.Alias, summary.ManifestJSON, true); err != nil {
		log.Error().Err(err).Str("iface", ifaceName).Msg("ble-peer: persist trusted peer failed")
		return
	}
	log.Info().
		Str("iface", ifaceName).
		Str("signer", summary.SignerID).
		Str("alias", summary.Alias).
		Int("bearers", vr.NumBearers).
		Msg("ble-peer: trusted peer registered via manifest")
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
