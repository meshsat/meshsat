package gateway

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"meshsat/internal/tetra"
	"meshsat/internal/transport"
)

// A Bridge acknowledges a text only once it took it in, and remembers it
// as delivered only then (review 2026-10-18 13:47, TETRA-RX1): a text the
// full receive queue dropped gets no acknowledgement and is not taken for a
// repeat, so the sender's retry comes in once. [MESHSAT-1584]

func tetraAcksFor(f *tetraFake, refs ...byte) int {
	n := 0
	for _, l := range f.sentLinks() {
		if l.Kind != tetra.LinkAck {
			continue
		}
		for _, r := range refs {
			if l.Ref == r {
				n++
			}
		}
	}
	return n
}

func fillTetraQueue(g *TetraGateway) {
	for len(g.inCh) < cap(g.inCh) {
		g.inCh <- InboundMessage{Text: "occupied"}
	}
}

func drainTetraQueue(g *TetraGateway) {
	for len(g.inCh) > 0 {
		<-g.inCh
	}
}

// quiet waits long enough for an acknowledgement that should not come
// (ackText pauses at most 600 ms before it sends).
func quiet() { time.Sleep(900 * time.Millisecond) }

func TestTetraAFullQueueNeitherAcksNorRemembers(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	text := "taken in only once there is room"
	in := func(ref byte) {
		pdu, bits := tetra.EncodeText(ref, text)
		f.incoming("+CTSDSR: 12,100,0,2000,0,"+itoa(bits), hex.EncodeToString(pdu))
	}

	fillTetraQueue(g)
	in(0x51)
	waitTetra(t, "the refusal", func() bool { return g.errs.Load() == 1 })
	quiet()
	if n := tetraAcksFor(f, 0x51); n != 0 {
		t.Fatalf("%d acks for a text the full queue dropped; want none, so the sender tries again", n)
	}

	drainTetraQueue(g)
	in(0x52) // the sender's retry
	waitTetra(t, "the retry's ack", func() bool { return tetraAcksFor(f, 0x52) == 1 })
	if st := g.TetraStatus(); st.MessagesIn != 1 || st.Repeats != 0 || len(g.inCh) != 1 {
		t.Fatalf("the retry: %+v, %d queued; want it taken in once", st, len(g.inCh))
	}

	in(0x53) // its ack lost, the sender tries once more
	waitTetra(t, "the repeat's ack", func() bool { return tetraAcksFor(f, 0x53) == 1 })
	if st := g.TetraStatus(); st.MessagesIn != 1 || st.Repeats != 1 || len(g.inCh) != 1 {
		t.Fatalf("a further copy: %+v, %d queued; want it acknowledged and not delivered again", st, len(g.inCh))
	}
}

// A split text: every part held is acknowledged; the part that completes
// it is acknowledged only once the whole text is in the queue. Dropped
// there, the sender's retry of every part comes in as one text.
func TestTetraASplitTextIsAcknowledgedWhenItsWholeIsTakenIn(t *testing.T) {
	sender := newTetraFake(1)
	tx := startTetra(t, sender, DefaultTetraConfig())
	long := strings.Repeat("a text that needs two SDS ", 6)
	if err := tx.Forward(context.Background(), &transport.MeshMessage{DecodedText: long, AsWritten: true}); err != nil {
		t.Fatal(err)
	}
	parts := sender.sent()
	if len(parts) != 2 {
		t.Fatalf("%d parts; want 2", len(parts))
	}
	// The same parts again, as the retry sends them: new references.
	reref := func(p string, ref byte) string {
		b, _ := hex.DecodeString(p)
		b[2] = ref // PID, transfer byte, then the message reference
		return hex.EncodeToString(b)
	}

	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	in := func(p string, ref byte) { f.incoming("+CTSDSR: 12,100,0,2000,0,0", reref(p, ref)) }

	fillTetraQueue(g)
	in(parts[0], 0x61)
	waitTetra(t, "the first part held and acknowledged", func() bool { return tetraAcksFor(f, 0x61) == 1 })
	in(parts[1], 0x62)
	waitTetra(t, "the refusal", func() bool { return g.errs.Load() == 1 })
	quiet()
	if n := tetraAcksFor(f, 0x62); n != 0 {
		t.Fatalf("the completing part was acknowledged although its text was dropped (%d)", n)
	}

	drainTetraQueue(g)
	in(parts[0], 0x71)
	in(parts[1], 0x72)
	waitTetra(t, "both parts of the retry acknowledged", func() bool { return tetraAcksFor(f, 0x71, 0x72) == 2 })
	select {
	case m := <-g.Receive():
		if strings.Join(strings.Fields(m.Text), " ") != strings.Join(strings.Fields(long), " ") {
			t.Fatalf("reassembled %q", m.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the retry never came in")
	}
	if st := g.TetraStatus(); st.MessagesIn != 1 || st.Repeats != 0 || len(g.inCh) != 0 {
		t.Fatalf("%+v, %d left queued; want one text", st, len(g.inCh))
	}
}

// pipeTetra is a gateway whose radio is a script on a pipe.
func pipeTetra(t *testing.T, script func(br *bufio.Reader, c net.Conn)) *TetraGateway {
	t.Helper()
	client, radio := net.Pipe()
	go func() {
		defer radio.Close()
		script(bufio.NewReader(radio), radio)
	}()
	cfg := DefaultTetraConfig()
	cfg.Acks = "off"
	g := NewTetraGateway(cfg, "tetra_0", nil)
	g.jitter = func(time.Duration) time.Duration { return 0 }
	g.sess = tetra.NewSession(client, nil, nil)
	t.Cleanup(func() { g.sess.Close() })
	return g
}

// The radio's port closing during a send (TETRA-TX1): before the SDS was
// written nothing went, not sent and tried again once the radio is back;
// after the whole SDS was written it may be on air, unknown and never sent
// again by itself. The legacy worker still reads both as the radio gone
// (transport.ErrNotConnected), as before.
func TestTetraIntent_APortClosedBeforeTheSDSIsNotSentAndAfterItUnknown(t *testing.T) {
	send := func(g *TetraGateway) IntentOutcome {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return g.IntentSend(ctx, IntentSend{Destination: "2000", Text: "port closes", CorrValue: "c-close"})
	}
	before := pipeTetra(t, func(br *bufio.Reader, c net.Conn) {
		br.ReadString('\r') // the service selection, never answered
	})
	if o := send(before); o.Class != IntentNotSent || !o.Retryable || o.Reason != "skipped_offline" {
		t.Fatalf("closed before the SDS: %+v", o)
	}

	after := func() *TetraGateway {
		return pipeTetra(t, func(br *bufio.Reader, c net.Conn) {
			br.ReadString('\r')
			c.Write([]byte("OK\r\n"))
			br.ReadString('\x1a') // the whole SDS, never answered
		})
	}
	if o := send(after()); o.Class != IntentUnknown || o.Retryable || !strings.Contains(o.Detail, "may have been sent") {
		t.Fatalf("closed after the SDS was written: %+v", o)
	}
	err := after().Forward(context.Background(), &transport.MeshMessage{DecodedText: "legacy", Destination: "2000", AsWritten: true})
	if !errors.Is(err, transport.ErrNotConnected) || !errors.Is(err, tetra.ErrSDSInFlight) {
		t.Fatalf("legacy Forward: %v; want the radio gone, as before, and the SDS in flight", err)
	}
}
