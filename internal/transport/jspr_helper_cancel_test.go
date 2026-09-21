package transport

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// CancelMO names the send the helper is working on, and does nothing when
// there is none. [MESHSAT-1282]
func TestHelperCancelMO(t *testing.T) {
	var buf bytes.Buffer
	h := &jsprHelperPort{stdin: nopWriteCloser{&buf}}
	if h.CancelMO() {
		t.Fatal("cancelled with no MO in flight")
	}
	h.moRef = 7
	h.moInFlightUntil = time.Now().Add(time.Minute)
	if !h.CancelMO() {
		t.Fatal("did not cancel the MO in flight")
	}
	if got := strings.TrimSpace(buf.String()); got != `{"cmd":"cancel_mo","request_reference":7}` {
		t.Fatalf("helper got %q", got)
	}
	buf.Reset()
	h.moInFlightUntil = time.Now().Add(-time.Second)
	if h.CancelMO() || buf.Len() != 0 {
		t.Fatal("cancelled an MO whose window is over")
	}
}
