package gateway

import (
	"context"
	"testing"
	"time"
)

// The beacon pair and the message pair must not share a cadence.
//
// Both used g.repeatGap(), 4000 ms on the kits, so the two pairs advanced in
// lockstep: once a receiver's beacon pair started near a sender's message pair,
// BOTH copies of the message landed inside the receiver's own transmission and
// a half-duplex radio hears nothing while it keys. Measured 10 Sep 2026: 1 to 3
// messages in 20 lost both copies. [MESHSAT-1021]
func TestAPRSTiming_BeaconGapDiffersFromMessageGap(t *testing.T) {
	g := &APRSGateway{config: APRSConfig{TXRepeat: 2, TXRepeatGapMs: 4000}}

	msgGap := g.repeatGap()
	beaconGap := g.beaconRepeatGap()

	if beaconGap == msgGap {
		t.Fatalf("beacon gap %v equals the message gap; the two pairs stay in lockstep", beaconGap)
	}
	// Far enough apart that a pair drifts within its own two copies, and still
	// well inside the peer's 3 minute receive watchdog.
	if diff := beaconGap - msgGap; diff < time.Second {
		t.Errorf("beacon gap %v is only %v from the message gap %v", beaconGap, diff, msgGap)
	}
	if beaconGap > 30*time.Second {
		t.Errorf("beacon gap %v is long enough to worry the peer's receive watchdog", beaconGap)
	}
}

// An explicit beacon gap in the config wins over the derived one.
func TestAPRSTiming_BeaconGapConfigurable(t *testing.T) {
	g := &APRSGateway{config: APRSConfig{TXRepeat: 2, TXRepeatGapMs: 4000, BeaconRepeatGapMs: 2500}}
	if got := g.beaconRepeatGap(); got != 2500*time.Millisecond {
		t.Errorf("beacon gap = %v, want the configured 2.5s", got)
	}
}

// The message repeat gap is jittered, so two kits at the same nominal cadence
// cannot hold a fixed offset between their pairs.
func TestAPRSTiming_MessageGapIsJittered(t *testing.T) {
	g := &APRSGateway{config: APRSConfig{TXRepeat: 2, TXRepeatGapMs: 4000}}
	base := g.repeatGap()

	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		d := jitterDuration(base, 0.25)
		seen[d] = true
		if d < base*3/4 || d > base*5/4 {
			t.Fatalf("jittered gap %v outside ±25%% of %v", d, base)
		}
	}
	if len(seen) < 10 {
		t.Errorf("only %d distinct gaps in 200 draws; that is not jitter", len(seen))
	}
}

// A beacon that falls due while the channel is busy waits for a gap, rather
// than transmitting on top of a message that is mid-pair.
func TestAPRSTiming_BeaconWaitsForAQuietChannel(t *testing.T) {
	origQuiet, origMax := beaconQuietFor, beaconDeferMax
	// lastActive is unix SECONDS, so the threshold has to be seconds too:
	// at the moment of the call the recorded traffic already reads as up to
	// one second old.
	beaconQuietFor = 2 * time.Second
	beaconDeferMax = 10 * time.Second
	defer func() { beaconQuietFor, beaconDeferMax = origQuiet, origMax }()

	g := &APRSGateway{config: APRSConfig{TXRepeat: 2, TXRepeatGapMs: 100}}
	g.lastActive.Store(time.Now().Unix()) // traffic right now

	start := time.Now()
	if !g.waitForQuietChannel(context.Background()) {
		t.Fatal("waitForQuietChannel reported shutdown")
	}
	if waited := time.Since(start); waited < 900*time.Millisecond {
		t.Errorf("beacon went out after %v with the channel busy; it should have waited", waited)
	}
}

// The deferral is capped: a permanently busy channel must not silence the
// liveness beacon the peer's receive watchdog waits for.
func TestAPRSTiming_BeaconDeferralIsCapped(t *testing.T) {
	origQuiet, origMax := beaconQuietFor, beaconDeferMax
	beaconQuietFor = 10 * time.Second // never satisfied within the cap
	beaconDeferMax = 400 * time.Millisecond
	defer func() { beaconQuietFor, beaconDeferMax = origQuiet, origMax }()

	g := &APRSGateway{config: APRSConfig{TXRepeat: 2, TXRepeatGapMs: 100}}
	g.lastActive.Store(time.Now().Unix())

	start := time.Now()
	if !g.waitForQuietChannel(context.Background()) {
		t.Fatal("waitForQuietChannel reported shutdown")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Errorf("beacon held for %v; the cap should have released it", waited)
	}
}

// Shutting down while a beacon is deferred must not block the gateway.
func TestAPRSTiming_BeaconDeferralHonoursShutdown(t *testing.T) {
	origQuiet, origMax := beaconQuietFor, beaconDeferMax
	beaconQuietFor = 10 * time.Second
	beaconDeferMax = time.Minute
	defer func() { beaconQuietFor, beaconDeferMax = origQuiet, origMax }()

	g := &APRSGateway{config: APRSConfig{TXRepeat: 2, TXRepeatGapMs: 100}}
	g.lastActive.Store(time.Now().Unix())

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()

	start := time.Now()
	if g.waitForQuietChannel(ctx) {
		t.Error("waitForQuietChannel should report shutdown when the context is cancelled")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Errorf("took %v to notice the shutdown", waited)
	}
}
