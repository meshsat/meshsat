package database

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// openStaged opens a fresh DB and runs migrations 1..through inclusive.
// "through" is the 1-based migration version to stop at (e.g. 43 runs v1..v43).
func openStaged(t *testing.T, through int) *DB {
	t.Helper()
	if through < 1 || through > len(migrations) {
		t.Fatalf("openStaged: through=%d out of range [1,%d]", through, len(migrations))
	}
	dir := t.TempDir()
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_synchronous=NORMAL",
		filepath.Join(dir, "staged.db"))
	conn, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open staged db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	db := &DB{DB: conn}
	for i := 0; i < through; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatalf("staged migration v%d: %v", i+1, err)
		}
	}
	return db
}

// TestDirectoryMigrationBackfill asserts that v44+v45 backfill legacy
// contacts/contact_addresses with kind normalisation and primary_rank
// preservation, and that v48 seeds the STANAG 4406 precedence defaults.
func TestDirectoryMigrationBackfill(t *testing.T) {
	// Stage to v43 so we can seed legacy data before the new migrations run.
	db := openStaged(t, 43)

	// Seed one v23 contact with mixed legacy address kinds.
	res, err := db.Exec("INSERT INTO contacts (display_name, notes) VALUES (?, ?)", "Alice", "Operator")
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	aliceID, _ := res.LastInsertId()

	legacy := []struct {
		kind, value, label string
		primary            int
	}{
		{"sms", "+31612345678", "Mobile", 1},
		{"mesh", "!abcd1234", "Meshtastic", 0},
		{"iridium", "300234012345670", "SBD", 0},
		{"iridium_imt", "300234012345699", "IMT", 0},
		{"aprs", "PA1XXX-9", "APRS", 0},
		{"webhook", "https://hooks.example/alice", "Webhook", 0},
		{"mqtt", "meshsat/alice", "MQTT", 0},
		{"zigbee", "00:11:22:33:44:55:66:77", "ZigBee", 0},
		{"ble", "AA:BB:CC:DD:EE:FF", "BLE", 0},
	}
	for _, a := range legacy {
		if _, err := db.Exec(
			`INSERT INTO contact_addresses (contact_id, type, address, label, is_primary) VALUES (?, ?, ?, ?, ?)`,
			aliceID, a.kind, a.value, a.label, a.primary); err != nil {
			t.Fatalf("seed address %s: %v", a.kind, err)
		}
	}

	// Now run v44..v48.
	for i := 43; i < len(migrations); i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatalf("migration v%d: %v", i+1, err)
		}
	}

	// Verify directory_contacts has exactly one backfilled row.
	var dirID string
	var dirName string
	var legacyRef int64
	if err := db.QueryRow(
		`SELECT id, display_name, COALESCE(legacy_contact_id, 0) FROM directory_contacts WHERE legacy_contact_id = ?`,
		aliceID).Scan(&dirID, &dirName, &legacyRef); err != nil {
		t.Fatalf("lookup directory_contact: %v", err)
	}
	if dirName != "Alice" {
		t.Errorf("display_name: got %q, want %q", dirName, "Alice")
	}
	if len(dirID) != 32 {
		t.Errorf("directory_contacts.id length: got %d, want 32 (hex(randomblob(16)))", len(dirID))
	}
	if legacyRef != aliceID {
		t.Errorf("legacy_contact_id: got %d, want %d", legacyRef, aliceID)
	}

	// Verify every legacy address was copied with the normalised kind.
	want := map[string]struct {
		value string
		rank  int
	}{
		"SMS":         {"+31612345678", 0},
		"MESHTASTIC":  {"!abcd1234", 1},
		"IRIDIUM_SBD": {"300234012345670", 1},
		"IRIDIUM_IMT": {"300234012345699", 1},
		"APRS":        {"PA1XXX-9", 1},
		"WEBHOOK":     {"https://hooks.example/alice", 1},
		"MQTT":        {"meshsat/alice", 1},
		"ZIGBEE":      {"00:11:22:33:44:55:66:77", 1},
		"BLE":         {"AA:BB:CC:DD:EE:FF", 1},
	}
	rows, err := db.Query(`SELECT kind, value, primary_rank FROM directory_addresses WHERE contact_id = ?`, dirID)
	if err != nil {
		t.Fatalf("query addresses: %v", err)
	}
	defer rows.Close()
	got := map[string]struct {
		value string
		rank  int
	}{}
	for rows.Next() {
		var kind, value string
		var rank int
		if err := rows.Scan(&kind, &value, &rank); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[kind] = struct {
			value string
			rank  int
		}{value, rank}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("backfilled addresses mismatch\n got: %+v\nwant: %+v", got, want)
	}

	// UNIQUE(kind, value) must be enforced post-migration.
	if _, err := db.Exec(
		`INSERT INTO directory_addresses (id, contact_id, kind, value) VALUES ('dup','` + dirID + `','SMS','+31612345678')`,
	); err == nil {
		t.Error("expected UNIQUE(kind,value) to reject duplicate SMS address")
	}

	// v48 must seed exactly 7 policies: 1 default + 6 precedence rows.
	var policyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM directory_dispatch_policy`).Scan(&policyCount); err != nil {
		t.Fatalf("policy count: %v", err)
	}
	if policyCount != 7 {
		t.Errorf("dispatch_policy seeded count: got %d, want 7", policyCount)
	}

	wantScopes := []string{"Deferred", "Flash", "Immediate", "Override", "Priority", "Routine"}
	prows, err := db.Query(
		`SELECT scope_id FROM directory_dispatch_policy WHERE scope_type = 'precedence' ORDER BY scope_id`)
	if err != nil {
		t.Fatalf("precedence rows: %v", err)
	}
	defer prows.Close()
	var gotScopes []string
	for prows.Next() {
		var s string
		if err := prows.Scan(&s); err != nil {
			t.Fatalf("scan scope: %v", err)
		}
		gotScopes = append(gotScopes, s)
	}
	sort.Strings(gotScopes)
	sort.Strings(wantScopes)
	if !reflect.DeepEqual(gotScopes, wantScopes) {
		t.Errorf("precedence scopes: got %v, want %v", gotScopes, wantScopes)
	}

	// Strategy lookups for two anchor cases.
	var flashStrategy string
	if err := db.QueryRow(
		`SELECT strategy FROM directory_dispatch_policy WHERE scope_type='precedence' AND scope_id='Flash'`,
	).Scan(&flashStrategy); err != nil {
		t.Fatalf("flash strategy: %v", err)
	}
	if flashStrategy != "HEMB_BONDED" {
		t.Errorf("Flash strategy: got %q, want HEMB_BONDED", flashStrategy)
	}
	var routineStrategy string
	if err := db.QueryRow(
		`SELECT strategy FROM directory_dispatch_policy WHERE scope_type='precedence' AND scope_id='Routine'`,
	).Scan(&routineStrategy); err != nil {
		t.Fatalf("routine strategy: %v", err)
	}
	if routineStrategy != "PRIMARY_ONLY" {
		t.Errorf("Routine strategy: got %q, want PRIMARY_ONLY", routineStrategy)
	}
}

// TestDirectorySchemaOnFreshDB sanity-checks that a freshly created DB
// has all five new tables, the seeded policies, and the schema version
// bumped to the new head.
func TestDirectorySchemaOnFreshDB(t *testing.T) {
	db := testDB(t)

	wantTables := []string{
		"directory_contacts",
		"directory_addresses",
		"directory_contact_keys",
		"directory_groups",
		"directory_group_members",
		"directory_dispatch_policy",
	}
	for _, tbl := range wantTables {
		var name string
		if err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, tbl,
		).Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
	}

	var version int
	if err := db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("schema_version: got %d, want %d", version, len(migrations))
	}

	// Seeds must be present even with no legacy data.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM directory_dispatch_policy`).Scan(&n); err != nil {
		t.Fatalf("policy count: %v", err)
	}
	if n != 7 {
		t.Errorf("seeded policy count on fresh DB: got %d, want 7", n)
	}
}

// TestDirectoryCascadeDelete verifies that deleting a directory_contacts
// row cascades into directory_addresses, directory_contact_keys, and
// directory_group_members. The production DSN sets `_foreign_keys=ON`
// but modernc.org/sqlite does not honour that bare parameter (the
// existing contacts.DeleteContact uses an explicit transaction as a
// safety net for the same reason). This test opts into FK enforcement
// with an explicit PRAGMA so it validates the schema shape — the
// ON DELETE CASCADE clauses are syntactically correct and fire when FK
// enforcement is active.
func TestDirectoryCascadeDelete(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable FKs: %v", err)
	}

	const cid = "11111111111111111111111111111111"
	mustExec(t, db, `INSERT INTO directory_contacts (id, display_name) VALUES (?, ?)`, cid, "Bob")
	mustExec(t, db,
		`INSERT INTO directory_addresses (id, contact_id, kind, value) VALUES (?, ?, ?, ?)`,
		"addr1", cid, "SMS", "+31687654321")
	mustExec(t, db,
		`INSERT INTO directory_contact_keys (id, contact_id, kind) VALUES (?, ?, ?)`,
		"key1", cid, "AES256_GCM_SHARED")
	mustExec(t, db,
		`INSERT INTO directory_groups (id, display_name, kind) VALUES (?, ?, ?)`,
		"g1", "Red Team", "TEAM")
	mustExec(t, db,
		`INSERT INTO directory_group_members (group_id, contact_id) VALUES (?, ?)`, "g1", cid)

	mustExec(t, db, `DELETE FROM directory_contacts WHERE id = ?`, cid)

	for _, q := range []string{
		`SELECT COUNT(*) FROM directory_addresses WHERE contact_id = '` + cid + `'`,
		`SELECT COUNT(*) FROM directory_contact_keys WHERE contact_id = '` + cid + `'`,
		`SELECT COUNT(*) FROM directory_group_members WHERE contact_id = '` + cid + `'`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("cascade verify %q: %v", q, err)
		}
		if n != 0 {
			t.Errorf("cascade leak: %q returned %d rows", q, n)
		}
	}
}

func mustExec(t *testing.T, db *DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
