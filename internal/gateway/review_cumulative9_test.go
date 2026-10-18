package gateway

// The review of c491c811 (18 Oct 2026 15:37 CEST): TETRA-ACK2, its cases
// as supplied. [MESHSAT-1584]

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

type reviewCumulative9Port struct {
	net.Conn
	sdsWrites atomic.Int32
}

func (p *reviewCumulative9Port) Write(b []byte) (int, error) {
	if strings.HasPrefix(string(b), "AT+CMGS=") {
		p.sdsWrites.Add(1)
	}
	return p.Conn.Write(b)
}

func TestReviewCumulative9_AckRequiresAnSDSSubmission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		submit   bool
		response string
		cancel   bool
		wantAck  bool
	}{
		{name: "selection_CME_control", response: "+CME ERROR: 3\r\n"},
		{name: "selection_ERROR_must_not_ack", response: "ERROR\r\n"},
		{name: "selection_cancel_must_not_ack", cancel: true},
		{name: "submitted_cancel_keeps_ack", submit: true, cancel: true, wantAck: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, radio := net.Pipe()
			port := &reviewCumulative9Port{Conn: client}
			t.Cleanup(func() { radio.Close() })
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			serverDone := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			text := "repeated text at reference wrap"
			cfg := DefaultTetraConfig()
			cfg.Acks = "on"
			g := NewTetraGateway(cfg, "tetra_0", nil)
			g.ref.Store(255)
			g.jitter = func(time.Duration) time.Duration { return 0 }
			var matched atomic.Bool
			g.sess = tetra.NewSession(port, func(r tetra.Received) {
				g.onSDS(r)
				l, err := tetra.DecodeLink(r.PDU)
				if err != nil || l.Kind != tetra.LinkAck {
					return
				}
				g.acks.mu.Lock()
				ch := g.acks.pending[tetraAckKey{l.Ref, l.Hash}]
				matched.Store(ch != nil && len(ch) == 1)
				g.acks.mu.Unlock()
				if tc.cancel {
					cancel()
				}
			}, nil)
			t.Cleanup(func() { g.sess.Close() })
			go func() {
				br := bufio.NewReader(radio)
				selection, err := br.ReadString('\r')
				if err != nil || selection != "AT+CTSDS=12,0,0,0,0\r" {
					serverDone <- fmt.Errorf("selection %q: %v", selection, err)
					return
				}
				ref, body := byte(0), tetra.BodyOf(text)
				if tc.submit {
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
						serverDone <- fmt.Errorf("PDU %q: %v", wire, err)
						return
					}
					ref, body = pdu[2], pdu[4:]
				}
				ack, bits := tetra.EncodeLink(tetra.Link{Kind: tetra.LinkAck, Ref: ref, Hash: tetra.TextHash(body)})
				lines := fmt.Sprintf("+CTSDSR: 12,100,0,2000,0,%d\r\n%s\r\n%s", bits, hex.EncodeToString(ack), tc.response)
				_, err = radio.Write([]byte(lines))
				serverDone <- err
				<-release
			}()
			o := g.IntentSend(ctx, IntentSend{Destination: "2000", Text: text, CorrValue: "review-submission-proof"})
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
			if !matched.Load() {
				t.Fatal("setup: ACK was not matched and buffered")
			}
			wantWrites := int32(0)
			if tc.submit {
				wantWrites = 1
			}
			if port.sdsWrites.Load() != wantWrites {
				t.Fatalf("setup: %d SDS writes; want %d", port.sdsWrites.Load(), wantWrites)
			}
			acked := o.Class == IntentAccepted && o.Receipt != nil && o.Receipt.Completes == "acked_station"
			t.Logf("sds_writes=%d matched_ack_buffered=%t class=%s receipt=%+v detail=%s", port.sdsWrites.Load(), matched.Load(), o.Class, o.Receipt, o.Detail)
			if acked != tc.wantAck {
				t.Fatalf("acked=%t; want %t: receipt precedence must depend on the SDS submission phase", acked, tc.wantAck)
			}
		})
	}
}
