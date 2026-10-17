package transport

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// CancelOp names the send it was decided for by its op token, and writes
// nothing when that send is not the one with the helper: not in flight,
// another op (the same request_reference included, which wraps at 100),
// its window over, or a helper session that never had it. [MESHSAT-1282,
// MESHSAT-1584, review IMT-R2]
func TestHelperCancelOp(t *testing.T) {
	var buf bytes.Buffer
	h := &jsprHelperPort{stdin: nopWriteCloser{&buf}}
	if h.CancelOp("a1") || h.CancelOp("") || buf.Len() != 0 {
		t.Fatal("cancelled with no MO in flight")
	}
	h.moRef, h.moOp = 7, "b2"
	h.moInFlightUntil = time.Now().Add(time.Minute)
	if h.CancelOp("a1") || h.CancelOp("") || buf.Len() != 0 {
		t.Fatal("a cancel for another op was written while b2 (reference 7) is in flight")
	}
	if !h.CancelOp("b2") {
		t.Fatal("did not cancel the MO in flight")
	}
	if got := strings.TrimSpace(buf.String()); got != `{"cmd":"cancel_mo","op":"b2"}` {
		t.Fatalf("helper got %q", got)
	}
	buf.Reset()
	h.moInFlightUntil = time.Now().Add(-time.Second)
	if h.CancelOp("b2") || buf.Len() != 0 {
		t.Fatal("cancelled an MO whose window is over")
	}

	// A new helper session (the port after a restart) never had b2, even
	// when its MO in flight carries the same reference.
	var buf2 bytes.Buffer
	h2 := &jsprHelperPort{stdin: nopWriteCloser{&buf2}}
	h2.moRef, h2.moOp = 7, "c3"
	h2.moInFlightUntil = time.Now().Add(time.Minute)
	if h2.CancelOp("b2") || buf2.Len() != 0 {
		t.Fatal("a cancel from the previous helper session was written to the new one")
	}
}
