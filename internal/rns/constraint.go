package rns

import "errors"

// ErrPaidRefused is returned by a send under Constraint.FreeOnly whose
// packet would leave on an interface that is not free. Nothing was sent.
var ErrPaidRefused = errors.New("rns: this send may use free interfaces only, and its path leaves on a paid one")

// Constraint limits the interfaces one send may leave on, at every
// outbound step it takes: path request, link request, the packet itself,
// link packets and resource parts. The paid policies (SetPaidPolicy) still
// apply to all other traffic.
//
// A delivery intent's attempt is free-only until a paid route has a price
// (MeshSat architecture r3 D8, B9): the free check made before the send
// cannot hold by itself, because the router may learn a path, or a
// different one, while it sends. [MESHSAT-1584]
type Constraint struct {
	// FreeOnly: only the interfaces the transport floods on, which are
	// the ones without a cost, never a paid one.
	FreeOnly bool
}

// FreeInterface reports whether an interface is one the transport floods
// on: online and without a cost.
func (n *Node) FreeInterface(id string) bool {
	if id == "" {
		return false
	}
	for _, f := range n.tx.Floodable() {
		if f == id {
			return true
		}
	}
	return false
}

func (c Constraint) allows(n *Node, iface string) bool {
	return !c.FreeOnly || n.FreeInterface(iface)
}
