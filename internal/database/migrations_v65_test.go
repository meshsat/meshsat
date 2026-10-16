package database

import "testing"

// B5 (HeMB-NG r3 D12, stage 0b): v65 moves the unedited precedence seeds
// that name a strategy the bridge does not implement to ALL_BEARERS, and
// keeps every edited row as it is. The fixture is a v64 database whose
// policies are the kits' rows on 16 Oct 2026 (all seven unedited seeds,
// 2026-04-18 12:17:59), plus two edited rows. [MESHSAT-1584]
func TestMigrationV65_MovesUneditedSeedsOnly(t *testing.T) {
	db := openStaged(t, 64)
	const seededAt = "2026-04-18 12:17:59"
	if _, err := db.Exec(`UPDATE directory_dispatch_policy SET created_at = ?, updated_at = ?`, seededAt, seededAt); err != nil {
		t.Fatal(err)
	}
	// Edited after the seed: Immediate kept at ANY_REACHABLE by somebody,
	// and a contact row with HEMB_BONDED.
	if _, err := db.Exec(`UPDATE directory_dispatch_policy SET updated_at = '2026-05-01 10:00:00' WHERE scope_id = 'Immediate'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO directory_dispatch_policy (id, scope_type, scope_id, strategy, created_at, updated_at)
		VALUES ('c1', 'contact', 'contact-1', 'HEMB_BONDED', ?, ?)`, seededAt, seededAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[64]); err != nil {
		t.Fatalf("v65: %v", err)
	}

	type row struct {
		strategy  string
		seeded    bool
		updatedAt string
	}
	got := map[string]row{}
	rows, err := db.Query(`SELECT scope_type || ':' || scope_id, strategy, seeded, updated_at FROM directory_dispatch_policy`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var r row
		if err := rows.Scan(&k, &r.strategy, &r.seeded, &r.updatedAt); err != nil {
			t.Fatal(err)
		}
		got[k] = r
	}
	want := map[string]struct {
		strategy string
		seeded   bool
		moved    bool
	}{
		"default:":             {"PRIMARY_ONLY", true, false},
		"precedence:Override":  {"ALL_BEARERS", true, true},
		"precedence:Flash":     {"ALL_BEARERS", true, true},
		"precedence:Immediate": {"ANY_REACHABLE", false, false}, // edited: kept, resolves as legacy compat
		"precedence:Priority":  {"ALL_BEARERS", true, true},
		"precedence:Routine":   {"PRIMARY_ONLY", true, false},
		"precedence:Deferred":  {"PRIMARY_ONLY", true, false},
		"contact:contact-1":    {"HEMB_BONDED", false, false}, // not a seed: kept
	}
	if len(got) != len(want) {
		t.Fatalf("%d rows after v65; want %d", len(got), len(want))
	}
	for k, w := range want {
		g := got[k]
		if g.strategy != w.strategy || g.seeded != w.seeded {
			t.Errorf("%s: %s seeded %v; want %s seeded %v", k, g.strategy, g.seeded, w.strategy, w.seeded)
		}
		// A moved row gets a new updated_at, so the policy version an
		// intent records (id@updated_at) changes with the value.
		if moved := g.updatedAt != seededAt && k != "precedence:Immediate"; moved != w.moved {
			t.Errorf("%s: updated_at %s; moved %v, want %v", k, g.updatedAt, moved, w.moved)
		}
	}
}
