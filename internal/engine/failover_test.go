package engine

import (
	"testing"

	"meshsat/internal/database"
)

func setupFailoverTest(t *testing.T) (*FailoverResolver, *InterfaceManager, *database.DB) {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	ifaceMgr := NewInterfaceManager(db)

	// Insert interfaces into DB and prime the manager's runtime state
	for _, iface := range []database.Interface{
		{ID: "iridium_0", ChannelType: "iridium", Label: "Iridium Primary", Enabled: true},
		{ID: "iridium_1", ChannelType: "iridium", Label: "Iridium Backup", Enabled: true},
	} {
		if err := db.InsertInterface(&iface); err != nil {
			t.Fatal(err)
		}
	}

	// Load interfaces into manager runtime (simulates Start without device scanning)
	ifaces, _ := db.GetAllInterfaces()
	ifaceMgr.mu.Lock()
	for _, iface := range ifaces {
		ifaceMgr.states[iface.ID] = &interfaceRuntime{
			iface: iface,
			state: StateOffline,
		}
	}
	ifaceMgr.mu.Unlock()

	fr := NewFailoverResolver(db, ifaceMgr)
	return fr, ifaceMgr, db
}

func TestFailoverResolver_PlainInterface(t *testing.T) {
	fr, _, _ := setupFailoverTest(t)

	// A plain interface ID (not a failover group) should be returned as-is
	got := fr.Resolve("iridium_0")
	if got != "iridium_0" {
		t.Errorf("expected iridium_0, got %s", got)
	}

	// Unknown ID should also pass through (not a group, not an interface)
	got = fr.Resolve("nonexistent_99")
	if got != "nonexistent_99" {
		t.Errorf("expected nonexistent_99, got %s", got)
	}
}

func TestFailoverResolver_PrimaryOnline(t *testing.T) {
	fr, ifaceMgr, db := setupFailoverTest(t)

	// Create failover group
	db.InsertFailoverGroup(&database.FailoverGroup{ID: "sat_group", Label: "Satellite Group", Mode: "failover"})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_0", Priority: 1})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_1", Priority: 2})

	// Set primary online
	ifaceMgr.mu.Lock()
	ifaceMgr.states["iridium_0"].state = StateOnline
	ifaceMgr.states["iridium_1"].state = StateOnline
	ifaceMgr.mu.Unlock()

	got := fr.Resolve("sat_group")
	if got != "iridium_0" {
		t.Errorf("expected iridium_0 (highest priority online), got %s", got)
	}
}

func TestFailoverResolver_PrimaryOffline(t *testing.T) {
	fr, ifaceMgr, db := setupFailoverTest(t)

	db.InsertFailoverGroup(&database.FailoverGroup{ID: "sat_group", Label: "Satellite Group", Mode: "failover"})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_0", Priority: 1})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_1", Priority: 2})

	// Primary offline, backup online
	ifaceMgr.mu.Lock()
	ifaceMgr.states["iridium_0"].state = StateOffline
	ifaceMgr.states["iridium_1"].state = StateOnline
	ifaceMgr.mu.Unlock()

	got := fr.Resolve("sat_group")
	if got != "iridium_1" {
		t.Errorf("expected iridium_1 (failover to backup), got %s", got)
	}
}

func TestFailoverResolver_AllOffline_FallbackEnabled(t *testing.T) {
	fr, ifaceMgr, db := setupFailoverTest(t)

	db.InsertFailoverGroup(&database.FailoverGroup{ID: "sat_group", Label: "Satellite Group", Mode: "failover"})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_0", Priority: 1})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_1", Priority: 2})

	// Both offline but enabled
	ifaceMgr.mu.Lock()
	ifaceMgr.states["iridium_0"].state = StateOffline
	ifaceMgr.states["iridium_1"].state = StateOffline
	ifaceMgr.mu.Unlock()

	got := fr.Resolve("sat_group")
	if got != "iridium_0" {
		t.Errorf("expected iridium_0 (first enabled fallback), got %s", got)
	}
}

func TestFailoverResolver_AllDisabled(t *testing.T) {
	fr, ifaceMgr, db := setupFailoverTest(t)

	db.InsertFailoverGroup(&database.FailoverGroup{ID: "sat_group", Label: "Satellite Group", Mode: "failover"})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_0", Priority: 1})
	db.InsertFailoverMember(&database.FailoverMember{GroupID: "sat_group", InterfaceID: "iridium_1", Priority: 2})

	// Both offline and disabled
	ifaceMgr.mu.Lock()
	ifaceMgr.states["iridium_0"].state = StateOffline
	ifaceMgr.states["iridium_0"].iface.Enabled = false
	ifaceMgr.states["iridium_1"].state = StateOffline
	ifaceMgr.states["iridium_1"].iface.Enabled = false
	ifaceMgr.mu.Unlock()

	got := fr.Resolve("sat_group")
	if got != "" {
		t.Errorf("expected empty string (no available member), got %s", got)
	}
}

func TestFailoverResolver_EmptyGroup(t *testing.T) {
	fr, _, db := setupFailoverTest(t)

	// Group with no members
	db.InsertFailoverGroup(&database.FailoverGroup{ID: "empty_group", Label: "Empty", Mode: "failover"})

	got := fr.Resolve("empty_group")
	if got != "" {
		t.Errorf("expected empty string (no members), got %s", got)
	}
}
