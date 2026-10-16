package engine

import (
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
)

// APRS-R2 boundaries past the reviewer's tests: the startup path (the
// process-wide sink main.go installs before any gateway starts, with no
// sink set by a send), and the answers that must not complete anything.
// [MESHSAT-1584]

// ackFromStation writes an ack for id from call-ssid to the gateway.
func (p *reviewAPRSPeer) ackFromStation(t *testing.T, call string, ssid int, id string, reject bool) {
	t.Helper()
	word := "ack"
	if reject {
		word = "rej"
	}
	frame := gateway.EncodeAX25Frame(gateway.AX25Address{Call: "APMSHT"},
		gateway.AX25Address{Call: call, SSID: ssid}, nil, gateway.EncodeAPRSMessage("TEST-10", word+id, ""))
	if _, err := p.conn.Write(gateway.KISSEncode(frame)); err != nil {
		t.Fatal(err)
	}
}

// startupRecorder is what main.go does: one recorder, the default sink,
// the Escalator told once it exists.
func startupRecorder(t *testing.T, h *intentHarness) {
	t.Helper()
	rec := NewIntentReceiptRecorder(h.db)
	rec.SetNotify(h.esc.Notify)
	gateway.SetDefaultIntentReceiptSink(rec.Record)
	t.Cleanup(func() { gateway.SetDefaultIntentReceiptSink(nil) })
}

// freshAfterClose sends one message, lets the intent close unconfirmed,
// then restarts the engine and replaces the gateway with one that never
// sent anything and has no sink of its own.
func freshAfterClose(t *testing.T, h *intentHarness) (id, corr string, p *reviewAPRSPeer) {
	t.Helper()
	p = reviewStartAPRS(t)
	id, corr = reviewAPRSSend(t, h, p)
	h.clock.Advance(3 * time.Minute)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("before the late ack: %s %s; want closed accepted_unconfirmed", s.Intent.State, s.Intent.Outcome)
	}
	if err := p.gw.Stop(); err != nil {
		t.Fatal(err)
	}
	h.restart()
	startupRecorder(t, h)
	p = reviewStartAPRS(t)
	prov := useAPRS(t, h, &fakeAPRS{})
	prov.gw = p.gw
	return id, corr, p
}

func matchedAcks(s *database.IntentSnapshot) (matched, completing int) {
	for _, e := range s.Evidence {
		if e.Matched {
			matched++
			if e.Completes != "" {
				completing++
			}
		}
	}
	return
}

// The addressed station's late ack, through the startup sink alone,
// upgrades the closed intent to acked_station, once, with no new send; a
// repeat of the ack changes nothing.
func TestIntentAPRS_LateAckThroughTheStartupSink(t *testing.T) {
	h := newIntentHarness(t)
	id, corr, p := freshAfterClose(t, h)
	p.ack(t, corr)
	s := reviewWaitAPRSAck(t, h, id)
	if s.Intent.Outcome != database.OutcomeAckedStation || len(s.Transmissions) != 1 {
		t.Fatalf("outcome %s, %d transmissions; want acked_station on the one kept transmission", s.Intent.Outcome, len(s.Transmissions))
	}
	version := s.Intent.Version
	p.ack(t, corr) // a station acks every copy it hears
	time.Sleep(100 * time.Millisecond)
	h.esc.Heartbeat()
	h.drain()
	s = h.snap(id)
	if s.Intent.Outcome != database.OutcomeAckedStation || len(s.Transmissions) != 1 || s.Intent.Version != version {
		t.Fatalf("after a repeated ack: outcome %s, %d transmissions, version %d (was %d)", s.Intent.Outcome, len(s.Transmissions), s.Intent.Version, version)
	}
	select {
	case f := <-p.frames:
		t.Fatalf("the fresh gateway sent a frame: %x", f)
	default:
	}
}

// Answers that must not complete the intent: another station's ack and
// reject with the right id, an id no transmission kept, and the right
// station's ack after the window.
func TestIntentAPRS_LateAnswersThatCompleteNothing(t *testing.T) {
	for _, tc := range []string{"other_station_ack", "other_station_reject", "addressed_station_reject", "unknown_id", "out_of_window"} {
		t.Run(tc, func(t *testing.T) {
			h := newIntentHarness(t)
			id, corr, p := freshAfterClose(t, h)
			switch tc {
			case "other_station_ack":
				p.ackFromStation(t, "PD0XYZ", 1, corr, false)
			case "other_station_reject":
				p.ackFromStation(t, "PD0XYZ", 1, corr, true)
			case "addressed_station_reject":
				p.ackFromStation(t, "PA3ENC", 7, corr, true)
			case "unknown_id":
				p.ackFromStation(t, "PA3ENC", 7, "ZZZZ9", false)
			case "out_of_window":
				if _, err := h.db.Exec(`UPDATE delivery_transmissions SET handoff_at = datetime('now', '-25 hours') WHERE intent_id = ?`, id); err != nil {
					t.Fatal(err)
				}
				p.ack(t, corr)
			}
			time.Sleep(150 * time.Millisecond)
			h.esc.Heartbeat()
			h.drain()
			s := h.snap(id)
			_, completing := matchedAcks(s)
			if s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed || completing != 0 || len(s.Transmissions) != 1 {
				t.Fatalf("outcome %s, %d completing, %d transmissions; want it left accepted_unconfirmed", s.Intent.Outcome, completing, len(s.Transmissions))
			}
		})
	}
}
