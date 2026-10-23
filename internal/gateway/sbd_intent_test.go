package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"meshsat/internal/transport"
)

// The 9603 as a delivery-intent route: admission only to the recorded
// destination and only what fits whole, the MO byte for byte as the legacy
// path builds it, the frame's size billed, and each kind of session read
// as not sent, taken or unknown. [MESHSAT-1584]

const sbdDest = "300434067943980"

// scriptSat answers each send with the next scripted result and error.
type scriptSat struct {
	fakeSat
	results []*transport.SatResult
	errs    []error
}

func (s *scriptSat) next() (*transport.SatResult, error) {
	r, e := s.results[0], s.errs[0]
	if len(s.results) > 1 {
		s.results, s.errs = s.results[1:], s.errs[1:]
	}
	return r, e
}
func (s *scriptSat) Send(ctx context.Context, data []byte) (*transport.SatResult, error) {
	s.sent = append(s.sent, data)
	return s.next()
}
func (s *scriptSat) SendText(ctx context.Context, text string) (*transport.SatResult, error) {
	s.texts = append(s.texts, text)
	return s.next()
}

func sbdIntentGW(t *testing.T, cfg IridiumConfig, sat transport.SatTransport) *SBDGateway {
	t.Helper()
	cfg.MODestination = sbdDest
	return NewSBDGateway(cfg, sat, testDB(t), nil)
}

func TestSBDIntent_AdmitsTheRecordedDestinationAndWhatFitsWhole(t *testing.T) {
	gw := sbdIntentGW(t, IridiumConfig{}, &fakeSat{})
	if r, _ := gw.IntentAdmit(sbdDest, []byte("hello"), false); r != "" {
		t.Fatalf("the recorded destination: %q", r)
	}
	if r, _ := gw.IntentAdmit("300434067943981", []byte("hello"), false); r != "skipped_unsupported" {
		t.Fatalf("another modem's address: %q; the 9603 carries no recipient", r)
	}
	if r, _ := NewSBDGateway(IridiumConfig{}, &fakeSat{}, nil, nil).IntentAdmit(sbdDest, []byte("x"), false); r != "skipped_unsupported" {
		t.Fatalf("no mo_destination: %q", r)
	}
	// The compact frame takes 255 bytes of text whole (329 of room, capped
	// by its one length byte); 256 would be cut, so it is refused.
	room := gw.compactTextRoom()
	if room != 255 {
		t.Fatalf("room %d", room)
	}
	bin := bytes.Repeat([]byte{0x01}, room)
	if r, _ := gw.IntentAdmit(sbdDest, bin, true); r != "" {
		t.Fatalf("%d bytes: %q", room, r)
	}
	if r, d := gw.IntentAdmit(sbdDest, append(bin, 0x02), true); r != "skipped_oversize" {
		t.Fatalf("%d bytes: %q %q; want skipped_oversize, never cut", room+1, r, d)
	}
	// With the position field the frame has 319 bytes of room, still more
	// than the length byte allows: 255 either way.
	if pos := sbdIntentGW(t, IridiumConfig{IncludePosition: true}, &fakeSat{}); pos.compactTextRoom() != 255 {
		t.Fatalf("room with position %d", pos.compactTextRoom())
	}
	if r, _ := gw.IntentAdmit(sbdDest, nil, false); r != "skipped_encoding" {
		t.Fatalf("empty: %q", r)
	}
}

// The intent's MO is the legacy path's MO for the same row, byte for byte:
// short printable text as text, anything else (the version byte of an
// encrypted chain) in the compact frame. The billed size is that MO.
func TestSBDIntent_TheMOIsTheLegacyPathsMO(t *testing.T) {
	for _, wire := range [][]byte{
		[]byte("short plain text"),
		append([]byte{0x01}, []byte(strings.Repeat("QUJD", 30))...), // version byte + base64
		[]byte(strings.Repeat("long plain text ", 10)),              // over 120: compact
	} {
		legacySat, intentSat := &fakeSat{}, &fakeSat{}
		legacy := sbdIntentGW(t, IridiumConfig{}, legacySat)
		in := sbdIntentGW(t, IridiumConfig{}, intentSat)
		if err := legacy.sendSBD(context.Background(), &transport.MeshMessage{PortNum: 1, PortNumName: "TEXT_MESSAGE_APP", DecodedText: string(wire)}); err != nil {
			t.Fatal(err)
		}
		o := in.IntentSend(context.Background(), IntentSend{Destination: sbdDest, Text: string(wire), CorrValue: "c1"})
		if o.Class != IntentAccepted {
			t.Fatalf("%q: %+v", wire[:8], o)
		}
		if fmt.Sprint(legacySat.sent, legacySat.texts) != fmt.Sprint(intentSat.sent, intentSat.texts) {
			t.Fatalf("%q: legacy sent %v %q, intent %v %q", wire[:8], legacySat.sent, legacySat.texts, intentSat.sent, intentSat.texts)
		}
		mo := len(intentSat.sent) + len(intentSat.texts)
		size := 0
		for _, b := range intentSat.sent {
			size += len(b)
		}
		for _, s := range intentSat.texts {
			size += len(s)
		}
		if mo != 1 || in.IntentBilledBytes(wire) != size || o.ChargedUnits != creditCost(size) || !o.Charged || o.ChargeConfirmed {
			t.Fatalf("%q: %d MOs of %d B, billed %d B, charge %+v", wire[:8], mo, size, in.IntentBilledBytes(wire), o)
		}
	}
}

func TestSBDIntent_EachKindOfSession(t *testing.T) {
	ok := &transport.SatResult{MOStatus: 0, MOMSN: 9}
	for _, tc := range []struct {
		name    string
		res     *transport.SatResult
		err     error
		class   string
		retry   bool
		reason  string
		charged bool
		bearer  bool
	}{
		{"taken", ok, nil, IntentAccepted, false, "", true, false},
		{"the line is down", nil, fmt.Errorf("x: %w", transport.ErrNotConnected), IntentNotSent, true, "skipped_offline", false, false},
		{"the modem failed before the SBDIX", nil, fmt.Errorf("%w: AT+SBDWB failed (%w)", transport.ErrNotSubmitted, transport.ErrModemFailed), IntentNotSent, true, "not_sent", false, true},
		{"refused before the modem", nil, fmt.Errorf("%w: text too long", transport.ErrNotSubmitted), IntentNotSent, true, "not_sent", false, false},
		{"no network service", &transport.SatResult{MOStatus: 32}, nil, IntentNotSent, true, "not_sent", false, true},
		{"access denied", &transport.SatResult{MOStatus: 15}, nil, IntentNotSent, false, "not_sent", false, true},
		{"the session did not complete", &transport.SatResult{MOStatus: 13}, nil, IntentUnknown, false, "", false, false},
		{"the node could not settle it", &transport.SatResult{MOStatus: 0}, fmt.Errorf("SBDIX failed: %w", transport.ErrOutcomeUnknown), IntentUnknown, false, "", false, false},
		{"an error at the SBDIX", nil, errors.New("SBDIX failed: read timeout"), IntentUnknown, false, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sat := &scriptSat{results: []*transport.SatResult{tc.res}, errs: []error{tc.err}}
			gw := sbdIntentGW(t, IridiumConfig{}, sat)
			o := gw.IntentSend(context.Background(), IntentSend{Destination: sbdDest, Text: "hello", CorrValue: "c1"})
			if o.Class != tc.class || o.Retryable != tc.retry || o.Reason != tc.reason || o.Charged != tc.charged || o.BearerRefused != tc.bearer {
				t.Fatalf("%+v; want class %s retry %v reason %q charged %v bearer refused %v", o, tc.class, tc.retry, tc.reason, tc.charged, tc.bearer)
			}
			if tc.class == IntentAccepted && o.AcceptedBy != "satellite_gateway" {
				t.Fatalf("accepted by %q", o.AcceptedBy)
			}
		})
	}
}

// The gateway's credit budget holds for an intent too: a send it cannot
// afford never reaches the modem.
func TestSBDIntent_TheCreditBudgetHolds(t *testing.T) {
	sat := &fakeSat{}
	gw := sbdIntentGW(t, IridiumConfig{DailyBudget: 3}, sat)
	if err := gw.db.InsertCreditUsage(nil, 3, nil); err != nil {
		t.Fatal(err)
	}
	o := gw.IntentSend(context.Background(), IntentSend{Destination: sbdDest, Text: "hello", CorrValue: "c1"})
	if o.Class != IntentNotSent || o.Reason != "skipped_budget" || len(sat.sent)+len(sat.texts) != 0 {
		t.Fatalf("%+v, %d sends", o, len(sat.sent)+len(sat.texts))
	}
	// A taken MO counts against the budget, as on the legacy path.
	gw2 := sbdIntentGW(t, IridiumConfig{}, &fakeSat{})
	gw2.IntentSend(context.Background(), IntentSend{Destination: sbdDest, Text: strings.Repeat("x", 60), CorrValue: "c2"})
	if n, err := gw2.db.GetDailyCreditTotal(); err != nil || n != 2 {
		t.Fatalf("credits counted %d %v; want 2", n, err)
	}
}
