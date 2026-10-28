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
	// In the control topology K is a transport: it carries S's announce
	// onto the mesh and M offers S back at four hops. While K's path through
	// H stands that offer loses; landing after SetLeaf has dropped it, it
	// wins. S's newer announce on the leaf displaces that echo since
	// MESHSAT-1748; waiting for it all the same keeps what K holds after
	// the mark the same on every run. A transport drops an announce it
	// holds for rebroadcast when a neighbour's copy comes back, so an empty
	// table at K means every echo of the first flood has landed
	// [MESHSAT-1747].
	waitFor(t, "the first flood's echoes reach K", func() bool {
		ln.k.mu.Lock()
		defer ln.k.mu.Unlock()
		return len(ln.k.announceTable) == 0
	})
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

// A neighbour heard on the mesh and, within the grace, anew on the leaf
// keeps its mesh path: a leaf path in its place would refuse the mesh's
// packets in transit to it. X is learned for real at one hop; the second
// announce is fed to K by hand, a wall-clock second later because the stamp
// is signed into it and the rule wants a strictly newer one. A path through
// another node (X behind M) is the displaced case in the table below.
func TestLeaf_AFreshNeighbourPathIsNotDisplacedByTheLeaf(t *testing.T) {
	ln := newLeafNet(t, true)
	ln.announceAll(t)
	x, _ := newTestNode(t, ln.net, "x")
	connect(ln.net, x, "x", "xk", ln.k, "k", "kx")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	x.Start(ctx)
	xDest := x.AddDestination(&Destination{Name: "test.delivery"})
	if err := x.Announce(xDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "K learns X as a neighbour", func() bool {
		e := ln.k.Paths().Get(xDest.Hash)
		return e != nil && e.Iface == "kx" && e.Direct()
	})
	time.Sleep(1100 * time.Millisecond) // a newer emission timestamp
	heardOnTheLeaf(t, ln, x.Identity())
	if got := ln.k.NextHopInterface(xDest.Hash); got != "kx" {
		t.Fatalf("K's path to X moved to %q", got)
	}
}

// stampBlob is a random blob whose emission stamp is ts, the trailing
// timestamp an announce carries (paths.go blobTimestamp).
func stampBlob(ts int64) []byte {
	b := make([]byte, 10)
	for i := 9; i >= 5; i-- {
		b[i] = byte(ts)
		ts >>= 8
	}
	return b
}

// heardOnTheLeaf feeds K an announce of id's test.delivery as H relays it:
// one hop at H, two at K, H the transport id. Inbound is synchronous, so
// what K holds afterwards is the decision, not a race with the wire.
func heardOnTheLeaf(t *testing.T, ln *leafNet, id *reticulum.Identity) {
	t.Helper()
	ann, err := reticulum.NewAnnounce(id, "test.delivery", nil)
	if err != nil {
		t.Fatal(err)
	}
	ln.k.Inbound(ann.MarshalPacketTransport(ln.h.IdentityHash(), 1, reticulum.ContextNone), "kh")
}

// askedByTheMesh feeds K a path request for dest from M (sendPathRequest's
// shape: destination, requestor, tag).
func askedByTheMesh(ln *leafNet, dest [HashLen]byte) {
	requestor, tag := ln.m.IdentityHash(), randomHash()
	data := make([]byte, 0, 3*HashLen)
	data = append(data, dest[:]...)
	data = append(data, requestor[:]...)
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
	ln.k.Inbound(h.Marshal(), "km")
}

// A destination announced anew on the leaf while a path to it stands on the
// mesh [MESHSAT-1748]. The mesh path is written in by hand with its stamp
// and its last use chosen; the announce is a real one from X with a stamp
// of now. Before this rule every row kept the mesh path for the path's
// whole lifetime: an old flood's echo, or a leak through a node that is not
// a leaf, held the leaf off for a week.
func TestLeaf_ANewerLeafAnnounceDisplacesAStalePathOnTheMesh(t *testing.T) {
	cases := []struct {
		name      string
		hops      int
		lastUse   time.Duration // how long ago the path was learned or used
		stamp     int64         // the stored announce's stamp, seconds from now
		displaced bool
	}{
		{"an old flood's echo at four hops, in use", 4, 0, -10, true},
		{"a neighbour used within the grace", 1, leafDisplaceGrace - time.Minute, -10, false},
		{"a neighbour idle past the grace", 1, leafDisplaceGrace + time.Minute, -10, true},
		{"a path whose own announce is the newer one", 1, leafDisplaceGrace + time.Minute, 60, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln := newLeafNet(t, true)
			xID, err := reticulum.GenerateIdentity()
			if err != nil {
				t.Fatal(err)
			}
			xDest := xID.DestHash("test.delivery")
			nextHop := ln.m.IdentityHash()
			if tc.hops == 1 {
				nextHop = xDest
			}
			now := time.Now()
			ln.k.Paths().Put(&PathEntry{DestHash: xDest, Timestamp: now.Add(-tc.lastUse), NextHop: nextHop, Hops: tc.hops,
				Expires: now.Add(time.Hour), RandomBlobs: [][]byte{stampBlob(now.Unix() + tc.stamp)}, Iface: "km"})
			heardOnTheLeaf(t, ln, xID)
			want := "km"
			if tc.displaced {
				want = "kh"
			}
			if got := ln.k.NextHopInterface(xDest); got != want {
				t.Fatalf("K's path to X is on %q, want %q", got, want)
			}
		})
	}
}

// The displacement changes what K holds, never what it tells the mesh. Every
// announce the node sends is a local destination's or comes out of its
// announce table (path responses included), so a table without X proves
// the announce heard on the leaf is never rebroadcast; and the mesh's path
// request for X is refused, not answered from the leaf.
func TestLeaf_ADisplacedPathIsNeverToldToTheMesh(t *testing.T) {
	ln := newLeafNet(t, true)
	xID, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	xDest := xID.DestHash("test.delivery")
	now := time.Now()
	ln.k.Paths().Put(&PathEntry{DestHash: xDest, Timestamp: now, NextHop: ln.m.IdentityHash(), Hops: 4,
		Expires: now.Add(time.Hour), RandomBlobs: [][]byte{stampBlob(now.Unix() - 10)}, Iface: "km"})
	heardOnTheLeaf(t, ln, xID)
	if got := ln.k.NextHopInterface(xDest); got != "kh" {
		t.Fatalf("setup: K's path to X is on %q, want the leaf", got)
	}
	queued := func() bool {
		ln.k.mu.Lock()
		defer ln.k.mu.Unlock()
		_, a := ln.k.announceTable[xDest]
		_, h := ln.k.heldAnnounces[xDest]
		return a || h
	}
	if queued() {
		t.Fatal("the announce heard on the leaf was queued for the mesh")
	}
	before := ln.k.Stats()
	askedByTheMesh(ln, xDest)
	after := ln.k.Stats()
	if after.PathRequestsRx != before.PathRequestsRx+1 {
		t.Fatal("setup: the mesh's path request was not taken")
	}
	if after.LeafRefused != before.LeafRefused+1 {
		t.Fatal("the mesh's path request for a destination on the leaf was not refused")
	}
	if queued() {
		t.Fatal("a path response from the leaf was queued for the mesh")
	}
}

// The mark and the path table change under one lock [MESHSAT-1748]. Before,
// SetLeaf let go of the node lock and then swept the table, so an announce
// handled in between met the new mark beside the old paths, or was learned
// on the interface and swept with them. With the table held, a SetLeaf that
// keeps the node lock while it waits leaves the mark unreadable until the
// paths are gone; one that let go shows the mark beside them.
func TestLeaf_TheMarkAndTheTableChangeTogether(t *testing.T) {
	net := &fakeNet{wires: map[string]*wire{}}
	k, _ := newTestNode(t, net, "k")
	id, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	dest := id.DestHash("test.delivery")
	now := time.Now()
	k.Paths().Put(&PathEntry{DestHash: dest, Timestamp: now, NextHop: dest, Hops: 1, Expires: now.Add(time.Hour), Iface: "kh"})
	k.paths.mu.Lock()
	done := make(chan struct{})
	go func() { k.SetLeaf("kh", true); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("SetLeaf never reached the path table")
		}
		if !k.mu.TryLock() {
			break // SetLeaf waits for the table with the node lock held
		}
		marked := k.leaf["kh"]
		k.mu.Unlock()
		if marked {
			t.Fatal("the mark was readable while the paths learned on the interface stood")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("SetLeaf returned while the table was held")
	default:
	}
	if _, still := k.paths.m[dest]; !still {
		t.Fatal("setup: the path left the held table")
	}
	k.paths.mu.Unlock()
	<-done
	if !k.IsLeaf("kh") || k.Paths().Get(dest) != nil {
		t.Fatal("SetLeaf did not leave the mark set and the table swept")
	}
}
