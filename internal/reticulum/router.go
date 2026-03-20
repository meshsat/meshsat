package reticulum

import (
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

// DefaultRouteTTL is the default time before a route expires without refresh.
const DefaultRouteTTL = 30 * time.Minute

// InterfaceType identifies the transport interface a route was learned from.
type InterfaceType string

const (
	IfaceMesh       InterfaceType = "mesh"
	IfaceMQTT       InterfaceType = "mqtt"
	IfaceTor        InterfaceType = "tor"
	IfaceWireGuard  InterfaceType = "wireguard"
	IfaceIridium    InterfaceType = "iridium"
	IfaceCellular   InterfaceType = "cellular"
	IfaceAstrocast  InterfaceType = "astrocast"
	IfaceGlobalstar InterfaceType = "globalstar"
	IfaceZigBee     InterfaceType = "zigbee"
	IfaceAPRS       InterfaceType = "aprs"
	IfaceTCP        InterfaceType = "tcp"
	IfaceWebhook    InterfaceType = "webhook"
	IfaceLocal      InterfaceType = "local"
)

// InterfaceCost returns the per-message cost for a given interface.
// Free interfaces return 0; satellite interfaces return their typical cost.
func InterfaceCost(iface InterfaceType) float64 {
	switch iface {
	case IfaceMesh, IfaceZigBee, IfaceAPRS, IfaceTCP, IfaceMQTT, IfaceTor, IfaceWireGuard, IfaceWebhook, IfaceLocal:
		return 0
	case IfaceCellular:
		return 0.005 // SMS cost
	case IfaceAstrocast:
		return 0.01
	case IfaceGlobalstar:
		return 0.02
	case IfaceIridium:
		return 0.05
	default:
		return 1.0
	}
}

// RouteEntry is a single route to a known Reticulum destination.
type RouteEntry struct {
	DestHash   [TruncatedHashLen]byte // Destination hash
	Interface  InterfaceType          // Transport interface the route was learned from
	Cost       float64                // Per-message cost (0 = free)
	Hops       int                    // Hop count from the announce
	LastSeen   time.Time              // When this route was last refreshed
	ExpiresAt  time.Time              // When this route expires
	SigningPub []byte                 // Ed25519 public key of the destination (32 bytes)
	AppData    []byte                 // Optional app data from the announce
}

// IsExpired returns true if the route has passed its expiry time.
func (re *RouteEntry) IsExpired() bool {
	return time.Now().After(re.ExpiresAt)
}

// RouteInfo is the JSON-friendly version of RouteEntry for API responses.
type RouteInfo struct {
	DestHash      string  `json:"dest_hash"`
	Interface     string  `json:"interface"`
	Cost          float64 `json:"cost"`
	Hops          int     `json:"hops"`
	LastSeen      string  `json:"last_seen"`
	ExpiresAt     string  `json:"expires_at"`
	SigningPubHex string  `json:"signing_pub_hex,omitempty"`
	AppData       string  `json:"app_data,omitempty"`
}

// Router maintains a table of known Reticulum destinations and their best
// paths. It processes announces to update routes and selects the cheapest
// available path for outbound packets.
type Router struct {
	mu     sync.RWMutex
	routes map[[TruncatedHashLen]byte]*RouteEntry // best route per destination
	ttl    time.Duration
}

// NewRouter creates a new Reticulum routing table.
func NewRouter(ttl time.Duration) *Router {
	if ttl <= 0 {
		ttl = DefaultRouteTTL
	}
	return &Router{
		routes: make(map[[TruncatedHashLen]byte]*RouteEntry),
		ttl:    ttl,
	}
}

// ProcessAnnounce updates the routing table from a verified announce.
// Returns true if the route was new or updated, false if ignored (e.g. worse
// path than existing).
func (rt *Router) ProcessAnnounce(a *Announce, iface InterfaceType) bool {
	cost := InterfaceCost(iface)
	now := time.Now()

	rt.mu.Lock()
	defer rt.mu.Unlock()

	existing, exists := rt.routes[a.DestHash]

	if exists && !existing.IsExpired() {
		// Keep existing route if it's cheaper or same-cost with fewer hops.
		if existing.Cost < cost {
			return false
		}
		if existing.Cost == cost && existing.Hops <= int(a.Hops) {
			// Same cost, same or fewer hops — just refresh the timestamp.
			if existing.Interface == iface && existing.Hops == int(a.Hops) {
				existing.LastSeen = now
				existing.ExpiresAt = now.Add(rt.ttl)
				return true
			}
			return false
		}
	}

	// New route or better route.
	rt.routes[a.DestHash] = &RouteEntry{
		DestHash:   a.DestHash,
		Interface:  iface,
		Cost:       cost,
		Hops:       int(a.Hops),
		LastSeen:   now,
		ExpiresAt:  now.Add(rt.ttl),
		SigningPub: a.SigningPublicKey(),
		AppData:    a.AppData,
	}

	if exists {
		slog.Debug("reticulum: route updated",
			"dest", hex.EncodeToString(a.DestHash[:]),
			"iface", iface,
			"cost", cost,
			"hops", a.Hops,
		)
	} else {
		slog.Info("reticulum: new route learned",
			"dest", hex.EncodeToString(a.DestHash[:]),
			"iface", iface,
			"cost", cost,
			"hops", a.Hops,
		)
	}

	return true
}

// Lookup returns the best route for a destination, or nil if unknown/expired.
func (rt *Router) Lookup(dest [TruncatedHashLen]byte) *RouteEntry {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	entry, ok := rt.routes[dest]
	if !ok || entry.IsExpired() {
		return nil
	}
	return entry
}

// LookupHex is a convenience method that accepts a hex-encoded dest hash.
func (rt *Router) LookupHex(destHex string) *RouteEntry {
	b, err := hex.DecodeString(destHex)
	if err != nil || len(b) != TruncatedHashLen {
		return nil
	}
	var dest [TruncatedHashLen]byte
	copy(dest[:], b)
	return rt.Lookup(dest)
}

// AllRoutes returns a snapshot of all non-expired routes.
func (rt *Router) AllRoutes() []RouteInfo {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	now := time.Now()
	routes := make([]RouteInfo, 0, len(rt.routes))
	for _, entry := range rt.routes {
		if now.After(entry.ExpiresAt) {
			continue
		}
		ri := RouteInfo{
			DestHash:  hex.EncodeToString(entry.DestHash[:]),
			Interface: string(entry.Interface),
			Cost:      entry.Cost,
			Hops:      entry.Hops,
			LastSeen:  entry.LastSeen.UTC().Format(time.RFC3339),
			ExpiresAt: entry.ExpiresAt.UTC().Format(time.RFC3339),
		}
		if len(entry.SigningPub) > 0 {
			ri.SigningPubHex = hex.EncodeToString(entry.SigningPub)
		}
		if len(entry.AppData) > 0 {
			ri.AppData = string(entry.AppData)
		}
		routes = append(routes, ri)
	}
	return routes
}

// RouteCount returns the number of non-expired routes.
func (rt *Router) RouteCount() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	count := 0
	now := time.Now()
	for _, entry := range rt.routes {
		if !now.After(entry.ExpiresAt) {
			count++
		}
	}
	return count
}

// ExpireStale removes expired entries from the routing table.
// Call this periodically (e.g. every minute) to keep the table clean.
func (rt *Router) ExpireStale() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	removed := 0
	for dest, entry := range rt.routes {
		if now.After(entry.ExpiresAt) {
			delete(rt.routes, dest)
			removed++
		}
	}
	if removed > 0 {
		slog.Debug("reticulum: expired stale routes", "removed", removed, "remaining", len(rt.routes))
	}
	return removed
}

// Remove deletes a route for the given destination.
func (rt *Router) Remove(dest [TruncatedHashLen]byte) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if _, ok := rt.routes[dest]; ok {
		delete(rt.routes, dest)
		return true
	}
	return false
}

// BestInterface returns the cheapest interface to reach a destination.
// Returns empty string if no route is known.
func (rt *Router) BestInterface(dest [TruncatedHashLen]byte) InterfaceType {
	entry := rt.Lookup(dest)
	if entry == nil {
		return ""
	}
	return entry.Interface
}
