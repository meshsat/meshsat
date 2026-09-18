package timesync

import (
	"context"
	"encoding/binary"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// requestInterval is the period of the time sync request on an interface
// where a time-sync peer is present.
const requestInterval = 30 * time.Second

// RequestInterval is requestInterval for callers outside the package.
const RequestInterval = requestInterval

// DefaultDiscoveryInterval is the period of the request on an interface where
// no time-sync peer has spoken within peerTTL. Until 18 Sep 2026 the request
// went out every 30 s on every free bearer, twice on LoRa, peer or not: 240
// mesh packets an hour at 1.09 s each, 7.3 % of the EU868 10 % duty cycle on
// a kit with nobody on its mesh able to answer. [MESHSAT-778]
const DefaultDiscoveryInterval = 10 * time.Minute

// peerTTL is how long an interface keeps the full request rate after a peer
// last asked or answered on it. Matches the stale-peer prune.
const peerTTL = 10 * time.Minute

// seenRequestTTL bounds the request dedup window: the same request can
// arrive over two links to the same peer (both TCP connections between the
// kits) and used to be answered twice on every bearer. [MESHSAT-778]
const seenRequestTTL = 60 * time.Second

// ReplyFunc sends a raw packet to one interface (the one a request came in on).
type ReplyFunc func(ifaceID string, data []byte)

type seenRequest struct {
	sender    [DestHashLen]byte
	timestamp int64
}

// Mesh time consensus packet types (Bridge-specific, 0x14-0x15).
const (
	PacketTimeSyncReq  byte = 0x14
	PacketTimeSyncResp byte = 0x15
)

// Wire sizes.
const (
	timeSyncReqLen  = 1 + 16 + 8 + 1     // type + dest_hash + timestamp + stratum = 26
	timeSyncRespLen = 1 + 16 + 8 + 1 + 8 // type + dest_hash + timestamp + stratum + echo = 34
)

// DestHashLen is the truncated Reticulum destination hash length.
const DestHashLen = 16

// StratumUnsynchronised is the NTP convention for a clock that must not be
// used as a reference. We answer with it when our own clock is not trusted,
// and we discard any peer reading that carries it. [MESHSAT-1056]
const StratumUnsynchronised = 16

// IdentityProvider gives access to the local routing identity.
type IdentityProvider interface {
	DestHash() [DestHashLen]byte
}

// SendFunc sends a raw packet to all active links (via processor).
type SendFunc func(data []byte)

// peerClock tracks the time state of a single mesh peer.
type peerClock struct {
	destHash    [DestHashLen]byte
	stratum     int
	lastOffset  int64   // nanoseconds
	offsetEWMA  float64 // exponentially weighted moving average
	lastSeen    time.Time
	lastRTT     time.Duration
	iface       string // interface the last response came in on
	sampleCount int
}

// ifaceSchedule is the request state of one interface.
type ifaceSchedule struct {
	peerSeen time.Time // last request or response from another bridge on it
	lastSent time.Time // last request we sent on it
}

// PeerState is a read-only view of one time-sync peer.
type PeerState struct {
	DestHash  string    `json:"dest_hash"`
	Stratum   int       `json:"stratum"`
	OffsetMs  float64   `json:"offset_ms"`
	LastRTTMs float64   `json:"last_rtt_ms"`
	LastSeen  time.Time `json:"last_seen"`
	Samples   int       `json:"samples"`
	Iface     string    `json:"iface"`
	Stale     bool      `json:"stale"`
}

// InterfaceState is a read-only view of the request schedule on one interface.
// Mode is "peer" while a time-sync peer spoke on the interface within the peer
// window, else "discovery".
type InterfaceState struct {
	Iface         string     `json:"iface"`
	Mode          string     `json:"mode"`
	PeriodSec     int        `json:"period_sec"`
	PeerSeenAt    *time.Time `json:"peer_seen_at,omitempty"`
	LastRequestAt *time.Time `json:"last_request_at,omitempty"`
}

// MeshTimeConsensus implements a simplified NTP-like protocol over
// Reticulum links. Bridges exchange timestamps via request/response
// packets and compute clock offsets using round-trip measurement.
type MeshTimeConsensus struct {
	ts       *TimeService
	identity IdentityProvider
	sendFn   SendFunc
	mu       sync.RWMutex
	peers    map[[DestHashLen]byte]*peerClock

	// Pending requests: echo_timestamp -> send_time (for RTT).
	pendingMu sync.Mutex
	pending   map[int64]time.Time // requestTimestampNanos -> localSendTime

	// replyFn answers a request on the interface it arrived on; nil falls
	// back to the broadcast sendFn. [MESHSAT-778]
	replyFn ReplyFunc

	// seen deduplicates requests that reach us over several links.
	seenMu sync.Mutex
	seen   map[seenRequest]time.Time

	// clockTrusted reports whether our own clock is worth answering with.
	// Nil means yes. [MESHSAT-1056]
	clockTrusted func() bool

	// startOffset delays the first request so two bridges restarted a
	// multiple of the period apart do not key their radios in the same
	// second forever (5 Sep 2026: both kits deaf on APRS after a deploy
	// 120 s apart). Randomised in NewMeshTimeConsensus; tests may zero it.
	startOffset time.Duration

	// Per-interface requests. With ifaceList and ifaceSend set, a request
	// goes out on an interface every requestInterval while a peer is there
	// and every discoveryInterval otherwise; without them every request is
	// broadcast through sendFn as before. [MESHSAT-778]
	ifaceList         func() []string
	ifaceSend         ReplyFunc
	discoveryInterval time.Duration
	ifaceMu           sync.Mutex
	ifaces            map[string]*ifaceSchedule
}

// NewMeshTimeConsensus creates a new mesh time consensus instance.
func NewMeshTimeConsensus(ts *TimeService, identity IdentityProvider, sendFn SendFunc) *MeshTimeConsensus {
	return &MeshTimeConsensus{
		ts:                ts,
		identity:          identity,
		sendFn:            sendFn,
		peers:             make(map[[DestHashLen]byte]*peerClock),
		pending:           make(map[int64]time.Time),
		seen:              make(map[seenRequest]time.Time),
		startOffset:       rand.N(requestInterval),
		discoveryInterval: DefaultDiscoveryInterval,
		ifaces:            make(map[string]*ifaceSchedule),
	}
}

// SetInterfaces switches requests from one broadcast to per-interface sends:
// list returns the interfaces a request may go out on (the free ones), send
// transmits on one of them. [MESHSAT-778]
func (mc *MeshTimeConsensus) SetInterfaces(list func() []string, send ReplyFunc) {
	mc.mu.Lock()
	mc.ifaceList = list
	mc.ifaceSend = send
	mc.mu.Unlock()
}

// SetDiscoveryInterval sets the request period on an interface with no peer.
// Values under requestInterval are raised to it.
func (mc *MeshTimeConsensus) SetDiscoveryInterval(d time.Duration) {
	if d < requestInterval {
		d = requestInterval
	}
	mc.ifaceMu.Lock()
	mc.discoveryInterval = d
	mc.ifaceMu.Unlock()
}

// SetReplyFunc routes time sync responses to the interface the request
// came in on instead of broadcasting them on every free bearer.
func (mc *MeshTimeConsensus) SetReplyFunc(fn ReplyFunc) {
	mc.mu.Lock()
	mc.replyFn = fn
	mc.mu.Unlock()
}

// SetClockTrustFn supplies the check that decides whether this node's clock is
// fit to answer a peer's time sync request. Without it the node answers as
// before. [MESHSAT-1056]
func (mc *MeshTimeConsensus) SetClockTrustFn(fn func() bool) {
	mc.mu.Lock()
	mc.clockTrusted = fn
	mc.mu.Unlock()
}

// Start launches the periodic time sync request sender.
func (mc *MeshTimeConsensus) Start(ctx context.Context) {
	go mc.requestLoop(ctx)
	go mc.pruneLoop(ctx)
}

func (mc *MeshTimeConsensus) requestLoop(ctx context.Context) {
	if mc.startOffset > 0 {
		log.Debug().Dur("offset", mc.startOffset).Msg("timesync: first request delayed by start offset")
		select {
		case <-ctx.Done():
			return
		case <-time.After(mc.startOffset):
		}
	}
	ticker := time.NewTicker(requestInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mc.sendRequest()
		}
	}
}

// seenBefore records a request and reports whether it was already seen
// within seenRequestTTL.
func (mc *MeshTimeConsensus) seenBefore(sender [DestHashLen]byte, ts int64, now time.Time) bool {
	key := seenRequest{sender: sender, timestamp: ts}
	mc.seenMu.Lock()
	defer mc.seenMu.Unlock()
	if at, ok := mc.seen[key]; ok && now.Sub(at) < seenRequestTTL {
		return true
	}
	mc.seen[key] = now
	return false
}

func (mc *MeshTimeConsensus) pruneSeen() {
	mc.seenMu.Lock()
	defer mc.seenMu.Unlock()
	cutoff := time.Now().Add(-seenRequestTTL)
	for k, at := range mc.seen {
		if at.Before(cutoff) {
			delete(mc.seen, k)
		}
	}
}

func (mc *MeshTimeConsensus) pruneLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mc.pruneStalePeers()
			mc.pruneStaleRequests()
			mc.pruneSeen()
		}
	}
}

// sendRequest sends a time sync request on every interface that is due, or
// broadcasts it when no per-interface sender is set.
func (mc *MeshTimeConsensus) sendRequest() {
	mc.sendRequestAt(time.Now())
}

func (mc *MeshTimeConsensus) sendRequestAt(now time.Time) {
	mc.mu.RLock()
	list, send := mc.ifaceList, mc.ifaceSend
	mc.mu.RUnlock()

	var due []string
	if list != nil && send != nil {
		due = mc.dueInterfaces(list(), now)
		if len(due) == 0 {
			return
		}
	}

	localHash := mc.identity.DestHash()
	nowNanos := now.UnixNano()
	stratum := mc.ts.Stratum()

	pkt := make([]byte, timeSyncReqLen)
	pkt[0] = PacketTimeSyncReq
	copy(pkt[1:17], localHash[:])
	binary.LittleEndian.PutUint64(pkt[17:25], uint64(nowNanos))
	pkt[25] = byte(stratum)

	// Track pending request for RTT calculation.
	mc.pendingMu.Lock()
	mc.pending[nowNanos] = now
	mc.pendingMu.Unlock()

	if list == nil || send == nil {
		mc.sendFn(pkt)
		return
	}
	for _, id := range due {
		send(id, pkt)
	}
}

// dueInterfaces returns the interfaces a request goes out on at now and marks
// them sent: every requestInterval where a peer spoke within peerTTL, every
// discoveryInterval elsewhere, and at once on an interface never sent on.
// Half a period of slack keeps ticker jitter from skipping a whole period.
func (mc *MeshTimeConsensus) dueInterfaces(ids []string, now time.Time) []string {
	mc.ifaceMu.Lock()
	defer mc.ifaceMu.Unlock()

	var due []string
	for _, id := range ids {
		st := mc.ifaces[id]
		if st == nil {
			st = &ifaceSchedule{}
			mc.ifaces[id] = st
		}
		period := mc.periodLocked(st, now)
		if st.lastSent.IsZero() || now.Sub(st.lastSent) >= period-requestInterval/2 {
			st.lastSent = now
			due = append(due, id)
		}
	}
	return due
}

// periodLocked is the request period of one interface. Caller holds ifaceMu.
func (mc *MeshTimeConsensus) periodLocked(st *ifaceSchedule, now time.Time) time.Duration {
	if hasPeer(st, now) {
		return requestInterval
	}
	return mc.discoveryInterval
}

func hasPeer(st *ifaceSchedule, now time.Time) bool {
	return !st.peerSeen.IsZero() && now.Sub(st.peerSeen) <= peerTTL
}

// notePeer records that another bridge spoke time sync on iface.
func (mc *MeshTimeConsensus) notePeer(iface string, now time.Time) {
	if iface == "" {
		return
	}
	mc.ifaceMu.Lock()
	defer mc.ifaceMu.Unlock()
	st := mc.ifaces[iface]
	if st == nil {
		st = &ifaceSchedule{}
		mc.ifaces[iface] = st
	}
	st.peerSeen = now
}

// HandleTimeSyncRequest processes an incoming time sync request and sends a response.
func (mc *MeshTimeConsensus) HandleTimeSyncRequest(data []byte, sourceIface string) {
	if len(data) < timeSyncReqLen {
		return
	}
	if data[0] != PacketTimeSyncReq {
		return
	}

	var senderHash [DestHashLen]byte
	copy(senderHash[:], data[1:17])
	requestTimestamp := int64(binary.LittleEndian.Uint64(data[17:25]))
	// senderStratum := int(data[25])

	// Build response: our timestamp + echo of their request timestamp.
	localHash := mc.identity.DestHash()
	if senderHash == localHash {
		return // our own request echoed back
	}
	now := time.Now()
	// A duplicate over a second link still shows a peer on that link.
	mc.notePeer(sourceIface, now)
	if mc.seenBefore(senderHash, requestTimestamp, now) {
		log.Debug().Str("peer", hexHash(senderHash)).Str("iface", sourceIface).
			Msg("timesync: duplicate request, already answered")
		return
	}
	stratum := mc.ts.Stratum()

	// A kit that booted with no time source must not be adopted as anyone's
	// reference. Answering with the unsynchronised stratum keeps the
	// request/response protocol intact — the peer still gets its echo and its
	// round-trip measurement — while making the reading unusable, which is
	// what every consumer of this field, including ApplyCorrection, expects
	// stratum 16 to mean. [MESHSAT-1056]
	mc.mu.RLock()
	trusted := mc.clockTrusted
	mc.mu.RUnlock()
	if trusted != nil && !trusted() {
		log.Debug().Str("peer", hexHash(senderHash)).
			Msg("timesync: answering unsynchronised, host clock is not trusted")
		stratum = StratumUnsynchronised
	}

	resp := make([]byte, timeSyncRespLen)
	resp[0] = PacketTimeSyncResp
	copy(resp[1:17], localHash[:])
	binary.LittleEndian.PutUint64(resp[17:25], uint64(now.UnixNano()))
	resp[25] = byte(stratum)
	binary.LittleEndian.PutUint64(resp[26:34], uint64(requestTimestamp))

	mc.mu.RLock()
	reply := mc.replyFn
	mc.mu.RUnlock()
	if reply != nil && sourceIface != "" {
		reply(sourceIface, resp)
		return
	}
	mc.sendFn(resp)
}

// HandleTimeSyncResponse processes an incoming time sync response, calculates
// the clock offset using round-trip measurement, and updates peer state.
// sourceIface is the interface it came in on ("" if unknown).
func (mc *MeshTimeConsensus) HandleTimeSyncResponse(data []byte, sourceIface string) {
	if len(data) < timeSyncRespLen {
		return
	}
	if data[0] != PacketTimeSyncResp {
		return
	}

	var responderHash [DestHashLen]byte
	copy(responderHash[:], data[1:17])
	remoteTimestamp := int64(binary.LittleEndian.Uint64(data[17:25]))
	remoteStratum := int(data[25])
	echoTimestamp := int64(binary.LittleEndian.Uint64(data[26:34]))

	if responderHash == mc.identity.DestHash() {
		return
	}
	// Any answer proves a peer on this interface, even the second copy of
	// one that already arrived over another link.
	mc.notePeer(sourceIface, time.Now())

	// Look up the pending request to get the local send time.
	mc.pendingMu.Lock()
	sendTime, ok := mc.pending[echoTimestamp]
	if ok {
		delete(mc.pending, echoTimestamp)
	}
	mc.pendingMu.Unlock()

	if !ok {
		log.Debug().Msg("timesync: response for unknown request (expired or duplicate)")
		return
	}

	now := time.Now()
	rtt := now.Sub(sendTime)

	// NTP-style offset calculation:
	// offset = remoteTimestamp + RTT/2 - localReceiveTime
	oneWayDelay := rtt / 2
	localReceiveNanos := now.UnixNano()
	offset := remoteTimestamp + oneWayDelay.Nanoseconds() - localReceiveNanos

	// Update peer state with EWMA (alpha = 0.3).
	mc.mu.Lock()
	peer, exists := mc.peers[responderHash]
	if !exists {
		peer = &peerClock{destHash: responderHash}
		mc.peers[responderHash] = peer
	}
	peer.stratum = remoteStratum
	peer.lastOffset = offset
	peer.lastSeen = now
	peer.lastRTT = rtt
	peer.iface = sourceIface
	peer.sampleCount++
	if peer.sampleCount == 1 {
		peer.offsetEWMA = float64(offset)
	} else {
		peer.offsetEWMA = 0.3*float64(offset) + 0.7*peer.offsetEWMA
	}
	mc.mu.Unlock()

	log.Info().
		Str("peer", hexHash(responderHash)).
		Int("stratum", remoteStratum).
		Float64("offset_ms", float64(offset)/1e6).
		Float64("rtt_ms", float64(rtt.Nanoseconds())/1e6).
		Int("samples", peer.sampleCount).
		Msg("timesync: peer response received")

	mc.recalculateConsensus()
}

// recalculateConsensus computes a weighted average of peer offsets and
// applies the correction to the TimeService if it improves stratum.
func (mc *MeshTimeConsensus) recalculateConsensus() {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if len(mc.peers) == 0 {
		return
	}

	var totalWeight float64
	var weightedOffset float64
	minStratum := 255

	for _, peer := range mc.peers {
		// Skip stale peers (>5 min since last response).
		if time.Since(peer.lastSeen) > 5*time.Minute {
			continue
		}
		// Skip peers that declared themselves unsynchronised. Weighting them
		// at 1/(stratum+1) is not enough: a kit that booted hours in the past
		// would still drag the weighted average by a large fraction of its own
		// error. [MESHSAT-1056]
		if peer.stratum >= StratumUnsynchronised {
			continue
		}
		// Weight inversely proportional to stratum.
		weight := 1.0 / float64(peer.stratum+1)
		weightedOffset += peer.offsetEWMA * weight
		totalWeight += weight
		if peer.stratum < minStratum {
			minStratum = peer.stratum
		}
	}

	if totalWeight == 0 {
		return
	}

	avgOffset := int64(weightedOffset / totalWeight)
	derivedStratum := minStratum + 1

	// Mesh consensus uncertainty: 1000ms (radio latency variability).
	mc.ts.ApplyCorrection("mesh_peer", derivedStratum, avgOffset, 1_000_000_000)

	// Update peer count on the service.
	activePeers := 0
	for _, peer := range mc.peers {
		if time.Since(peer.lastSeen) <= 5*time.Minute {
			activePeers++
		}
	}
	mc.ts.SetPeerCount(activePeers)
}

func (mc *MeshTimeConsensus) pruneStalePeers() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	for hash, peer := range mc.peers {
		if time.Since(peer.lastSeen) > 10*time.Minute {
			delete(mc.peers, hash)
		}
	}
}

func (mc *MeshTimeConsensus) pruneStaleRequests() {
	mc.pendingMu.Lock()
	defer mc.pendingMu.Unlock()

	cutoff := time.Now().Add(-2 * time.Minute)
	for ts, sendTime := range mc.pending {
		if sendTime.Before(cutoff) {
			delete(mc.pending, ts)
		}
	}
}

// PeerCount returns the number of active mesh time peers.
func (mc *MeshTimeConsensus) PeerCount() int {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	count := 0
	for _, peer := range mc.peers {
		if time.Since(peer.lastSeen) <= 5*time.Minute {
			count++
		}
	}
	return count
}

// Peers returns the known time-sync peers, most recently heard first.
func (mc *MeshTimeConsensus) Peers() []PeerState {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	now := time.Now()
	out := make([]PeerState, 0, len(mc.peers))
	for _, p := range mc.peers {
		out = append(out, PeerState{
			DestHash:  hexHash(p.destHash),
			Stratum:   p.stratum,
			OffsetMs:  p.offsetEWMA / 1e6,
			LastRTTMs: float64(p.lastRTT.Nanoseconds()) / 1e6,
			LastSeen:  p.lastSeen,
			Samples:   p.sampleCount,
			Iface:     p.iface,
			Stale:     now.Sub(p.lastSeen) > 5*time.Minute,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// Interfaces returns the request schedule of every interface requests may go
// out on, sorted by name. A peer heard on any other interface (a paid one,
// which never carries a request) is not listed. Without per-interface sends
// it lists the interfaces a peer spoke on.
func (mc *MeshTimeConsensus) Interfaces() []InterfaceState {
	mc.mu.RLock()
	list := mc.ifaceList
	mc.mu.RUnlock()
	var ids []string
	if list != nil {
		ids = list()
	}

	mc.ifaceMu.Lock()
	defer mc.ifaceMu.Unlock()
	now := time.Now()
	names := make(map[string]bool, len(ids)+len(mc.ifaces))
	for _, id := range ids {
		names[id] = true
	}
	if list == nil {
		for id := range mc.ifaces {
			names[id] = true
		}
	}
	out := make([]InterfaceState, 0, len(names))
	for id := range names {
		st := mc.ifaces[id]
		if st == nil {
			st = &ifaceSchedule{}
		}
		s := InterfaceState{Iface: id, Mode: "discovery", PeriodSec: int(mc.periodLocked(st, now) / time.Second)}
		if hasPeer(st, now) {
			s.Mode = "peer"
		}
		if !st.peerSeen.IsZero() {
			t := st.peerSeen
			s.PeerSeenAt = &t
		}
		if !st.lastSent.IsZero() {
			t := st.lastSent
			s.LastRequestAt = &t
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Iface < out[j].Iface })
	return out
}

// DiscoveryInterval returns the request period on an interface with no peer.
func (mc *MeshTimeConsensus) DiscoveryInterval() time.Duration {
	mc.ifaceMu.Lock()
	defer mc.ifaceMu.Unlock()
	return mc.discoveryInterval
}

func hexHash(h [DestHashLen]byte) string {
	const hextable = "0123456789abcdef"
	buf := make([]byte, DestHashLen*2)
	for i, b := range h {
		buf[i*2] = hextable[b>>4]
		buf[i*2+1] = hextable[b&0x0f]
	}
	return string(buf)
}
