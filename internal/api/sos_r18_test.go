package api

import (
	"testing"
	"time"

	"meshsat/internal/hubreporter"
)

// The r17 review's acceptance case for C10-R7 (MESHSAT-1584): more than eight earlier runs owe
// their cancel when the next SOS starts, the Bridge restarts, then the Hub comes back. Every
// owed cancel is published and completes on its own ack, and the newest SOS stays on.
func TestR18NineOwedCancelsOutliveARestartAndAllComplete(t *testing.T) {
	old := sosFollowEvery
	sosFollowEvery = 20 * time.Millisecond
	t.Cleanup(func() { sosFollowEvery = old })
	s, _, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)

	var owed []sosRun
	for range 9 {
		run := startWent(t, s) // its 0x07 went by satellite, so the cancel is owed
		cancelSOS(t, s)        // no Hub session: the cancel waits
		owed = append(owed, run)
	}
	newest := startWent(t, s)
	s.sos.mu.Lock()
	kept := len(s.sos.prev)
	s.sos.mu.Unlock()
	if kept != len(owed) {
		t.Fatalf("%d earlier runs kept, %d owe their cancel", kept, len(owed))
	}

	// A restart before the Hub is back: the new process finds every owed cancel.
	after := &Server{db: s.db, mesh: &sosMesh{}, gwManager: s.gwManager, nowFn: clk.now}
	after.SetDispatcher(s.dispatcher)
	after.SetHubBridgeID("msa-flaneur")
	hub := &sosFakeHub{up: false}
	after.sos.hub = hub
	stopSOS(s)()
	after.RestoreSOS()
	t.Cleanup(stopSOS(after))
	for _, r := range owed {
		if _, active, ok := after.sosSnapshot(r.ID); !ok || active {
			t.Fatalf("run %d after the restart: kept %v, active %v", r.ID, ok, active)
		}
	}
	if _, active, ok := after.sosSnapshot(newest.ID); !ok || !active {
		t.Fatal("the newest SOS is not on after the restart")
	}

	// The Hub comes back: every owed cancel goes, each acked on its own.
	hub.setUp(true)
	clk.add(time.Second)
	cancelled := func() map[string]bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		out := map[string]bool{}
		for _, e := range hub.events {
			if e.Kind == hubreporter.SOSEventCancel {
				out[e.SOSID] = true
			}
		}
		return out
	}
	eventually(t, "every owed cancel published", func() bool {
		got := cancelled()
		for _, r := range owed {
			if !got[r.SOSID] {
				return false
			}
		}
		return true
	})
	if cancelled()[newest.SOSID] {
		t.Fatal("the newest SOS was cancelled")
	}
	for _, r := range owed {
		after.SOSAckReceived(hubreporter.SOSAck{V: 2, SOSID: r.SOSID, Kind: hubreporter.SOSEventCancel, State: "cancelled", Rev: 2})
	}
	eventually(t, "every earlier run forgotten once acked", func() bool {
		after.sos.mu.Lock()
		defer after.sos.mu.Unlock()
		return len(after.sos.prev) == 0
	})
	if _, active, ok := after.sosSnapshot(newest.ID); !ok || !active {
		t.Fatal("the newest SOS is not on after the earlier runs' acks")
	}
}
