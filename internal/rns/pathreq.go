package rns

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

// discoveryEntry is one row of Transport.discovery_path_requests.
type discoveryEntry struct {
	timeout          time.Time
	requestingIfaces []string
	engaged          bool
}

// handlePathRequest implements the path-request branch of Transport.inbound
// and Transport.path_request for a transport instance that searches on
// every interface (the bridge is the hub of its network).
func (n *Node) handlePathRequest(pkt *Packet) {
	data := pkt.Hdr.Data
	if len(data) < HashLen {
		return
	}
	var dest [HashLen]byte
	copy(dest[:], data[:HashLen])
	var tag []byte
	var requestor [HashLen]byte
	hasRequestor := false
	switch {
	case len(data) > 2*HashLen:
		copy(requestor[:], data[HashLen:2*HashLen])
		hasRequestor = true
		tag = data[2*HashLen:]
	case len(data) > HashLen:
		tag = data[HashLen:]
	default:
		return // tagless
	}
	if len(tag) > HashLen {
		tag = tag[:HashLen]
	}
	var unique [2 * HashLen]byte
	copy(unique[:HashLen], dest[:])
	copy(unique[HashLen:], tag)

	n.mu.Lock()
	if _, seen := n.prTags[unique]; seen {
		n.mu.Unlock()
		return
	}
	if _, seen := n.prTagsPrev[unique]; seen {
		n.mu.Unlock()
		return
	}
	n.prTags[unique] = struct{}{}
	n.stats.PathRequestsRx++
	if _, inflight := n.inflightPRs[dest]; inflight {
		// Batch onto the existing discovery.
		d := n.discoveryPRs[dest]
		if d == nil {
			d = &discoveryEntry{timeout: n.now().Add(reticulum.PathRequestTimeoutSec * time.Second)}
			n.discoveryPRs[dest] = d
		}
		if !contains(d.requestingIfaces, pkt.Iface) {
			d.requestingIfaces = append(d.requestingIfaces, pkt.Iface)
		}
		n.mu.Unlock()
		return
	}
	n.inflightPRs[dest] = n.now()
	local := n.dests[dest]
	n.mu.Unlock()

	// Local destination: answer with an announce on the requesting interface.
	if local != nil {
		if err := n.Announce(local, pkt.Iface, true); err == nil {
			log.Debug().Str("dest", hexh(dest)).Str("iface", pkt.Iface).Msg("rns: answered path request for local destination")
		}
		n.resolveInflight(dest)
		return
	}

	// Known path: queue the cached announce as a path response after the grace period.
	if e := n.paths.Get(dest); e != nil && e.Announce != nil {
		if hasRequestor && e.NextHop == requestor {
			log.Debug().Str("dest", hexh(dest)).Msg("rns: not answering path request, next hop is the requestor")
			n.resolveInflight(dest)
			return
		}
		now := n.now()
		n.mu.Lock()
		if held, ok := n.announceTable[dest]; ok && !held.blockRebroadcasts {
			n.heldAnnounces[dest] = held
		}
		n.announceTable[dest] = &announceEntry{
			timestamp:         now,
			retransmitTimeout: now.Add(time.Duration(reticulum.PathRequestGraceSec * float64(time.Second))),
			retries:           reticulum.PathfinderR,
			receivedFrom:      e.NextHop,
			hops:              e.Hops,
			ann:               e.Announce,
			blockRebroadcasts: true,
			attachedIface:     pkt.Iface,
		}
		n.mu.Unlock()
		n.resolveInflight(dest)
		return
	}

	// Unknown: search on every other floodable interface with the same tag.
	n.mu.Lock()
	d := n.discoveryPRs[dest]
	if d != nil && d.engaged {
		if !contains(d.requestingIfaces, pkt.Iface) {
			d.requestingIfaces = append(d.requestingIfaces, pkt.Iface)
		}
		n.mu.Unlock()
		return
	}
	if d == nil {
		d = &discoveryEntry{}
		n.discoveryPRs[dest] = d
	}
	d.timeout = n.now().Add(reticulum.PathRequestTimeoutSec * time.Second)
	d.engaged = true
	if !contains(d.requestingIfaces, pkt.Iface) {
		d.requestingIfaces = append(d.requestingIfaces, pkt.Iface)
	}
	n.mu.Unlock()
	var tag16 [HashLen]byte
	copy(tag16[:], tag)
	for _, id := range n.tx.Floodable() {
		if id != pkt.Iface {
			n.sendPathRequest(dest, id, tag16)
		}
	}
}

func (n *Node) resolveInflight(dest [HashLen]byte) {
	n.mu.Lock()
	delete(n.inflightPRs, dest)
	n.mu.Unlock()
}

func (n *Node) expireDiscovery(now time.Time) {
	n.mu.Lock()
	for k, d := range n.discoveryPRs {
		if now.After(d.timeout) {
			delete(n.discoveryPRs, k)
			delete(n.inflightPRs, k)
		}
	}
	for k, t := range n.inflightPRs {
		if now.After(t.Add(reticulum.PathRequestTimeoutSec * time.Second)) {
			delete(n.inflightPRs, k)
		}
	}
	n.mu.Unlock()
}

// sendPathRequest emits one path request packet on an interface.
func (n *Node) sendPathRequest(dest [HashLen]byte, ifaceID string, tag [HashLen]byte) {
	data := make([]byte, 0, 3*HashLen)
	data = append(data, dest[:]...)
	data = append(data, n.idHash[:]...) // we are a transport instance
	data = append(data, tag[:]...)
	h := &reticulum.Header{
		HeaderType:    reticulum.HeaderType1,
		TransportType: reticulum.TransportBroadcast,
		DestType:      reticulum.DestPlain,
		PacketType:    reticulum.PacketData,
		DestHash:      reticulum.PathRequestDestHash(),
		Context:       reticulum.ContextNone,
		Data:          data,
	}
	n.transmit(ifaceID, h.Marshal())
}

// RequestPath asks the network for a path and waits up to ctx/timeout for
// an announce to arrive. Returns true when a path is known afterwards.
func (n *Node) RequestPath(ctx context.Context, dest [HashLen]byte, timeout time.Duration) bool {
	if n.paths.Has(dest) {
		return true
	}
	if timeout <= 0 {
		timeout = reticulum.PathRequestTimeoutSec * time.Second
	}
	ch := make(chan struct{})
	n.mu.Lock()
	n.pathWaiters[dest] = append(n.pathWaiters[dest], ch)
	n.pathRequests[dest] = n.now()
	n.mu.Unlock()
	tag := randomHash()
	for _, id := range n.tx.Floodable() {
		n.sendPathRequest(dest, id, tag)
	}
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
	case <-ctx.Done():
	}
	n.mu.Lock()
	ws := n.pathWaiters[dest]
	for i, w := range ws {
		if w == ch {
			n.pathWaiters[dest] = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	n.mu.Unlock()
	return n.paths.Has(dest)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
