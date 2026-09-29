package database

import (
	"bytes"
	"strings"
	"testing"
)

// The key_bundles reads name their columns: a column a later migration adds
// must not fail every key lookup, as v58's label failed a binary that read
// with SELECT * (sqlx maps strictly). The raw SELECT * below shows the
// failure the named columns avoid.
func TestKeyBundles_ReadWithAColumnALaterMigrationAdds(t *testing.T) {
	db := testDB(t)
	key := []byte{0xa1, 0xa1, 0xa1}
	if err := db.InsertKeyBundleLabelled("sms", "+31612345678", key, 1, "hub-rotated-v1"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertKeyBundle("sms", "*", []byte{0xb2}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE key_bundles ADD COLUMN rotated_by TEXT NOT NULL DEFAULT 'later'`); err != nil {
		t.Fatal(err)
	}

	var raw KeyBundleRow
	if err := db.Get(&raw, `SELECT * FROM key_bundles LIMIT 1`); err == nil || !strings.Contains(err.Error(), "rotated_by") {
		t.Fatalf("SELECT * with the new column: %v, want sqlx's missing destination error", err)
	}

	row, err := db.GetActiveKeyBundle("sms", "+31612345678")
	if err != nil {
		t.Fatalf("active key with a column added: %v", err)
	}
	if row.ChannelType != "sms" || row.Address != "+31612345678" || !bytes.Equal(row.EncryptedKey, key) ||
		row.KeyVersion != 1 || row.Status != "active" || row.Label != "hub-rotated-v1" || row.CreatedAt == "" {
		t.Fatalf("active key: %+v", row)
	}
	rows, err := db.ListKeyBundles()
	if err != nil {
		t.Fatalf("list with a column added: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("list: %d rows, want 2: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Address == "*" && (r.Label != "" || !bytes.Equal(r.EncryptedKey, []byte{0xb2})) {
			t.Fatalf("wildcard row: %+v", r)
		}
	}
}
