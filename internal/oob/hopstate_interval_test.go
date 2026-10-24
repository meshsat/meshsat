package oob

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/hopwire"
)

// The hop exchange on media hopping's real switch: nothing it kept in one
// enabled interval is read in the next, and a share under way is done
// before the switch to off returns, with nothing more sent after
// (R55-B1, R55-B2). [MESHSAT-1650]

// hookedHopSet is the real switch, with the change hook FollowHopSet sets
// kept, so a test can fire it as a change of our links would.
type hookedHopSet struct {
	*engine.HopSetManager
	mu     sync.Mutex
	change func(gen uint64)
}

func (w *hookedHopSet) SetOnChange(f func(gen uint64)) {
	w.mu.Lock()
	w.change = f
	w.mu.Unlock()
	w.HopSetManager.SetOnChange(f)
}

func (w *hookedHopSet) fire(gen uint64) {
	w.mu.Lock()
	f := w.change
	w.mu.Unlock()
	f(gen)
}

// switchHarness is a harness wired to a real switch the way main.go wires
// it, watching over aprs_0 and mesh_0 (free) and cellular_0 (paid).
func switchHarness(t *testing.T) (*harness, *hookedHopSet) {
	t.Helper()
	h := newHarness(t, RoleControl, false)
	links := []database.Interface{
		{ID: "aprs_0", ChannelType: "aprs", Enabled: true},
		{ID: "mesh_0", ChannelType: "mesh", Enabled: true},
		{ID: "cellular_0", ChannelType: "cellular", Enabled: true},
	}
	hs := &hookedHopSet{HopSetManager: engine.NewHopSetManager(engine.DefaultHopSetConfig(),
		func() ([]database.Interface, error) { return links, nil }, engine.HopWatch)}
	h.svc.d.HopState = hs.Compact
	h.svc.d.HopSession = hs.Session
	h.svc.d.HopPeer = hs.NotePeer
	h.svc.d.FreeBearer = func(id string) bool { return id == "aprs_0" || id == "mesh_0" }
	h.svc.FollowHopSet(context.Background(), hs)
	return h, hs
}

func setMode(t *testing.T, hs *hookedHopSet, modes ...engine.HopMode) {
	t.Helper()
	for _, m := range modes {
		if _, err := hs.SetMode(m); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *harness) asked() int {
	h.svc.hopMu.Lock()
	defer h.svc.hopMu.Unlock()
	return len(h.svc.hopAsked)
}

var peerForm = hopwire.Encode([]hopwire.Link{{Iface: "mesh_0", State: hopwire.Excluded}}, 100)

// request_of_an_ended_interval_not_read: our STATUS-NET request goes in one
// interval; the switch goes off and comes back; the reply is not read.
func TestHopExchangeRequestOfEndedIntervalNotRead(t *testing.T) {
	h, hs := switchHarness(t)
	req := askStatusNet(t, h, h.peer.PeerID)
	if h.asked() != 1 {
		t.Fatalf("our request not remembered (%d)", h.asked())
	}
	setMode(t, hs, engine.HopOff)
	if h.asked() != 0 {
		t.Fatalf("off kept %d requests", h.asked())
	}
	setMode(t, hs, engine.HopWatch)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 1, req, 1, 1, "wlan0:up "+peerForm))
	if p := hs.Status().Peers; len(p) != 0 {
		t.Fatalf("a reply to a request of the ended interval was read: %+v", p)
	}
	// The same exchange inside one interval is read.
	req = askStatusNet(t, h, h.peer.PeerID)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 2, req, 1, 1, "wlan0:up "+peerForm))
	if p := hs.Status().Peers; len(p) != 1 {
		t.Fatalf("a reply in its own interval was not read: %+v", p)
	}
}

// partial_reply_dropped_at_off: a reply half in when the switch goes off is
// dropped then, and its last chunk after the switch comes back reads nothing.
func TestHopExchangePartialReplyDroppedAtOff(t *testing.T) {
	h, hs := switchHarness(t)
	req := askStatusNet(t, h, h.peer.PeerID)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 1, req, 1, 2, "wlan0:up "))
	if n := h.openParts(h.peer.PeerID); n != 1 {
		t.Fatalf("first chunk not kept (%d)", n)
	}
	setMode(t, hs, engine.HopOff)
	if n := h.openParts(h.peer.PeerID); n != 0 {
		t.Fatalf("off kept %d replies", n)
	}
	setMode(t, hs, engine.HopOn)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 2, req, 2, 2, peerForm))
	if p := hs.Status().Peers; len(p) != 0 || h.openParts(h.peer.PeerID) != 0 {
		t.Fatalf("the rest of a reply of the ended interval: peers %+v, kept %d", p, h.openParts(h.peer.PeerID))
	}
}

// record_of_an_ended_interval_not_read_even_if_kept: a request record or a
// half reply that outlived the reset (made as the switch went off) is still
// not read in the next interval: each is kept under its interval.
func TestHopExchangeRecordOfEndedIntervalNotReadEvenIfKept(t *testing.T) {
	h, hs := switchHarness(t)
	req := askStatusNet(t, h, h.peer.PeerID)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 1, req, 1, 2, "wlan0:up "))
	h.svc.hopMu.Lock()
	asked, parts := map[hopPartsKey]hopAskedMark{}, map[hopPartsKey]*hopParts{}
	for k, v := range h.svc.hopAsked {
		asked[k] = v
	}
	for k, v := range h.svc.hopParts {
		parts[k] = v
	}
	h.svc.hopMu.Unlock()
	setMode(t, hs, engine.HopOff, engine.HopWatch)
	h.svc.hopMu.Lock()
	h.svc.hopAsked, h.svc.hopParts = asked, parts
	h.svc.hopMu.Unlock()
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 2, req, 2, 2, peerForm))
	if p := hs.Status().Peers; len(p) != 0 {
		t.Fatalf("a kept record of the ended interval was read: %+v", p)
	}
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 3, req, 1, 1, "wlan0:up "+peerForm))
	if p := hs.Status().Peers; len(p) != 0 {
		t.Fatalf("a whole reply to a kept request of the ended interval was read: %+v", p)
	}
}

// share_under_way_done_before_off_returns: the switch to off waits for the
// share sending to its first peer, its audit included, and no other peer is
// told after.
func TestHopShareUnderWayDoneBeforeOffReturns(t *testing.T) {
	h, hs := switchHarness(t)
	if _, err := h.svc.CreatePeer(PeerSpec{Alias: "tesseract", Role: RoleControl, Addresses: map[string]string{"mesh_0": "!0000beef"}}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var sends atomic.Int32
	h.svc.d.Send = func(ctx context.Context, iface, addr, text string) (int64, error) {
		sends.Add(1)
		entered <- struct{}{}
		<-release
		return int64(sends.Load()), nil
	}
	gen, _ := hs.Session()
	hs.fire(gen)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the share never sent")
	}
	offed := make(chan struct{})
	go func() {
		setMode(t, hs, engine.HopOff)
		close(offed)
	}()
	select {
	case <-offed:
		t.Fatal("off returned while the share was sending")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-offed:
	case <-time.After(3 * time.Second):
		t.Fatal("off never returned")
	}
	h.mu.Lock()
	shareAudits := 0
	for _, a := range h.audits {
		if a == "oob_hop_share" {
			shareAudits++
		}
	}
	h.mu.Unlock()
	if sends.Load() != 1 || shareAudits != 1 {
		t.Fatalf("at off: %d sends, %d share audits, want 1 and 1", sends.Load(), shareAudits)
	}
	time.Sleep(100 * time.Millisecond)
	if sends.Load() != 1 {
		t.Fatalf("%d sends after off returned", sends.Load()-1)
	}
	if h.asked() != 0 {
		t.Fatalf("the request that went as off came kept %d records", h.asked())
	}
}

// share_prepared_before_off_never_sent: a change of our links in an ended
// interval tells no peer, with the switch off or back on.
func TestHopSharePreparedBeforeOffNeverSent(t *testing.T) {
	h, hs := switchHarness(t)
	old, _ := hs.Session()
	setMode(t, hs, engine.HopOff)
	hs.fire(old)
	setMode(t, hs, engine.HopWatch)
	hs.fire(old)
	time.Sleep(100 * time.Millisecond)
	if n := len(h.sends()); n != 0 {
		t.Fatalf("%d sends for a change of an ended interval", n)
	}
	now, _ := hs.Session()
	hs.fire(now)
	waitFor(t, func() bool { return len(h.sends()) == 1 })
}

// old_chunks_never_join_a_new_reply: chunks kept from an ended interval are
// dropped when a reply of this interval arrives under the same key, never
// put together with it.
func TestHopExchangeOldChunksNeverJoinANewReply(t *testing.T) {
	h, hs := switchHarness(t)
	req := askStatusNet(t, h, h.peer.PeerID)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 1, req, 1, 2, "wlan0:up "))
	h.svc.hopMu.Lock()
	parts := map[hopPartsKey]*hopParts{}
	for k, v := range h.svc.hopParts {
		parts[k] = v
	}
	h.svc.hopMu.Unlock()
	setMode(t, hs, engine.HopOff, engine.HopWatch)
	gen, _ := hs.Session()
	h.svc.hopMu.Lock()
	h.svc.hopParts = parts // kept from the ended interval
	h.svc.hopAsked = map[hopPartsKey]hopAskedMark{{peer: h.peer.PeerID, counter: req}: {at: h.svc.now(), gen: gen}}
	h.svc.hopMu.Unlock()
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 2, req, 2, 2, peerForm))
	if p := hs.Status().Peers; len(p) != 0 {
		t.Fatalf("a chunk of the ended interval joined this interval's reply: %+v", p)
	}
	if n := h.openParts(h.peer.PeerID); n != 1 {
		t.Fatalf("this interval's chunk not kept on its own (%d)", n)
	}
}

// peer_request_read_in_its_interval: a peer's request carrying its state is
// read in the interval it arrives in, after the switch has gone off and on.
func TestHopExchangePeerRequestReadInItsInterval(t *testing.T) {
	h, hs := switchHarness(t)
	setMode(t, hs, engine.HopOff, engine.HopWatch)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", h.peerFrame(t, 1, CmdStatusNet, []byte(peerForm), false, false))
	waitFor(t, func() bool { return len(h.sends()) > 0 }) // the request is executed, and answered
	if p := hs.Status().Peers; len(p) != 1 || p[0].Via != "request" {
		t.Fatalf("a peer's request in this interval not read: %+v", p)
	}
}

// reply_with_our_form_done_before_off_returns: a STATUS-NET being answered
// with our form when the switch goes off is queued, every chunk, before off
// returns, and nothing of it after.
func TestHopReplyWithOurFormDoneBeforeOffReturns(t *testing.T) {
	h, hs := switchHarness(t)
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var sends atomic.Int32
	var texts []string
	var mu sync.Mutex
	h.svc.d.Send = func(ctx context.Context, iface, addr, text string) (int64, error) {
		if sends.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
		mu.Lock()
		texts = append(texts, text)
		mu.Unlock()
		return int64(sends.Load()), nil
	}
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", h.peerFrame(t, 1, CmdStatusNet, nil, false, false))
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the reply never went")
	}
	offed := make(chan struct{})
	go func() {
		setMode(t, hs, engine.HopOff)
		close(offed)
	}()
	select {
	case <-offed:
		t.Fatal("off returned while the reply was being queued")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-offed:
	case <-time.After(3 * time.Second):
		t.Fatal("off never returned")
	}
	at := sends.Load()
	time.Sleep(100 * time.Millisecond)
	if n := sends.Load(); n != at {
		t.Fatalf("%d reply chunks queued after off returned", n-at)
	}
	mu.Lock()
	defer mu.Unlock()
	items := make([]sentItem, 0, len(texts))
	for _, x := range texts {
		items = append(items, sentItem{text: x})
	}
	if body := replyBody(t, h, items); !strings.Contains(body, " H1:") {
		t.Fatalf("the reply held was not the one with our form: %q", body)
	}
}
