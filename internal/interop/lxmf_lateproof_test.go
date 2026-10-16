package interop

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"testing"
	"time"

	"meshsat/internal/lxmf"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
)

// A-13 against upstream: the bridge stops waiting the moment its LXMF
// packet is out, so the stock Python receiver's proof (RNS 1.5.4, LXMF
// 1.1.0) arrives after the wait. It reaches Node.OnUnmatchedProof and
// validates against the hash and key Prepare reported, which is all the
// ledger needs to record it. [MESHSAT-1584]
func TestInteropLXMFLateProofReachesTheHook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, r, _ := newLXMFBridge(t, ctx, 0)
	p := startPeer(t, peerOpts{name: "A", connect: fmt.Sprintf("127.0.0.1:%d", b.port)})
	waitFor(t, "tcp peer", 10*time.Second, func() bool { return b.tcp.PeerCount() > 0 })
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge learns python delivery dest", 10*time.Second, func() bool { return b.node.Paths().Hops(h16(p.Delivery)) == 1 })

	var mu sync.Mutex
	var unmatched []*rns.Packet
	b.node.OnUnmatchedProof = func(pkt *rns.Packet) { mu.Lock(); unmatched = append(unmatched, pkt); mu.Unlock() }
	var sentHash [32]byte
	var sentKey ed25519.PublicKey
	sendCtx, stop := context.WithCancel(ctx)
	_, _, err := r.SendWith(sendCtx, h16(p.Delivery), []byte("late proof"), nil, lxmf.MapOf(), lxmf.Opportunistic, lxmf.SendOptions{
		Prepare: func(h [32]byte, k ed25519.PublicKey) error { sentHash, sentKey = h, k; stop(); return nil },
	})
	if err == nil {
		t.Skip("the proof beat the cancelled wait; nothing late to check this run")
	}
	p.wait("lxm_received", nil, 15*time.Second)
	waitFor(t, "the late proof reaches the hook", 10*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(unmatched) > 0 })
	mu.Lock()
	pkt := unmatched[0]
	mu.Unlock()
	if len(pkt.Hdr.Data) == reticulum.ImplicitProofLen && string(pkt.Hdr.DestHash[:]) != string(sentHash[:16]) {
		t.Fatalf("implicit proof addressed to %x; want the packet hash prefix %x", pkt.Hdr.DestHash, sentHash[:16])
	}
	if err := reticulum.ValidateProof(pkt.Hdr.Data, sentHash, sentKey); err != nil {
		t.Fatalf("upstream's proof does not validate against what Prepare reported: %v (%d bytes)", err, len(pkt.Hdr.Data))
	}
	t.Logf("upstream proof: %d bytes (implicit=%v), validated after the wait ended", len(pkt.Hdr.Data), len(pkt.Hdr.Data) == reticulum.ImplicitProofLen)
}
