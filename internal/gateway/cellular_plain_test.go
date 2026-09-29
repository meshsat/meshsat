package gateway

import (
	"context"
	"sync"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// fakeATModem is a kit's USB modem: it takes SMS in AT text mode.
type fakeATModem struct{ *fakeCellTransport }

func (fakeATModem) SMSTextMode() bool { return true }

// A plain text (an SOS or an alarm test to an emergency contact) skips the
// chat key, the chain and the attribution, but a kit's USB modem cannot carry
// every character: there it gets the GSM clean-up every other SMS gets, so
// the words reach the contact (the Huawei E220 fails the whole SMS on an
// extension-table character). The history and the packet feed show what went
// on air. ModemManager encodes any text and gets it exactly as written, € and
// Greek included. An OOB frame stays verbatim on either.
func TestCellular_PlainTextCleanedUpOnATModemOnly(t *testing.T) {
	const contact = "+31612345678"
	// The Linux app's SOS, with the accuracy as ±.
	const sos = "SOS: Anna needs help. Position 52.12345, 4.12345 (±12 m). Sent by MeshSat at 12:34 UTC."
	// Every extension-table character, Greek outside GSM's basic set, and a degree.
	const odd = "[SOS] {Anna} ~ 5€ a|b\\c ^ Βοήθεια 45°"
	// An OOB frame's alphabet is GSM-safe by construction; the brackets here
	// only show that the clean-up keys on AsWritten, not on RawText.
	const frame = "MS:9W899JR[0]~"

	send := func(t *testing.T, cell transport.CellTransport) (history []database.SMSMessageRecord, feed map[string]string) {
		t.Helper()
		db, err := database.New(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		gw := NewCellularGateway(CellularConfig{SMSPrefix: "[MeshSat]", MaxSMSSegments: 3}, cell, db)
		var mu sync.Mutex
		feed = map[string]string{}
		gw.SetPacketSink(func(r PacketRecord) {
			mu.Lock()
			feed[r.To+" "+r.MsgRef] = r.Text
			mu.Unlock()
		}, "cellular_0")
		for _, m := range []*transport.MeshMessage{
			{DecodedText: sos, RawText: true, AsWritten: true, MsgRef: "sos"},
			{DecodedText: odd, RawText: true, AsWritten: true, MsgRef: "odd"},
			{DecodedText: frame, RawText: true, MsgRef: "frame"},
		} {
			m.Destination, m.SMSDestinations = contact, []string{contact}
			if err := gw.sendSMSSync(context.Background(), m); err != nil {
				t.Fatalf("%s: %v", m.MsgRef, err)
			}
		}
		history, err = db.GetSMSMessages(10, 0)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return history, feed
	}

	for _, c := range []struct {
		name     string
		at       bool
		sos, odd string // on air
	}{
		{"kit's USB modem (AT text mode)", true,
			"SOS: Anna needs help. Position 52.12345, 4.12345 (+/-12 m). Sent by MeshSat at 12:34 UTC.",
			"(SOS) (Anna) - 5EUR a/b/c ' ??????? 45deg"},
		{"ModemManager", false, sos, odd},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := newFakeCellTransport()
			var cell transport.CellTransport = base
			if c.at {
				cell = fakeATModem{base}
			}
			history, feed := send(t, cell)

			sends := base.sends()
			if len(sends) != 3 {
				t.Fatalf("%d SMS sent, want 3: %q", len(sends), sends)
			}
			for i, want := range []string{c.sos, c.odd, frame} {
				if sends[i][0] != contact || sends[i][1] != want {
					t.Fatalf("SMS %d: modem got %q to %s, want %q", i, sends[i][1], sends[i][0], want)
				}
			}
			words := map[string]bool{}
			for _, r := range history {
				if r.Encrypted || r.Status != "sent" {
					t.Fatalf("history row %+v", r)
				}
				words[r.Text] = true
			}
			if !words[c.sos] || !words[c.odd] {
				t.Fatalf("history %v, want what went on air", history)
			}
			if feed[contact+" sos"] != c.sos || feed[contact+" odd"] != c.odd {
				t.Fatalf("packet feed %q, want what went on air", feed)
			}
		})
	}
}
