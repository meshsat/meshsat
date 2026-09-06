package transport

import (
	"context"
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

// Subscribe twice on the IMT transport must not leave two poll loops that
// close the same done channel: the second Subscribe stops the first set of
// loops and each loop closes only the channel it was started with. This is
// the parallax crash of 6 Sep 2026 (pollLoop, direct_imt.go). The transport
// has no serial port here, so connectOnce is bypassed by pre-marking it
// connected with a nil conn; the loops exit on their own when they find no
// connection, which is exactly the path that used to double close.
func TestDirectIMT_ResubscribeDoesNotDoubleClose(t *testing.T) {
	tr := &DirectIMTTransport{eventSubs: map[uint64]chan SatEvent{}}
	tr.connected = true // skip the serial connect
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("resubscribe panicked: %v", r)
		}
	}()
	for i := 0; i < 3; i++ {
		if _, err := tr.Subscribe(ctx); err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Close must return: every done channel it waits on is closed exactly once.
	finished := make(chan struct{})
	go func() { _ = tr.Close(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("Close did not return, a loop never closed its done channel")
	}
}
