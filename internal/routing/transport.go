package routing

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// TransportNode enables the Bridge to act as a Reticulum Transport Node.
// It maintains a forwarding table (via reticulum.Router) and relays packets
// between interfaces based on cost-aware routing decisions.
type TransportNode struct {
	mu       sync.RWMutex
	identity *Identity
	router   *reticulum.Router
	enabled  bool

	// sendFn is called to transmit a packet on a specific interface.
	// The interface ID (e.g. "mesh_0", "iridium_0") selects the transport.
	sendFn TransportSendFunc
}

// TransportSendFunc transmits a raw Reticulum packet on the named interface.
type TransportSendFunc func(ifaceID string, packet []byte) error

// NewTransportNode creates a Transport Node with the given identity and
// route TTL. The node is disabled by default; call Enable() to start relaying.
func NewTransportNode(identity *Identity, routeTTL time.Duration, sendFn TransportSendFunc) *TransportNode {
	return &TransportNode{
		identity: identity,
		router:   reticulum.NewRouter(routeTTL),
		sendFn:   sendFn,
	}
}

// Enable activates transport node functionality.
func (tn *TransportNode) Enable() {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	tn.enabled = true
	log.Info().Str("dest_hash", tn.identity.DestHashHex()).Msg("transport node enabled")
}

// Disable deactivates transport node functionality.
func (tn *TransportNode) Disable() {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	tn.enabled = false
}

// IsEnabled returns whether the transport node is active.
func (tn *TransportNode) IsEnabled() bool {
	tn.mu.RLock()
	defer tn.mu.RUnlock()
	return tn.enabled
}

// Router returns the underlying Reticulum router for direct access.
func (tn *TransportNode) Router() *reticulum.Router {
	return tn.router
}

// ProcessAnnounce updates the routing table from a verified announce received
// on the given interface. Returns true if the route was new or improved.
func (tn *TransportNode) ProcessAnnounce(announce *Announce, sourceIface string) bool {
	if announce.ret == nil {
		return false
	}
	ifaceType := reticulum.InterfaceType(sourceIface)
	return tn.router.ProcessAnnounce(announce.ret, ifaceType)
}

// ForwardPacket attempts to relay a Reticulum packet to its destination via
// the best known route. It rewrites the header to HEADER_2 format with our
// dest hash as the transport ID, increments the hop count, and sends on the
// appropriate interface.
//
// Returns true if the packet was forwarded, false if no route was found or
// the transport node is disabled.
func (tn *TransportNode) ForwardPacket(packet []byte, sourceIface string) bool {
	if !tn.IsEnabled() {
		return false
	}

	hdr, err := reticulum.UnmarshalHeader(packet)
	if err != nil {
		return false
	}

	// Don't forward announces — those are handled by AnnounceRelay
	if hdr.PacketType == reticulum.PacketAnnounce {
		return false
	}

	// Don't forward packets addressed to us
	ourHash := tn.identity.DestHash()
	if hdr.DestHash == ourHash {
		return false
	}

	// Look up best route for the destination
	route := tn.router.Lookup(hdr.DestHash)
	if route == nil {
		log.Debug().
			Str("dest", reticulum.DestHashHex(hdr.DestHash)).
			Msg("transport: no route for destination")
		return false
	}

	// Don't forward back to the same interface
	destIface := string(route.Interface)
	if destIface == sourceIface {
		return false
	}

	// Increment hop count
	if !hdr.IncrementHop() {
		log.Debug().
			Str("dest", reticulum.DestHashHex(hdr.DestHash)).
			Msg("transport: max hops exceeded")
		return false
	}

	// Rewrite to HEADER_2 with our identity as transport_id
	transportHdr := reticulum.Header{
		HeaderType:    reticulum.HeaderType2,
		TransportType: reticulum.TransportTransport,
		DestType:      hdr.DestType,
		PacketType:    hdr.PacketType,
		Hops:          hdr.Hops,
		TransportID:   ourHash,
		DestHash:      hdr.DestHash,
		Context:       hdr.Context,
		Data:          hdr.Data,
	}

	relayPacket := transportHdr.Marshal()

	// Send on the best interface
	if tn.sendFn != nil {
		if err := tn.sendFn(destIface, relayPacket); err != nil {
			log.Warn().Err(err).
				Str("dest", reticulum.DestHashHex(hdr.DestHash)).
				Str("via", destIface).
				Msg("transport: forward failed")
			return false
		}
	}

	log.Debug().
		Str("dest", reticulum.DestHashHex(hdr.DestHash)).
		Str("from", sourceIface).
		Str("to", destIface).
		Int("hops", int(hdr.Hops)).
		Msg("transport: packet forwarded")
	return true
}

// StartExpiry launches a background goroutine that periodically removes
// expired routes from the forwarding table.
func (tn *TransportNode) StartExpiry(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tn.router.ExpireStale()
			}
		}
	}()
}

// RouteCount returns the number of active routes in the forwarding table.
func (tn *TransportNode) RouteCount() int {
	return tn.router.RouteCount()
}

// BestInterface returns the interface to reach a destination, or "" if unknown.
func (tn *TransportNode) BestInterface(destHash [DestHashLen]byte) string {
	return string(tn.router.BestInterface(destHash))
}
