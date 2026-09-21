package main

import (
	"testing"
	"time"

	"meshsat/internal/oob"
)

// An OOB mesh reset waits for the Meshtastic config handshake. The executor's
// deadline must outlast it, or a reconnect that succeeds is reported as
// agent_error "context deadline exceeded" (MESHSAT-810: 30 s against a 60 s
// handshake).
func TestOOBExecTimeoutOutlastsMeshHandshake(t *testing.T) {
	for _, hs := range []time.Duration{0, 60 * time.Second, 120 * time.Second, 300 * time.Second} {
		effective := hs
		if effective <= 0 {
			effective = 60 * time.Second // the transport's default
		}
		got := oobExecTimeout(effective, oob.ExecTimeout)
		if got < effective+oobMargin {
			t.Errorf("handshake %s: exec deadline %s leaves less than %s margin", effective, got, oobMargin)
		}
		if got < oob.ExecTimeout {
			t.Errorf("handshake %s: exec deadline %s is below the default %s", effective, got, oob.ExecTimeout)
		}
	}
	if oob.ExecTimeout < 60*time.Second+oobMargin {
		t.Fatalf("oob.ExecTimeout %s is shorter than the default 60 s handshake plus margin", oob.ExecTimeout)
	}
}
