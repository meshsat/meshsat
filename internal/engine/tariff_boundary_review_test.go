package engine

import (
	"math"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/gateway"
)

func TestReviewB9CurrencyMustMatchIntent(t *testing.T) {
	for _, tc := range []struct {
		name, currency string
		reprice        bool
	}{
		{"eur_control", "EUR", false},
		{"gbp_at_admission", "GBP", false},
		{"gbp_at_reprice", "GBP", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIntentHarness(t)
			f := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
			useSMS(t, h, f)
			currency := tc.currency
			if tc.reprice {
				currency = "EUR"
			}
			tr := database.Tariff{InterfaceID: "cellular_0", UnitKind: "sms", MinUnits: 1, PriceMinor: 3, Currency: currency}
			if _, err := h.db.InsertTariff(tr); err != nil {
				t.Fatal(err)
			}
			sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: smsContact(h), Body: []byte("review"), Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(3)})
			if err != nil {
				if tc.currency != "EUR" && !tc.reprice {
					return
				}
				t.Fatal(err)
			}
			id := sub.Intents[0].ID
			if tc.reprice {
				tr.Currency = tc.currency
				if _, err := h.db.InsertTariff(tr); err != nil {
					t.Fatal(err)
				}
			}
			h.work("cellular_0")
			h.drain()
			in, err := h.db.GetIntent(id)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("tariff_currency=%s intent_currency=%s sends=%d incurred=%d", tc.currency, in.Currency, f.count(), in.IncurredMinor)
			if tc.currency != in.Currency && f.count() != 0 {
				t.Fatal("sent with a foreign-currency tariff under an unconverted EUR cap")
			}
			if tc.currency == "EUR" && (f.count() != 1 || in.IncurredMinor != 3) {
				t.Fatal("EUR control failed")
			}
		})
	}
}

func TestReviewB9MinimumChargeSurvivesFinalization(t *testing.T) {
	for _, minimum := range []int{1, 2} {
		name := "one_unit_control"
		if minimum == 2 {
			name = "two_unit_minimum"
		}
		t.Run(name, func(t *testing.T) {
			h := newIntentHarness(t)
			f := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentAccepted, AcceptedBy: "carrier", Charged: true, ChargedUnits: 1}}
			useSMS(t, h, f)
			tr, err := h.db.InsertTariff(database.Tariff{InterfaceID: "cellular_0", UnitKind: "sms", MinUnits: minimum, PriceMinor: 3, Currency: "EUR"})
			if err != nil {
				t.Fatal(err)
			}
			id := h.submit(smsContact(h), "review", directory.StrategyPrimaryOnly)
			var reservedDuring int64
			f.onSend = func(gateway.IntentSend) { reservedDuring, _, _ = intentMoney(t, h, id) }
			h.work("cellular_0")
			h.drain()
			r, inc, pos := intentMoney(t, h, id)
			want := tr.Price(tr.Units(len("review")))
			t.Logf("min_units=%d reserved_during=%d reserved_after=%d incurred=%d possible=%d tariff_cost=%d", minimum, reservedDuring, r, inc, pos, want)
			if inc != want {
				t.Fatalf("incurred=%d; configured minimum charge=%d", inc, want)
			}
		})
	}
}

func TestReviewB9OverflowCannotAuthorize(t *testing.T) {
	h := newIntentHarness(t)
	f := &fakeSMS{answer: gateway.IntentOutcome{Class: gateway.IntentUnknown}}
	useSMS(t, h, f)
	tr, err := h.db.InsertTariff(database.Tariff{InterfaceID: "cellular_0", UnitKind: "sms", MinUnits: 2, PriceMinor: math.MaxInt64, Currency: "EUR"})
	if err != nil {
		return
	}
	sub, err := h.esc.Submit(h.ctx, IntentRequest{ContactID: smsContact(h), Body: []byte("review"), Strategy: directory.StrategyPrimaryOnly, MaxCostCents: capOf(0)})
	if err != nil {
		return
	}
	id := sub.Intents[0].ID
	h.work("cellular_0")
	h.drain()
	r, inc, pos := intentMoney(t, h, id)
	t.Logf("price=%d cap=0 sends=%d reserved=%d incurred=%d possible=%d", tr.Price(tr.Units(6)), f.count(), r, inc, pos)
	if f.count() != 0 {
		t.Fatal("overflowing tariff authorized a paid submission under a zero cap")
	}
}
