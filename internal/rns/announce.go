package rns

import (
	"math/rand/v2"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
)

const maxRandomBlobs = 64

// announceEntry is one row of Transport.announce_table.
type announceEntry struct {
	timestamp         time.Time
	retransmitTimeout time.Time
	retries           int
	receivedFrom      [HashLen]byte
	hops              int
	ann               *reticulum.Announce
	sourceIface       string
	localRebroadcasts int
	blockRebroadcasts bool // true: send as PATH_RESPONSE on attachedIface only
	attachedIface     string
}

// handleAnnounce implements the announce branch of Transport.inbound.
func (n *Node) handleAnnounce(pkt *Packet) bool {
	h := pkt.Hdr
	ann, err := reticulum.UnmarshalAnnouncePayload(h.Data, h.DestHash, h.Hops, h.ContextFlag)
	if err != nil {
		return true
	}
	if err := ann.Verify(); err != nil {
		log.Debug().Str("iface", pkt.Iface).Err(err).Msg("rns: invalid announce")
		return true
	}
	ann.Context = h.Context
	n.count(func(s *Stats) { s.AnnouncesRx++ })

	if n.LocalDestination(h.DestHash) != nil {
		return true // our own announce echoed back
	}

	now := n.now()
	var receivedFrom [HashLen]byte
	if h.HeaderType == reticulum.HeaderType2 {
		receivedFrom = h.TransportID
	} else {
		receivedFrom = h.DestHash
	}
	hops := int(h.Hops)
	randomBlob := append([]byte(nil), ann.Random[:]...)
	emitted := blobTimestamp(randomBlob)

	n.mu.Lock()
	// A rebroadcast by another node of an announce we hold for retransmission.
	if h.HeaderType == reticulum.HeaderType2 {
		if e, ok := n.announceTable[h.DestHash]; ok {
			if hops-1 == e.hops {
				e.localRebroadcasts++
				if e.retries > 0 && e.localRebroadcasts >= reticulum.LocalRebroadcastsMax {
					delete(n.announceTable, h.DestHash)
				}
			}
			if hops-1 == e.hops+1 && e.retries > 0 && now.Before(e.retransmitTimeout) {
				delete(n.announceTable, h.DestHash)
			}
		}
	}

	shouldAdd := false
	existing := n.paths.Get(h.DestHash)
	var blobs [][]byte
	if hops < reticulum.PathfinderM+1 {
		if existing == nil {
			shouldAdd = true
		} else {
			blobs = existing.RandomBlobs
			if hops <= existing.Hops {
				if !containsBlob(blobs, randomBlob) && emitted > existing.EmittedAt() {
					shouldAdd = true
				}
			} else {
				pathEmitted := int64(0)
				for _, b := range blobs {
					if ts := blobTimestamp(b); ts > pathEmitted {
						pathEmitted = ts
					}
					if pathEmitted >= emitted {
						break
					}
				}
				if !now.Before(existing.Expires) {
					shouldAdd = !containsBlob(blobs, randomBlob)
				} else if emitted > pathEmitted {
					shouldAdd = !containsBlob(blobs, randomBlob)
				} else if emitted == pathEmitted && existing.Unresponsive {
					shouldAdd = true
				}
			}
		}
	}
	if !shouldAdd {
		n.mu.Unlock()
		return true
	}

	if !containsBlob(blobs, randomBlob) {
		blobs = append(blobs, randomBlob)
		if len(blobs) > maxRandomBlobs {
			blobs = blobs[len(blobs)-maxRandomBlobs:]
		}
	}
	entry := &PathEntry{
		DestHash:    h.DestHash,
		Timestamp:   now,
		NextHop:     receivedFrom,
		Hops:        hops,
		Expires:     now.Add(n.paths.TTL()),
		RandomBlobs: blobs,
		Iface:       pkt.Iface,
		Announce:    ann,
		PacketHash:  pkt.Hash,
	}
	if h.Context != reticulum.ContextPathResponse {
		n.announceTable[h.DestHash] = &announceEntry{
			timestamp:         now,
			retransmitTimeout: now.Add(time.Duration(rand.Float64() * reticulum.PathfinderRW * float64(time.Second))),
			receivedFrom:      receivedFrom,
			hops:              hops,
			ann:               ann,
			sourceIface:       pkt.Iface,
		}
	}
	// Answer waiting discovery path requests immediately.
	var answerOn []string
	if d, ok := n.discoveryPRs[h.DestHash]; ok {
		answerOn = append(answerOn, d.requestingIfaces...)
		delete(n.discoveryPRs, h.DestHash)
	}
	n.paths.Put(entry)
	// Remember the identity.
	k := n.identities[h.DestHash]
	if k == nil {
		k = &KnownIdentity{DestHash: h.DestHash}
		n.identities[h.DestHash] = k
	}
	k.PublicKey = append([]byte(nil), ann.PublicKey...)
	k.Identity, _ = reticulum.IdentityFromPublicBytes(ann.PublicKey)
	k.AppData = append([]byte(nil), ann.AppData...)
	if ann.ContextFlag != 0 {
		k.Ratchet = append([]byte(nil), ann.Ratchet[:]...)
		k.RatchetAt = now
	}
	k.LastSeen = now
	waiters := n.pathWaiters[h.DestHash]
	delete(n.pathWaiters, h.DestHash)
	delete(n.inflightPRs, h.DestHash)
	n.mu.Unlock()

	for _, ifc := range answerOn {
		n.transmit(ifc, ann.MarshalPacketTransport(n.idHash, byte(hops), reticulum.ContextPathResponse))
	}
	for _, w := range waiters {
		close(w)
	}
	log.Debug().Str("dest", hexh(h.DestHash)).Int("hops", hops).Str("iface", pkt.Iface).Str("via", hexh(receivedFrom)).Msg("rns: path learned")
	if n.OnPathChanged != nil {
		n.OnPathChanged(h.DestHash, entry)
	}
	if n.OnAnnounce != nil {
		n.OnAnnounce(ann, pkt)
	}
	return true
}

func containsBlob(blobs [][]byte, b []byte) bool {
	for _, x := range blobs {
		if string(x) == string(b) {
			return true
		}
	}
	return false
}

// processAnnounceTable retransmits queued announces (Transport.jobs).
func (n *Node) processAnnounceTable(now time.Time) {
	type out struct {
		raw    []byte
		ifaces []string
	}
	var outgoing []out
	n.mu.Lock()
	for dest, e := range n.announceTable {
		switch {
		case e.retries > 0 && e.retries >= reticulum.LocalRebroadcastsMax:
			delete(n.announceTable, dest)
		case e.retries > reticulum.PathfinderR:
			delete(n.announceTable, dest)
		case now.After(e.retransmitTimeout):
			e.retransmitTimeout = now.Add(time.Duration((reticulum.PathfinderG + reticulum.PathfinderRW) * float64(time.Second)))
			e.retries++
			ctx := byte(reticulum.ContextNone)
			if e.blockRebroadcasts {
				ctx = reticulum.ContextPathResponse
			}
			raw := e.ann.MarshalPacketTransport(n.idHash, byte(e.hops), ctx)
			// A rebroadcast goes out on every floodable interface, the one it
			// arrived on included: on a shared medium that repetition is the
			// whole point, and on a multi-peer TCP interface the other peers
			// only hear it that way. Upstream sends a fresh packet with no
			// receiving interface, so it does the same.
			o := out{raw: raw}
			if e.attachedIface != "" {
				o.ifaces = []string{e.attachedIface}
			}
			outgoing = append(outgoing, o)
			if held, ok := n.heldAnnounces[dest]; ok {
				delete(n.heldAnnounces, dest)
				n.announceTable[dest] = held
			}
		}
	}
	n.mu.Unlock()
	for _, o := range outgoing {
		ifaces := o.ifaces
		if ifaces == nil {
			ifaces = n.tx.Floodable()
		}
		for _, id := range ifaces {
			n.sendAnnounceOn(id, o.raw, now)
		}
		n.count(func(s *Stats) { s.AnnouncesTx++ })
	}
}

// announceQueue delays announces on a slow interface to the 2 % cap.
type announceQueue struct {
	allowedAt time.Time
	pending   [][]byte
	timer     *time.Timer
}

// sendAnnounceOn applies the announce cap: an interface with a known bit
// rate may only spend ANNOUNCE_CAP percent of its time on announces
// (Transport.outbound). Announces beyond the cap are queued, newest last,
// and released as the budget allows; the queue is bounded.
func (n *Node) sendAnnounceOn(ifaceID string, raw []byte, now time.Time) {
	bitrate := n.tx.Bitrate(ifaceID)
	if bitrate <= 0 {
		n.transmit(ifaceID, raw)
		return
	}
	txTime := float64(len(raw)*8) / float64(bitrate)
	wait := time.Duration(txTime / (float64(reticulum.AnnounceCap) / 100) * float64(time.Second))
	n.mu.Lock()
	q := n.announceQueue[ifaceID]
	if q == nil {
		q = &announceQueue{}
		n.announceQueue[ifaceID] = q
	}
	if len(q.pending) == 0 && now.After(q.allowedAt) {
		q.allowedAt = now.Add(wait)
		n.mu.Unlock()
		n.transmit(ifaceID, raw)
		return
	}
	if len(q.pending) >= 16 {
		n.mu.Unlock()
		log.Debug().Str("iface", ifaceID).Msg("rns: announce queue full, dropping")
		return
	}
	q.pending = append(q.pending, raw)
	if q.timer == nil {
		delay := time.Until(q.allowedAt)
		if delay < 0 {
			delay = 0
		}
		q.timer = time.AfterFunc(delay, func() { n.flushAnnounceQueue(ifaceID) })
	}
	n.mu.Unlock()
}

func (n *Node) flushAnnounceQueue(ifaceID string) {
	now := n.now()
	n.mu.Lock()
	q := n.announceQueue[ifaceID]
	if q == nil || len(q.pending) == 0 {
		if q != nil {
			q.timer = nil
		}
		n.mu.Unlock()
		return
	}
	raw := q.pending[0]
	q.pending = q.pending[1:]
	bitrate := n.tx.Bitrate(ifaceID)
	txTime := float64(len(raw)*8) / float64(bitrate)
	q.allowedAt = now.Add(time.Duration(txTime / (float64(reticulum.AnnounceCap) / 100) * float64(time.Second)))
	if len(q.pending) > 0 {
		q.timer = time.AfterFunc(time.Until(q.allowedAt), func() { n.flushAnnounceQueue(ifaceID) })
	} else {
		q.timer = nil
	}
	n.mu.Unlock()
	n.transmit(ifaceID, raw)
}

// Announce emits an announce for a local destination on every floodable
// interface (or only on ifaceID when set), with the given app data.
func (n *Node) Announce(d *Destination, ifaceID string, pathResponse bool) error {
	var appData []byte
	if d.AppData != nil {
		appData = d.AppData()
	}
	ann, err := reticulum.NewAnnounce(d.Identity, d.Name, appData)
	if err != nil {
		return err
	}
	if pathResponse {
		ann.Context = reticulum.ContextPathResponse
	}
	raw := ann.MarshalPacket()
	now := n.now()
	if ifaceID != "" {
		n.sendAnnounceOn(ifaceID, raw, now)
	} else {
		for _, id := range n.tx.Floodable() {
			n.sendAnnounceOn(id, raw, now)
		}
	}
	n.count(func(s *Stats) { s.AnnouncesTx++ })
	return nil
}
