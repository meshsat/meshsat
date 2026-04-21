package api

// host-ops: allowed-in-standalone — the reconciler shells out to
// `ip addr` via nsenter for exactly the same reason every other
// handler in this directory does: standalone-mode field kits run the
// bridge as the sole trusted container on a dedicated Pi, with no
// HAL container in the loop. The nsenter calls are narrow (ip addr
// replace / ip addr del / ip addr show) and gated by a role derived
// from wpa_supplicant's own P2P status so they only fire when a
// WiFi-Direct group is genuinely active. [MESHSAT-647]

// P2P reconciler — bulletproof kit-to-kit transport anchoring for
// WiFi-Direct. [MESHSAT-647]
//
// Problem it solves (discovered live 2026-04-21 on tesseract + parallax):
// the earlier auto-wire path dialed the peer at its IPv6 link-local +
// zone ID (fe80::...%p2p-0:4242). The zone binds the TCP socket to
// an interface index at dial time, so if the kernel later renames
// the iface (e.g. p2p-0 → p2p-wlxXXXX-0), tears it down and recreates
// it with a different index (observed: ifindex 451 during group
// formation, gone afterwards), or wpa_supplicant cleans up the short-
// lived group-dev iface, the socket keeps trying to route via the
// original ifindex. Linux silently drops the packets; TCP retransmits
// back off exponentially; Path-MTU discovery collapses to 68 bytes;
// parallax stalled at mss=36 cwnd=1.
//
// Fix: give the P2P group a deterministic IPv4 overlay — GO always
// gets 10.42.43.1/24, client always gets 10.42.43.2/24 — and dial the
// peer by IP, no zone. Whatever name/ifindex the group-iface takes,
// the overlay IP rides on top and the dialed address never changes.
//
// The reconciler runs continuously. Every 2 s it reads the current
// wpa_cli P2P status and reconciles:
//
//   * No active group → torn down overlay + remove dynamic TCP peer.
//   * Group iface differs from last-bound → move the overlay IP to
//     the new iface (`ip addr replace`), drop the old IP if we owned
//     it, re-register the TCP peer at the same overlay address.
//   * Overlay IP went missing on the current iface (NetworkManager
//     kicked it, bridge restart, etc.) → re-apply. This self-heals.
//
// The overlay address never contains a zone, so the TCP socket has
// nothing stale to point at. If the iface is renamed mid-session the
// reconciler re-applies the IP to the new name within 2 s; existing
// TCP connections keep working because Linux's routing table matches
// on destination IP, not ifindex.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// Deterministic role-keyed overlay IPs. /24 picked to avoid both
	// typical LAN ranges and hemb0's 10.42.42.0/24.
	P2POverlayGOIP     = "10.42.43.1"
	P2POverlayClientIP = "10.42.43.2"
	P2POverlayPrefix   = "24"
	P2POverlayPort     = 4242

	// Poll cadence. Fast enough to catch iface renames within a couple
	// of seconds; slow enough that an idle kit (no P2P in sight) barely
	// spends any CPU on it.
	P2PReconcileInterval = 2 * time.Second
)

// P2PReconciler keeps the WiFi-Direct overlay IPv4 address pinned to
// whatever group iface wpa_supplicant currently reports, and keeps the
// Reticulum TCP peer registered at the peer's overlay IP. Safe to
// instantiate with a nil tcpIface — the TCP wiring is skipped until
// Server.tcpIface is set.
type P2PReconciler struct {
	server *Server

	mu          sync.Mutex
	boundIface  string // current group iface our IP is attached to
	boundIP     string // overlay IP we assigned to boundIface
	peerAddr    string // TCP peer address we registered (no zone)
	lastRole    string // "go" / "client" — last reconciliation
	lastLogTick time.Time
}

// Package-level function variables for test seams. Tests replace
// these to drive the state machine deterministically without exec'ing
// wpa_cli or ip.
var (
	p2pReadStatus = readP2PStatus
	p2pAssignIP   = assignOverlayIP
	p2pRemoveIP   = removeOverlayIP
	p2pIfaceHasIP = ifaceHasIP
)

// NewP2PReconciler constructs a reconciler. Call Start to begin the
// watch loop.
func NewP2PReconciler(s *Server) *P2PReconciler {
	return &P2PReconciler{server: s}
}

// Start launches the reconcile loop. Returns immediately; the loop
// exits when ctx is cancelled. Safe to call more than once; additional
// calls spawn extra goroutines which race harmlessly (the mutex
// serialises state). Callers wanting exactly-once behaviour should
// gate on their own sync.Once.
func (r *P2PReconciler) Start(ctx context.Context) {
	go r.loop(ctx)
}

// TriggerReconcile runs one reconcile pass immediately. Used by the
// p2p_connect handler to close the gap between group formation and
// the next 2-second tick.
func (r *P2PReconciler) TriggerReconcile(ctx context.Context) {
	r.reconcileOnce(ctx)
}

func (r *P2PReconciler) loop(ctx context.Context) {
	tick := time.NewTicker(P2PReconcileInterval)
	defer tick.Stop()
	log.Info().Msg("p2p-reconciler: started")
	for {
		select {
		case <-ctx.Done():
			r.mu.Lock()
			r.teardownLocked(context.Background()) // best-effort on shutdown
			r.mu.Unlock()
			log.Info().Msg("p2p-reconciler: stopped")
			return
		case <-tick.C:
		}
		r.reconcileOnce(ctx)
	}
}

func (r *P2PReconciler) reconcileOnce(ctx context.Context) {
	st, err := p2pReadStatus(ctx, "")
	if err != nil {
		// wpa_cli not running or no p2p-dev iface yet — normal on
		// boot before any p2p_find has happened. Don't spam logs.
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if !st.Active || st.GroupIface == "" {
		if r.boundIface != "" {
			r.teardownLocked(ctx)
		}
		return
	}

	// Pick the overlay IP by role. If wpa_supplicant hasn't populated
	// role yet (rare — happens in the first few hundred ms after
	// group formation) we fall through and try again next tick.
	var myIP, peerIP string
	switch st.Role {
	case "go":
		myIP, peerIP = P2POverlayGOIP, P2POverlayClientIP
	case "client":
		myIP, peerIP = P2POverlayClientIP, P2POverlayGOIP
	default:
		return
	}
	peerAddr := fmt.Sprintf("%s:%d", peerIP, P2POverlayPort)

	// Steady-state: iface + IP unchanged. Verify the IP is still on
	// the iface (NetworkManager / netplan can strip it) and move on.
	if r.boundIface == st.GroupIface && r.boundIP == myIP {
		if !p2pIfaceHasIP(ctx, st.GroupIface, myIP) {
			log.Warn().Str("iface", st.GroupIface).Str("ip", myIP).
				Msg("p2p-reconciler: overlay IP stripped, re-applying")
			if err := p2pAssignIP(ctx, st.GroupIface, myIP); err != nil {
				log.Warn().Err(err).Msg("p2p-reconciler: re-apply failed")
			}
		}
		return
	}

	// State changed — iface rename, new group, role flip, or first
	// time seeing an active group. Remove the old binding (if on a
	// different iface), re-apply on the new iface.
	if r.boundIface != "" && r.boundIface != st.GroupIface {
		_ = p2pRemoveIP(ctx, r.boundIface, r.boundIP)
	}
	if err := p2pAssignIP(ctx, st.GroupIface, myIP); err != nil {
		log.Warn().Err(err).Str("iface", st.GroupIface).Str("ip", myIP).
			Msg("p2p-reconciler: assign overlay IP failed")
		return
	}

	// Move the Reticulum TCP peer registration to the new address.
	// RemovePeer is idempotent; AddPeer may already exist (we restart
	// after a group flap and the old peer entry persists) — not fatal.
	if r.server != nil && r.server.tcpIface != nil {
		if r.peerAddr != "" && r.peerAddr != peerAddr {
			r.server.tcpIface.RemovePeer(r.peerAddr)
		}
		if err := r.server.tcpIface.AddPeer(ctx, peerAddr); err != nil {
			log.Debug().Err(err).Str("addr", peerAddr).
				Msg("p2p-reconciler: AddPeer (already present?)")
		}
	}

	r.boundIface = st.GroupIface
	r.boundIP = myIP
	r.peerAddr = peerAddr
	r.lastRole = st.Role

	log.Info().
		Str("iface", st.GroupIface).
		Str("role", st.Role).
		Str("my_ip", myIP+"/"+P2POverlayPrefix).
		Str("peer", peerAddr).
		Msg("p2p-reconciler: overlay bound")
}

func (r *P2PReconciler) teardownLocked(ctx context.Context) {
	if r.boundIface != "" {
		_ = p2pRemoveIP(ctx, r.boundIface, r.boundIP)
	}
	if r.peerAddr != "" && r.server != nil && r.server.tcpIface != nil {
		r.server.tcpIface.RemovePeer(r.peerAddr)
	}
	log.Info().
		Str("iface", r.boundIface).
		Str("peer", r.peerAddr).
		Msg("p2p-reconciler: overlay torn down (group inactive)")
	r.boundIface = ""
	r.boundIP = ""
	r.peerAddr = ""
	r.lastRole = ""
}

// Snapshot returns a read-only view of the reconciler's current
// binding. Used by /api tests and future UI surfacing.
func (r *P2PReconciler) Snapshot() P2POverlaySnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return P2POverlaySnapshot{
		Iface:    r.boundIface,
		LocalIP:  r.boundIP,
		PeerAddr: r.peerAddr,
		Role:     r.lastRole,
	}
}

// P2POverlaySnapshot is exported so the status handler and tests can
// read the reconciler's current binding without grabbing the mutex.
type P2POverlaySnapshot struct {
	Iface    string `json:"iface,omitempty"`
	LocalIP  string `json:"local_ip,omitempty"`
	PeerAddr string `json:"peer_addr,omitempty"`
	Role     string `json:"role,omitempty"`
}

// assignOverlayIP idempotently assigns an IPv4 /N to the given iface
// in the host namespace via nsenter. `ip addr replace` is the
// hammer-and-anvil idempotent add — it's equivalent to "add if
// missing, no-op if present with the same cidr, overwrite if present
// with a different cidr on the same primary". Fatal errors (iface
// doesn't exist) propagate; we retry on the next tick.
func assignOverlayIP(ctx context.Context, iface, ip string) error {
	cidr := fmt.Sprintf("%s/%s", ip, P2POverlayPrefix)
	if _, err := execWithTimeout(ctx, "nsenter", "-t", "1", "-m", "-n", "--",
		"ip", "addr", "replace", cidr, "dev", iface); err != nil {
		return fmt.Errorf("ip addr replace %s dev %s: %w", cidr, iface, err)
	}
	// Bring iface up too — group-created ifaces are sometimes added
	// in DOWN state while wpa_supplicant waits for a client.
	_, _ = execWithTimeout(ctx, "nsenter", "-t", "1", "-m", "-n", "--",
		"ip", "link", "set", iface, "up")
	return nil
}

// removeOverlayIP best-effort strips the assigned IP. Any failure
// (iface already gone, IP already absent) is swallowed — the goal is
// just to avoid leaving a stale address behind when the group dies.
func removeOverlayIP(ctx context.Context, iface, ip string) error {
	if iface == "" || ip == "" {
		return nil
	}
	cidr := fmt.Sprintf("%s/%s", ip, P2POverlayPrefix)
	_, _ = execWithTimeout(ctx, "nsenter", "-t", "1", "-m", "-n", "--",
		"ip", "addr", "del", cidr, "dev", iface)
	return nil
}

// ifaceHasIP reports whether iface currently has ip assigned. Used
// by the self-healing check — NetworkManager / netplan can strip a
// manually-added address when it reconciles its own state.
func ifaceHasIP(ctx context.Context, iface, ip string) bool {
	out, err := execWithTimeout(ctx, "nsenter", "-t", "1", "-m", "-n", "--",
		"ip", "-o", "-4", "addr", "show", "dev", iface)
	if err != nil {
		return false
	}
	return strings.Contains(out, " "+ip+"/")
}
