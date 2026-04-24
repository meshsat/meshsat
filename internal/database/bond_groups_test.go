package database

import "testing"

// Tests for the MESHSAT-687 auto-label heuristic. Human-authored
// labels (anything with spaces or non-standard punctuation) must be
// preserved across membership changes; only labels that look like
// auto-generated "Part+Part" strings get rewritten.

func TestLooksAutoBondLabel(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"(empty)", true},
		{"Mesh", true},
		{"Mesh+APRS", true},
		{"Mesh+APRS+TCP", true},
		{"Mesh+APRS+BLE+TCP+SMS", true}, // 5 bearers all short
		// Human-authored — must be preserved
		{"Mesh + Iridium SBD", false}, // spaces
		{"Mission Charlie", false},    // spaces
		{"bond1", false},              // lowercase-only
		{"ops-primary", false},        // dash
		{"Fleet, Alpha", false},       // comma
		// Edge: mixed auto + human — err on "human" to avoid overwrite
		{"Mesh+Custom Label", false},
		{"+Mesh", false},
		{"Mesh+", false},
	}
	for _, c := range cases {
		got := looksAutoBondLabel(c.in)
		if got != c.want {
			t.Errorf("looksAutoBondLabel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRecomputeBondLabel_AutoLabelGrowsWithMembers(t *testing.T) {
	db := testDB(t)

	if err := db.InsertBondGroup(&BondGroup{ID: "b", Label: "Mesh+APRS"}); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	// Auto label to start; recompute happens on next Insert.
	if err := db.InsertBondMember(&BondMember{GroupID: "b", InterfaceID: "mesh_0"}); err != nil {
		t.Fatalf("insert mesh: %v", err)
	}
	got, _ := db.GetBondGroup("b")
	if got.Label != "Mesh" {
		t.Errorf("after mesh_0: label=%q, want %q", got.Label, "Mesh")
	}
	if err := db.InsertBondMember(&BondMember{GroupID: "b", InterfaceID: "ax25_0"}); err != nil {
		t.Fatalf("insert ax25: %v", err)
	}
	got, _ = db.GetBondGroup("b")
	if got.Label != "Mesh+APRS" {
		t.Errorf("after ax25_0: label=%q, want %q", got.Label, "Mesh+APRS")
	}
	if err := db.InsertBondMember(&BondMember{GroupID: "b", InterfaceID: "tcp_0"}); err != nil {
		t.Fatalf("insert tcp: %v", err)
	}
	got, _ = db.GetBondGroup("b")
	if got.Label != "Mesh+APRS+TCP" {
		t.Errorf("after tcp_0: label=%q, want %q", got.Label, "Mesh+APRS+TCP")
	}
}

func TestRecomputeBondLabel_HumanLabelPreserved(t *testing.T) {
	db := testDB(t)
	// Human-authored label with spaces — must NOT be auto-rewritten
	// when members change.
	if err := db.InsertBondGroup(&BondGroup{
		ID: "b", Label: "Mission Charlie",
	}); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if err := db.InsertBondMember(&BondMember{GroupID: "b", InterfaceID: "mesh_0"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.InsertBondMember(&BondMember{GroupID: "b", InterfaceID: "ax25_0"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _ := db.GetBondGroup("b")
	if got.Label != "Mission Charlie" {
		t.Errorf("human label got rewritten: %q, want %q", got.Label, "Mission Charlie")
	}
}

func TestRecomputeBondLabel_DeleteUpdatesLabel(t *testing.T) {
	db := testDB(t)
	if err := db.InsertBondGroup(&BondGroup{ID: "b", Label: ""}); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	_ = db.InsertBondMember(&BondMember{GroupID: "b", InterfaceID: "mesh_0"})
	_ = db.InsertBondMember(&BondMember{GroupID: "b", InterfaceID: "ax25_0"})
	// Drop all — label should collapse to "(empty)".
	if err := db.DeleteBondMembers("b"); err != nil {
		t.Fatalf("delete members: %v", err)
	}
	got, _ := db.GetBondGroup("b")
	if got.Label != "(empty)" {
		t.Errorf("after delete all: label=%q, want %q", got.Label, "(empty)")
	}
}
