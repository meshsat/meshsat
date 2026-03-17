package database

import "testing"

const testTenant = "default"

func TestDeviceCRUD(t *testing.T) {
	db := testDB(t)

	// Create
	id, err := db.CreateDevice("300234063904190", "Field Unit 1", "rockblock", "deployed in NL", testTenant)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	// Get by ID
	d, err := db.GetDevice(id, testTenant)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if d.IMEI != "300234063904190" {
		t.Errorf("imei: got %q, want 300234063904190", d.IMEI)
	}
	if d.Label != "Field Unit 1" {
		t.Errorf("label: got %q, want Field Unit 1", d.Label)
	}
	if d.Type != "rockblock" {
		t.Errorf("type: got %q, want rockblock", d.Type)
	}
	if d.Notes != "deployed in NL" {
		t.Errorf("notes: got %q, want 'deployed in NL'", d.Notes)
	}
	if d.TenantID != testTenant {
		t.Errorf("tenant_id: got %q, want %q", d.TenantID, testTenant)
	}
	if d.LastSeen != nil {
		t.Errorf("last_seen: expected nil, got %v", d.LastSeen)
	}

	// Get by IMEI
	d2, err := db.GetDeviceByIMEI("300234063904190", testTenant)
	if err != nil {
		t.Fatalf("get by imei: %v", err)
	}
	if d2.ID != id {
		t.Errorf("get by imei ID: got %d, want %d", d2.ID, id)
	}

	// List
	devices, err := db.GetDevices(testTenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("list len: got %d, want 1", len(devices))
	}

	// Update
	if err := db.UpdateDevice(id, "Updated Unit", "iridium", "new notes", testTenant); err != nil {
		t.Fatalf("update: %v", err)
	}
	d3, _ := db.GetDevice(id, testTenant)
	if d3.Label != "Updated Unit" {
		t.Errorf("updated label: got %q, want Updated Unit", d3.Label)
	}
	if d3.Type != "iridium" {
		t.Errorf("updated type: got %q, want iridium", d3.Type)
	}

	// Touch last_seen
	if err := db.TouchDeviceLastSeen("300234063904190"); err != nil {
		t.Fatalf("touch last_seen: %v", err)
	}
	d4, _ := db.GetDevice(id, testTenant)
	if d4.LastSeen == nil {
		t.Error("last_seen: expected non-nil after touch")
	}

	// Delete
	if err := db.DeleteDevice(id, testTenant); err != nil {
		t.Fatalf("delete: %v", err)
	}
	devices2, _ := db.GetDevices(testTenant)
	if len(devices2) != 0 {
		t.Errorf("after delete: got %d devices, want 0", len(devices2))
	}
}

func TestDeviceTenantIsolation(t *testing.T) {
	db := testDB(t)

	// Create devices in different tenants
	id1, _ := db.CreateDevice("300234063904190", "Tenant A device", "rockblock", "", "tenant-a")
	id2, _ := db.CreateDevice("300234063904191", "Tenant B device", "rockblock", "", "tenant-b")

	// Tenant A can only see their device
	devicesA, _ := db.GetDevices("tenant-a")
	if len(devicesA) != 1 {
		t.Fatalf("tenant-a: got %d devices, want 1", len(devicesA))
	}
	if devicesA[0].ID != id1 {
		t.Errorf("tenant-a: got device %d, want %d", devicesA[0].ID, id1)
	}

	// Tenant B can only see their device
	devicesB, _ := db.GetDevices("tenant-b")
	if len(devicesB) != 1 {
		t.Fatalf("tenant-b: got %d devices, want 1", len(devicesB))
	}
	if devicesB[0].ID != id2 {
		t.Errorf("tenant-b: got device %d, want %d", devicesB[0].ID, id2)
	}

	// Cross-tenant GetDevice fails
	_, err := db.GetDevice(id1, "tenant-b")
	if err == nil {
		t.Error("expected error accessing tenant-a device from tenant-b")
	}

	// Cross-tenant Update fails
	err = db.UpdateDevice(id1, "Hacked", "hacked", "", "tenant-b")
	if err == nil {
		t.Error("expected error updating tenant-a device from tenant-b")
	}

	// Cross-tenant Delete fails
	err = db.DeleteDevice(id1, "tenant-b")
	if err == nil {
		t.Error("expected error deleting tenant-a device from tenant-b")
	}

	// AnyTenant sees all
	all, _ := db.GetDevicesAnyTenant()
	if len(all) != 2 {
		t.Errorf("any-tenant: got %d devices, want 2", len(all))
	}
}

func TestDeviceDuplicateIMEI(t *testing.T) {
	db := testDB(t)

	_, err := db.CreateDevice("300234063904190", "Unit 1", "", "", testTenant)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}

	_, err = db.CreateDevice("300234063904190", "Unit 2", "", "", testTenant)
	if err == nil {
		t.Error("expected error on duplicate IMEI, got nil")
	}
}

func TestDeviceGetByIMEI_NotFound(t *testing.T) {
	db := testDB(t)

	_, err := db.GetDeviceByIMEI("999999999999999", testTenant)
	if err == nil {
		t.Error("expected error for non-existent IMEI")
	}
}

func TestDeviceTouchLastSeen_NonExistentIMEI(t *testing.T) {
	db := testDB(t)

	// Should not error, just affect 0 rows
	if err := db.TouchDeviceLastSeen("999999999999999"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
