package oob

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/hopwire"
)

// The guards on the STATUS-NET hop state exchange (the review of part two).
// [MESHSAT-1650]

// guardHarness is a harness with media hopping on or off, aprs_0 and mesh_0
// free and everything else paid.
func guardHarness(t *testing.T, on *atomic.Bool) (*harness, *peerNotes) {
	t.Helper()
	h := newHarness(t, RoleControl, false)
	notes := &peerNotes{}
	h.svc.d.HopState = func(budget int) string {
		if !on.Load() {
			return ""
		}
		return hopwire.Encode(ourLinks, budget)
	}
	h.svc.d.HopPeer = notes.note
	h.svc.d.FreeBearer = func(id string) bool { return id == "aprs_0" || id == "mesh_0" }
	setHopOn(h, on.Load)
	return h, notes
}

// askStatusNet sends a STATUS-NET request to peer and returns the counter
// its reply names.
func askStatusNet(t *testing.T, h *harness, peer uint16) uint16 {
	t.Helper()
	res, err := h.svc.Send(context.Background(), SendRequest{PeerID: peer, Via: "aprs_0", Cmd: CmdStatusNet})
	if err != nil {
		t.Fatal(err)
	}
	return uint16(res.Counter)
}

// sealAs seals a frame as the remote side of peer p.
func sealAs(t *testing.T, h *harness, p *database.OOBPeer, key []byte, f Frame) string {
	t.Helper()
	f.PeerID = p.PeerID
	wire, err := Seal(f, key, Role(p.LocalRole).Other())
	if err != nil {
		t.Fatal(err)
	}
	return Encode(wire)
}

func statusReply(t *testing.T, h *harness, p *database.OOBPeer, key []byte, counter uint32, req uint16, seq, total byte, body string) string {
	return sealAs(t, h, p, key, Frame{Reply: true, Counter: counter, Cmd: CmdStatusNet,
		Args: EncodeReplyArgs(ReplyArgs{RC: RCOK, ReqCounterLo: req, Seq: seq, Total: total, Body: []byte(body)})})
}

func (h *harness) openParts(peer uint16) int {
	h.svc.hopMu.Lock()
	defer h.svc.hopMu.Unlock()
	n := 0
	for k := range h.svc.hopParts {
		if k.peer == peer {
			n++
		}
	}
	return n
}

// share_once_per_peer_under_concurrency (R1): two shares at once, the first
// still sending, tell a peer our state once.
func TestShareHopStateOnceUnderConcurrency(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	h, _ := guardHarness(t, &on)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var sends atomic.Int32
	h.svc.d.Send = func(ctx context.Context, iface, addr, text string) (int64, error) {
		sends.Add(1)
		entered <- struct{}{}
		<-release
		return 1, nil
	}
	defer close(release)
	go h.svc.ShareHopState(context.Background())
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the first share never sent")
	}
	second := make(chan []HopShare, 1)
	go func() { second <- h.svc.ShareHopState(context.Background()) }()
	select {
	case res := <-second:
		if len(res) != 1 || res[0].Sent || res[0].Skipped != "recent" {
			t.Fatalf("second share %+v, want skipped as recent", res)
		}
	case <-entered:
		t.Fatalf("a second send to the same peer while the first was going out (%d sends)", sends.Load())
	case <-time.After(3 * time.Second):
		t.Fatal("the second share hung")
	}
	if n := sends.Load(); n != 1 {
		t.Fatalf("%d sends, want 1", n)
	}
}

// A share whose send fails frees the peer at once.
func TestShareHopStateFailedSendFreesThePeer(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	h, _ := guardHarness(t, &on)
	fail := true
	h.svc.d.Send = func(ctx context.Context, iface, addr, text string) (int64, error) {
		if fail {
			return 0, context.DeadlineExceeded
		}
		return 1, nil
	}
	if res := h.svc.ShareHopState(context.Background()); len(res) != 1 || res[0].Skipped != "send_error" {
		t.Fatalf("failed share %+v", res)
	}
	fail = false
	if res := h.svc.ShareHopState(context.Background()); len(res) != 1 || !res[0].Sent {
		t.Fatalf("share after a failed one %+v, want sent", res)
	}
}

// status_net_reply_on_a_paid_bearer_unchanged (R2): over SMS the reply is
// the STATUS-NET it always was, byte for byte, in as many chunks; on a free
// bearer it carries our form.
func TestStatusNetReplyOnAPaidBearerUnchanged(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	h, _ := guardHarness(t, &on)
	h.svc.HandleInbound(context.Background(), "cellular_0", "+31653207829", h.peerFrame(t, 1, CmdStatusNet, nil, true, false))
	waitFor(t, func() bool { return len(h.sends()) >= 1 })
	time.Sleep(50 * time.Millisecond)
	if n := len(h.sends()); n != 1 {
		t.Fatalf("SMS reply in %d chunks, want 1", n)
	}
	if b := replyBody(t, h, h.sends()); b != "wlan0:up agent:none" {
		t.Fatalf("SMS reply %q, want the plain STATUS-NET", b)
	}
	h.svc.HandleInbound(context.Background(), "mesh_0", "!0000beef", h.peerFrame(t, 2, CmdStatusNet, nil, true, false))
	waitFor(t, func() bool { return len(h.sends()) >= 2 })
	if b := replyBody(t, h, h.sends()[1:]); b != "wlan0:up agent:none "+hopwire.Encode(ourLinks, 1000) {
		t.Fatalf("mesh reply %q, want the status and our form", b)
	}
}

// status_net_reply_chunks_not_kept_while_off (R3): with media hopping off a
// peer's reply chunks are not kept, and turning it off drops what was kept.
func TestStatusNetReplyChunksNotKeptWhileOff(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	h, notes := guardHarness(t, &on)
	req := askStatusNet(t, h, h.peer.PeerID)
	on.Store(false)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 1, req, 1, 2, "wlan0:up H1:"))
	if n := h.openParts(h.peer.PeerID); n != 0 {
		t.Fatalf("off: %d replies kept", n)
	}
	on.Store(true)
	req2 := askStatusNet(t, h, h.peer.PeerID)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 2, req2, 1, 2, "wlan0:up H1:"))
	if n := h.openParts(h.peer.PeerID); n != 1 {
		t.Fatalf("on: %d replies kept, want 1", n)
	}
	on.Store(false)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 3, req2, 2, 2, "m0X"))
	if n := h.openParts(h.peer.PeerID); n != 0 || notes.count() != 0 {
		t.Fatalf("after off: %d kept, %d noted", n, notes.count())
	}
}

// reassembly_capped_per_peer (R7): one peer's open replies never starve
// another's.
func TestStatusNetReplyReassemblyPerPeerCap(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	h, notes := guardHarness(t, &on)
	other, err := h.svc.CreatePeer(PeerSpec{Alias: "tesseract-2", Role: RoleReadonly, Addresses: map[string]string{"aprs_0": "PD0ABC-7"}})
	if err != nil {
		t.Fatal(err)
	}
	otherKey, _, _ := h.keys.GetKey("mgmt", "tesseract-2")
	for i := 1; i <= 40; i++ {
		req := askStatusNet(t, h, h.peer.PeerID)
		h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, uint32(i), req, 1, 2, "wlan0:up"))
	}
	if n := h.openParts(h.peer.PeerID); n > 4 {
		t.Fatalf("one peer holds %d open replies, want at most 4", n)
	}
	req := askStatusNet(t, h, other.PeerID)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0ABC-7", statusReply(t, h, other, otherKey, 1, req, 1, 2, "wlan0:up "))
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0ABC-7", statusReply(t, h, other, otherKey, 2, req, 2, 2, "H1:m0X"))
	found := false
	notes.mu.Lock()
	for _, n := range notes.notes {
		if n == "tesseract-2 aprs_0 reply" {
			found = true
		}
	}
	notes.mu.Unlock()
	if !found {
		t.Fatalf("the second peer's reply was starved: %v", notes.notes)
	}
}

// reply_form_only_for_our_request (R8): a STATUS-NET reply that answers no
// request of ours is not read; one that does is read once.
func TestStatusNetReplyOnlyForOurRequest(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	h, notes := guardHarness(t, &on)
	ctx := context.Background()
	h.svc.HandleInbound(ctx, "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 1, 777, 1, 1, "wlan0:up H1:m0X"))
	if notes.count() != 0 {
		t.Fatalf("an unsolicited reply was read: %v", notes.notes)
	}
	req := askStatusNet(t, h, h.peer.PeerID)
	h.svc.HandleInbound(ctx, "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 2, req, 1, 1, "wlan0:up H1:m0X"))
	if notes.count() != 1 {
		t.Fatalf("the reply to our request was not read: %v", notes.notes)
	}
	h.svc.HandleInbound(ctx, "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 3, req, 1, 1, "wlan0:up H1:a0X"))
	if notes.count() != 1 {
		t.Fatalf("a second reply to one request was read: %v", notes.notes)
	}
}
