package transport

import (
	"testing"
	"time"
)

// quietTransport is a connected transport with one remote node whose channel
// has been silent for longer than the watchdog threshold.
func quietTransport(t *testing.T, watchdogMin int, silence time.Duration) *DirectMeshTransport {
	t.Helper()
	tr := NewDirectMeshTransport("/dev/null")
	tr.watchdogMin = watchdogMin
	tr.myNodeNum = 0x11111111
	tr.nodes[0x11111111] = &MeshNode{Num: 0x11111111}
	tr.nodes[0x22222222] = &MeshNode{Num: 0x22222222}
	tr.connected = true
	tr.lastExternalPkt.Store(time.Now().Add(-silence).Unix())
	return tr
}

// A quiet channel with a radio that still answers local requests keeps its
// port: no reconnect, still connected. [MESHSAT-1122]
func TestMeshWatchdog_QuietChannelWithLiveRadioKeepsPort(t *testing.T) {
	tr := quietTransport(t, 10, 25*time.Minute)
	tr.lastLocalReply.Store(time.Now().Add(-40 * time.Second).UnixNano())
	if tr.watchdogTriggered() {
		t.Fatal("watchdog reopened the port although the radio answered a local request 40 s ago")
	}
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	if !tr.connected {
		t.Fatal("transport marked disconnected")
	}
}

// A radio that has neither forwarded a packet nor answered anything locally
// inside the window is a stale session: the watchdog still reopens it.
func TestMeshWatchdog_SilentRadioIsReopened(t *testing.T) {
	tr := quietTransport(t, 10, 25*time.Minute)
	tr.lastLocalReply.Store(time.Now().Add(-25 * time.Minute).UnixNano())
	if !tr.watchdogTriggered() {
		t.Fatal("watchdog did not reopen a radio that answers nothing")
	}
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	if tr.connected {
		t.Fatal("transport still marked connected after the reopen")
	}
}

// No local reply ever recorded (device health off, no handshake reply seen):
// the old behaviour stands, the port is reopened.
func TestMeshWatchdog_NoLocalReplyEverFallsBackToReopen(t *testing.T) {
	tr := quietTransport(t, 10, 25*time.Minute)
	if !tr.watchdogTriggered() {
		t.Fatal("watchdog did not reopen with no local reply on record")
	}
}

// Below the threshold nothing happens, whatever the radio said.
func TestMeshWatchdog_BelowThresholdIsNoop(t *testing.T) {
	tr := quietTransport(t, 10, 3*time.Minute)
	if tr.watchdogTriggered() {
		t.Fatal("watchdog fired below its threshold")
	}
}

// No remote nodes: nothing to hear, never fires.
func TestMeshWatchdog_NoRemoteNodesIsNoop(t *testing.T) {
	tr := quietTransport(t, 10, 25*time.Minute)
	delete(tr.nodes, 0x22222222)
	if tr.watchdogTriggered() {
		t.Fatal("watchdog fired with no remote nodes")
	}
}
