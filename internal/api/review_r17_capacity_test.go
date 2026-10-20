package api

import (
	"testing"
	"time"

	"meshsat/internal/hubmsg"
)

// The r17 review's reproduction of C10-R7, as the reviewer wrote it (MESHSAT-1584): nine runs
// whose cancellation is owed, kept through the helper that keeps a replaced run.
func TestReviewR17PendingCancelsAreNotEvictedBeforeDeadline(t *testing.T) {
	s, _, clk := sosC10Server(t, &apiSat{kind: "sbd", connected: true}, true, false)
	s.sosLoad()
	s.sos.mu.Lock()
	first := clk.now().UnixMilli()
	for i := int64(0); i < 9; i++ {
		id := first + i
		s.sos.run = &sosRun{ID: id, SOSID: hubmsg.NewSOSID(id), CancelledAt: clk.now().UTC().Format(time.RFC3339), HubCancelV2: true}
		s.sos.active = false
		s.sosKeepOwedLocked()
	}
	kept := len(s.sos.prev)
	found := s.sosFindLocked(first) != nil
	s.sos.mu.Unlock()
	if !found {
		t.Fatalf("unacked cancel evicted before deadline: kept=%d original_owed=9 elapsed=0", kept)
	}
}
