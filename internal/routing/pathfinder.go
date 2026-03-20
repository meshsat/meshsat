package routing

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// PathFinderConfig controls path discovery behavior.
type PathFinderConfig struct {
	// RequestTimeout is how long to wait for a path response before giving up.
	RequestTimeout time.Duration
	// DedupTTL is how long to remember seen path requests (prevents re-flooding).
	DedupTTL time.Duration
	// MaxDedupEntries caps the dedup cache size.
	MaxDedupEntries int
	// MaxHops is the maximum hop count for path request flooding.
	MaxHops int
}

// DefaultPathFinderConfig returns sensible defaults.
func DefaultPathFinderConfig() PathFinderConfig {
	return PathFinderConfig{
		RequestTimeout:  30 * time.Second,
		DedupTTL:        5 * time.Minute,
		MaxDedupEntries: 5000,
		MaxHops:         reticulum.PathfinderM,
	}
}

// PathSendFunc transmits a raw Reticulum packet on the named interface.
// If ifaceID is "", the packet should be sent on all interfaces (flood).
type PathSendFunc func(ifaceID string, packet []byte) error

// pendingRequest tracks an outstanding path request.
type pendingRequest struct {
	destHash [reticulum.TruncatedHashLen]byte
	tag      [reticulum.TruncatedHashLen]byte
	created  time.Time
	resultCh chan *reticulum.PathResponse
}

// PathFinder handles Reticulum path discovery via request/response flooding.
//
// When a node needs a route it doesn't have, it floods a PathRequest to all
// interfaces. Nodes that know the route reply with a PathResponse containing
// the announce data. The requester uses the response to populate its routing
// table.
type PathFinder struct {
	config   PathFinderConfig
	router   *reticulum.Router
	registry *InterfaceRegistry
	sendFn   PathSendFunc
	localID  *Identity

	mu      sync.Mutex
	seen    map[[reticulum.TruncatedHashLen]byte]time.Time       // tag → first-seen (dedup)
	pending map[[reticulum.TruncatedHashLen]byte]*pendingRequest // tag → pending request
}

// NewPathFinder creates a new path discovery manager.
func NewPathFinder(config PathFinderConfig, router *reticulum.Router, registry *InterfaceRegistry, localID *Identity, sendFn PathSendFunc) *PathFinder {
	return &PathFinder{
		config:   config,
		router:   router,
		registry: registry,
		sendFn:   sendFn,
		localID:  localID,
		seen:     make(map[[reticulum.TruncatedHashLen]byte]time.Time),
		pending:  make(map[[reticulum.TruncatedHashLen]byte]*pendingRequest),
	}
}

// RequestPath sends a path request for the given destination and waits for a
// response (up to the configured timeout). Returns the path response if found,
// or nil if the request timed out.
func (pf *PathFinder) RequestPath(ctx context.Context, destHash [reticulum.TruncatedHashLen]byte) *reticulum.PathResponse {
	// Generate random tag for dedup
	var tag [reticulum.TruncatedHashLen]byte
	if _, err := rand.Read(tag[:]); err != nil {
		log.Error().Err(err).Msg("pathfinder: failed to generate tag")
		return nil
	}

	// Register pending request
	pr := &pendingRequest{
		destHash: destHash,
		tag:      tag,
		created:  time.Now(),
		resultCh: make(chan *reticulum.PathResponse, 1),
	}

	pf.mu.Lock()
	pf.pending[tag] = pr
	pf.mu.Unlock()

	defer func() {
		pf.mu.Lock()
		delete(pf.pending, tag)
		pf.mu.Unlock()
	}()

	// Build and flood the path request
	req := &reticulum.PathRequest{
		DestHash: destHash,
		Tag:      tag,
	}

	// Flood to all online interfaces
	pf.floodRequest(req)

	log.Debug().
		Str("dest", reticulum.DestHashHex(destHash)).
		Msg("pathfinder: path request sent")

	// Wait for response or timeout
	timeout := pf.config.RequestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	select {
	case resp := <-pr.resultCh:
		log.Info().
			Str("dest", reticulum.DestHashHex(destHash)).
			Int("hops", int(resp.Hops)).
			Str("via", resp.InterfaceType).
			Msg("pathfinder: path response received")
		return resp
	case <-time.After(timeout):
		log.Debug().
			Str("dest", reticulum.DestHashHex(destHash)).
			Msg("pathfinder: path request timed out")
		return nil
	case <-ctx.Done():
		return nil
	}
}

// HandlePathRequest processes an incoming path request. If we know the route,
// we send a path response. Otherwise, we flood the request to other interfaces.
func (pf *PathFinder) HandlePathRequest(data []byte, sourceIface string) {
	req, err := reticulum.UnmarshalPathRequest(data)
	if err != nil {
		log.Debug().Err(err).Msg("pathfinder: failed to parse path request")
		return
	}

	// Dedup check
	pf.mu.Lock()
	if _, exists := pf.seen[req.Tag]; exists {
		pf.mu.Unlock()
		return
	}
	pf.seen[req.Tag] = time.Now()
	pf.mu.Unlock()

	destHex := reticulum.DestHashHex(req.DestHash)

	// Check if we are the destination
	if pf.localID != nil && pf.localID.DestHash() == req.DestHash {
		log.Debug().
			Str("dest", destHex).
			Msg("pathfinder: we are the destination, sending response")
		pf.sendLocalResponse(req, sourceIface)
		return
	}

	// Check if we know the route
	route := pf.router.Lookup(req.DestHash)
	if route != nil {
		log.Debug().
			Str("dest", destHex).
			Str("via", string(route.Interface)).
			Msg("pathfinder: known route, sending response")
		pf.sendKnownResponse(req, route, sourceIface)
		return
	}

	// Flood to all other interfaces
	log.Debug().
		Str("dest", destHex).
		Str("from", sourceIface).
		Msg("pathfinder: flooding path request")
	pf.floodRequestExcept(req, sourceIface)
}

// HandlePathResponse processes an incoming path response.
func (pf *PathFinder) HandlePathResponse(data []byte, sourceIface string) {
	resp, err := reticulum.UnmarshalPathResponse(data)
	if err != nil {
		log.Debug().Err(err).Msg("pathfinder: failed to parse path response")
		return
	}

	destHex := reticulum.DestHashHex(resp.DestHash)

	// Check if we have a pending request for this tag
	pf.mu.Lock()
	pr, exists := pf.pending[resp.Tag]
	pf.mu.Unlock()

	if exists {
		// Deliver to the waiting goroutine
		select {
		case pr.resultCh <- resp:
		default:
		}
		return
	}

	// Not our request — forward the response toward the requester.
	// In Reticulum, path responses travel back along the reverse path.
	// For now, we update our own routing table from the response and
	// relay it on all interfaces except the source.
	if resp.Hops > 0 {
		resp.Hops++ // increment hop count for relay
	}

	log.Debug().
		Str("dest", destHex).
		Str("from", sourceIface).
		Msg("pathfinder: relaying path response")

	pf.relayResponse(resp, sourceIface)
}

// StartPruner launches a background goroutine to clean up stale dedup entries
// and expired pending requests.
func (pf *PathFinder) StartPruner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pf.prune()
			}
		}
	}()
}

func (pf *PathFinder) prune() {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	now := time.Now()

	// Prune dedup cache
	for tag, ts := range pf.seen {
		if now.Sub(ts) > pf.config.DedupTTL {
			delete(pf.seen, tag)
		}
	}
	if len(pf.seen) > pf.config.MaxDedupEntries {
		excess := len(pf.seen) - pf.config.MaxDedupEntries
		removed := 0
		for tag := range pf.seen {
			if removed >= excess {
				break
			}
			delete(pf.seen, tag)
			removed++
		}
	}

	// Prune expired pending requests
	for tag, pr := range pf.pending {
		if now.Sub(pr.created) > pf.config.RequestTimeout*2 {
			delete(pf.pending, tag)
		}
	}
}

// floodRequest sends a path request to all online interfaces.
func (pf *PathFinder) floodRequest(req *reticulum.PathRequest) {
	pf.floodRequestExcept(req, "")
}

// floodRequestExcept sends a path request to all online interfaces except the source.
func (pf *PathFinder) floodRequestExcept(req *reticulum.PathRequest, exceptIface string) {
	// Build the packet: broadcast dest hash (all zeros = "anyone who knows")
	var broadcastDest [reticulum.TruncatedHashLen]byte
	packet := reticulum.BuildPathRequestPacket(broadcastDest, req)

	if pf.registry != nil {
		for _, iface := range pf.registry.All() {
			if iface.ID() == exceptIface || !iface.IsOnline() {
				continue
			}
			if err := pf.sendFn(iface.ID(), packet); err != nil {
				log.Debug().Err(err).
					Str("iface", iface.ID()).
					Msg("pathfinder: failed to flood request")
			}
		}
	} else if pf.sendFn != nil {
		pf.sendFn("", packet)
	}
}

// sendLocalResponse sends a path response for our own identity.
func (pf *PathFinder) sendLocalResponse(req *reticulum.PathRequest, targetIface string) {
	resp := &reticulum.PathResponse{
		DestHash:      req.DestHash,
		Tag:           req.Tag,
		Hops:          0,
		InterfaceType: "local",
	}
	packet := reticulum.BuildPathResponsePacket(req.DestHash, resp)
	if err := pf.sendFn(targetIface, packet); err != nil {
		log.Debug().Err(err).Msg("pathfinder: failed to send local response")
	}
}

// sendKnownResponse sends a path response based on a known route.
func (pf *PathFinder) sendKnownResponse(req *reticulum.PathRequest, route *reticulum.RouteEntry, targetIface string) {
	resp := &reticulum.PathResponse{
		DestHash:      req.DestHash,
		Tag:           req.Tag,
		Hops:          byte(route.Hops),
		InterfaceType: string(route.Interface),
	}
	packet := reticulum.BuildPathResponsePacket(req.DestHash, resp)
	if err := pf.sendFn(targetIface, packet); err != nil {
		log.Debug().Err(err).Msg("pathfinder: failed to send known response")
	}
}

// relayResponse forwards a path response to all interfaces except the source.
func (pf *PathFinder) relayResponse(resp *reticulum.PathResponse, exceptIface string) {
	packet := reticulum.BuildPathResponsePacket(resp.DestHash, resp)
	if pf.registry != nil {
		for _, iface := range pf.registry.All() {
			if iface.ID() == exceptIface || !iface.IsOnline() {
				continue
			}
			if err := pf.sendFn(iface.ID(), packet); err != nil {
				log.Debug().Err(err).
					Str("iface", iface.ID()).
					Msg("pathfinder: failed to relay response")
			}
		}
	}
}

// SeenCount returns the number of entries in the dedup cache (for metrics).
func (pf *PathFinder) SeenCount() int {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return len(pf.seen)
}

// PendingCount returns the number of outstanding path requests (for metrics).
func (pf *PathFinder) PendingCount() int {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return len(pf.pending)
}
