package transport

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// CancelRef names the send it was decided for, and writes nothing when
// that send is not the one with the helper: not in flight, another
// reference, or its window over. [MESHSAT-1282, MESHSAT-1584]
func TestHelperCancelRef(t *testing.T) {
	var buf bytes.Buffer
	h := &jsprHelperPort{stdin: nopWriteCloser{&buf}}
	if h.CancelRef(7) || buf.Len() != 0 {
		t.Fatal("cancelled with no MO in flight")
	}
	h.moRef = 7
	h.moInFlightUntil = time.Now().Add(time.Minute)
	if h.CancelRef(6) || buf.Len() != 0 {
		t.Fatal("a cancel for reference 6 was written while 7 is in flight")
	}
	if !h.CancelRef(7) {
		t.Fatal("did not cancel the MO in flight")
	}
	if got := strings.TrimSpace(buf.String()); got != `{"cmd":"cancel_mo","request_reference":7}` {
		t.Fatalf("helper got %q", got)
	}
	buf.Reset()
	h.moInFlightUntil = time.Now().Add(-time.Second)
	if h.CancelRef(7) || buf.Len() != 0 {
		t.Fatal("cancelled an MO whose window is over")
	}
}
