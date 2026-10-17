package gateway

import (
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
// body is another message.
func TestAPRSRxSeen_Windows(t *testing.T) {
	var s aprsRxSeen
	t0 := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	if s.duplicate("PA3ENC-7", "AB12C", "x", false, t0) {
		t.Fatal("first copy taken as a duplicate")
	}
	if !s.duplicate("PA3ENC-7", "AB12C", "x", false, t0.Add(9*time.Minute)) {
		t.Fatal("a copy with its id 9 minutes later was taken again")
	}
	if s.duplicate("PA3ENC-7", "AB12C", "x", false, t0.Add(11*time.Minute)) {
		t.Fatal("the same id and body after the window was dropped")
	}
	for _, c := range []struct{ from, id, body string }{
		{"PA3ENC-8", "AB12C", "x"}, {"PA3ENC-7", "ZZ99Z", "x"}, {"PA3ENC-7", "AB12C", "y"},
	} {
		if s.duplicate(c.from, c.id, c.body, false, t0.Add(12*time.Minute)) {
			t.Fatalf("%+v taken as a copy of another message", c)
		}
	}
	if s.duplicate("PA3ENC-7", "", "ok", false, t0) || !s.duplicate("PA3ENC-7", "", "ok", false, t0.Add(2*time.Second)) {
		t.Fatal("a plain repeat copy 2 s later was not dropped")
	}
	if s.duplicate("PA3ENC-7", "", "ok", false, t0.Add(time.Minute)) {
		t.Fatal("a plain text sent again a minute later was dropped")
	}
	if s.duplicate("PA3ENC-7", "", "Q0lQSEVS", true, t0) || !s.duplicate("PA3ENC-7", "", "Q0lQSEVS", true, t0.Add(5*time.Minute)) {
		t.Fatal("an identical ciphertext 5 minutes later was not dropped")
	}
}
