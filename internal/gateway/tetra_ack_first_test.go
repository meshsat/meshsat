package gateway

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"meshsat/internal/tetra"
	"meshsat/internal/transport"
)

// A MeshSat Bridge's acknowledgement of the SDS that came while the radio
// still had it outweighs the radio's later account of a failure (review
// 2026-10-18 14:53, TETRA-ACK1); anything short of that matched
// acknowledgement leaves the outcome as before. [MESHSAT-1584]

// ackFirstRadio is a scripted PEI: it takes the selection and the SDS, then
// writes what after(ref, body) returns and, when hangUp, closes the port.
func ackFirstRadio(t *testing.T, after func(ref byte, body []byte) string, hangUp bool) *TetraGateway {
	t.Helper()
	client, radio := net.Pipe()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	go func() {
		defer radio.Close()
		br := bufio.NewReader(radio)
		if _, err := br.ReadString('\r'); err != nil {
			return
		}
		radio.Write([]byte("OK\r\n"))
		br.ReadString('\n')
		wire, err := br.ReadString('\x1a')
		if err != nil {
			return
		}
		pdu, err := hex.DecodeString(strings.TrimSuffix(wire, "\x1a"))
		if err != nil || len(pdu) < 5 {
			return
		}
		radio.Write([]byte(after(pdu[2], pdu[4:])))
		if !hangUp {
			<-release
		}
	}()
	cfg := DefaultTetraConfig()
	cfg.Acks = "on"
	g := NewTetraGateway(cfg, "tetra_0", nil)
	g.jitter = func(time.Duration) time.Duration { return 0 }
	g.ackTimeout = 300 * time.Millisecond
	g.sess = tetra.NewSession(client, g.onSDS, nil)
	t.Cleanup(func() { g.sess.Close() })
	return g
}

// ackLine is a link SDS acknowledging ref and the hash of body.
func ackLine(ref byte, body []byte) string {
	ack, bits := tetra.EncodeLink(tetra.Link{Kind: tetra.LinkAck, Ref: ref, Hash: tetra.TextHash(body)})
	return fmt.Sprintf("+CTSDSR: 12,100,0,2000,0,%d\r\n%s\r\n", bits, hex.EncodeToString(ack))
}

func intentSendTo(g *TetraGateway, text string) IntentOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return g.IntentSend(ctx, IntentSend{Destination: "2000", Text: text, CorrValue: "c-ack-first"})
}

func TestTetraIntent_AnAckBeforeTheRadiosFailureReportWinsAndNothingLessDoes(t *testing.T) {
	failed := func(ref byte) string {
		return fmt.Sprintf("+CME ERROR: 42\r\n+CMGS: 0,5,%d\r\n", ref)
	}
	acked := func(o IntentOutcome) bool {
		return o.Class == IntentAccepted && o.Receipt != nil && o.Receipt.Completes == "acked_station" && o.Receipt.Trust == "unauthenticated"
	}
	unknown := func(o IntentOutcome) bool {
		return o.Class == IntentUnknown && o.Receipt == nil && !o.Retryable
	}

	t.Run("acked then status 5: acked, the report kept", func(t *testing.T) {
		g := ackFirstRadio(t, func(ref byte, body []byte) string {
			return "+CMGS: 0\r\nOK\r\n" + ackLine(ref, body) + failed(ref)
		}, false)
		o := intentSendTo(g, "acked first")
		if !acked(o) || !strings.Contains(o.Detail, "status 5") || !strings.Contains(o.Detail, "CME ERROR: 42") {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("no ack, status 5: unknown", func(t *testing.T) {
		g := ackFirstRadio(t, func(ref byte, body []byte) string { return "+CMGS: 0\r\nOK\r\n" + failed(ref) }, false)
		if o := intentSendTo(g, "nobody acked"); !unknown(o) {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("an ack for other bytes, status 5: unknown", func(t *testing.T) {
		g := ackFirstRadio(t, func(ref byte, body []byte) string {
			return "+CMGS: 0\r\nOK\r\n" + ackLine(ref, []byte("another text")) + failed(ref)
		}, false)
		if o := intentSendTo(g, "wrong hash"); !unknown(o) {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("an ack for another reference, status 5: unknown", func(t *testing.T) {
		g := ackFirstRadio(t, func(ref byte, body []byte) string {
			return "+CMGS: 0\r\nOK\r\n" + ackLine(ref+1, body) + failed(ref)
		}, false)
		if o := intentSendTo(g, "wrong reference"); !unknown(o) {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("acked, then the port closes before the radio answered: acked", func(t *testing.T) {
		g := ackFirstRadio(t, func(ref byte, body []byte) string { return ackLine(ref, body) }, true)
		o := intentSendTo(g, "acked then gone")
		if !acked(o) || !strings.Contains(o.Detail, "port closed") {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("an ack, then the radio refuses the command: not sent", func(t *testing.T) {
		// Refused before anything went on air, so an ack with the same
		// reference and text can only be an old one.
		g := ackFirstRadio(t, func(ref byte, body []byte) string { return ackLine(ref, body) + "+CME ERROR: 3\r\n" }, false)
		if o := intentSendTo(g, "refused"); o.Class != IntentNotSent || o.Reason != "not_sent" || o.Receipt != nil {
			t.Fatalf("%+v", o)
		}
	})
}

// The legacy worker gets no error for a text a MeshSat Bridge acknowledged
// before the radio reported a failure, so it does not send it again.
func TestTetraForward_AnAckBeforeTheRadiosFailureReportIsDelivered(t *testing.T) {
	g := ackFirstRadio(t, func(ref byte, body []byte) string {
		return "+CMGS: 0\r\nOK\r\n" + ackLine(ref, body) + fmt.Sprintf("+CMGS: 0,5,%d\r\n", ref)
	}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := g.Send(ctx, &transport.MeshMessage{DecodedText: "legacy acked first", Destination: "2000", AsWritten: true})
	if err != nil || !res.Confirmed || !strings.Contains(res.RadioReport, "status 5") {
		t.Fatalf("%+v %v", res, err)
	}
	if st := g.TetraStatus(); st.MessagesOut != 1 || st.NotSent != 0 {
		t.Fatalf("counters %+v", st)
	}
}
