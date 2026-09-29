package engine

import (
	"strings"
	"testing"
	"unicode/utf8"

	"meshsat/internal/database"
	"meshsat/internal/gateway"
)

// longChatText is 300 bytes in two scripts whose 200th byte falls inside a
// Greek letter: the 200-byte preview the worker used to seal cut that letter
// in half and dropped the rest.
const longChatText = "The weather is turning, we are heading down to the hut by the east ridge and will call again from there. Keep the kettle warm. " +
	"Ο καιρός χαλάει, κατεβαίνουμε στο καταφύγιο από την ανατολική κορυφογραμμή. " +
	"See you at six, all of us are OK."

func requireLongChatText(t *testing.T) {
	t.Helper()
	if len(longChatText) != 300 || utf8.ValidString(longChatText[:200]) {
		t.Fatalf("the test text is %d bytes and its first 200 are whole characters (%v): want 300, cut inside one",
			len(longChatText), utf8.ValidString(longChatText[:200]))
	}
}

// A direct SMS to a keyed chat is sealed whole, as POST /api/cellular/sms/send
// checked it, not as its 200-byte preview: what the far end opens is every
// word, and the history keeps them all.
func TestChatKeys_WholeTextSealedNotThePreview(t *testing.T) {
	requireLongChatText(t)
	c := newChatSendHarness(t, gateway.CellularConfig{MaxSMSSegments: 3}, "[]")
	c.setKey(t, chatPeer, chatKeyNumber)

	if del := c.send(t, longChatText, chatPeer); del.Status != "sent" && del.Status != "delivered" {
		t.Fatalf("delivery %s %q", del.Status, del.LastError)
	}
	onAir := c.cell.last(t, chatPeer)
	words, ref, ok := c.h.dispatch.TransformPipeline().OpenChatSMS(chatPeer, onAir)
	if !ok || string(words) != longChatText || ref != "sms:"+chatPeer {
		t.Fatalf("opened %q with %s (%v), want the whole text with the number's key", words, ref, ok)
	}
	if h := lastHistory(t, c.h.db); h.Text != longChatText || !h.Encrypted {
		t.Fatalf("history %+v, want the whole text, encrypted", h)
	}

	// The far end: another Bridge's receive path with this chat's key.
	rec, db, tp := newSMSRecorderHarness(t, "[]")
	tp.SetChatKeyResolver(newFakeChatKeys("sms:+31699999999", chatKeyNumber))
	rec.handleSMSReceived(smsEvent(t, "+31699999999", onAir))
	if h := lastSMS(t, db); h.Text != longChatText || !h.Encrypted {
		t.Fatalf("the far end read %q encrypted=%v", h.Text, h.Encrypted)
	}
}

// With no chat key, a chain that encrypts takes the whole direct SMS too.
func TestChainEncrypt_WholeTextNotThePreview(t *testing.T) {
	requireLongChatText(t)
	chain := `[{"type":"encrypt","params":{"key":"` + chatKeyChain + `"}},{"type":"base64"}]`
	c := newChatSendHarness(t, gateway.CellularConfig{MaxSMSSegments: 3}, chain)

	if del := c.send(t, longChatText, chatPeer); del.Status != "sent" && del.Status != "delivered" {
		t.Fatalf("delivery %s %q", del.Status, del.LastError)
	}
	if got, ok := chainOpen(c.h.dispatch.TransformPipeline(), c.cell.last(t, chatPeer), chain); !ok || got != longChatText {
		t.Fatalf("the chain reads %q (%v), want the whole text", got, ok)
	}
	if h := lastHistory(t, c.h.db); h.Text != longChatText || !h.Encrypted {
		t.Fatalf("history %+v, want the whole text, encrypted", h)
	}
}

// With neither, the whole direct SMS goes in the clear (the GSM clean-up
// turns the Greek into "?", the tail past byte 200 is there).
func TestDirectSMS_WholeTextInTheClear(t *testing.T) {
	requireLongChatText(t)
	c := newChatSendHarness(t, gateway.CellularConfig{MaxSMSSegments: 3}, "[]")

	c.send(t, longChatText, chatPeer)
	got := c.cell.last(t, chatPeer)
	if got != gateway.SanitizeSMSText(longChatText) || !strings.HasSuffix(got, "See you at six, all of us are OK.") {
		t.Fatalf("modem got %q, want the whole text", got)
	}
}

// Sealed whole, a direct SMS too long for the kit's SMS size is not sent at
// all: the gateway would cut it, and a cut ciphertext cannot be read. POST
// /api/cellular/sms/send refuses such a text up front; the senders that do not
// check find the delivery dead with the reason. A text that fits still goes.
func TestSealedSMS_TooLongIsDeadNotCut(t *testing.T) {
	requireLongChatText(t)
	encChain := `[{"type":"encrypt","params":{"key":"` + chatKeyChain + `"}},{"type":"base64"}]`
	for _, tc := range []struct {
		name, chain string
		key         bool
	}{
		{"chat key", "[]", true},
		{"encrypting chain", encChain, false},
	} {
		c := newChatSendHarness(t, gateway.CellularConfig{MaxSMSSegments: 1}, tc.chain)
		if tc.key {
			c.setKey(t, chatPeer, chatKeyNumber)
		}
		del := c.send(t, longChatText, chatPeer)
		if del.Status != "dead" || !strings.Contains(del.LastError, "too long") || !strings.Contains(del.LastError, "at most 160") {
			t.Fatalf("%s: delivery %s %q, want dead as too long", tc.name, del.Status, del.LastError)
		}
		if n := c.cell.count(); n != 0 {
			t.Fatalf("%s: %d SMS left the modem", tc.name, n)
		}

		c.send(t, "ok", chatPeer)
		if n := c.cell.count(); n != 1 {
			t.Fatalf("%s: a short text did not go (%d sent)", tc.name, n)
		}
	}
}

// directSendText takes a direct send's payload as its whole text, and nothing
// else: a routed row, a label over a binary payload, a transformed or custody
// row keep what they had.
func TestDirectSendText(t *testing.T) {
	rule := int64(7)
	for _, tc := range []struct {
		name string
		del  database.MessageDelivery
		want string
		ok   bool
	}{
		{"direct, longer than the preview", database.MessageDelivery{Payload: []byte(longChatText), TextPreview: longChatText[:200]}, longChatText, true},
		{"direct, short", database.MessageDelivery{Payload: []byte("ok"), TextPreview: "ok"}, "ok", true},
		{"routed by a rule", database.MessageDelivery{RuleID: &rule, Payload: []byte(longChatText), TextPreview: longChatText[:200]}, "", false},
		{"binary payload under a label", database.MessageDelivery{Payload: []byte{0x01, 0xff, 0x00}, TextPreview: "hub uplink frame, 3 B"}, "", false},
		{"custody-wrapped", database.MessageDelivery{Payload: []byte{0x43, 0x01}, TextPreview: "[custody:0a0b0c0d] ok"}, "", false},
		{"no preview", database.MessageDelivery{Payload: []byte(`{"decoded_text":"x"}`)}, "", false},
	} {
		got, ok := directSendText(tc.del)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%s: got %q %v, want %q %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
