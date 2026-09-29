package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
)

// With the credit budget used up, the SBD gateway refuses what can wait and
// sends what cannot. An ordinary text (as plain text or compact), the
// satellite fallback's position frame and a routing rule's delivery are
// refused with "budget exceeded" before the modem sees them; the SOS burst's
// direct text and the SOS's Hub frame (msg.Critical) go, and their credits
// are counted. For the daily and the monthly budget alike. sendSBD asked for
// priority 1 whatever it sent, so a real SOS failed over a used-up budget.
// [MESHSAT-1431]
func TestSBDBudget_OnlyAnSOSPassesAnExhaustedBudget(t *testing.T) {
	now := time.Unix(1790000000, 0)
	position := hubreporter.EncodeSatPosition("nllei01tesseract01", 52.1601, 4.4970, 3.9, 1, now)
	sos := hubreporter.EncodeSatSOS("nllei01tesseract01", "bridge", 52.1601, 4.4970, "SOS: Anna needs help.", now)
	long := strings.Repeat("a text too long for one plain SBD message, ", 4)
	sosText := "SOS - EMERGENCY ALERT - Requesting immediate assistance"

	cases := []struct {
		name     string
		msg      *transport.MeshMessage
		critical bool
	}{
		{"an ordinary text", &transport.MeshMessage{PortNum: 1, DecodedText: "hello from the kit"}, false},
		{"an ordinary long text, compact", &transport.MeshMessage{PortNum: 1, DecodedText: long}, false},
		{"the fallback's position frame", &transport.MeshMessage{PortNum: 256, RawPayload: position}, false},
		{"a rule's delivery, whatever its rule said", &transport.MeshMessage{PortNum: 1, DecodedText: "relayed", Precedence: "Flash"}, false},
		{"the SOS burst's direct text", &transport.MeshMessage{PortNum: 1, DecodedText: sosText, Critical: true}, true},
		{"the SOS's Hub frame", &transport.MeshMessage{PortNum: 256, RawPayload: sos, Critical: true}, true},
		{"a long SOS text, compact", &transport.MeshMessage{PortNum: 1, DecodedText: long, Critical: true}, true},
	}

	for _, budget := range []struct {
		name string
		cfg  IridiumConfig
	}{
		{"daily", IridiumConfig{DailyBudget: 3}},
		{"monthly", IridiumConfig{MonthlyBudget: 3}},
	} {
		t.Run(budget.name, func(t *testing.T) {
			db := testDB(t)
			if err := db.InsertCreditUsage(nil, 3, nil); err != nil {
				t.Fatal(err)
			}
			sat := &fakeSat{moStatus: 0}
			gw := NewSBDGateway(budget.cfg, sat, db, nil)
			spent := 3
			for _, tc := range cases {
				before := len(sat.sent) + len(sat.texts)
				err := gw.sendSBD(context.Background(), tc.msg)
				reached := len(sat.sent)+len(sat.texts) > before
				switch {
				case tc.critical && (err != nil || !reached):
					t.Fatalf("%s: %v (reached the modem: %v), want it sent over the used-up budget", tc.name, err, reached)
				case !tc.critical && (err == nil || !strings.Contains(err.Error(), "budget exceeded") || reached):
					t.Fatalf("%s: %v (reached the modem: %v), want budget exceeded and nothing sent", tc.name, err, reached)
				}
				if tc.critical {
					spent++ // at least one credit each
				}
			}
			if got, err := db.GetMonthlyCreditTotal(); err != nil || got < spent {
				t.Fatalf("credits counted %d (%v), want at least %d: an SOS is paid for like any send", got, err, spent)
			}
		})
	}

	// The control: with credit left, the same ordinary sends go.
	db := testDB(t)
	sat := &fakeSat{moStatus: 0}
	gw := NewSBDGateway(IridiumConfig{DailyBudget: 100, MonthlyBudget: 100}, sat, db, nil)
	for _, tc := range cases {
		if !tc.critical {
			if err := gw.sendSBD(context.Background(), tc.msg); err != nil {
				t.Fatalf("%s with credit left: %v", tc.name, err)
			}
		}
	}
}
