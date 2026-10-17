package gateway

// The closure review of 8ebfaa1 (17 Oct 2026 18:10 CEST), its APRS-RX1
// cases as supplied: a frame the full inbound queue refused must be taken
// when its sender sends it again, through the parsed handler and both
// KISS receive branches. [MESHSAT-1584]

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func reviewPressurePacket(t *testing.T, text, id string) *APRSPacket {
	t.Helper()
	frame, err := DecodeAX25Frame(EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, EncodeAPRSMessage("TEST-10", text, id)))
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := ParseAPRSPacket(frame)
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func reviewFillInbound(g *APRSGateway) {
	for len(g.inCh) < cap(g.inCh) {
		g.inCh <- InboundMessage{Text: "occupied", Source: "fixture"}
	}
}

func reviewDrainInbound(g *APRSGateway) []InboundMessage {
	var messages []InboundMessage
	for {
		select {
		case message := <-g.inCh:
			messages = append(messages, message)
		default:
			return messages
		}
	}
}

func reviewWaitWireFrame(t *testing.T, frames <-chan []byte, want []byte) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case frame := <-frames:
			if bytes.Equal(frame, want) {
				return
			}
		case <-deadline:
			t.Fatal("the local read worker did not reach its barrier frame")
		}
	}
}

func TestReviewCumulative3ParsedRetryAfterQueuePressure(t *testing.T) {
	g := NewAPRSGateway(APRSConfig{Callsign: "TEST", SSID: 10}, nil)
	packet := reviewPressurePacket(t, "recover me", "Q0001")
	reviewFillInbound(g)
	g.handleParsedPacket(packet, packet.Source)
	if g.msgsIn.Load() != 0 {
		t.Fatal("the full-queue setup accepted the first frame")
	}
	reviewDrainInbound(g)
	g.handleParsedPacket(packet, packet.Source)
	messages := reviewDrainInbound(g)
	t.Logf("first attempt admitted=0; same frame after capacity returns: admitted=%d, messages=%v", g.msgsIn.Load(), messages)
	if len(messages) != 1 {
		t.Fatal("a frame never admitted to the receive queue was remembered as already taken")
	}
}

func TestReviewCumulative3WireRetryAfterQueuePressure(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted_%v", encrypted), func(t *testing.T) {
			tnc := newMockKISSTNC(t)
			defer tnc.close()
			g := startAckGateway(t, tnc, APRSConfig{})
			defer g.Stop()
			frames, unsubscribe := g.SubscribeFrames()
			defer unsubscribe()
			makeFrame := func(text, id string) []byte {
				body := EncodeAPRSMessage("TEST-10", text, id)
				if encrypted {
					body = []byte("{E1}" + text + "{" + id)
				}
				return EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, body)
			}
			targetText, otherText := "recover me", "fresh control"
			if encrypted {
				targetText, otherText = "QUFBQUFBQUFBQUE=", "QkJCQkJCQkJCQkI="
			}
			target := makeFrame(targetText, "Q0001")
			barrier := EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, []byte(">review first barrier"))
			reviewFillInbound(g)
			tnc.sendRaw(target)
			tnc.sendRaw(barrier)
			reviewWaitWireFrame(t, frames, barrier)
			if g.msgsIn.Load() != 0 {
				t.Fatal("the full-queue setup accepted the first frame")
			}
			firstAcks := g.acksSent.Load()
			reviewDrainInbound(g)
			time.Sleep(aprsReAckAfter + 100*time.Millisecond)
			tnc.sendRaw(target)
			tnc.sendRaw(makeFrame(otherText, "Q0002"))
			barrier = EncodeAX25Frame(AX25Address{Call: "APMSHT"}, testPeer, nil, []byte(">review second barrier"))
			tnc.sendRaw(barrier)
			reviewWaitWireFrame(t, frames, barrier)
			messages := reviewDrainInbound(g)
			t.Logf("encrypted=%v; initial full-queue acks=%d; total acks=%d; after recovery admitted=%d messages=%v", encrypted, firstAcks, g.acksSent.Load(), g.msgsIn.Load(), messages)
			if len(messages) != 2 {
				t.Fatal("the retried target was lost while the fresh control was admitted")
			}
		})
	}
}
