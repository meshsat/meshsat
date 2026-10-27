package rns

import (
	"context"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

// leafNet is the shape a kit joins its workspace's private network in
// [MESHSAT-1722]: S - H = K - M. H is the Hub's private node (a transport),
// S a member's phone on it, K the kit with its coded link "kh" as a leaf,
// and M a node on the kit's LoRa mesh. K forwards HEADER_1 from the mesh,
// as the Bridge does for Meshtastic.
type leafNet struct {
	s, h, k, m                 *Node
	sDest, hDest, kDest, mDest *Destination
	sGot, kGot, mGot           *receiver
	net                        *fakeNet
}

type receiver struct {
	mu  sync.Mutex
	got []string
}

func (r *receiver) take(plain []byte, _ *Packet) {
	r.mu.Lock()
	r.got = append(r.got, string(plain))
	r.mu.Unlock()
}

func (r *receiver) has(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range r.got {
		if g == s {
			return true
		}
	}
	return false
}

func newLeafNet(t *testing.T, leaf bool) *leafNet {
	t.Helper()
	net := &fakeNet{wires: map[string]*wire{}}
	ln := &leafNet{sGot: &receiver{}, kGot: &receiver{}, mGot: &receiver{}, net: net}
	ln.s, _ = newTestNode(t, net, "s")
	ln.h, _ = newTestNode(t, net, "h")
	ln.m, _ = newTestNode(t, net, "m")
	id, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ln.k, err = New(Config{Identity: id, ForwardHeader1From: map[string]bool{"km": true}}, &fakeTx{net: net, name: "k", sent: map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	if leaf {
		ln.k.SetLeaf("kh", true)
	}
	connect(net, ln.s, "s", "sh", ln.h, "h", "hs")
	connect(net, ln.h, "h", "hk", ln.k, "k", "kh")
	connect(net, ln.k, "k", "km", ln.m, "m", "mk")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, n := range []*Node{ln.s, ln.h, ln.k, ln.m} {
		n.Start(ctx)
	}
	ln.sDest = ln.s.AddDestination(&Destination{Name: "test.delivery", AcceptLinks: true, ProveAll: true, OnPacket: ln.sGot.take})
	ln.hDest = ln.h.AddDestination(&Destination{Name: "test.delivery", AcceptLinks: true, ProveAll: true})
	ln.kDest = ln.k.AddDestination(&Destination{Name: "test.delivery", AcceptLinks: true, ProveAll: true, OnPacket: ln.kGot.take})
	ln.mDest = ln.m.AddDestination(&Destination{Name: "test.delivery", AcceptLinks: true, ProveAll: true, OnPacket: ln.mGot.take})
	return ln
}

// announceAll has every node announce and waits for K to learn S and M.
func (ln *leafNet) announceAll(t *testing.T) {
	t.Helper()
	for _, p := range []struct {
		n *Node
		d *Destination
	}{{ln.s, ln.sDest}, {ln.h, ln.hDest}, {ln.k, ln.kDest}, {ln.m, ln.mDest}} {
		if err := p.n.Announce(p.d, "", false); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "K learns S through H", func() bool { return ln.k.Paths().Hops(ln.sDest.Hash) == 2 })
	waitFor(t, "K learns M", func() bool { return ln.k.Paths().Hops(ln.mDest.Hash) == 1 })
	waitFor(t, "S learns K through H", func() bool { return ln.s.Paths().Hops(ln.kDest.Hash) == 2 })
	waitFor(t, "M learns K", func() bool { return ln.m.Paths().Hops(ln.kDest.Hash) == 1 })
}

// settle waits past K's rebroadcast window (PATHFINDER_RW plus a tick).
func settle() { time.Sleep(1500 * time.Millisecond) }

// The control: without the leaf mark K is an ordinary transport and joins
// the two sides, which is exactly what a coded link must not do.
func TestLeaf_WithoutTheMarkTheKitJoinsBothSides(t *testing.T) {
	ln := newLeafNet(t, false)
	ln.announceAll(t)
	waitFor(t, "M learns S through K", func() bool { return ln.m.Paths().Has(ln.sDest.Hash) })
	waitFor(t, "S learns M through K", func() bool { return ln.s.Paths().Has(ln.mDest.Hash) })
}

func TestLeaf_AnnouncesNeverCross(t *testing.T) {
	ln := newLeafNet(t, true)
	ln.announceAll(t)
	settle()
	if ln.m.Paths().Has(ln.sDest.Hash) || ln.m.Paths().Has(ln.hDest.Hash) {
		t.Fatal("an announce heard on the leaf was rebroadcast to the mesh")
	}
	if ln.s.Paths().Has(ln.mDest.Hash) || ln.h.Paths().Has(ln.mDest.Hash) {
		t.Fatal("an announce heard on the mesh was rebroadcast onto the leaf")
	}
	ln.k.mu.Lock()
	_, held := ln.k.announceTable[ln.sDest.Hash]
	ln.k.mu.Unlock()
	if held {
		t.Fatal("an announce from the leaf was queued for rebroadcast")
	}
}

func TestLeaf_PathRequestsNeverCross(t *testing.T) {
	ln := newLeafNet(t, true)
	ln.announceAll(t)
	ctx := context.Background()

	// The mesh asks for a member of the private network K knows: no answer.
	if ln.m.RequestPath(ctx, ln.sDest.Hash, 1500*time.Millisecond) {
		t.Fatal("the mesh got a path to a member of the private network")
	}
	// The private network asks for a mesh node K knows: no answer.
	if ln.s.RequestPath(ctx, ln.mDest.Hash, 1500*time.Millisecond) {
		t.Fatal("the private network got a path to a node on the mesh")
	}
	// Nor is an unknown destination searched for across the leaf.
	before := ln.m.Stats().PathRequestsRx
	var unknown [HashLen]byte
	unknown[0] = 0x42
	ln.s.RequestPath(ctx, unknown, 1000*time.Millisecond)
	if got := ln.m.Stats().PathRequestsRx; got != before {
		t.Fatalf("a path request from the private network reached the mesh (%d -> %d)", before, got)
	}
	before = ln.s.Stats().PathRequestsRx
	unknown[0] = 0x43
	ln.m.RequestPath(ctx, unknown, 1000*time.Millisecond)
	if got := ln.s.Stats().PathRequestsRx; got > before {
		t.Fatalf("a path request from the mesh reached the private network (%d -> %d)", before, got)
	}
	if ln.k.Stats().LeafRefused == 0 {
		t.Fatal("refusals were not counted")
	}
}

func TestLeaf_TheKitsOwnDestinationIsAnsweredOnTheLeaf(t *testing.T) {
	ln := newLeafNet(t, true)
	ln.announceAll(t)
	ln.s.Paths().Remove(ln.kDest.Hash)
	ln.h.Paths().Remove(ln.kDest.Hash)
	if !ln.s.RequestPath(context.Background(), ln.kDest.Hash, 3*time.Second) {
		t.Fatal("a path request on the leaf for the kit's own destination was not answered")
	}
}

func TestLeaf_NoPacketInTransportCrosses(t *testing.T) {
	ln := newLeafNet(t, true)
	ln.announceAll(t)

	// The mesh holds a path to S through K (as if it had leaked) and sends:
	// HEADER_2 with K as the transport. K must not carry it onto the leaf.
	ln.m.Paths().Put(&PathEntry{DestHash: ln.sDest.Hash, NextHop: ln.k.IdentityHash(), Hops: 3,
		Iface: "mk", Timestamp: time.Now(), Expires: time.Now().Add(time.Hour)})
	if err := ln.m.Remember(ln.sDest.Hash, ln.s.Identity().PublicBytes(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ln.m.SendData(ln.sDest.Hash, []byte("mesh to private, header 2"), false); err != nil {
		t.Fatal(err)
	}
	// A link request from the mesh the same way.
	if _, err := ln.m.Links().Initiate(ln.sDest.Hash); err != nil {
		t.Fatal(err)
	}
	// HEADER_1 from the mesh, which K forwards for Meshtastic peers.
	ln.m.Paths().Remove(ln.sDest.Hash)
	if _, err := ln.m.SendData(ln.sDest.Hash, []byte("mesh to private, header 1"), false); err != nil {
		t.Fatal(err)
	}
	// And the other way: the private network holds a path to M through K.
	ln.h.Paths().Put(&PathEntry{DestHash: ln.mDest.Hash, NextHop: ln.k.IdentityHash(), Hops: 2,
		Iface: "hk", Timestamp: time.Now(), Expires: time.Now().Add(time.Hour)})
	if err := ln.h.Remember(ln.mDest.Hash, ln.m.Identity().PublicBytes(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ln.h.SendData(ln.mDest.Hash, []byte("private to mesh"), false); err != nil {
		t.Fatal(err)
	}
	settle()
	if ln.sGot.has("mesh to private, header 2") || ln.sGot.has("mesh to private, header 1") {
		t.Fatal("a packet from the mesh crossed the leaf")
	}
	if len(ln.s.Links().All()) != 0 {
		t.Fatal("a link from the mesh crossed the leaf")
	}
	if ln.mGot.has("private to mesh") {
		t.Fatal("a packet from the private network crossed the leaf")
	}
	if st := ln.k.Stats(); st.Forwarded != 0 || st.LeafRefused < 4 {
		t.Fatalf("forwarded %d, refused %d; want 0 and at least 4", st.Forwarded, st.LeafRefused)
	}
}

// The kit's own traffic uses the leaf like any interface: a packet with its
// proof, and a link, both ways.
func TestLeaf_TheKitsOwnTrafficFlows(t *testing.T) {
	ln := newLeafNet(t, true)
	ln.announceAll(t)
	r, err := ln.k.SendData(ln.sDest.Hash, []byte("kit to member"), true)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "S receives from K", func() bool { return ln.sGot.has("kit to member") })
	select {
	case res := <-r.Done():
		if !res.Delivered {
			t.Fatal("the kit's packet was not proved")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no proof for the kit's packet")
	}
	if _, err := ln.s.SendData(ln.kDest.Hash, []byte("member to kit"), false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "K receives from S", func() bool { return ln.kGot.has("member to kit") })
	if _, err := ln.k.SendData(ln.mDest.Hash, []byte("kit to mesh"), false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "M receives from K", func() bool { return ln.mGot.has("kit to mesh") })

	established := make(chan struct{}, 1)
	l, err := ln.k.Links().Initiate(ln.sDest.Hash)
	if err != nil {
		t.Fatal(err)
	}
	l.OnEstablished = func(*Link) { established <- struct{}{} }
	select {
	case <-established:
	case <-time.After(5 * time.Second):
		t.Fatalf("the kit's link to a member did not come up, state %s", l.State())
	}
}

// SetLeaf forgets what the node holds about the interface, so a connection
// that reuses an id never inherits the previous one's paths.
func TestLeaf_ChangingTheMarkForgetsTheInterface(t *testing.T) {
	ln := newLeafNet(t, false)
	ln.announceAll(t)
	if !ln.k.Paths().Has(ln.sDest.Hash) {
		t.Fatal("setup: K has no path to S")
	}
	ln.k.SetLeaf("kh", true)
	if onKH(ln.k, ln.sDest) || onKH(ln.k, ln.hDest) {
		t.Fatal("marking a leaf kept the paths learned on it")
	}
	if !ln.k.Paths().Has(ln.mDest.Hash) {
		t.Fatal("marking a leaf dropped a path on another interface")
	}
	if !ln.k.IsLeaf("kh") {
		t.Fatal("IsLeaf after SetLeaf(true)")
	}
	// An announce timestamp has one-second resolution, and H takes a second
	// announce from S only when it is newer.
	time.Sleep(1100 * time.Millisecond)
	if err := ln.s.Announce(ln.sDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "K relearns S on the leaf", func() bool { return onKH(ln.k, ln.sDest) })
	// The Bridge clears the mark after the connection stopped: take the
	// wire down first so no retransmission from H lands in between.
	ln.net.mu.Lock()
	delete(ln.net.wires, "h/hk")
	delete(ln.net.wires, "k/kh")
	ln.net.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	ln.k.SetLeaf("kh", false)
	if onKH(ln.k, ln.sDest) {
		t.Fatal("clearing a leaf kept the paths learned while it was one")
	}
}

// onKH: the node's path to d is the one through its link to H. (In the
// control topology M also offers S, rebroadcast back from the mesh.)
func onKH(n *Node, d *Destination) bool {
	e := n.Paths().Get(d.Hash)
	return e != nil && e.Iface == "kh"
}

// A destination heard on the mesh and later on the leaf keeps its mesh path:
// a leaf path in its place would refuse the mesh's packets in transit to it.
func TestLeaf_ALeafPathNeverDisplacesALivePathElsewhere(t *testing.T) {
	ln := newLeafNet(t, true)
	ln.announceAll(t)
	x, _ := newTestNode(t, ln.net, "x")
	connect(ln.net, x, "x", "xm", ln.m, "m", "mx")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	x.Start(ctx)
	xDest := x.AddDestination(&Destination{Name: "test.delivery"})
	if err := x.Announce(xDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "K learns X through the mesh", func() bool { return ln.k.NextHopInterface(xDest.Hash) == "km" })
	// X leaves the mesh and joins the private network, then announces anew.
	ln.net.mu.Lock()
	delete(ln.net.wires, "x/xm")
	delete(ln.net.wires, "m/mx")
	ln.net.mu.Unlock()
	connect(ln.net, x, "x", "xh", ln.h, "h", "hx")
	time.Sleep(1100 * time.Millisecond) // a newer emission timestamp
	if err := x.Announce(xDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "H learns X", func() bool { return ln.h.Paths().Has(xDest.Hash) })
	settle()
	if got := ln.k.NextHopInterface(xDest.Hash); got != "km" {
		t.Fatalf("K's path to X moved to %q", got)
	}
}
