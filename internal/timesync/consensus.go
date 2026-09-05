package timesync

import (
	"context"
	"encoding/binary"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// requestInterval is the period of the time sync request broadcast.
const requestInterval = 30 * time.Second

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
	sampleCount int
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

	// startOffset delays the first request so two bridges restarted a
	// multiple of the period apart do not key their radios in the same
	// second forever (5 Sep 2026: both kits deaf on APRS after a deploy
	// 120 s apart). Randomised in NewMeshTimeConsensus; tests may zero it.
	startOffset time.Duration
}

// NewMeshTimeConsensus creates a new mesh time consensus instance.
func NewMeshTimeConsensus(ts *TimeService, identity IdentityProvider, sendFn SendFunc) *MeshTimeConsensus {
	return &MeshTimeConsensus{
		ts:          ts,
		identity:    identity,
		sendFn:      sendFn,
		peers:       make(map[[DestHashLen]byte]*peerClock),
		pending:     make(map[int64]time.Time),
		seen:        make(map[seenRequest]time.Time),
		startOffset: rand.N(requestInterval),
	}
}

// SetReplyFunc routes time sync responses to the interface the request
// came in on instead of broadcasting them on every free bearer.
func (mc *MeshTimeConsensus) SetReplyFunc(fn ReplyFunc) {
	mc.mu.Lock()
	mc.replyFn = fn
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

// sendRequest broadcasts a time sync request to all active links.
func (mc *MeshTimeConsensus) sendRequest() {
	localHash := mc.identity.DestHash()
	now := time.Now()
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

	mc.sendFn(pkt)
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
	if mc.seenBefore(senderHash, requestTimestamp, now) {
		log.Debug().Str("peer", hexHash(senderHash)).Str("iface", sourceIface).
			Msg("timesync: duplicate request, already answered")
		return
	}
	stratum := mc.ts.Stratum()

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
func (mc *MeshTimeConsensus) HandleTimeSyncResponse(data []byte) {
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

func hexHash(h [DestHashLen]byte) string {
	const hextable = "0123456789abcdef"
	buf := make([]byte, DestHashLen*2)
	for i, b := range h {
		buf[i*2] = hextable[b>>4]
		buf[i*2+1] = hextable[b&0x0f]
	}
	return string(buf)
}
