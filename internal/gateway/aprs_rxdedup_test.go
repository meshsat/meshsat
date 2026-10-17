package gateway

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// A sender's retransmission (our ack was late) is acked again and taken
// once; a different message still gets through. [MESHSAT-1584]
func TestAPRSRx_ACopyIsAckedAndTakenOnce(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{})
	defer gw.Stop()
	time.Sleep(100 * time.Millisecond)

	cipher := "QUFBQUFBQUFBQUE="
	frame := EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, []byte("{E1}"+cipher+"{7K2Q9"))
	tnc.sendRaw(frame)
	time.Sleep(aprsReAckAfter + 300*time.Millisecond) // a sender's retry comes after its ack timeout
	tnc.sendRaw(frame)
	other := "QkJCQkJCQkJCQkI="
	tnc.sendRaw(EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, []byte("{E1}"+other+"{8M3R1")))

	var got []string
	deadline := time.After(6 * time.Second)
	for len(got) < 2 {
		select {
		case msg := <-gw.Receive():
			got = append(got, msg.Text)
		case <-deadline:
			t.Fatalf("got %v, want the two messages", got)
		}
	}
	select {
	case msg := <-gw.Receive():
		t.Fatalf("the copy was taken again: %q", msg.Text)
	case <-time.After(500 * time.Millisecond):
	}
	if got[0] != cipher || got[1] != other {
		t.Fatalf("got %v", got)
	}
	acks := waitTNCFrames(t, tnc, 3, 3*time.Second)
	if len(acks) != 3 {
		t.Fatalf("%d acks, want one for every copy (3)", len(acks))
	}
	if n := gw.rxDuplicates.Load(); n != 1 {
		t.Fatalf("duplicates_dropped %d, want 1", n)
	}
}

// A plain directed message with an id: its copy is acked and dropped.
func TestAPRSRx_APlainMessageCopyIsTakenOnce(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()
	gw := startAckGateway(t, tnc, APRSConfig{})
	defer gw.Stop()
	time.Sleep(100 * time.Millisecond)

	frame := EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, EncodeAPRSMessage("TEST-10", "hello kit", "AB12C"))
	tnc.sendRaw(frame)
	time.Sleep(aprsReAckAfter + 300*time.Millisecond)
	tnc.sendRaw(frame)
	n := 0
	timeout := time.After(1500 * time.Millisecond)
loop:
	for {
		select {
		case <-gw.Receive():
			n++
		case <-timeout:
			break loop
		}
	}
	if n != 1 {
		t.Fatalf("taken %d times, want 1", n)
	}
	waitTNCFrames(t, tnc, 2, 3*time.Second)
}

// The windows: a frame with an id or a ciphertext is a copy for 10
// minutes; a plain text without an id only for 30 s, so a short reply
// sent twice on purpose a minute apart gets through. Another sender, id or
// body is another message. A message the queue refused is not remembered.
func TestAPRSRxSeen_Windows(t *testing.T) {
	var s aprsRxSeen
	took := func() bool { return true }
	full := func() bool { return false }
	t0 := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	if a := s.admit("PA3ENC-7", "AB12C", "x", false, t0, took); a != rxAdmitted {
		t.Fatalf("first copy: %v", a)
	}
	if a := s.admit("PA3ENC-7", "AB12C", "x", false, t0.Add(9*time.Minute), took); a != rxCopy {
		t.Fatalf("a copy with its id 9 minutes later: %v", a)
	}
	if a := s.admit("PA3ENC-7", "AB12C", "x", false, t0.Add(11*time.Minute), took); a != rxAdmitted {
		t.Fatalf("the same id and body after the window: %v", a)
	}
	for _, c := range []struct{ from, id, body string }{
		{"PA3ENC-8", "AB12C", "x"}, {"PA3ENC-7", "ZZ99Z", "x"}, {"PA3ENC-7", "AB12C", "y"},
	} {
		if a := s.admit(c.from, c.id, c.body, false, t0.Add(12*time.Minute), took); a != rxAdmitted {
			t.Fatalf("%+v: %v, want another message", c, a)
		}
	}
	if s.admit("PA3ENC-7", "", "ok", false, t0, took) != rxAdmitted || s.admit("PA3ENC-7", "", "ok", false, t0.Add(2*time.Second), took) != rxCopy {
		t.Fatal("a plain repeat copy 2 s later was not dropped")
	}
	if s.admit("PA3ENC-7", "", "ok", false, t0.Add(time.Minute), took) != rxAdmitted {
		t.Fatal("a plain text sent again a minute later was dropped")
	}
	if s.admit("PA3ENC-7", "", "Q0lQSEVS", true, t0, took) != rxAdmitted || s.admit("PA3ENC-7", "", "Q0lQSEVS", true, t0.Add(5*time.Minute), took) != rxCopy {
		t.Fatal("an identical ciphertext 5 minutes later was not dropped")
	}
	// Refused by a full queue: not remembered, so the next copy is taken.
	if a := s.admit("PA3ENC-7", "RX1AA", "z", false, t0, full); a != rxRejected || a.ackable() {
		t.Fatalf("refused: %v, ackable %v", a, a.ackable())
	}
	if a := s.admit("PA3ENC-7", "RX1AA", "z", false, t0.Add(10*time.Second), took); a != rxAdmitted {
		t.Fatalf("the copy after a refusal: %v, want taken", a)
	}
	// A copy of a taken message is answered without asking the queue.
	asked := false
	if a := s.admit("PA3ENC-7", "RX1AA", "z", false, t0.Add(20*time.Second), func() bool { asked = true; return false }); a != rxCopy || asked || !a.ackable() {
		t.Fatalf("a copy while the queue is full: %v, queue asked %v", a, asked)
	}
}

// Copies arriving at once: one is taken, the rest are copies.
func TestAPRSRxSeen_ConcurrentCopiesAreTakenOnce(t *testing.T) {
	var s aprsRxSeen
	var mu sync.Mutex
	enqueued := 0
	results := make(chan rxAdmission, 64)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- s.admit("PA3ENC-7", "CC001", "same", true, time.Now(), func() bool {
				mu.Lock()
				enqueued++
				mu.Unlock()
				return true
			})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	taken, copies := 0, 0
	for a := range results {
		switch a {
		case rxAdmitted:
			taken++
		case rxCopy:
			copies++
		}
	}
	if taken != 1 || copies != 63 || enqueued != 1 {
		t.Fatalf("taken %d, copies %d, enqueued %d; want 1, 63, 1", taken, copies, enqueued)
	}
}

// ackFrames counts the acks for id the gateway handed the TNC.
func ackFrames(t *testing.T, tnc *mockKISSTNC, id string) int {
	t.Helper()
	want := string(EncodeAPRSMessage(FormatCallsign(testPeer), "ack"+id, ""))
	n := 0
	for _, f := range tnc.frames() {
		if ax, err := DecodeAX25Frame(f); err == nil && string(ax.Info) == want {
			n++
		}
	}
	return n
}

// Through the KISS read loop, plain and encrypted: a frame the full queue
// refused gets no ack and is not counted as a copy; its next copy is taken
// and acked; a copy of it after that is acked again and dropped, also while
// the queue is full. [review APRS-RX1]
func TestAPRSRx_RefusedIsNotAckedTakenCopyIsAckedWhileFull(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted_%v", encrypted), func(t *testing.T) {
			tnc := newMockKISSTNC(t)
			defer tnc.close()
			g := startAckGateway(t, tnc, APRSConfig{})
			defer g.Stop()
			frames, unsubscribe := g.SubscribeFrames()
			defer unsubscribe()
			text := "recover me"
			body := EncodeAPRSMessage("TEST-10", text, "R0001")
			if encrypted {
				text = "QUFBQUFBQUFBQUE="
				body = []byte("{E1}" + text + "{R0001")
			}
			target := EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, body)
			n := 0
			step := func(after func()) {
				n++
				barrier := EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, []byte(fmt.Sprintf(">barrier %d", n)))
				tnc.sendRaw(target)
				tnc.sendRaw(barrier)
				reviewWaitWireFrame(t, frames, barrier)
				time.Sleep(100 * time.Millisecond) // the ack goes out through the write worker
				after()
			}

			// 1. Refused by the full queue: no ack, not a copy.
			reviewFillInbound(g)
			step(func() {
				if g.msgsIn.Load() != 0 || ackFrames(t, tnc, "R0001") != 0 || g.rxDuplicates.Load() != 0 {
					t.Fatalf("refused: in %d, acks %d, copies %d; want 0, 0, 0", g.msgsIn.Load(), ackFrames(t, tnc, "R0001"), g.rxDuplicates.Load())
				}
			})
			reviewDrainInbound(g)

			// 2. The sender's next copy: taken and acked.
			time.Sleep(aprsReAckAfter + 100*time.Millisecond)
			step(func() {
				got := reviewDrainInbound(g)
				if len(got) != 1 || !strings.HasSuffix(got[0].Text, text) || ackFrames(t, tnc, "R0001") != 1 {
					t.Fatalf("retry: taken %v, acks %d; want the message once and one ack", got, ackFrames(t, tnc, "R0001"))
				}
			})

			// 3. Another copy while the queue is full: acked again, dropped,
			// counted as a copy.
			reviewFillInbound(g)
			time.Sleep(aprsReAckAfter + 100*time.Millisecond)
			step(func() {
				if ackFrames(t, tnc, "R0001") != 2 || g.rxDuplicates.Load() != 1 || g.msgsIn.Load() != 1 {
					t.Fatalf("copy while full: acks %d, copies %d, taken %d; want 2, 1, 1", ackFrames(t, tnc, "R0001"), g.rxDuplicates.Load(), g.msgsIn.Load())
				}
			})
			for _, m := range reviewDrainInbound(g) {
				if m.Source != "fixture" {
					t.Fatalf("a copy reached the queue: %+v", m)
				}
			}
		})
	}
}

// APRS-IS takes the same path: a message the full queue refused gets no
// ack; the sender's next copy is taken and acked once. [review APRS-RX1]
func TestAPRSIS_RefusedIsNotAckedNextCopyIsTaken(t *testing.T) {
	setAPRSISKnobs(t, 50*time.Millisecond)
	f := newFakeAPRSIS(t, logrespVerified)
	g := newISGateway(t, f, nil)
	startGateway(t, g)
	f.waitLogins(t, 1, 5*time.Second)
	waitISState(t, g, APRSStateConnected, 5*time.Second)
	ack := "N0CALL-10>APMSHT,TCPIP*::PA3ABC-5 :ackR7"
	acks := func() int {
		_, lines, _ := f.snapshot()
		n := 0
		for _, l := range lines {
			if l == ack {
				n++
			}
		}
		return n
	}
	waitRX := func(n int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for g.is.rx.Load() < n {
			if time.Now().After(deadline) {
				t.Fatalf("lines decoded: %d, want %d", g.is.rx.Load(), n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	reviewFillInbound(g)
	f.send(t, "PA3ABC-5>APRS,TCPIP*::N0CALL-10:hi again{R7")
	waitRX(1)
	time.Sleep(300 * time.Millisecond)
	if g.msgsIn.Load() != 0 || acks() != 0 || g.rxDuplicates.Load() != 0 {
		t.Fatalf("refused: in %d, acks %d, copies %d; want 0, 0, 0", g.msgsIn.Load(), acks(), g.rxDuplicates.Load())
	}
	reviewDrainInbound(g)

	time.Sleep(aprsReAckAfter + 100*time.Millisecond)
	f.send(t, "PA3ABC-5>APRS,TCPIP*::N0CALL-10:hi again{R7")
	select {
	case msg := <-g.Receive():
		if msg.Text != "[APRS:PA3ABC-5→N0CALL-10] hi again" {
			t.Fatalf("inbound %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the next copy never reached the inbound channel")
	}
	f.waitLine(t, ack, 5*time.Second)
	if n := acks(); n != 1 {
		t.Fatalf("%d acks, want 1", n)
	}
}
