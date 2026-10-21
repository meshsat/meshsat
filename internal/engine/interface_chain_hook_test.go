package engine

import (
	"testing"

	"meshsat/internal/database"
)

// A chain written through the manager is announced, so the birth can declare
// whether a satellite chain seals (HeMB-NG S3-2); a failed write is not.
func TestInterfaceManagerAnnouncesChainChanges(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	m := NewInterfaceManager(db)
	var heard []string
	m.SetChainChangeHook(func(id string) { heard = append(heard, id) })
	chain := `[{"type":"smaz2g","params":{"dict":"meshtastic"}}]`
	if _, err := m.SetTransforms("iridium_imt_0", &chain, &chain); err != nil {
		t.Fatal(err)
	}
	iface, err := db.GetInterface("iridium_imt_0")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateInterface(*iface); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetTransforms("no_such_link", &chain, nil); err == nil {
		t.Fatal("a missing link took a chain")
	}
	if len(heard) != 2 || heard[0] != "iridium_imt_0" || heard[1] != "iridium_imt_0" {
		t.Fatalf("heard %v, want iridium_imt_0 twice", heard)
	}
}
