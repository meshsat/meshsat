package transport

import (
	"strings"
	"testing"
	"time"
)

// TestPermitJoin_NotReadyReturnsFriendlyError verifies that PermitJoin refuses
// to send ZDO_MGMT_PERMIT_JOIN_REQ when the coordinator isn't in
// DEV_ZB_COORD state — the operator gets a clear "network still forming"
// message instead of "status=0xc2". [MESHSAT-510]
func TestPermitJoin_NotReadyReturnsFriendlyError(t *testing.T) {
	z := NewDirectZigBeeTransport()
	// Simulate a running transport in a pre-coord state (e.g. just after
	// startup, before DEV_ZB_COORD has been signalled).
	z.mu.Lock()
	z.running = true
	z.coordState = ZNPDevStateInit
	z.mu.Unlock()

	err := z.PermitJoin(120)
	if err == nil {
		t.Fatal("expected error when coordinator not ready")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("error message should mention 'not ready', got: %v", err)
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("error message should include state name 'init', got: %v", err)
	}
}

func TestPermitJoin_NotRunningReturnsError(t *testing.T) {
	z := NewDirectZigBeeTransport()
	err := z.PermitJoin(120)
	if err == nil {
		t.Fatal("expected error when transport not running")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("got: %v", err)
	}
}

func TestIsReady_RequiresCoordState(t *testing.T) {
	z := NewDirectZigBeeTransport()
	z.mu.Lock()
	z.running = true
	z.coordState = ZNPDevStateInit
	z.mu.Unlock()
	if z.IsReady() {
		t.Error("IsReady should be false in INIT state")
	}

	z.mu.Lock()
	z.coordState = ZNPDevStateCoord
	z.mu.Unlock()
	if !z.IsReady() {
		t.Error("IsReady should be true in COORD state")
	}

	z.mu.Lock()
	z.running = false
	z.mu.Unlock()
	if z.IsReady() {
		t.Error("IsReady should be false when not running, even if coord state")
	}
}

// TestWatchStateChange_FanOut verifies the waiter pattern used by
// initCoordinator: register a waiter, trigger a state change, receive it.
// Mirrors zigbee-herdsman's znp.waitFor(AREQ, ZDO, "stateChangeInd", ..., 9)
// pattern.
func TestWatchStateChange_FanOut(t *testing.T) {
	z := NewDirectZigBeeTransport()

	ch1, unsub1 := z.watchStateChange()
	defer unsub1()
	ch2, unsub2 := z.watchStateChange()
	defer unsub2()

	z.notifyStateChange(ZNPDevStateCoord)

	select {
	case got := <-ch1:
		if got != ZNPDevStateCoord {
			t.Errorf("ch1 got 0x%02x, want 0x%02x", got, ZNPDevStateCoord)
		}
	default:
		t.Error("ch1 did not receive state change")
	}
	select {
	case got := <-ch2:
		if got != ZNPDevStateCoord {
			t.Errorf("ch2 got 0x%02x, want 0x%02x", got, ZNPDevStateCoord)
		}
	default:
		t.Error("ch2 did not receive state change")
	}
}

func TestWatchStateChange_Unsub(t *testing.T) {
	z := NewDirectZigBeeTransport()
	ch, unsub := z.watchStateChange()

	z.stateWaitersMu.Lock()
	count := len(z.stateWaiters)
	z.stateWaitersMu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 waiter, got %d", count)
	}

	unsub()
	z.stateWaitersMu.Lock()
	count = len(z.stateWaiters)
	z.stateWaitersMu.Unlock()
	if count != 0 {
		t.Errorf("unsub did not remove waiter: %d remaining", count)
	}

	// After unsub, notify should not block on the closed-from-view
	// channel — it's still a live Go channel, but we never read from it.
	z.notifyStateChange(ZNPDevStateCoord)
	_ = ch
}

// TestHandleFrame_SysResetIndSchedulesReinit verifies that when the
// coordinator emits SYS_RESET_IND (which happens on watchdog/fault/
// external reset), the reinit queue is populated so reinitLoop can bring
// the network back up automatically. [MESHSAT-510]
func TestHandleFrame_SysResetIndSchedulesReinit(t *testing.T) {
	z := NewDirectZigBeeTransport()
	z.mu.Lock()
	z.running = true
	z.coordState = ZNPDevStateCoord
	z.permitJoinEnd = time.Now().Add(60 * time.Second)
	z.mu.Unlock()

	// Build a synthetic SYS_RESET_IND payload (reason=watchdog)
	f := ZNPFrame{
		Cmd:  CmdSysResetInd,
		Data: []byte{ZNPResetReasonWatchdog, 0x02, 0x01, 0x02, 0x07, 0x01},
	}
	z.handleFrame(f)

	// coordState should be knocked back to HOLD so subsequent PermitJoin
	// calls return "not ready" immediately instead of sending 0xC2 down
	// the line.
	if got := z.CoordState(); got != ZNPDevStateHold {
		t.Errorf("coordState after reset: got 0x%02x, want 0x%02x", got, ZNPDevStateHold)
	}

	// permit-join should be cleared — it's gone with the reset.
	z.mu.Lock()
	pe := z.permitJoinEnd
	z.mu.Unlock()
	if !pe.IsZero() {
		t.Errorf("permitJoinEnd should be zeroed after reset, got %v", pe)
	}

	// reinitPending should contain a signal.
	select {
	case <-z.reinitPending:
		// ok
	default:
		t.Error("reinitPending was not signalled on SYS_RESET_IND")
	}
}

// TestClassifyDeviceWithProbe_CachesResult locks down the probe cache
// behaviour — the root fix for the periodic DTR/RTS resets that were
// driving permit-join into the 0xC2 state window. [MESHSAT-510]
//
// We can't probe real hardware in unit tests, so we seed the cache
// directly and confirm it's returned without calling ProbeZNP.
func TestClassifyDeviceWithProbe_CachesResult(t *testing.T) {
	const vidpid = "10c4:ea60"
	const port = "/dev/ttyUSB-test-zigbee"
	const key = vidpid + "|" + port

	probeCacheMu.Lock()
	probeCache[key] = probeCacheEntry{result: "zigbee", at: time.Now()}
	probeCacheMu.Unlock()
	defer InvalidateProbeCache(port)

	got := ClassifyDeviceWithProbe(vidpid, port)
	if got != "zigbee" {
		t.Errorf("cached zigbee result not returned: got %q", got)
	}
}

// TestClassifyDeviceWithProbe_CachesCellular confirms that a "cellular"
// probe result (ProbeAT hit after ZNP/Meshtastic missed) is persisted
// in the same cache and short-circuits subsequent calls — so the T-Call
// A7670E only sees the DTR-guarded AT probe once per port enumeration.
// [MESHSAT-646]
func TestClassifyDeviceWithProbe_CachesCellular(t *testing.T) {
	const vidpid = "1a86:55d4"
	const port = "/dev/ttyUSB-test-cellular"
	const key = vidpid + "|" + port

	probeCacheMu.Lock()
	probeCache[key] = probeCacheEntry{result: "cellular", at: time.Now()}
	probeCacheMu.Unlock()
	defer InvalidateProbeCache(port)

	got := ClassifyDeviceWithProbe(vidpid, port)
	if got != "cellular" {
		t.Errorf("cached cellular result not returned: got %q", got)
	}
}

// TestClassifyDeviceWithProbe_CachesAmbiguousFallback pins down the
// all-three-probes-miss path: the base "ambiguous" verdict must be
// cached as-is so /api/devices keeps rendering amber (operator attention)
// instead of silently mis-labelling the port on each scan. [MESHSAT-646]
func TestClassifyDeviceWithProbe_CachesAmbiguousFallback(t *testing.T) {
	const vidpid = "1a86:55d4"
	const port = "/dev/ttyUSB-test-ambiguous"
	const key = vidpid + "|" + port

	probeCacheMu.Lock()
	probeCache[key] = probeCacheEntry{result: "ambiguous", at: time.Now()}
	probeCacheMu.Unlock()
	defer InvalidateProbeCache(port)

	got := ClassifyDeviceWithProbe(vidpid, port)
	if got != "ambiguous" {
		t.Errorf("cached ambiguous fallback not returned: got %q", got)
	}
}

func TestInvalidateProbeCache_RemovesEntry(t *testing.T) {
	const vidpid = "10c4:ea60"
	const port = "/dev/ttyUSB-invalidate"
	const key = vidpid + "|" + port

	probeCacheMu.Lock()
	probeCache[key] = probeCacheEntry{result: "zigbee", at: time.Now()}
	probeCacheMu.Unlock()

	InvalidateProbeCache(port)

	probeCacheMu.RLock()
	_, stillCached := probeCache[key]
	probeCacheMu.RUnlock()
	if stillCached {
		t.Error("probe cache entry should be removed by InvalidateProbeCache")
	}
}
