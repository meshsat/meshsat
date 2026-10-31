package database

import "testing"

// v87 removes HeMB v1's tables. A rule that still forwarded to a bond group
// is switched off first, so it never resolves the group's id as an interface;
// every other rule is left alone, and the migration can be applied again on
// a database that no longer has the tables. [MESHSAT-1763]
func TestV87DropsHeMBTablesAndDisablesBondRules(t *testing.T) {
	db := openStaged(t, 86)

	if _, err := db.Exec(`INSERT INTO bond_groups (id, label) VALUES ('bond1', 'Mesh+APRS+TCP')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hemb_events (event_type, payload) VALUES ('HEMB_GENERATION_DECODED', '{}')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ name, to string }{{"to the bond", "bond1"}, {"to the mesh", "mesh_0"}} {
		if _, err := db.Exec(`INSERT INTO access_rules (interface_id, direction, name, enabled, forward_to) VALUES ('mqtt_0', 'ingress', ?, 1, ?)`, r.name, r.to); err != nil {
			t.Fatal(err)
		}
	}

	for pass := 1; pass <= 2; pass++ {
		if _, err := db.Exec(migrations[86]); err != nil {
			t.Fatalf("v87 pass %d: %v", pass, err)
		}
	}

	for _, tbl := range []string{"bond_groups", "bond_members", "hemb_events"} {
		var n int
		if err := db.Get(&n, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl); err != nil || n != 0 {
			t.Errorf("table %s still there (n=%d, %v)", tbl, n, err)
		}
	}
	enabled := map[string]int{}
	rows, err := db.Query(`SELECT name, enabled FROM access_rules WHERE name IN ('to the bond', 'to the mesh')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var on int
		if err := rows.Scan(&name, &on); err != nil {
			t.Fatal(err)
		}
		enabled[name] = on
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if enabled["to the bond"] != 0 || enabled["to the mesh"] != 1 {
		t.Errorf("enabled after v87 = %v, want the bond rule off and the mesh rule on", enabled)
	}
}
