package database

import (
	"errors"
	"math"
	"testing"
)

// B9SMS-R3, R5: what a usable tariff is, a cost that never wraps, and no
// negative amount at the ledger's boundary. [MESHSAT-1584]
func TestTariffValidateAndCost(t *testing.T) {
	ok := Tariff{UnitKind: "sms", MinUnits: 1, PriceMinor: 3, Currency: "EUR"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("the kits' SMS tariff: %v", err)
	}
	largest := Tariff{UnitKind: "sms", MinUnits: MaxTariffMinUnits, PriceMinor: MaxTariffPriceMinor, Currency: "EUR"}
	if u, m, err := largest.Cost(1); err != nil || u != MaxTariffMinUnits || m != int64(MaxTariffMinUnits)*MaxTariffPriceMinor {
		t.Fatalf("the largest valid tariff: %d units %d (%v)", u, m, err)
	}
	for name, bad := range map[string]Tariff{
		"gbp":                {UnitKind: "sms", MinUnits: 1, PriceMinor: 3, Currency: "GBP"},
		"price_past_max":     {UnitKind: "sms", MinUnits: 1, PriceMinor: MaxTariffPriceMinor + 1, Currency: "EUR"},
		"min_past_max":       {UnitKind: "sms", MinUnits: MaxTariffMinUnits + 1, PriceMinor: 3, Currency: "EUR"},
		"unit_bytes":         {UnitKind: "imt", UnitBytes: MaxTariffUnitBytes + 1, MinUnits: 1, PriceMinor: 3, Currency: "EUR"},
		"overflow":           {UnitKind: "sms", MinUnits: 2, PriceMinor: math.MaxInt64, Currency: "EUR"},
		"negative_price":     {UnitKind: "sms", MinUnits: 1, PriceMinor: -1, Currency: "EUR"},
		"paid_with_no_price": {UnitKind: "sms", MinUnits: 1, Currency: "EUR"},
		"no_unit":            {MinUnits: 1, PriceMinor: 3, Currency: "EUR"},
	} {
		if _, _, err := bad.Cost(10); err == nil {
			t.Errorf("%s: priced", name)
		}
	}
	// Price never wraps, whatever the tariff says.
	if p := (Tariff{PriceMinor: math.MaxInt64}).Price(2); p != math.MaxInt64 {
		t.Fatalf("2 x MaxInt64 = %d; want saturated", p)
	}
	if p := (Tariff{PriceMinor: 3}).Price(-1); p != 0 {
		t.Fatalf("negative units priced %d", p)
	}
}

func TestLedgerRefusesNegativeAmounts(t *testing.T) {
	db := intentTestDB(t)
	rows := newTestIntent(t, db, "neg", 1, nil)
	gen := claim(t, db, rows[0])
	if _, err := db.AuthorizeTransmission(rows[0], gen, "sms", 1, -2); !errors.Is(err, ErrBadAmount) {
		t.Fatalf("a reservation of -2: %v; want ErrBadAmount", err)
	}
	if _, err := db.AuthorizeTransmission(rows[0], gen, "sms", -1, 0); !errors.Is(err, ErrBadAmount) {
		t.Fatalf("-1 units: %v; want ErrBadAmount", err)
	}
	tx, err := db.AuthorizeTransmission(rows[0], gen, "sms", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.MarkTransmissionHandedOff(tx)
	if _, err := db.FinalizeTransmission(tx, TransmissionResult{Class: ClassAccepted, ChargedUnits: 1, ChargedMinor: -3}); !errors.Is(err, ErrBadAmount) {
		t.Fatalf("a charge of -3: %v; want ErrBadAmount", err)
	}
	in, _ := db.GetIntent("neg")
	if in.ReservedMinor != 3 || in.IncurredMinor != 0 {
		t.Fatalf("after the refused finalization: reserved %d incurred %d; want the reservation kept", in.ReservedMinor, in.IncurredMinor)
	}
}
