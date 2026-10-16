package engine

import (
	"errors"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
)

// The B9/SMS review's acceptance past its own tests (B9SMS-R1, R4, R5).
// [MESHSAT-1584]

// An unknown SMS keeps its price as possible and is never sent again by
// itself, however long the intent waits: no paid retry on the same route.
func TestIntentB9R_UnknownIsNeverRetried(t *testing.T) {
	h := newIntentHarness(t)
	f := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentUnknown, Detail: "may still be submitted: the modem command still runs"}}
	useSMS(t, h, f)
	smsTariff(t, h, 3)
	id := h.submit(smsContact(h), "x", directory.StrategyPrimaryOnly)
	h.work("cellular_0")
	h.drain()
	for i := 0; i < 4; i++ {
		h.clock.Advance(10 * time.Minute)
		h.esc.Heartbeat()
		h.drain()
		h.due()
		h.work("cellular_0")
		h.drain()
	}
	s := h.snap(id)
	r, inc, pos := intentMoney(t, h, id)
	if f.count() != 1 || len(s.Transmissions) != 1 || r != 0 || inc != 0 || pos != 3 {
		t.Fatalf("%d sends, %d transmissions, reserved %d incurred %d possible %d; want one send, its 3 kept as possible",
			f.count(), len(s.Transmissions), r, inc, pos)
	}
	if s.Intent.Outcome != database.OutcomeMayHaveBeenSent {
		t.Fatalf("outcome %s; want may_have_been_sent", s.Intent.Outcome)
	}
}

// A provider's confirmed charge is the provider's figure; an estimate is
// the tariff's own count, its minimum included.
func TestIntentB9R_ConfirmedChargeIsTheProvidersFigure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		confirmed bool
		want      int64
	}{{"estimated_keeps_the_minimum", false, 6}, {"confirmed_by_the_provider", true, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIntentHarness(t)
			useSMS(t, h, &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier",
				Charged: true, ChargedUnits: 1, ChargeConfirmed: tc.confirmed}})
			if _, err := h.db.InsertTariff(database.Tariff{InterfaceID: "cellular_0", UnitKind: "sms", MinUnits: 2, PriceMinor: 3, Currency: "EUR"}); err != nil {
				t.Fatal(err)
			}
			id := h.submit(smsContact(h), "x", directory.StrategyPrimaryOnly)
			h.work("cellular_0")
			h.drain()
			if _, inc, _ := intentMoney(t, h, id); inc != tc.want {
				t.Fatalf("incurred %d; want %d", inc, tc.want)
			}
		})
	}
}

// The largest valid price sends; a stored tariff past the bounds (written
// before them, or not through the API) prices nothing, at admission or
// when it replaces a good one before the send.
func TestIntentB9R_PriceBounds(t *testing.T) {
	t.Run("largest_valid_price", func(t *testing.T) {
		h := newIntentHarness(t)
		f := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
		useSMS(t, h, f)
		smsTariff(t, h, database.MaxTariffPriceMinor)
		id := h.submit(smsContact(h), "x", directory.StrategyPrimaryOnly)
		h.work("cellular_0")
		h.drain()
		if _, inc, _ := intentMoney(t, h, id); f.count() != 1 || inc != database.MaxTariffPriceMinor {
			t.Fatalf("%d sends, incurred %d", f.count(), inc)
		}
	})
	t.Run("stored_past_the_bound_at_admission", func(t *testing.T) {
		h := newIntentHarness(t)
		f := &fakeSMS{}
		useSMS(t, h, f)
		smsTariff(t, h, database.MaxTariffPriceMinor+1)
		_, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: smsContact(h), Body: []byte("x"), Strategy: directory.StrategyPrimaryOnly})
		var nr *NoRouteError
		if !errors.As(err, &nr) || nr.Plan[0].Reason != reasonNoPrice || f.count() != 0 {
			t.Fatalf("submit: %v, %d sends; want skipped_no_price and no send", err, f.count())
		}
	})
	t.Run("stored_past_the_bound_before_the_send", func(t *testing.T) {
		h := newIntentHarness(t)
		f := &fakeSMS{}
		useSMS(t, h, f)
		smsTariff(t, h, 3)
		id := h.submit(smsContact(h), "x", directory.StrategyPrimaryOnly)
		smsTariff(t, h, database.MaxTariffPriceMinor+1)
		h.work("cellular_0")
		h.drain()
		s := h.snap(id)
		r, inc, pos := intentMoney(t, h, id)
		if f.count() != 0 || len(s.Transmissions) != 0 || r+inc+pos != 0 || s.Rows[0].Reason != reasonNoPrice {
			t.Fatalf("%d sends, %d transmissions, money %d/%d/%d, reason %q; want no send, no money, skipped_no_price",
				f.count(), len(s.Transmissions), r, inc, pos, s.Rows[0].Reason)
		}
	})
}
