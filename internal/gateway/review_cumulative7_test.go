package gateway

// The review of 38f2e17 (18 Oct 2026 13:47 CEST): its cases as supplied
// (RADIO-R2's accepted to unknown, TETRA-RX1, TETRA-TX1, CELL-R1). [MESHSAT-1584]

import (
	"bufio"
	"context"
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"

	"meshsat/internal/smpp"
	"meshsat/internal/tetra"
)

func TestReviewCumulative7_TetraFullQueueMustNotAckOrRemember(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	for i := 0; i < cap(g.inCh); i++ {
		g.inCh <- InboundMessage{Text: "occupied"}
	}
	countAcks := func() int {
		n := 0
		for _, l := range f.sentLinks() {
			if l.Kind == tetra.LinkAck && (l.Ref == 91 || l.Ref == 92) {
				n++
			}
		}
		return n
	}
	incoming := func(ref byte) {
		pdu, bits := tetra.EncodeText(ref, "queue admission review")
		f.incoming("+CTSDSR: 12,100,0,2000,0,"+itoa(bits), hex.EncodeToString(pdu))
	}
	incoming(91)
	waitTetra(t, "queue refusal", func() bool { return g.errs.Load() > 0 })
	deadline := time.Now().Add(250 * time.Millisecond)
	for countAcks() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	firstAcks := countAcks()
	for i := 0; i < cap(g.inCh); i++ {
		<-g.inCh
	}
	incoming(92)
	waitTetra(t, "retry decision", func() bool { return g.msgsIn.Load()+g.acks.repeats.Load() >= 1 })
	waitTetra(t, "ACK for the retry", func() bool { return countAcks() >= firstAcks+1 })
	st := g.TetraStatus()
	t.Logf("acks_while_full=%d total_acks=%d messages_in=%d repeats=%d queue_after_retry=%d", firstAcks, countAcks(), st.MessagesIn, st.Repeats, len(g.inCh))
	if firstAcks != 0 || st.MessagesIn != 1 || len(g.inCh) != 1 {
		t.Fatal("the refused text was acknowledged and remembered; its retry was then suppressed")
	}
}

func TestReviewCumulative7_CellDeliveryFailureIsAmbiguous(t *testing.T) {
	r := startCell(t, nil)
	r.srv.OnSubmit = func(sm smpp.SM) (uint32, time.Duration, string) {
		return smpp.StatusDeliveryFailure, 0, ""
	}
	o := r.gw.IntentSend(context.Background(), IntentSend{Destination: "1001", Text: "delivery failure review", CorrValue: "review-fe"})
	t.Logf("status=0xFE class=%s retryable=%t consume_retry=%t submits=%d detail=%s", o.Class, o.Retryable, o.ConsumeRetry, len(r.srv.Submits()), o.Detail)
	if o.Class != IntentUnknown || o.Retryable {
		t.Fatal("generic delivery failure was treated as proof that nothing was sent")
	}
}

func TestReviewCumulative7_TetraClosedAfterPDUWriteIsUnknown(t *testing.T) {
	client, radio := net.Pipe()
	seen := make(chan string, 1)
	go func() {
		defer radio.Close()
		br := bufio.NewReader(radio)
		if _, err := br.ReadString('\r'); err != nil {
			seen <- "selection read failed: " + err.Error()
			return
		}
		if _, err := radio.Write([]byte("OK\r\n")); err != nil {
			seen <- "selection reply failed: " + err.Error()
			return
		}
		wire, err := br.ReadString('\x1a')
		if err != nil {
			seen <- "payload read failed: " + err.Error()
			return
		}
		seen <- wire
	}()
	cfg := DefaultTetraConfig()
	cfg.Acks = "off"
	g := NewTetraGateway(cfg, "tetra_0", nil)
	g.jitter = func(time.Duration) time.Duration { return 0 }
	g.sess = tetra.NewSession(client, nil, nil)
	t.Cleanup(func() { g.sess.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	o := g.IntentSend(ctx, IntentSend{Destination: "2000", Text: "written before close", CorrValue: "review-close"})
	wire := <-seen
	if !strings.HasPrefix(wire, "AT+CMGS=2000,") || !strings.HasSuffix(wire, "\x1a") {
		t.Fatalf("setup did not consume a complete payload: %q", wire)
	}
	t.Logf("complete_pdu_written=true wire_bytes=%d class=%s retryable=%t detail=%s", len(wire), o.Class, o.Retryable, o.Detail)
	if o.Class != IntentUnknown || o.Retryable {
		t.Fatal("port closure after the complete PDU was written was treated as proof of non-submission")
	}
}
