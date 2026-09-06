package transport

import (
	"sync"
	"testing"
	"time"
)

// A second stopReader on the same connection must not close readerStop
// again: Close() and reconnect() both stop the reader, and the double
// close took parallax's bridge down with "close of closed channel"
// (6 Sep 2026, MESHSAT-829).
func TestJSPRConn_StopReaderIsIdempotent(t *testing.T) {
	c := &jsprConn{writeCh: make(chan jsprWriteRequest)}
	arm := func() {
		c.readerStop = make(chan struct{})
		c.readerDone = make(chan struct{})
		c.stopOnce = sync.Once{}
		// Stand in for readerLoop: exit when told to stop, close done on exit.
		go func(stop <-chan struct{}, done chan<- struct{}) {
			defer close(done)
			<-stop
		}(c.readerStop, c.readerDone)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stopReader panicked on a repeated call: %v", r)
		}
	}()

	arm()
	c.stopReader()
	c.stopReader()
	c.stopReader()

	// A fresh start arms a new stop channel and a new once, and the
	// next stop must actually stop that reader.
	arm()
	finished := make(chan struct{})
	go func() { c.stopReader(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("stopReader after a re-arm did not return")
	}
	c.stopReader()
}
