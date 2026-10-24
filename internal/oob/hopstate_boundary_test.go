package oob

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/engine"
)

// The two interval boundaries the r56 verification found: a STATUS-NET
// request of ours keeps the media hopping interval it was made in (R56-B1),
// and a STATUS-NET answer carries our form only as output of an interval it
// holds, so no such reply is queued after a switch to off has returned
// (R56-B2). [MESHSAT-1650]

// ordinary_request_keeps_its_interval: an ordinary STATUS-NET request (no
// argument, as the API sends it) belongs to the interval it is made in.
// When that interval ends while the request is being queued, or the request
// was made with the switch off and the switch comes on meanwhile, it is not
// remembered and its reply is not read; the next request, made and answered
// in one interval, is.
func TestHopExchangeOrdinaryRequestKeepsItsInterval(t *testing.T) {
	for _, tc := range []struct {
		name           string
		before, during []engine.HopMode
	}{
		{"interval_ends_while_queued", nil, []engine.HopMode{engine.HopOff, engine.HopWatch}},
		{"made_with_the_switch_off", []engine.HopMode{engine.HopOff}, []engine.HopMode{engine.HopOn}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, hs := switchHarness(t)
			setMode(t, hs, tc.before...)
			send := h.svc.d.Send
			var once sync.Once
			h.svc.d.Send = func(ctx context.Context, iface, addr, text string) (int64, error) {
				once.Do(func() { setMode(t, hs, tc.during...) })
				return send(ctx, iface, addr, text)
			}
			req := askStatusNet(t, h, h.peer.PeerID)
			if h.asked() != 0 {
				t.Errorf("a request of another interval was remembered (%d)", h.asked())
			}
			h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 1, req, 1, 1, "wlan0:up "+peerForm))
			if p := hs.Status().Peers; len(p) != 0 {
				t.Fatalf("the reply to a request of another interval was read: %+v", p)
			}
			req = askStatusNet(t, h, h.peer.PeerID)
			h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", statusReply(t, h, h.peer, h.key, 2, req, 1, 1, "wlan0:up "+peerForm))
			if p := hs.Status().Peers; len(p) != 1 {
				t.Fatalf("a reply in its request's interval was not read: %+v", p)
			}
		})
	}
}

// answer_begun_while_off_never_outlives_off: a STATUS-NET that arrives with
// the switch off, the switch coming on while it is answered (as its status
// is read, or as its bearer is looked at), never queues a reply carrying
// our form after a switch to off has returned: the reply is plain, or off
// waits for it.
func TestHopAnswerBegunWhileOffNeverOutlivesOff(t *testing.T) {
	for _, at := range []string{"status", "bearer"} {
		t.Run(at, func(t *testing.T) {
			h, hs := switchHarness(t)
			setMode(t, hs, engine.HopOff)
			var once sync.Once
			turnOn := func() {
				once.Do(func() {
					if _, err := hs.SetMode(engine.HopWatch); err != nil {
						t.Error(err)
					}
				})
			}
			switch at {
			case "status":
				h.svc.d.Status.WLAN = func() (string, bool) { turnOn(); return "up", true }
			case "bearer":
				free := h.svc.d.FreeBearer
				h.svc.d.FreeBearer = func(id string) bool { turnOn(); return free(id) }
			}
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
				if _, err := hs.SetMode(engine.HopOff); err != nil {
					t.Error(err)
				}
				close(offed)
			}()
			offFirst := false
			select {
			case <-offed:
				offFirst = true
			case <-time.After(150 * time.Millisecond):
			}
			close(release)
			select {
			case <-offed:
			case <-time.After(3 * time.Second):
				t.Fatal("off never returned")
			}
			time.Sleep(100 * time.Millisecond)
			mu.Lock()
			items := make([]sentItem, 0, len(texts))
			for _, x := range texts {
				items = append(items, sentItem{text: x})
			}
			mu.Unlock()
			body := replyBody(t, h, items)
			if !strings.HasPrefix(body, "wlan0:up") {
				t.Fatalf("no STATUS-NET reply: %q", body)
			}
			if offFirst && strings.Contains(body, " H1:") {
				t.Fatalf("a reply carrying our form was queued after off returned: %q", body)
			}
		})
	}
}

// answered_plain_while_off: with the switch off a STATUS-NET is answered as
// it always was: the requester's state is not read, ours is not attached.
func TestHopStatusNetAnsweredPlainWhileOff(t *testing.T) {
	h, hs := switchHarness(t)
	setMode(t, hs, engine.HopOff)
	h.svc.HandleInbound(context.Background(), "aprs_0", "PD0XYZ-7", h.peerFrame(t, 1, CmdStatusNet, []byte(peerForm), false, false))
	waitFor(t, func() bool { return len(h.sends()) > 0 })
	if body := replyBody(t, h, h.sends()); body != "wlan0:up agent:none" {
		t.Fatalf("STATUS-NET while off answered %q", body)
	}
	if p := hs.Status().Peers; len(p) != 0 {
		t.Fatalf("a peer's state read while off: %+v", p)
	}
}

// answer_outside_a_reply_leaves_no_hold: a STATUS-NET run outside a reply
// (the Hub's ExecuteLocal, or Execute) gives back the interval it held, so
// the switch to off still returns.
func TestHopAnswerOutsideAReplyLeavesNoHold(t *testing.T) {
	for _, via := range []string{"execute_local", "execute"} {
		t.Run(via, func(t *testing.T) {
			h, hs := switchHarness(t)
			var res Result
			switch via {
			case "execute_local":
				res = h.svc.ExecuteLocal(context.Background(), Origin{Alias: "hub", Role: RoleControl, Bearer: OriginHub}, CmdStatusNet, nil)
			case "execute":
				res = h.svc.Execute(context.Background(), Origin{PeerID: h.peer.PeerID, Alias: "parallax", Role: RoleControl, Bearer: "aprs_0"}, CmdStatusNet, nil)
			}
			if res.Code != RCOK {
				t.Fatalf("STATUS-NET: %s", res.Code)
			}
			offed := make(chan struct{})
			go func() {
				if _, err := hs.SetMode(engine.HopOff); err != nil {
					t.Error(err)
				}
				close(offed)
			}()
			select {
			case <-offed:
			case <-time.After(3 * time.Second):
				t.Fatal("off never returned after the STATUS-NET")
			}
		})
	}
}
