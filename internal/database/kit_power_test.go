package database

import (
	"testing"
	"time"
)

func TestKitPowerKeepsTheFirstSightingAndMovesTheLast(t *testing.T) {
	db := testDB(t)
	t0 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for i := range 4 {
		if err := db.TouchKitPower("boot-a", t0.Add(time.Duration(i)*5*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.TouchKitPower("boot-b", t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchKitPower("", t0); err == nil {
		t.Fatal("an empty boot id must be refused")
	}
	rows, err := db.KitPower()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].BootID != "boot-a" || rows[0].FirstSeen != "2026-11-13T10:00:00Z" || rows[0].LastSeen != "2026-11-13T10:15:00Z" {
		t.Fatalf("rows %+v", rows)
	}
	if h := rows[0].Hours(); h != 0.25 {
		t.Fatalf("hours %v, want 0.25", h)
	}
}
