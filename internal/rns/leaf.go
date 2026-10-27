package rns

// A leaf interface carries only this node's own traffic [MESHSAT-1722].
//
// A kit joins its workspace's private Reticulum network on the Hub with an
// access code (IFAC). As a transport node on that link it would join the
// private network to everything else it is on: its LoRa mesh, the Hub's
// public node over MQTT, every other TCP peer. On a leaf the node is a
// member of the network on the other side and nothing more:
//
//   - an announce heard on a leaf updates the path table and is never
//     rebroadcast; an announce heard elsewhere is never rebroadcast onto a
//     leaf;
//   - a path request from a leaf is answered for a local destination only,
//     never from the path table and never forwarded;
//   - a path request from elsewhere is never answered with a path learned on
//     a leaf, and never forwarded onto one;
//   - no packet in transport is forwarded into or out of a leaf, so no link
//     or reverse table entry ever has one at either end.
//
// The node's own announces, path requests, packets and links use a leaf like
// any other interface. A node with no leaf behaves exactly as before.

// SetLeaf marks an interface as a leaf, or clears the mark. Either change
// forgets what the node holds about the interface (paths, transit links,
// reverse entries, queued announces), so a connection that reuses an
// interface id never inherits the previous connection's state across the
// boundary. Set the mark before the interface carries its first frame and
// clear it after the last: a frame in flight while it changes is handled
// under whichever mark it meets.
func (n *Node) SetLeaf(ifaceID string, leaf bool) {
	n.mu.Lock()
	if n.leaf[ifaceID] == leaf {
		n.mu.Unlock()
		return
	}
	if leaf {
		n.leaf[ifaceID] = true
	} else {
		delete(n.leaf, ifaceID)
	}
	for k, e := range n.linkTable {
		if e.receivedIface == ifaceID || e.nextHopIface == ifaceID {
			delete(n.linkTable, k)
		}
	}
	for k, e := range n.reverseTable {
		if e.receivedIface == ifaceID || e.outboundIface == ifaceID {
			delete(n.reverseTable, k)
		}
	}
	for k, e := range n.announceTable {
		if e.sourceIface == ifaceID || e.attachedIface == ifaceID {
			delete(n.announceTable, k)
		}
	}
	for k, e := range n.heldAnnounces {
		if e.sourceIface == ifaceID || e.attachedIface == ifaceID {
			delete(n.heldAnnounces, k)
		}
	}
	for k, d := range n.discoveryPRs {
		if contains(d.requestingIfaces, ifaceID) {
			delete(n.discoveryPRs, k)
		}
	}
	n.mu.Unlock()
	n.paths.RemoveIface(ifaceID)
}

// IsLeaf reports whether an interface is a leaf.
func (n *Node) IsLeaf(ifaceID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.leaf[ifaceID]
}

// crossesLeaf reports whether relaying from one interface to another would
// carry traffic into or out of a leaf. It counts the refusal.
func (n *Node) crossesLeaf(in, out string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.leaf[in] || n.leaf[out] {
		n.stats.LeafRefused++
		return true
	}
	return false
}

// withoutLeaves drops the leaf interfaces from a list of interfaces to relay
// on.
func (n *Node) withoutLeaves(ifaces []string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.leaf) == 0 {
		return ifaces
	}
	out := make([]string, 0, len(ifaces))
	for _, id := range ifaces {
		if !n.leaf[id] {
			out = append(out, id)
		}
	}
	return out
}
