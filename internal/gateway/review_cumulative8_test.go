package gateway

// The review of 69ad4632 (18 Oct 2026 14:53 CEST): TETRA-ACK1, its case as
// supplied. [MESHSAT-1584]

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/tetra"
)

func TestReviewCumulative8_MatchedAckBeforeFailureMustSurvive(t *testing.T) {
	for _, status := range []int{4, 5} {
		t.Run(fmt.Sprintf("radio_status_%d", status), func(t *testing.T) {
			reviewMatchedAckBeforeReport(t, status)
		})
	}
}

func reviewMatchedAckBeforeReport(t *testing.T, status int) {
	t.Helper()
	client, radio := net.Pipe()
	defer radio.Close()
	var matched atomic.Bool
	var receivedRef atomic.Uint32
	serverDone := make(chan error, 1)
	release := make(chan struct{})
	defer close(release)
	go func() {
		br := bufio.NewReader(radio)
		if _, err := br.ReadString('\r'); err != nil {
			serverDone <- err
			return
		}
		if _, err := radio.Write([]byte("OK\r\n")); err != nil {
			serverDone <- err
			return
		}
		if _, err := br.ReadString('\n'); err != nil {
			serverDone <- err
			return
		}
		wire, err := br.ReadString('\x1a')
		if err != nil {
			serverDone <- err
			return
		}
		pdu, err := hex.DecodeString(strings.TrimSuffix(wire, "\x1a"))
		if err != nil || len(pdu) < 5 {
			serverDone <- fmt.Errorf("invalid submitted PDU %q: %v", wire, err)
			return
		}
		ref := pdu[2]
		receivedRef.Store(uint32(ref))
		ack, bits := tetra.EncodeLink(tetra.Link{Kind: tetra.LinkAck, Ref: ref, Hash: tetra.TextHash(pdu[4:])})
		lateError := ""
		if status == 5 {
			lateError = "+CME ERROR: 42\r\n"
		}
		lines := fmt.Sprintf("+CMGS: 0\r\nOK\r\n+CTSDSR: 12,100,0,2000,0,%d\r\n%s\r\n%s+CMGS: 0,%d,%d\r\n", bits, hex.EncodeToString(ack), lateError, status, ref)
		_, err = radio.Write([]byte(lines))
		serverDone <- err
		<-release
	}()
	cfg := DefaultTetraConfig()
	cfg.Acks = "on"
	g := NewTetraGateway(cfg, "tetra_0", nil)
	g.jitter = func(time.Duration) time.Duration { return 0 }
	g.ackTimeout = 400 * time.Millisecond
	g.sess = tetra.NewSession(client, func(r tetra.Received) {
		g.onSDS(r)
		l, err := tetra.DecodeLink(r.PDU)
		if err != nil || l.Kind != tetra.LinkAck {
			return
		}
		g.acks.mu.Lock()
		ch := g.acks.pending[tetraAckKey{l.Ref, l.Hash}]
		matched.Store(ch != nil && len(ch) == 1)
		g.acks.mu.Unlock()
	}, nil)
	t.Cleanup(func() { g.sess.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	o := g.IntentSend(ctx, IntentSend{Destination: "2000", Text: "ACK before radio failure", CorrValue: "review-ack-proof"})
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if !matched.Load() {
		t.Fatal("setup: the ACK did not match the active reference and payload hash before the failure report")
	}
	t.Logf("matched_ack_buffered_before_report=%t radio_status=%d ref=%d class=%s receipt=%+v detail=%s", matched.Load(), status, receivedRef.Load(), o.Class, o.Receipt, o.Detail)
	if o.Class != IntentAccepted || o.Receipt == nil || o.Receipt.Completes != "acked_station" || o.Receipt.Trust != "unauthenticated" {
		t.Fatal("an already received matching station ACK was discarded when the radio later reported failure")
	}
}
