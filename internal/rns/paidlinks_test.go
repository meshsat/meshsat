package rns

import (
	"context"
	"sync"
	"testing"
	"time"
)

// satNet builds A - T1 =sat= T2 - B: T1 and T2 are bridges joined by a paid
// interface "sat" that Floodable never lists, A and B sit on free links.
func satNet(t *testing.T) (a, t1, t2, b *Node, tx1, tx2 *fakeTx, cancel func()) {
	t.Helper()
	net := &fakeNet{wires: map[string]*wire{}}
	a, _ = newTestNode(t, net, "a")
	t1, tx1 = newTestNode(t, net, "t1")
	t2, tx2 = newTestNode(t, net, "t2")
	b, _ = newTestNode(t, net, "b")
	tx1.paid = map[string]bool{"sat": true}
	tx2.paid = map[string]bool{"sat": true}
	connect(net, a, "a", "at1", t1, "t1", "t1a")
	connect(net, t1, "t1", "sat", t2, "t2", "sat")
	connect(net, t2, "t2", "t2b", b, "b", "bt2")
	ctx, c := context.WithCancel(context.Background())
	for _, n := range []*Node{a, t1, t2, b} {
		n.Start(ctx)
	}
	return a, t1, t2, b, tx1, tx2, c
}

func satSent(tx *fakeTx) int {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.sent["sat"]
}

func TestPaidLinkClosedWithoutPolicy(t *testing.T) {
	a, _, t2, b, _, tx2, cancel := satNet(t)
	defer cancel()
	bDest := b.AddDestination(&Destination{Name: "test.delivery"})
	if err := b.Announce(bDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "T2 learns B", func() bool { return t2.Paths().Hops(bDest.Hash) == 1 })
	time.Sleep(1500 * time.Millisecond) // past every rebroadcast
	if n := satSent(tx2); n != 0 {
		t.Fatalf("an announce crossed the paid link without a policy (%d packets)", n)
	}
	if a.Paths().Has(bDest.Hash) {
		t.Fatalf("A learned B across a closed paid link")
	}
}

func TestPaidLinkCarriesListedDestinationsOnly(t *testing.T) {
	a, t1, t2, b, _, tx2, cancel := satNet(t)
	defer cancel()
	var got []byte
	done := make(chan struct{}, 1)
	bDest := b.AddDestination(&Destination{Name: "test.delivery", ProveAll: true,
		OnPacket: func(plain []byte, pkt *Packet) { got = plain; done <- struct{}{} }})
	cDest := b.AddDestination(&Destination{Name: "test.other"})
	for _, n := range []*Node{t1, t2} {
		n.SetPaidPolicy("sat", &PaidPolicy{Dests: [][HashLen]byte{bDest.Hash}, MinInterval: time.Hour})
	}

	if err := b.Announce(cDest, "", false); err != nil {
		t.Fatal(err)
	}
	if err := b.Announce(bDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A learns B across the paid link", func() bool { return a.Paths().Hops(bDest.Hash) == 3 })
	waitFor(t, "T2 learns C", func() bool { return t2.Paths().Has(cDest.Hash) })
	time.Sleep(1500 * time.Millisecond)
	if t1.Paths().Has(cDest.Hash) {
		t.Fatalf("an unlisted destination crossed the paid link")
	}
	if n := satSent(tx2); n != 1 {
		t.Fatalf("want exactly one announce on the paid link (rebroadcast retries held by the interval), got %d", n)
	}

	// A second announce inside the interval stays home.
	if err := b.Announce(bDest, "", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if n := satSent(tx2); n != 1 {
		t.Fatalf("a repeat announce crossed inside the interval (%d packets)", n)
	}
	if t2.Stats().PaidSuppressed == 0 {
		t.Fatalf("PaidSuppressed did not count the held announce")
	}

	// Data along the learned path crosses, and the proof comes back.
	r, err := a.SendData(bDest.Hash, []byte("over the satellite"), true)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("B did not receive")
	}
	if string(got) != "over the satellite" {
		t.Fatalf("got %q", got)
	}
	select {
	case res := <-r.Done():
		if !res.Delivered {
			t.Fatalf("receipt not delivered")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no proof back across the paid link")
	}
}

func TestPaidLinkPathRequestFindsListedDestination(t *testing.T) {
	a, t1, t2, b, _, _, cancel := satNet(t)
	defer cancel()
	dDest := b.AddDestination(&Destination{Name: "test.delivery"})
	// D announces while the link is closed: T2 knows it, A does not.
	if err := b.Announce(dDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "T2 learns D", func() bool { return t2.Paths().Has(dDest.Hash) })
	time.Sleep(1500 * time.Millisecond)
	if a.Paths().Has(dDest.Hash) {
		t.Fatalf("A learned D across a closed link")
	}
	for _, n := range []*Node{t1, t2} {
		n.SetPaidPolicy("sat", &PaidPolicy{Dests: [][HashLen]byte{dDest.Hash}})
	}
	ctx, c := context.WithTimeout(context.Background(), 6*time.Second)
	defer c()
	if !a.RequestPath(ctx, dDest.Hash, 6*time.Second) {
		t.Fatalf("path request for a listed destination did not resolve across the paid link")
	}
	if h := a.Paths().Hops(dDest.Hash); h != 3 {
		t.Fatalf("A should see D at 3 hops, got %d", h)
	}
}

func TestPaidLinkUnlistedPathRequestStaysHome(t *testing.T) {
	a, t1, t2, b, tx1, _, cancel := satNet(t)
	defer cancel()
	eDest := b.AddDestination(&Destination{Name: "test.delivery"})
	var other [HashLen]byte
	other[0] = 0x42
	for _, n := range []*Node{t1, t2} {
		n.SetPaidPolicy("sat", &PaidPolicy{Dests: [][HashLen]byte{other}})
	}
	ctx, c := context.WithTimeout(context.Background(), 2*time.Second)
	defer c()
	if a.RequestPath(ctx, eDest.Hash, 2*time.Second) {
		t.Fatalf("an unlisted destination resolved across the paid link")
	}
	if n := satSent(tx1); n != 0 {
		t.Fatalf("a path request for an unlisted destination crossed (%d packets)", n)
	}
}

func TestParsePaidPolicy(t *testing.T) {
	p, err := ParsePaidPolicy(PaidPolicyJSON{Dests: []string{
		"00112233445566778899aabbccddeeff", "00112233445566778899aabbccddeeff"}, MinIntervalSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Dests) != 1 || p.MinInterval != time.Minute {
		t.Fatalf("parsed %+v", p)
	}
	if _, err := ParsePaidPolicy(PaidPolicyJSON{Dests: []string{"abc"}}); err == nil {
		t.Fatalf("short hash accepted")
	}
	if _, err := ParsePaidPolicy(PaidPolicyJSON{MinIntervalSec: -1}); err == nil {
		t.Fatalf("negative interval accepted")
	}

	net := &fakeNet{wires: map[string]*wire{}}
	n, _ := newTestNode(t, net, "n")
	n.SetPaidPolicy("iridium_imt_0", p)
	raw, err := n.MarshalPaidPolicies()
	if err != nil {
		t.Fatal(err)
	}
	m, _ := newTestNode(t, net, "m")
	if c, err := m.LoadPaidPolicies(raw); err != nil || c != 1 {
		t.Fatalf("load: %d %v", c, err)
	}
	if got := m.PaidPolicies()["iridium_imt_0"]; len(got.Dests) != 1 || got.Dests[0] != p.Dests[0] {
		t.Fatalf("round trip lost the policy: %+v", got)
	}
	m.SetPaidPolicy("iridium_imt_0", nil)
	if len(m.PaidPolicies()) != 0 {
		t.Fatalf("nil policy did not remove it")
	}
}

// ExtraHops applies to every packet that arrives on the paid link, so the
// path, a link request and its proof all agree on the count: a link still
// establishes across it, and the far side sees the slow hop as several.
func TestPaidLinkExtraHopsKeepLinksWorking(t *testing.T) {
	a, t1, t2, b, _, _, cancel := satNet(t)
	defer cancel()
	got := make(chan string, 4)
	bDest := b.AddDestination(&Destination{Name: "test.delivery", AcceptLinks: true, ProveAll: true,
		OnPacket:     func(plain []byte, pkt *Packet) { got <- string(plain) },
		OnLinkPacket: func(l *Link, plain []byte, pkt *Packet) { got <- string(plain) }})
	for _, n := range []*Node{t1, t2} {
		n.SetPaidPolicy("sat", &PaidPolicy{Dests: [][HashLen]byte{bDest.Hash}, ExtraHops: 8})
	}
	if err := b.Announce(bDest, "", false); err != nil {
		t.Fatal(err)
	}
	// B -> T2 (1) -> sat (2 + 8) -> A (11).
	waitFor(t, "A learns B with the satellite hop counted as nine", func() bool { return a.Paths().Hops(bDest.Hash) == 11 })

	r, err := a.SendData(bDest.Hash, []byte("single packet"), true)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-r.Done():
		if !res.Delivered {
			t.Fatalf("proof not delivered across the inflated hop")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no proof back")
	}

	established := make(chan *Link, 1)
	l, err := a.Links().Initiate(bDest.Hash)
	if err != nil {
		t.Fatal(err)
	}
	l.OnEstablished = func(l *Link) { established <- l }
	select {
	case <-established:
	case <-time.After(5 * time.Second):
		t.Fatalf("link not established across the inflated hop, state %s", l.State())
	}
	waitFor(t, "responder active", func() bool {
		for _, x := range b.Links().All() {
			if x.ID == l.ID && x.State() == LinkActive {
				return true
			}
		}
		return false
	})
	if _, err := a.Links().Send(l, []byte("over the link")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-got:
			if s == "over the link" {
				if _, err := ParsePaidPolicy(PaidPolicyJSON{ExtraHops: MaxPaidExtraHops + 1}); err == nil {
					t.Fatalf("extra_hops above the ceiling accepted")
				}
				return
			}
		case <-deadline:
			t.Fatalf("link data did not cross")
		}
	}
}

// A data packet crossing an opened paid link is reported once per minute
// per interface, destination and direction; announces and proofs are not.
func TestPaidCrossingReportsMessagesOnly(t *testing.T) {
	a, t1, t2, b, _, _, cancel := satNet(t)
	defer cancel()
	bDest := b.AddDestination(&Destination{Name: "test.delivery", ProveAll: true})
	var mu sync.Mutex
	var seen []PaidCrossing
	record := func(c PaidCrossing) { mu.Lock(); seen = append(seen, c); mu.Unlock() }
	t1.OnPaidCrossing, t2.OnPaidCrossing = record, record
	for _, n := range []*Node{t1, t2} {
		n.SetPaidPolicy("sat", &PaidPolicy{Dests: [][HashLen]byte{bDest.Hash}})
	}
	if err := b.Announce(bDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A learns B", func() bool { return a.Paths().Has(bDest.Hash) })
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	if len(seen) != 0 {
		t.Fatalf("an announce was reported as a crossing: %+v", seen)
	}
	mu.Unlock()
	for i := 0; i < 2; i++ {
		r, err := a.SendData(bDest.Hash, []byte("hello"), true)
		if err != nil {
			t.Fatal(err)
		}
		<-r.Done()
	}
	waitFor(t, "both crossings", func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) >= 2 })
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("want one outbound (T1) and one inbound (T2) crossing for two messages in a minute, got %+v", seen)
	}
	var in, out int
	for _, c := range seen {
		if c.Dest != bDest.Hash || c.Iface != "sat" {
			t.Fatalf("crossing %+v", c)
		}
		if c.Inbound {
			in++
		} else {
			out++
		}
	}
	if in != 1 || out != 1 {
		t.Fatalf("directions: in %d out %d", in, out)
	}
}

func TestCrossesPaidLink(t *testing.T) {
	a, t1, t2, b, _, _, cancel := satNet(t)
	defer cancel()
	bDest := b.AddDestination(&Destination{Name: "test.delivery"})
	for _, n := range []*Node{t1, t2} {
		n.SetPaidPolicy("sat", &PaidPolicy{Dests: [][HashLen]byte{bDest.Hash}})
	}
	if err := b.Announce(bDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A learns B", func() bool { return a.Paths().Has(bDest.Hash) })
	if !t1.CrossesPaidLink(bDest.Hash) {
		t.Fatalf("T1 reaches B over the paid link but does not say so")
	}
	if t2.CrossesPaidLink(bDest.Hash) || a.CrossesPaidLink(bDest.Hash) {
		t.Fatalf("a node whose next hop is a free link claims a paid crossing")
	}
	var unknown [HashLen]byte
	if t1.CrossesPaidLink(unknown) {
		t.Fatalf("an unknown destination crosses nothing")
	}
}
