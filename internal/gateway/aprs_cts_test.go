package gateway

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// TestMain opens the APRS transmit gate for the whole package: the existing
// tests assume a frame goes out as soon as the write worker takes it. The gate
// tests below set their own values and restore them. [MESHSAT-1069]
func TestMain(m *testing.M) {
	aprsRXHoldoff = 0
	aprsRand = func() float64 { return 0 }
	os.Exit(m.Run())
}

// setCTSKnobs sets the gate knobs for one test and restores them afterwards.
// Call it before starting any gateway goroutine. A nil rnd keeps the current
// random source.
func setCTSKnobs(t *testing.T, holdoff, max time.Duration, rnd func() float64) {
	t.Helper()
	origHold, origMax, origRand := aprsRXHoldoff, aprsCTSMax, aprsRand
	aprsRXHoldoff, aprsCTSMax = holdoff, max
	if rnd != nil {
		aprsRand = rnd
	}
	t.Cleanup(func() { aprsRXHoldoff, aprsCTSMax, aprsRand = origHold, origMax, origRand })
}

// countingRand returns the values in order (the last one repeats) and counts
// the calls.
func countingRand(calls *atomic.Int64, values ...float64) func() float64 {
	return func() float64 {
		n := calls.Add(1)
		i := int(n) - 1
		if i >= len(values) {
			i = len(values) - 1
		}
		return values[i]
	}
}

// restampPeer keeps lastPeerRX fresh every period until the returned stop
// func is called, like a channel that never goes quiet. The first stamp is
// synchronous, so the channel already reads busy when restampPeer returns.
func restampPeer(g *APRSGateway, period time.Duration) (stop func()) {
	g.lastPeerRX.Store(time.Now().UnixNano())
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(period)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				g.lastPeerRX.Store(time.Now().UnixNano())
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

// A clear channel sends at once and never rolls persistence: no peer frame at
// all, or the last one older than the hold-off.
func TestAPRSCTS_ClearChannelSendsAtOnce(t *testing.T) {
	var calls atomic.Int64
	setCTSKnobs(t, 1500*time.Millisecond, 3*time.Second, countingRand(&calls, 0.99))

	for _, tc := range []struct {
		name string
		last int64
	}{
		{"never heard a peer", 0},
		{"peer frame older than the hold-off", time.Now().Add(-2 * time.Second).UnixNano()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}}
			g.lastPeerRX.Store(tc.last)
			start := time.Now()
			if !g.waitClearToSend(context.Background()) {
				t.Fatal("gate reported shutdown")
			}
			if waited := time.Since(start); waited > 150*time.Millisecond {
				t.Errorf("clear channel held the frame for %v", waited)
			}
			if g.ctsDeferred.Load() != 0 {
				t.Errorf("cts_deferred %d on a clear channel, want 0", g.ctsDeferred.Load())
			}
		})
	}
	if calls.Load() != 0 {
		t.Errorf("persistence rolled %d times on a clear channel, want 0", calls.Load())
	}
}

// A peer frame 200 ms ago with a 500 ms hold-off holds the frame for the
// remaining ~300 ms.
func TestAPRSCTS_HoldsOffAfterPeerFrame(t *testing.T) {
	setCTSKnobs(t, 500*time.Millisecond, 3*time.Second, func() float64 { return 0 })

	g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}}
	g.lastPeerRX.Store(time.Now().Add(-200 * time.Millisecond).UnixNano())

	start := time.Now()
	if !g.waitClearToSend(context.Background()) {
		t.Fatal("gate reported shutdown")
	}
	waited := time.Since(start)
	if waited < 290*time.Millisecond {
		t.Errorf("sent after %v; the hold-off had ~300 ms left", waited)
	}
	if waited > time.Second {
		t.Errorf("held for %v; the hold-off had only ~300 ms left", waited)
	}
	if g.ctsDeferred.Load() != 1 {
		t.Errorf("cts_deferred %d, want 1", g.ctsDeferred.Load())
	}
}

// The own-source check matches how our callsign goes on the air: case does not
// matter, the SSID does, and a callsign longer than six characters is cut.
func TestAPRSCTS_IsOwnSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		call string
		ssid int
		src  AX25Address
		want bool
	}{
		{"exact", "MSTESS", 10, AX25Address{Call: "MSTESS", SSID: 10}, true},
		{"config lower case", "mstess", 10, AX25Address{Call: "MSTESS", SSID: 10}, true},
		{"frame mixed case", "MSTESS", 10, AX25Address{Call: "MsTeSs", SSID: 10}, true},
		{"other ssid", "MSTESS", 10, AX25Address{Call: "MSTESS", SSID: 9}, false},
		{"other call", "MSTESS", 10, AX25Address{Call: "MSPRLX", SSID: 10}, false},
		{"seven chars cut to six", "MSTESSX", 10, AX25Address{Call: "MSTESS", SSID: 10}, true},
		{"ssid zero", "MSTESS", 0, AX25Address{Call: "MSTESS"}, true},
		{"no callsign configured", "", 0, AX25Address{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &APRSGateway{config: APRSConfig{Callsign: tc.call, SSID: tc.ssid}}
			if got := g.isOwnSource(tc.src); got != tc.want {
				t.Errorf("isOwnSource(%+v) with %q-%d = %v, want %v", tc.src, tc.call, tc.ssid, got, tc.want)
			}
		})
	}
}

// A frame with our own callsign-SSID (a TNC echo) read through the real read
// worker does not stamp lastPeerRX and does not hold the next frame; the same
// callsign with another SSID does.
func TestAPRSCTS_OwnEchoDoesNotHold(t *testing.T) {
	setCTSKnobs(t, 300*time.Millisecond, time.Second, func() float64 { return 0 })
	g, rw, stop := newSerialTestGateway(t, nil) // MSTESS-10
	defer stop()

	position := func(src AX25Address, comment string) []byte {
		return KISSEncode(EncodeAX25Frame(AX25Address{Call: "APRS"}, src, nil,
			EncodeAPRSPosition(52.3676, 4.9041, '/', '-', aprsMeshSatMarker+"!00000001] "+comment)))
	}
	// The read worker delivers a MeshSat-tagged position as an inbound
	// message AFTER the lastPeerRX decision, so receiving it means the
	// decision was made (an untagged third-party position would stay out of
	// the pipeline since MESHSAT-1128).
	waitInbound := func(what string) {
		t.Helper()
		select {
		case <-g.Receive():
		case <-time.After(3 * time.Second):
			t.Fatalf("read worker never delivered the %s frame", what)
		}
	}

	rw.feed(position(AX25Address{Call: "MSTESS", SSID: 10}, "echo of our own frame"))
	waitInbound("own")
	if g.lastFrameAt.Load() == 0 {
		t.Fatal("lastFrameAt not stamped: the frame was not read")
	}
	if ts := g.lastPeerRX.Load(); ts != 0 {
		t.Fatalf("our own echo stamped lastPeerRX (%d)", ts)
	}
	start := time.Now()
	if !g.waitClearToSend(context.Background()) {
		t.Fatal("gate reported shutdown")
	}
	if waited := time.Since(start); waited > 150*time.Millisecond {
		t.Errorf("our own echo held the next frame for %v", waited)
	}

	// Positive control: same call, other SSID, is another station.
	rw.feed(position(AX25Address{Call: "MSTESS", SSID: 9}, "a different station"))
	waitInbound("peer")
	if g.lastPeerRX.Load() == 0 {
		t.Fatal("a peer frame did not stamp lastPeerRX")
	}
	start = time.Now()
	if !g.waitClearToSend(context.Background()) {
		t.Fatal("gate reported shutdown")
	}
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Errorf("a peer frame just now held the next frame for only %v", waited)
	}
}

// A channel that never goes quiet still releases the frame at aprsCTSMax.
func TestAPRSCTS_BusyChannelIsCapped(t *testing.T) {
	setCTSKnobs(t, 100*time.Millisecond, 300*time.Millisecond, func() float64 { return 0.99 })

	g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}}
	stopStamp := restampPeer(g, 50*time.Millisecond)
	defer stopStamp()

	start := time.Now()
	if !g.waitClearToSend(context.Background()) {
		t.Fatal("gate reported shutdown")
	}
	waited := time.Since(start)
	if waited < 280*time.Millisecond {
		t.Errorf("released after %v on a busy channel; the cap is 300 ms", waited)
	}
	if waited > 700*time.Millisecond {
		t.Errorf("held for %v; the cap is 300 ms", waited)
	}
}

// Every copy of a repeated message passes the gate, not only the first: on a
// channel that stays busy each copy waits out the cap. [MESHSAT-1069]
func TestAPRSCTS_EveryRepeatCopyPassesTheGate(t *testing.T) {
	setCTSKnobs(t, 60*time.Millisecond, 120*time.Millisecond, func() float64 { return 0.99 })

	rw := newPipeRW()
	c := newCollectSink()
	g := &APRSGateway{
		config:  APRSConfig{Callsign: "MSTESS", SSID: 10, TXRepeat: 3, TXRepeatGapMs: 10},
		kiss:    newKISSConnRW(rw),
		tracker: NewAPRSTracker(),
	}
	g.SetPacketSink(c.sink, "aprs_0")
	stopStamp := restampPeer(g, 20*time.Millisecond)

	g.sendMessage(context.Background(), &aprsOutbound{msg: &transport.MeshMessage{From: 0xAABBCCDD, DecodedText: "three copies", MsgRef: "r3"}})
	stopStamp()

	var times []time.Time
	for i := 0; i < 3; i++ {
		times = append(times, c.wait(t).Time)
	}
	if got := g.ctsDeferred.Load(); got != 3 {
		t.Fatalf("cts_deferred %d, want 3 (one per copy)", got)
	}
	if st := g.GetAPRSStatus(); st["cts_deferred"] != int64(3) {
		t.Errorf("status cts_deferred = %v, want 3", st["cts_deferred"])
	}
	if g.kiss.TX.Load() != 3 || g.lastTX.Load() == 0 {
		t.Errorf("tx %d lastTX %d, want 3 frames and a stamp", g.kiss.TX.Load(), g.lastTX.Load())
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < 115*time.Millisecond {
			t.Errorf("copy %d went out %v after copy %d; it should have waited out the 120 ms cap", i+1, gap, i)
		}
	}
}

// p-persistence: once the hold-off has passed, a failed roll waits exactly one
// slot before the next. Persist 0 is the default 63 (p = 0.25) and SlotTime 0
// the default 10 (100 ms); configured values are used when set.
func TestAPRSCTS_PersistenceWaitsOneSlot(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		var calls atomic.Int64
		setCTSKnobs(t, 20*time.Millisecond, 3*time.Second, countingRand(&calls, 0.9, 0.1))
		g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}}
		g.lastPeerRX.Store(time.Now().UnixNano())

		start := time.Now()
		if !g.waitClearToSend(context.Background()) {
			t.Fatal("gate reported shutdown")
		}
		waited := time.Since(start)
		if calls.Load() != 2 {
			t.Fatalf("persistence rolled %d times, want 2 (0.9 fails p=0.25, 0.1 passes)", calls.Load())
		}
		if waited < 115*time.Millisecond {
			t.Errorf("waited %v, want the 20 ms hold-off plus one 100 ms slot", waited)
		}
		if waited > 400*time.Millisecond {
			t.Errorf("waited %v, far more than one slot", waited)
		}
	})
	t.Run("configured", func(t *testing.T) {
		var calls atomic.Int64
		// Persist 127 is p = 0.5: 0.6 fails three times, 0.4 passes. SlotTime 2
		// is 20 ms, so three slots take ~60 ms where the default 100 ms slot
		// would take ~300 ms: far enough apart for a loaded CI runner.
		setCTSKnobs(t, 20*time.Millisecond, 3*time.Second, countingRand(&calls, 0.6, 0.6, 0.6, 0.4))
		g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10, Persist: 127, SlotTime: 2}}
		g.lastPeerRX.Store(time.Now().UnixNano())

		start := time.Now()
		if !g.waitClearToSend(context.Background()) {
			t.Fatal("gate reported shutdown")
		}
		waited := time.Since(start)
		if calls.Load() != 4 {
			t.Fatalf("persistence rolled %d times, want 4", calls.Load())
		}
		if waited < 70*time.Millisecond {
			t.Errorf("waited %v, want the 20 ms hold-off plus three 20 ms slots", waited)
		}
		if waited >= 250*time.Millisecond {
			t.Errorf("waited %v: the default 100 ms slot was used instead of SlotTime 2", waited)
		}
	})
}

// Shutdown releases the gate at once with false, and sendMessage stops in the
// middle of a repeat gap instead of sleeping through it.
func TestAPRSCTS_CancelReturnsFalse(t *testing.T) {
	setCTSKnobs(t, 10*time.Second, 10*time.Second, func() float64 { return 0.99 })

	g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}}
	g.lastPeerRX.Store(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if g.waitClearToSend(ctx) {
		t.Fatal("gate returned true after the context was cancelled")
	}
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Errorf("took %v to notice the cancel", waited)
	}

	// Already cancelled: false even on a clear channel.
	g.lastPeerRX.Store(0)
	if g.waitClearToSend(ctx) {
		t.Error("gate returned true on a cancelled context")
	}

	// The repeat gap honours the context.
	rw := newPipeRW()
	s := &APRSGateway{
		config:  APRSConfig{Callsign: "MSTESS", SSID: 10, TXRepeat: 2, TXRepeatGapMs: 10000},
		kiss:    newKISSConnRW(rw),
		tracker: NewAPRSTracker(),
	}
	sctx, scancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, scancel)
	start = time.Now()
	s.sendMessage(sctx, &aprsOutbound{msg: &transport.MeshMessage{From: 1, DecodedText: "cancel me", MsgRef: "c1"}})
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("sendMessage slept %v through a cancelled repeat gap", waited)
	}
	if s.kiss.TX.Load() != 1 {
		t.Errorf("frames sent %d, want 1 (the repeat was cancelled)", s.kiss.TX.Load())
	}
}

// The beacon deferral still counts our own transmission: a beacon due right
// after we sent a frame waits for the quiet period, and our own frame is not a
// peer frame for the transmit gate. [MESHSAT-1021]
func TestAPRSCTS_BeaconDefersAfterOwnTX(t *testing.T) {
	origQuiet, origMax := beaconQuietFor, beaconDeferMax
	beaconQuietFor, beaconDeferMax = 300*time.Millisecond, 2*time.Second
	t.Cleanup(func() { beaconQuietFor, beaconDeferMax = origQuiet, origMax })
	setCTSKnobs(t, 1500*time.Millisecond, 3*time.Second, func() float64 { return 0 })

	rw := newPipeRW()
	g := &APRSGateway{
		config:  APRSConfig{Callsign: "MSTESS", SSID: 10, BeaconSecs: 60},
		kiss:    newKISSConnRW(rw),
		tracker: NewAPRSTracker(),
	}
	g.sendRaw(context.Background(), g.beaconFrame(1))
	if g.kiss.TX.Load() != 1 || g.lastTX.Load() == 0 {
		t.Fatalf("sendRaw: tx %d lastTX %d", g.kiss.TX.Load(), g.lastTX.Load())
	}
	if g.lastPeerRX.Load() != 0 {
		t.Fatal("our own transmission stamped lastPeerRX")
	}

	start := time.Now()
	if !g.waitForQuietChannel(context.Background()) {
		t.Fatal("waitForQuietChannel reported shutdown")
	}
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Errorf("beacon went out %v after our own frame; it should wait ~300 ms", waited)
	}
}

// A beacon waits while this kit has a frame waiting for the peer's ack, and
// goes out once the ack arrives (or the waiter is released). [MESHSAT-1021]
func TestAPRSCTS_BeaconWaitsForPendingAck(t *testing.T) {
	origMax, origPoll := beaconAckDeferMax, beaconAckPoll
	beaconAckDeferMax, beaconAckPoll = 2*time.Second, 20*time.Millisecond
	t.Cleanup(func() { beaconAckDeferMax, beaconAckPoll = origMax, origPoll })

	g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}, tracker: NewAPRSTracker()}
	if g.acks.pending() != 0 {
		t.Fatal("fresh gateway has a pending ack")
	}
	g.acks.register("ABCDE")
	if g.acks.pending() != 1 {
		t.Fatalf("pending = %d, want 1", g.acks.pending())
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		g.acks.resolve("ABCDE")
	}()
	start := time.Now()
	if !g.waitForNoPendingAck(context.Background()) {
		t.Fatal("reported shutdown")
	}
	if waited := time.Since(start); waited < 150*time.Millisecond || waited > 1500*time.Millisecond {
		t.Fatalf("beacon waited %v; want about 200 ms until the ack resolved", waited)
	}
	if g.acks.pending() != 0 {
		t.Fatalf("pending after resolve = %d", g.acks.pending())
	}
}

// A peer that never acks cannot starve the liveness beacon: the wait is capped.
func TestAPRSCTS_BeaconAckDeferralIsCapped(t *testing.T) {
	origMax, origPoll := beaconAckDeferMax, beaconAckPoll
	beaconAckDeferMax, beaconAckPoll = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { beaconAckDeferMax, beaconAckPoll = origMax, origPoll })

	g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}, tracker: NewAPRSTracker()}
	g.acks.register("ZZZZZ")
	start := time.Now()
	if !g.waitForNoPendingAck(context.Background()) {
		t.Fatal("reported shutdown")
	}
	if waited := time.Since(start); waited < 250*time.Millisecond || waited > 1500*time.Millisecond {
		t.Fatalf("beacon waited %v; want about the 300 ms cap", waited)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if g.waitForNoPendingAck(ctx) {
		t.Fatal("cancelled context must report shutdown")
	}
}

// An ack from the peer is proof it decoded a frame of ours, so the beacon that
// would carry the same proof is skipped. The test is "since the last beacon was
// due", not a fixed window: an ack landing anywhere in the interval counts, and
// the jittered wake cannot step over it. Only an ack counts; a frame we
// transmitted says nothing about what the peer heard. [MESHSAT-1021]
func TestAPRSCTS_BeaconSkippedWhenThePeerAckedUs(t *testing.T) {
	g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10, BeaconSecs: 30}, tracker: NewAPRSTracker()}

	if g.peerAckedSinceLastBeacon() {
		t.Fatal("a gateway that never heard an ack claims the peer heard us")
	}
	// Our own transmissions must not count.
	g.lastTX.Store(time.Now().UnixNano())
	if g.peerAckedSinceLastBeacon() {
		t.Fatal("our own transmission counted as the peer hearing us")
	}

	// An ack early in the interval still counts: this is the case a fixed
	// window missed, and it is the common one, since an ack follows our
	// frame by 2 to 3 s and frames come right after a beacon as often as
	// before the next.
	now := time.Now()
	g.lastBeaconAt.Store(now.Add(-30 * time.Second).UnixNano())
	g.lastAckAt.Store(now.Add(-29 * time.Second).UnixNano())
	if !g.peerAckedSinceLastBeacon() {
		t.Fatal("an ack one second into the interval did not count")
	}
	// An ack older than the last beacon is already represented by it.
	g.lastAckAt.Store(now.Add(-31 * time.Second).UnixNano())
	if g.peerAckedSinceLastBeacon() {
		t.Fatal("an ack older than the last beacon suppressed the next one")
	}
	// The skip cannot latch: once a cycle passes with no new ack, the
	// beacon is due again.
	g.lastAckAt.Store(now.UnixNano())
	if !g.peerAckedSinceLastBeacon() {
		t.Fatal("a fresh ack did not count")
	}
	g.lastBeaconAt.Store(time.Now().UnixNano()) // the worker stamps this every pass
	if g.peerAckedSinceLastBeacon() {
		t.Fatal("the skip latched: the same ack suppressed a second beacon")
	}
}

// handleAckReply stamps the proof and counts the ack. [MESHSAT-1021]
func TestAPRSAck_ReceivedAckStampsTheProof(t *testing.T) {
	g := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}, tracker: NewAPRSTracker()}
	g.acks.register("ABCDE")
	if g.lastAckAt.Load() != 0 {
		t.Fatal("fresh gateway carries an ack timestamp")
	}
	g.handleAckReply(&APRSPacket{Source: "MSPRLX-10", MsgTo: "MSTESS-10"}, "ABCDE", false)
	if g.acksReceived.Load() != 1 {
		t.Fatalf("acks_received = %d, want 1", g.acksReceived.Load())
	}
	if !g.peerAckedSinceLastBeacon() {
		t.Fatal("the ack did not stamp the proof")
	}

	// A reject releases nothing and proves nothing.
	g2 := &APRSGateway{config: APRSConfig{Callsign: "MSTESS", SSID: 10}, tracker: NewAPRSTracker()}
	g2.acks.register("FGHIJ")
	g2.handleAckReply(&APRSPacket{Source: "MSPRLX-10", MsgTo: "MSTESS-10"}, "FGHIJ", true)
	if g2.lastAckAt.Load() != 0 {
		t.Fatal("a reject stamped the proof")
	}
}
