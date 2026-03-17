package database

import "testing"

func TestDeviceCRUD(t *testing.T) {
	db := testDB(t)

	// Create
	id, err := db.CreateDevice("300234063904190", "Field Unit 1", "rockblock", "deployed in NL")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	// Get by ID
	d, err := db.GetDevice(id)
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
	if d.LastSeen != nil {
		t.Errorf("last_seen: expected nil, got %v", d.LastSeen)
	}

	// Get by IMEI
	d2, err := db.GetDeviceByIMEI("300234063904190")
	if err != nil {
		t.Fatalf("get by imei: %v", err)
	}
	if d2.ID != id {
		t.Errorf("get by imei ID: got %d, want %d", d2.ID, id)
	}

	// List
	devices, err := db.GetDevices()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("list len: got %d, want 1", len(devices))
	}

	// Update
	if err := db.UpdateDevice(id, "Updated Unit", "iridium", "new notes"); err != nil {
		t.Fatalf("update: %v", err)
	}
	d3, _ := db.GetDevice(id)
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
	d4, _ := db.GetDevice(id)
	if d4.LastSeen == nil {
		t.Error("last_seen: expected non-nil after touch")
	}

	// Delete
	if err := db.DeleteDevice(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	devices2, _ := db.GetDevices()
	if len(devices2) != 0 {
		t.Errorf("after delete: got %d devices, want 0", len(devices2))
	}
}

func TestDeviceDuplicateIMEI(t *testing.T) {
	db := testDB(t)

	_, err := db.CreateDevice("300234063904190", "Unit 1", "", "")
	if err != nil {
		t.Fatalf("create first: %v", err)
	}

	_, err = db.CreateDevice("300234063904190", "Unit 2", "", "")
	if err == nil {
		t.Error("expected error on duplicate IMEI, got nil")
	}
}

func TestDeviceGetByIMEI_NotFound(t *testing.T) {
	db := testDB(t)

	_, err := db.GetDeviceByIMEI("999999999999999")
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
