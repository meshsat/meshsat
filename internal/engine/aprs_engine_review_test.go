package engine

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
)

type reviewAPRSPeer struct {
	gw     *gateway.APRSGateway
	conn   net.Conn
	frames chan []byte
}

func reviewStartAPRS(t *testing.T) *reviewAPRSPeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	accepted := make(chan net.Conn, 1)
	frames := make(chan []byte, 16)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn
		var frame []byte
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			for _, b := range buf[:n] {
				if b == 0xc0 {
					if len(frame) > 0 {
						if decoded, err := gateway.KISSDecode(frame); err == nil {
							frames <- decoded
						}
					}
					frame = nil
				} else {
					frame = append(frame, b)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	gw := gateway.NewAPRSGateway(gateway.APRSConfig{Callsign: "TEST", SSID: 10, KISSHost: host,
		KISSPort: port, FrequencyMHz: 868, ExternalDirewolf: true, AckAttempts: 1, AckTimeoutMs: 100}, nil)
	if err := gw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gw.Stop() })
	var conn net.Conn
	select {
	case conn = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("no TNC connection")
	}
	t.Cleanup(func() { conn.Close() })
	return &reviewAPRSPeer{gw: gw, conn: conn, frames: frames}
}

func (p *reviewAPRSPeer) ack(t *testing.T, id string) {
	t.Helper()
	frame := gateway.EncodeAX25Frame(gateway.AX25Address{Call: "APMSHT"},
		gateway.AX25Address{Call: "PA3ENC", SSID: 7}, nil, gateway.EncodeAPRSMessage("TEST-10", "ack"+id, ""))
	if _, err := p.conn.Write(gateway.KISSEncode(frame)); err != nil {
		t.Fatal(err)
	}
}

func reviewAPRSSend(t *testing.T, h *intentHarness, p *reviewAPRSPeer) (string, string) {
	t.Helper()
	prov := useAPRS(t, h, &fakeAPRS{})
	prov.gw = p.gw
	id := h.submit(aprsContact(h), "review actual gateway", directory.StrategyPrimaryOnly)
	h.work("aprs_0")
	h.drain()
	select {
	case frame := <-p.frames:
		ax, err := gateway.DecodeAX25Frame(frame)
		if err != nil {
			t.Fatal(err)
		}
		pkt, err := gateway.ParseAPRSPacket(ax)
		if err != nil {
			t.Fatal(err)
		}
		s := h.snap(id)
		if len(s.Transmissions) != 1 || pkt.MsgID != s.Transmissions[0].CorrValue {
			t.Fatalf("wire and durable correlation differ: packet=%+v snapshot=%+v", pkt, s)
		}
		return id, pkt.MsgID
	case <-time.After(time.Second):
		t.Fatal("no message frame")
	}
	return "", ""
}

func reviewWaitAPRSAck(t *testing.T, h *intentHarness, id string) *database.IntentSnapshot {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		h.esc.Heartbeat()
		h.drain()
		s := h.snap(id)
		for _, e := range s.Evidence {
			if e.Event == "ack" && e.Matched && s.Intent.Outcome == database.OutcomeAckedStation {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return h.snap(id)
}

func TestReviewAPRSReceiptSurvivesGatewayRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same_gateway_control"
		if restart {
			name = "fresh_gateway_same_durable_transmission"
		}
		t.Run(name, func(t *testing.T) {
			h := newIntentHarness(t)
			p := reviewStartAPRS(t)
			id, corr := reviewAPRSSend(t, h, p)
			h.clock.Advance(3 * time.Minute)
			h.esc.Heartbeat()
			h.drain()
			if restart {
				if err := p.gw.Stop(); err != nil {
					t.Fatal(err)
				}
				h.restart()
				p = reviewStartAPRS(t)
				prov := useAPRS(t, h, &fakeAPRS{})
				prov.gw = p.gw
				p.gw.SetIntentReceiptSink(h.workers["aprs_0"].intentGatewayReceipt)
			}
			p.ack(t, corr)
			s := reviewWaitAPRSAck(t, h, id)
			n := 0
			for _, e := range s.Evidence {
				if e.Event == "ack" && e.Matched {
					n++
				}
			}
			t.Logf("restart=%v durable_corr=%s transmissions=%d ack_rows=%d state=%s outcome=%s", restart, corr, len(s.Transmissions), n, s.Intent.State, s.Intent.Outcome)
			if n != 1 || s.Intent.Outcome == database.OutcomeAcceptedUnconfirmed || len(s.Transmissions) != 1 {
				t.Fatal("late ack did not resolve the kept transmission")
			}
		})
	}
}

func TestReviewAPRSStatusUsesStationAck(t *testing.T) {
	h := newIntentHarness(t)
	p := reviewStartAPRS(t)
	id, corr := reviewAPRSSend(t, h, p)
	p.ack(t, corr)
	s := reviewWaitAPRSAck(t, h, id)
	view := BuildIntentView(s)
	t.Logf("outcome=%s label=%q completion_rule=%s evidence=%+v", s.Intent.Outcome, view.Label, s.Intent.RoutePlan[0].CompletionRule, s.Evidence)
	if s.Intent.Outcome != database.OutcomeAckedStation || view.Label != "acked by station (unauthenticated)" {
		t.Fatal("APRS ack was promoted beyond the agreed station-ack status")
	}
}
