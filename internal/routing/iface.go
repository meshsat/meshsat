package routing

import (
	"context"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// ReticulumInterface wraps a transport for bidirectional Reticulum packet exchange.
type ReticulumInterface struct {
	id        string // e.g. "mesh_0", "iridium_0"
	ifaceType reticulum.InterfaceType
	cost      float64
	mtu       int
	sendFn    func(ctx context.Context, packet []byte) error
	online    bool
	floodable bool // false for paid transports (satellite, SMS) — excluded from path request flooding and announce broadcasts
	mu        sync.RWMutex
}

// NewReticulumInterface creates an interface wrapper. Interfaces with cost > 0
// (satellite, cellular) are marked non-floodable by default to prevent burning
// credits on path discovery and announce broadcasts.
func NewReticulumInterface(id string, ifaceType reticulum.InterfaceType, mtu int, sendFn func(ctx context.Context, packet []byte) error) *ReticulumInterface {
	cost := reticulum.InterfaceCost(ifaceType)
	return &ReticulumInterface{
		id:        id,
		ifaceType: ifaceType,
		cost:      cost,
		mtu:       mtu,
		sendFn:    sendFn,
		online:    true,
		floodable: cost == 0,
	}
}

func (ri *ReticulumInterface) ID() string                    { return ri.id }
func (ri *ReticulumInterface) Type() reticulum.InterfaceType { return ri.ifaceType }
func (ri *ReticulumInterface) Cost() float64                 { return ri.cost }
func (ri *ReticulumInterface) MTU() int                      { return ri.mtu }
func (ri *ReticulumInterface) IsFloodable() bool             { return ri.floodable }

// SetFloodable overrides the default floodable flag for this interface.
func (ri *ReticulumInterface) SetFloodable(f bool) { ri.floodable = f }

func (ri *ReticulumInterface) SetOnline(online bool) {
	ri.mu.Lock()
	defer ri.mu.Unlock()
	ri.online = online
}

func (ri *ReticulumInterface) IsOnline() bool {
	ri.mu.RLock()
	defer ri.mu.RUnlock()
	return ri.online
}

func (ri *ReticulumInterface) Send(ctx context.Context, packet []byte) error {
	if !ri.IsOnline() {
		return fmt.Errorf("interface %s is offline", ri.id)
	}
	if len(packet) > ri.mtu {
		return fmt.Errorf("packet %d bytes exceeds MTU %d for %s", len(packet), ri.mtu, ri.id)
	}
	return ri.sendFn(ctx, packet)
}

// InterfaceRegistry manages named Reticulum interfaces for the Transport Node.
type InterfaceRegistry struct {
	mu     sync.RWMutex
	ifaces map[string]*ReticulumInterface
}

// NewInterfaceRegistry creates an empty interface registry.
func NewInterfaceRegistry() *InterfaceRegistry {
	return &InterfaceRegistry{
		ifaces: make(map[string]*ReticulumInterface),
	}
}

// Register adds an interface to the registry.
func (r *InterfaceRegistry) Register(iface *ReticulumInterface) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ifaces[iface.id] = iface
	log.Info().
		Str("id", iface.id).
		Str("type", string(iface.ifaceType)).
		Float64("cost", iface.cost).
		Int("mtu", iface.mtu).
		Msg("reticulum interface registered")
}

// Get returns an interface by ID, or nil if not found.
func (r *InterfaceRegistry) Get(id string) *ReticulumInterface {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ifaces[id]
}

// Unregister removes an interface from the registry. Used for dynamic
// transports whose lifecycle is driven by external events — e.g.
// BLE-client peers that come and go as operators pair/unpair remote
// MeshSat kits. [MESHSAT-633]
func (r *InterfaceRegistry) Unregister(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.ifaces[id]; !ok {
		return false
	}
	delete(r.ifaces, id)
	log.Info().Str("id", id).Msg("reticulum interface unregistered")
	return true
}

// Send transmits a packet on the named interface. This is the function
// that TransportNode.sendFn should call.
func (r *InterfaceRegistry) Send(ifaceID string, packet []byte) error {
	iface := r.Get(ifaceID)
	if iface == nil {
		return fmt.Errorf("unknown interface: %s", ifaceID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*1e9) // 30s
	defer cancel()
	return iface.Send(ctx, packet)
}

// OnlineIDs returns the IDs of all online interfaces.
func (r *InterfaceRegistry) OnlineIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ids []string
	for id, iface := range r.ifaces {
		if iface.IsOnline() {
			ids = append(ids, id)
		}
	}
	return ids
}

// All returns all registered interfaces.
func (r *InterfaceRegistry) All() []*ReticulumInterface {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*ReticulumInterface, 0, len(r.ifaces))
	for _, iface := range r.ifaces {
		result = append(result, iface)
	}
	return result
}

// Floodable returns all online interfaces that are safe to flood
// (path requests, announce broadcasts). Paid transports (satellite, SMS)
// are excluded to avoid burning credits on discovery traffic.
func (r *InterfaceRegistry) Floodable() []*ReticulumInterface {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*ReticulumInterface, 0, len(r.ifaces))
	for _, iface := range r.ifaces {
		if iface.IsOnline() && iface.floodable {
			result = append(result, iface)
		}
	}
	return result
}
