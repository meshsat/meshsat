package rns

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
)

// A single packet may carry as much plaintext as fits the MTU once
// encrypted, as upstream RNS allows: 399 bytes go (a 499-byte packet), 400
// do not. LXMF's single-packet room (295 content bytes, 391 of plaintext)
// sits inside it; the plaintext limit of 383 used to refuse it. [MESHSAT-1641]
func TestSingleDataPacketFillsTheMTU(t *testing.T) {
	net := &fakeNet{wires: map[string]*wire{}}
	a, _ := newTestNode(t, net, "a")
	b, _ := newTestNode(t, net, "b")
	connect(net, a, "a", "ab", b, "b", "ba")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	b.Start(ctx)
	var mu sync.Mutex
	var got [][]byte
	bDest := b.AddDestination(&Destination{Name: "test.delivery", OnPacket: func(p []byte, _ *Packet) {
		mu.Lock()
		got = append(got, append([]byte(nil), p...))
		mu.Unlock()
	}})
	if err := b.Announce(bDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A learns B", func() bool { return a.Paths().Hops(bDest.Hash) == 1 })
	for _, n := range []int{383, 391, 399} {
		body := bytes.Repeat([]byte{'p'}, n)
		if _, err := a.SendData(bDest.Hash, body, false); err != nil {
			t.Fatalf("%d bytes refused: %v", n, err)
		}
		waitFor(t, "B gets the packet", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(got) > 0 && bytes.Equal(got[len(got)-1], body)
		})
	}
	if _, err := a.SendData(bDest.Hash, bytes.Repeat([]byte{'p'}, 400), false); err == nil || !strings.Contains(err.Error(), "MTU") {
		t.Fatalf("400 bytes: %v, want an MTU refusal", err)
	}
}
