package gateway

import (
	"context"
	"strings"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// SMS as a delivery-intent route (stage 1b, paid). [MESHSAT-1584]

type submitCell struct {
	*fakeCellTransport
	res  transport.SMSSubmit
	err  error
	sent []string
}

func (c *submitCell) SubmitSMS(_ context.Context, to, text string) (transport.SMSSubmit, error) {
	c.sent = append(c.sent, to+" "+text)
	return c.res, c.err
}

func smsIntentGateway(t *testing.T, cell transport.CellTransport, cfg CellularConfig) (*CellularGateway, *database.DB) {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewCellularGateway(cfg, cell, db), db
}

func TestSMSIntent_Admission(t *testing.T) {
	gw, _ := smsIntentGateway(t, &submitCell{fakeCellTransport: newFakeCellTransport()}, CellularConfig{PlaintextPeers: []string{"+3197010258258"}})
	cases := []struct {
		dest, wire string
		enc        bool
		want       string
	}{
		{"+31612345678", strings.Repeat("a", 160), false, ""},
		{"+31612345678", strings.Repeat("a", 161), false, "skipped_oversize"},
		{"+31612345678", "\x01" + strings.Repeat("Q", 160), true, ""}, // the version byte is not sent
		{"+31612345678", "\x01" + strings.Repeat("Q", 161), true, "skipped_oversize"},
		{"+31612345678", "costs 5€", false, "skipped_encoding"},
		{"+31612345678", "a [bracket]", false, "skipped_encoding"},
		{"+31612345678", "café", false, "skipped_encoding"},
		{"+31612345678", "", false, "skipped_encoding"},
		{"cell:1001", "hi", false, "skipped_bad_address"},
		{"+3197010258258", "\x01QUJD", true, "skipped_encoding"}, // a plain-text peer and an encrypting chain
		{"+3197010258258", "plain to the Hub", false, ""},
	}
	for _, c := range cases {
		if got, _ := gw.IntentAdmit(c.dest, []byte(c.wire), c.enc); got != c.want {
			t.Errorf("admit(%q, %d bytes, enc=%v) = %q; want %q", c.dest, len(c.wire), c.enc, got, c.want)
		}
	}
	plain, _ := smsIntentGateway(t, newFakeCellTransport(), CellularConfig{})
	if got, _ := plain.IntentAdmit("+31612345678", []byte("hi"), false); got != "skipped_unsupported" {
		t.Fatalf("a modem that cannot say whether an SMS left: %q", got)
	}
}

// How far the SMS got decides the outcome; only an accepted SMS is charged.
func TestSMSIntent_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     transport.SMSSubmit
		err     error
		class   string
		charged bool
		reason  string
	}{
		{"accepted", transport.SMSSubmit{Phase: transport.SMSAccepted, Ref: 42}, nil, IntentAccepted, true, ""},
		{"refused", transport.SMSSubmit{Phase: transport.SMSRefused}, errTest("+CMS ERROR: 500"), IntentNotSent, false, "not_sent"},
		{"handed_over", transport.SMSSubmit{Phase: transport.SMSHandedOver}, errTest("timeout"), IntentUnknown, false, ""},
		{"offline", transport.SMSSubmit{}, transport.ErrNotConnected, IntentNotSent, false, "skipped_offline"},
		{"not_sent", transport.SMSSubmit{}, errTest("no prompt"), IntentNotSent, false, "not_sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cell := &submitCell{fakeCellTransport: newFakeCellTransport(), res: tc.res, err: tc.err}
			gw, db := smsIntentGateway(t, cell, CellularConfig{})
			o := gw.IntentSend(context.Background(), IntentSend{Destination: "+31612345678", Text: "\x01QUJD", Encrypted: true,
				PlainText: "the words", CorrValue: "abc"})
			if o.Class != tc.class || o.Charged != tc.charged || o.Reason != tc.reason {
				t.Fatalf("outcome %+v", o)
			}
			if tc.charged && (o.ChargedUnits != 1 || o.ChargeConfirmed || o.AcceptedBy != "carrier") {
				t.Fatalf("an accepted SMS: %+v; want one estimated unit, accepted by the carrier", o)
			}
			if len(cell.sent) != 1 || cell.sent[0] != "+31612345678 QUJD" {
				t.Fatalf("the modem got %q; want the base64 without the version byte", cell.sent)
			}
			if tc.class == IntentAccepted {
				h, _ := db.GetSMSMessages(5, 0)
				if len(h) != 1 || h[0].Text != "the words" || h[0].Status != "sent" {
					t.Fatalf("history %+v; want the words, sent", h)
				}
			}
		})
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
