package database

import "testing"

// B9 tariffs: units per submission, price, versions. [MESHSAT-1584]
func TestTariffUnitsPriceAndVersions(t *testing.T) {
	credit := Tariff{UnitBytes: 50, MinUnits: 1, PriceMinor: 17}
	for _, c := range []struct{ bytes, units int }{{0, 1}, {1, 1}, {50, 1}, {51, 2}, {100, 2}, {101, 3}} {
		if got := credit.Units(c.bytes); got != c.units {
			t.Errorf("%d bytes: %d units; want %d", c.bytes, got, c.units)
		}
	}
	if p := credit.Price(credit.Units(120)); p != 51 {
		t.Fatalf("120 bytes: %d; want 3 credits, 51", p)
	}
	sms := Tariff{UnitBytes: 0, MinUnits: 1, PriceMinor: 3}
	if sms.Units(160) != 1 || sms.Price(1) != 3 {
		t.Fatalf("sms: %d units, %d", sms.Units(160), sms.Price(1))
	}
	if (Tariff{MinUnits: 2, PriceMinor: 5}).Units(1) != 2 {
		t.Fatal("min_units not applied")
	}
	if (Tariff{Free: true, PriceMinor: 9}).Price(4) != 0 {
		t.Fatal("a free tariff costs something")
	}

	db := intentTestDB(t)
	if cur, _ := db.CurrentTariff("cellular_0"); cur != nil {
		t.Fatalf("a tariff on a fresh database: %+v", cur)
	}
	a, err := db.InsertTariff(Tariff{InterfaceID: "cellular_0", UnitKind: "sms", MinUnits: 1, PriceMinor: 3})
	if err != nil || a.Version != 1 || a.Currency != "EUR" {
		t.Fatalf("first: %+v %v", a, err)
	}
	b, _ := db.InsertTariff(Tariff{InterfaceID: "cellular_0", UnitKind: "sms", MinUnits: 1, PriceMinor: 4})
	_, _ = db.InsertTariff(Tariff{InterfaceID: "iridium_imt_0", UnitKind: "imt_credit", UnitBytes: 50, MinUnits: 1, PriceMinor: 17})
	cur, _ := db.CurrentTariff("cellular_0")
	if cur == nil || cur.ID != b.ID || cur.Version != 2 || cur.PriceMinor != 4 {
		t.Fatalf("current: %+v; want version 2", cur)
	}
	all, _ := db.ListTariffs()
	if len(all) != 3 {
		t.Fatalf("%d tariffs listed; want 3", len(all))
	}
}
