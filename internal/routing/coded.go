package routing

// Access-coded tcp_rns rows [MESHSAT-1722].
//
// A tcp_rns row with a network name and passphrase (IFAC) joins one
// workspace's private Reticulum network on the Hub (rns.meshsat.net:4242).
// What crosses it is the Reticulum node's own traffic, wrapped in that
// access code, and nothing else:
//
//   - the row is not in the InterfaceRegistry and has no packet sender in
//     the processor, so the legacy routing layer (transport node, path
//     finder, announce relay, time sync, custody, resources, HeMB) cannot
//     address it;
//   - its frames go to the node's Inbound and nowhere else; a frame the node
//     declines is dropped;
//   - the node holds the access code and marks the interface a leaf
//     (rns.Node.SetLeaf), so it never relays between the private network
//     and the bridge's other interfaces;
//   - Send refuses a frame that does not carry the IFAC flag.
//
// "Own traffic" includes LXMF messages to and from this bridge's own
// lxmf.delivery address. What the workspace's access rules do with them
// (lxmf_0 to a satellite bearer and back, MESHSAT-1554) is the one way across,
// and it is deliberate: the rules are the owner's, the messages are filed and
// counted like any bearer's, and the rules name the senders they take. The
// Reticulum layer itself never relays.
//
// The node is built after the interfaces start (main.go), so a row's frames
// are dropped until Attach hands the node over and the row is armed.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"meshsat/internal/reticulum"
)

// CodedNode is the part of rns.Node the coded rows use.
type CodedNode interface {
	SetIFAC(ifaceID string, f *reticulum.IFAC)
	SetLeaf(ifaceID string, leaf bool)
	Inbound(raw []byte, ifaceID string) bool
}

// ErrUncodedFrame is returned by CodedIfaces.Send for a frame without the
// IFAC flag: on a coded row that would be a frame the network on the other
// side reads as a stranger's, or traffic the node did not wrap.
var ErrUncodedFrame = errors.New("routing: frame without an access code refused on a coded interface")

type codedEntry struct {
	ifac   *reticulum.IFAC
	send   func(ctx context.Context, packet []byte) error
	online func() bool
	mtu    int
	armed  bool // the node holds the access code and the leaf mark
}

// CodedIfaces holds the running access-coded rows for the node's
// transmitter (rnsstack) and their inbound path.
type CodedIfaces struct {
	opMu sync.Mutex // serialises add, remove and Attach, which call the node

	mu      sync.Mutex
	node    CodedNode
	onUp    func(ifaceID string)
	entries map[string]*codedEntry

	dropped atomic.Uint64
}

// NewCodedIfaces creates the empty set.
func NewCodedIfaces() *CodedIfaces {
	return &CodedIfaces{entries: make(map[string]*codedEntry)}
}

// Attach hands the node over and arms every row already running. onUp is
// called when a row connects (and here, for one already connected): the
// stack announces its own destinations on that row, which also tells the
// Hub which network the connection belongs to.
func (c *CodedIfaces) Attach(n CodedNode, onUp func(ifaceID string)) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	c.node, c.onUp = n, onUp
	entries := make(map[string]*codedEntry, len(c.entries))
	for id, e := range c.entries {
		entries[id] = e
	}
	c.mu.Unlock()
	for id, e := range entries {
		c.arm(n, id, e)
		if e.online() && onUp != nil {
			go onUp(id)
		}
	}
}

// arm gives the node the row's code and leaf mark, then lets frames through.
// The caller holds opMu.
func (c *CodedIfaces) arm(n CodedNode, id string, e *codedEntry) {
	n.SetLeaf(id, true)
	n.SetIFAC(id, e.ifac)
	c.mu.Lock()
	if c.entries[id] == e {
		e.armed = true
	}
	c.mu.Unlock()
}

// add registers a row before its connection starts.
func (c *CodedIfaces) add(id string, e *codedEntry) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	c.entries[id] = e
	n := c.node
	c.mu.Unlock()
	if n != nil {
		c.arm(n, id, e)
	}
}

// remove forgets a row after its connection stopped: the node drops the code,
// the leaf mark and everything it learned on the row.
func (c *CodedIfaces) remove(id string) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	_, ok := c.entries[id]
	delete(c.entries, id)
	n := c.node
	c.mu.Unlock()
	if ok && n != nil {
		n.SetIFAC(id, nil)
		n.SetLeaf(id, false)
	}
}

// up is the row's connect hook.
func (c *CodedIfaces) up(id string) {
	c.mu.Lock()
	e := c.entries[id]
	fn := c.onUp
	ok := e != nil && e.armed
	c.mu.Unlock()
	if ok && fn != nil {
		fn(id)
	}
}

// deliver hands one frame received by entry e to the node and nothing else.
// A frame from a connection that stopped while a restarted row took its id
// is not the new row's.
func (c *CodedIfaces) deliver(id string, e *codedEntry, raw []byte) {
	c.mu.Lock()
	n := c.node
	ok := c.entries[id] == e && e.armed && n != nil
	c.mu.Unlock()
	if !ok || !n.Inbound(append([]byte(nil), raw...), id) {
		c.dropped.Add(1)
	}
}

// Has reports whether an interface id is a coded row.
func (c *CodedIfaces) Has(id string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[id]
	return ok
}

// Send puts one frame the node wrapped on a coded row.
func (c *CodedIfaces) Send(id string, raw []byte) error {
	c.mu.Lock()
	e := c.entries[id]
	ok := e != nil && e.armed
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown interface: %s", id)
	}
	if !reticulum.HasIFACFlag(raw) {
		c.dropped.Add(1)
		return ErrUncodedFrame
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return e.send(ctx, raw)
}

// Floodable lists the armed rows that are connected.
func (c *CodedIfaces) Floodable() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	type cand struct {
		id     string
		online func() bool
	}
	var cands []cand
	for id, e := range c.entries {
		if e.armed {
			cands = append(cands, cand{id, e.online})
		}
	}
	c.mu.Unlock()
	var out []string
	for _, x := range cands {
		if x.online() {
			out = append(out, x.id)
		}
	}
	sort.Strings(out)
	return out
}

// MTU returns a coded row's hardware MTU, or 0.
func (c *CodedIfaces) MTU(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[id]; e != nil {
		return e.mtu
	}
	return 0
}

// Armed reports whether the node holds a coded row's code and leaf mark.
func (c *CodedIfaces) Armed(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[id]
	return e != nil && e.armed
}

// Online reports whether a coded row is armed and connected.
func (c *CodedIfaces) Online(id string) bool {
	c.mu.Lock()
	e := c.entries[id]
	c.mu.Unlock()
	return e != nil && e.armed && e.online()
}

// Dropped counts frames refused or declined on coded rows.
func (c *CodedIfaces) Dropped() uint64 { return c.dropped.Load() }
