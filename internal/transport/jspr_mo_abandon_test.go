package transport

import (
	"strings"
	"testing"
	"time"
)

// A reset or reconnect that closes the session under an MO ends the wait at
// once instead of holding the channel for three minutes. [MESHSAT-1282]
func TestAwaitHelperMOResult_SessionClosedEndsTheWait(t *testing.T) {
	c := &jsprConn{pending: map[string]*pendingRequest{}, readerDone: make(chan struct{})}
	close(c.readerDone)
	start := time.Now()
	_, err := c.awaitHelperMOResult(1, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "session closed") {
		t.Fatalf("err = %v, want the session-closed error", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %s", waited)
	}
}
