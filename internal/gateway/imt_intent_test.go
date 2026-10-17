package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"meshsat/internal/transport"
)

// The 9704 as a delivery-intent route (paid). [MESHSAT-1584]

// submitSat is a fakeSat that can say how far an MO got.
type submitSat struct {
	fakeSat
	res  transport.IMTSubmit
	err  error
	mos  [][]byte
	sent int // legacy sends
}

func (s *submitSat) SubmitMO(_ context.Context, data []byte) (transport.IMTSubmit, error) {
	s.mos = append(s.mos, data)
	return s.res, s.err
}

func (s *submitSat) Send(ctx context.Context, data []byte) (*transport.SatResult, error) {
	s.sent++
	return s.fakeSat.Send(ctx, data)
}

const imtFar = "300258060512710"

func imtIntentGateway(sat transport.SatTransport, dest string) *IMTGateway {
	return NewIMTGateway(IridiumConfig{MODestination: dest}, sat, nil, nil)
}

// The 9704 carries no recipient: only the recorded destination of this
// modem's messages is a route, never another address.
func TestIMTIntent_Admission(t *testing.T) {
	gw := imtIntentGateway(&submitSat{}, imtFar)
	for _, c := range []struct {
		dest, wire, want string
	}{
		{imtFar, "\x01QUJD", ""},
		{" " + imtFar + " ", "plain", ""},
		{"300258060902280", "\x01QUJD", "skipped_unsupported"}, // another modem: its message would go to imtFar
		{"", "x", "skipped_unsupported"},
		{imtFar, "", "skipped_encoding"},
		{imtFar, strings.Repeat("Q", transport.IMTOneSegmentBytes), ""},
		{imtFar, strings.Repeat("Q", transport.IMTOneSegmentBytes+1), "skipped_oversize"},
	} {
		if got, _ := gw.IntentAdmit(c.dest, []byte(c.wire), true); got != c.want {
			t.Errorf("admit(%q, %d bytes) = %q; want %q", c.dest, len(c.wire), got, c.want)
		}
	}
	if got, detail := imtIntentGateway(&submitSat{}, "").IntentAdmit(imtFar, []byte("x"), false); got != "skipped_unsupported" || !strings.Contains(detail, "mo_destination") {
		t.Fatalf("no recorded destination: %q %q", got, detail)
	}
	if got, _ := imtIntentGateway(&fakeSat{}, imtFar).IntentAdmit(imtFar, []byte("x"), false); got != "skipped_unsupported" {
		t.Fatalf("a modem that cannot say whether an MO left: %q", got)
	}
}

// How far the MO got decides the outcome; only an acknowledged MO is
// charged, and the intent path never touches the legacy send.
func TestIMTIntent_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		res                  transport.IMTSubmit
		err                  error
		class, reason        string
		charged, retry, cons bool
	}{
		{"acknowledged", transport.IMTSubmit{Phase: transport.IMTAccepted, Status: "mo_ack_received"}, nil, IntentAccepted, "", true, false, false},
		{"may have left", transport.IMTSubmit{Phase: transport.IMTUnknown, Status: "message_cancelled_in_transit"}, nil, IntentUnknown, "", false, false, false},
		{"Go stopped waiting", transport.IMTSubmit{Phase: transport.IMTUnknown}, errors.New("MO helper timeout after 3m0s"), IntentUnknown, "", false, false, false},
		{"no sky", transport.IMTSubmit{Phase: transport.IMTNotSent, Retry: true, Status: "message_cancelled_pre_transit"}, nil, IntentNotSent, "not_sent", false, true, true},
		{"invalid subscription", transport.IMTSubmit{Phase: transport.IMTNotSent, Status: "subscription_invalid"}, nil, IntentNotSent, "not_sent", false, false, true},
		{"modem not connected", transport.IMTSubmit{Phase: transport.IMTNotSent, Retry: true}, fmt.Errorf("imt: %w", transport.ErrNotConnected), IntentNotSent, "skipped_offline", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sat := &submitSat{res: tc.res, err: tc.err}
			gw := imtIntentGateway(sat, imtFar)
			o := gw.IntentSend(context.Background(), IntentSend{Destination: imtFar, Text: "\x01QUJD", Encrypted: true, PlainText: "hi", CorrValue: "c1"})
			if o.Class != tc.class || o.Reason != tc.reason || o.Charged != tc.charged || o.Retryable != tc.retry || o.ConsumeRetry != tc.cons {
				t.Fatalf("%+v", o)
			}
			if o.Charged && (o.ChargedUnits != 1 || o.ChargeConfirmed || o.AcceptedBy != "satellite_gateway") {
				t.Fatalf("charge %+v", o)
			}
			if len(sat.mos) != 1 || string(sat.mos[0]) != "\x01QUJD" || sat.sent != 0 {
				t.Fatalf("MOs %q, legacy sends %d; want the wire once through SubmitMO", sat.mos, sat.sent)
			}
		})
	}
}

// A send refused again before the call (the destination changed since
// admission) makes no MO.
func TestIMTIntent_RefusedBeforeTheCall(t *testing.T) {
	sat := &submitSat{res: transport.IMTSubmit{Phase: transport.IMTAccepted}}
	gw := imtIntentGateway(sat, imtFar)
	o := gw.IntentSend(context.Background(), IntentSend{Destination: "300258060902280", Text: "x"})
	if o.Class != IntentNotSent || o.Reason != "skipped_unsupported" || len(sat.mos) != 0 {
		t.Fatalf("%+v, %d MOs", o, len(sat.mos))
	}
}

// An acknowledged MO lands in the packet feed with the words, not the
// ciphertext.
func TestIMTIntent_FeedShowsTheWords(t *testing.T) {
	sat := &submitSat{res: transport.IMTSubmit{Phase: transport.IMTAccepted, Status: "mo_ack_received"}}
	gw := imtIntentGateway(sat, imtFar)
	sink := newCollectSink()
	gw.SetPacketSink(sink.sink, "iridium_imt_0")
	gw.IntentSend(context.Background(), IntentSend{Destination: imtFar, Text: "\x01QUJD", Encrypted: true, PlainText: "on my way", MsgRef: "m-9"})
	rec := sink.wait(t)
	if rec.Text != "on my way" || rec.MsgRef != "m-9" || rec.Dir != DirTX {
		t.Fatalf("feed %+v", rec)
	}
}
