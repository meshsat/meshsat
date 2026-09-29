package engine

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
)

// sendClass is send with a delivery class.
func (c *chatSendHarness) sendClass(t *testing.T, text, to, class string) *database.MessageDelivery {
	t.Helper()
	id, _, err := c.h.dispatch.QueueDirectSendTo("cellular_0", text, DirectSendOptions{Destination: to, Class: class})
	if err != nil {
		t.Fatal(err)
	}
	del, err := c.h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	c.w.deliver(context.Background(), *del)
	after, err := c.h.db.GetDelivery(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "sent" && after.Status != "delivered" {
		t.Fatalf("%s delivery to %s: %s %q", class, to, after.Status, after.LastError)
	}
	return after
}

// An SOS or an alarm test to an emergency contact goes exactly as given,
// as MeshSat Android sends it through SmsManager whatever its encryption
// settings: with a number key, a wildcard key and an encrypting chain on
// cellular_0, a plain delivery reaches the modem as its text, character for
// character (nothing sealed, compressed, base64'd, attributed, sanitised or
// cut to the 200-byte preview), and the history keeps it unencrypted. The
// same text without plain is sealed as before. The modem here encodes any
// text, as ModemManager does; a kit's AT modem is the test below.
func TestPlainSMS_GoesExactlyAsGiven(t *testing.T) {
	chain := `[{"type":"smaz2"},{"type":"encrypt","params":{"key":"` + chatKeyChain + `"}},{"type":"base64"}]`
	c := newChatSendHarness(t, gateway.CellularConfig{SMSPrefix: "MeshSat", MaxSMSSegments: 3}, chain)
	c.setKey(t, chatPeer, chatKeyNumber)
	c.setKey(t, "*", chatKeyWildcard)

	sos := "SOS: Anna needs help. At 52.37022, 4.89517 at 14:03. https://osm.org/?mlat=52.37022&mlon=4.89517"
	linuxSOS := "SOS: Anna needs help. Position 52.12345, 4.12345 (±12 m). Sent by MeshSat at 12:34 UTC."
	odd := "  [SOS] Βοήθεια! ~ 5€ {x} " + strings.Repeat("y", 200) // sanitising and the preview would change it
	for _, to := range []string{chatPeer, chatOther} {             // its own key; the wildcard's
		for _, text := range []string{sos, linuxSOS, odd} {
			c.sendClass(t, text, to, database.DeliveryClassPlain)
			if got := c.cell.last(t, to); got != text {
				t.Fatalf("plain to %s: modem got %q, want %q", to, got, text)
			}
			if h := lastHistory(t, c.h.db); h.Text != text || h.Encrypted || h.Phone != to {
				t.Fatalf("plain to %s: history %+v, want the text, not encrypted", to, h)
			}
		}
	}

	// Without plain: sealed with the number's key, and with the wildcard.
	for to, key := range map[string]string{chatPeer: chatKeyNumber, chatOther: chatKeyWildcard} {
		c.send(t, sos, to)
		onAir := c.cell.last(t, to)
		if got, ok := androidOpen(onAir, key); !ok || got != sos {
			t.Fatalf("not plain to %s: on air %q reads %q (%v)", to, onAir, got, ok)
		}
		if h := lastHistory(t, c.h.db); h.Text != sos || !h.Encrypted {
			t.Fatalf("not plain to %s: history %+v", to, h)
		}
	}
}

// atRecordingCell is a kit's USB modem: it takes SMS in AT text mode.
type atRecordingCell struct{ *recordingCell }

func (atRecordingCell) SMSTextMode() bool { return true }

// onATModem puts a kit's USB modem behind the harness's delivery worker: a
// gateway with cfg on an AT text-mode modem that records into c.cell.
func (c *chatSendHarness) onATModem(cfg gateway.CellularConfig) {
	if cfg.MaxSMSSegments == 0 {
		cfg.MaxSMSSegments = 3
	}
	c.w.gwProv = cellProvider{gw: gateway.NewCellularGateway(cfg, atRecordingCell{c.cell}, c.h.db)}
}

// On a kit's USB modem (AT text mode) a plain SMS still skips the chat key
// and the chain, but gets the GSM clean-up that modem needs: the
// extension-table characters the Huawei E220 refuses become stand-ins, ± and
// ° readable ones, and the words reach the contact. The history keeps what
// went on air, unencrypted. Without plain, the chat key seals it as before.
func TestPlainSMS_ATModemGetsTheGSMCleanUpOnly(t *testing.T) {
	chain := `[{"type":"smaz2"},{"type":"encrypt","params":{"key":"` + chatKeyChain + `"}},{"type":"base64"}]`
	cfg := gateway.CellularConfig{SMSPrefix: "MeshSat", MaxSMSSegments: 3}
	c := newChatSendHarness(t, cfg, chain)
	c.onATModem(cfg)
	c.setKey(t, chatPeer, chatKeyNumber)
	c.setKey(t, "*", chatKeyWildcard)

	linuxSOS := "SOS: Anna needs help. Position 52.12345, 4.12345 (±12 m). Sent by MeshSat at 12:34 UTC."
	for _, tc := range []struct{ text, onAir string }{
		{linuxSOS, "SOS: Anna needs help. Position 52.12345, 4.12345 (+/-12 m). Sent by MeshSat at 12:34 UTC."},
		{"[SOS] Anna ~ 5€ {x} at 52°N", "(SOS) Anna - 5EUR (x) at 52degN"},
		{"Test from Anna: checking the MeshSat alarm routes.\nNo help needed.", "Test from Anna: checking the MeshSat alarm routes.\nNo help needed."},
	} {
		for _, to := range []string{chatPeer, chatOther} { // its own key; the wildcard's
			c.sendClass(t, tc.text, to, database.DeliveryClassPlain)
			if got := c.cell.last(t, to); got != tc.onAir {
				t.Fatalf("plain %q to %s: modem got %q, want %q", tc.text, to, got, tc.onAir)
			}
			if h := lastHistory(t, c.h.db); h.Text != tc.onAir || h.Encrypted || h.Phone != to {
				t.Fatalf("plain to %s: history %+v, want what went on air, not encrypted", to, h)
			}
		}
	}

	c.send(t, linuxSOS, chatPeer)
	words, ref, ok := c.h.dispatch.TransformPipeline().OpenChatSMS(chatPeer, c.cell.last(t, chatPeer))
	if !ok || string(words) != linuxSOS || ref != "sms:"+chatPeer {
		t.Fatalf("not plain: opened %q with %s (%v), want the text sealed with the number's key", words, ref, ok)
	}
}

// The Bridge's own SOS sends one SMS: the Hub-uplink frame to the Hub's
// number when MQTT and the satellite are out (class hub_uplink). Chat keys
// never touch it either: the frame goes exactly as queued.
func TestHubUplinkSOSFrame_NeverSealed(t *testing.T) {
	const hubNumber = "+3197010258258"
	chain := `[{"type":"encrypt","params":{"key":"` + chatKeyChain + `"}},{"type":"base64"}]`
	c := newChatSendHarness(t, gateway.CellularConfig{MaxSMSSegments: 3}, chain)
	c.setKey(t, hubNumber, chatKeyNumber)
	c.setKey(t, "*", chatKeyWildcard)

	frame := base64.StdEncoding.EncodeToString(hubreporter.EncodeSatSOS("bridge-1", "bridge", 52.37, 4.89, "SOS: help", time.Now().UTC()))
	c.sendClass(t, frame, hubNumber, database.DeliveryClassHubUplink)
	if got := c.cell.last(t, hubNumber); got != frame {
		t.Fatalf("hub uplink frame: modem got %q, want %q", got, frame)
	}
}
