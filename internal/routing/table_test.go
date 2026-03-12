package routing

import (
	"testing"
)

func TestDestinationTable_UpdateNew(t *testing.T) {
	table := NewDestinationTable(nil)

	id := testIdentity(t)
	announce, _ := NewAnnounce(id, []byte("node-1"))

	isNew := table.Update(announce, "mesh_0")
	if !isNew {
		t.Fatal("first update should return isNew=true")
	}
	if table.Count() != 1 {
		t.Errorf("count: got %d, want 1", table.Count())
	}

	dest := table.Lookup(announce.DestHash)
	if dest == nil {
		t.Fatal("lookup should find the destination")
	}
	if string(dest.AppData) != "node-1" {
		t.Errorf("app data: got %q, want %q", dest.AppData, "node-1")
	}
	if dest.HopCount != 0 {
		t.Errorf("hop count: got %d, want 0", dest.HopCount)
	}
	if dest.SourceIface != "mesh_0" {
		t.Errorf("source: got %q, want %q", dest.SourceIface, "mesh_0")
	}
	if dest.AnnounceCount != 1 {
		t.Errorf("count: got %d, want 1", dest.AnnounceCount)
	}
}

func TestDestinationTable_UpdateExisting(t *testing.T) {
	table := NewDestinationTable(nil)

	id := testIdentity(t)
	a1, _ := NewAnnounce(id, []byte("v1"))
	a1.HopCount = 3

	table.Update(a1, "mesh_0")

	// Second announce with lower hop count should update path
	a2, _ := NewAnnounce(id, []byte("v2"))
	a2.HopCount = 1

	isNew := table.Update(a2, "iridium_0")
	if isNew {
		t.Fatal("second update should return isNew=false")
	}

	dest := table.Lookup(id.DestHash())
	if dest.HopCount != 1 {
		t.Errorf("hop count should be updated to 1, got %d", dest.HopCount)
	}
	if dest.SourceIface != "iridium_0" {
		t.Errorf("source should be updated to iridium_0, got %s", dest.SourceIface)
	}
	if dest.AnnounceCount != 2 {
		t.Errorf("count: got %d, want 2", dest.AnnounceCount)
	}
	if string(dest.AppData) != "v2" {
		t.Errorf("app data should be updated to v2, got %q", dest.AppData)
	}
}

func TestDestinationTable_UpdateExisting_HigherHops(t *testing.T) {
	table := NewDestinationTable(nil)

	id := testIdentity(t)
	a1, _ := NewAnnounce(id, nil)
	a1.HopCount = 1
	table.Update(a1, "mesh_0")

	// Higher hop count should NOT update path
	a2, _ := NewAnnounce(id, nil)
	a2.HopCount = 5
	table.Update(a2, "iridium_0")

	dest := table.Lookup(id.DestHash())
	if dest.HopCount != 1 {
		t.Errorf("hop count should remain 1, got %d", dest.HopCount)
	}
	if dest.SourceIface != "mesh_0" {
		t.Errorf("source should remain mesh_0, got %s", dest.SourceIface)
	}
}

func TestDestinationTable_Lookup_Unknown(t *testing.T) {
	table := NewDestinationTable(nil)
	if table.Lookup([DestHashLen]byte{}) != nil {
		t.Fatal("unknown hash should return nil")
	}
}

func TestDestinationTable_All(t *testing.T) {
	table := NewDestinationTable(nil)

	for i := 0; i < 5; i++ {
		id := testIdentity(t)
		a, _ := NewAnnounce(id, nil)
		table.Update(a, "mesh_0")
	}

	all := table.All()
	if len(all) != 5 {
		t.Errorf("All: got %d, want 5", len(all))
	}
}

func TestDestinationTable_Remove(t *testing.T) {
	table := NewDestinationTable(nil)
	id := testIdentity(t)
	a, _ := NewAnnounce(id, nil)
	table.Update(a, "mesh_0")

	table.Remove(a.DestHash)
	if table.Count() != 0 {
		t.Error("count should be 0 after remove")
	}
	if table.Lookup(a.DestHash) != nil {
		t.Error("lookup should return nil after remove")
	}
}
