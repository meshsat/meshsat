package database

import (
	"database/sql"
	"testing"
)

func TestDeviceConfigVersionCRUD(t *testing.T) {
	db := testDB(t)

	// Create a device first
	devID, err := db.CreateDevice("300234063904190", "Unit 1", "rockblock", "")
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	// No config yet — should return ErrNoRows
	_, err = db.GetDeviceConfigLatest(devID)
	if err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}

	// Create first version
	v1, err := db.CreateDeviceConfigVersion(devID, "radio:\n  frequency: 915.0\n", "initial config")
	if err != nil {
		t.Fatalf("create v1: %v", err)
	}
	if v1.Version != 1 {
		t.Errorf("v1 version: got %d, want 1", v1.Version)
	}
	if v1.Comment != "initial config" {
		t.Errorf("v1 comment: got %q", v1.Comment)
	}

	// Create second version
	v2, err := db.CreateDeviceConfigVersion(devID, "radio:\n  frequency: 868.0\n", "switched to EU band")
	if err != nil {
		t.Fatalf("create v2: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("v2 version: got %d, want 2", v2.Version)
	}

	// Get latest — should be v2
	latest, err := db.GetDeviceConfigLatest(devID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest.Version != 2 {
		t.Errorf("latest version: got %d, want 2", latest.Version)
	}

	// Get specific version
	got, err := db.GetDeviceConfigVersion(devID, 1)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if got.YAML != "radio:\n  frequency: 915.0\n" {
		t.Errorf("v1 yaml: got %q", got.YAML)
	}

	// List versions — should be newest first
	versions, err := db.GetDeviceConfigVersions(devID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("list len: got %d, want 2", len(versions))
	}
	if versions[0].Version != 2 {
		t.Errorf("first in list should be v2, got v%d", versions[0].Version)
	}

	// Non-existent version
	_, err = db.GetDeviceConfigVersion(devID, 99)
	if err == nil {
		t.Error("expected error for non-existent version")
	}
}

func TestDeviceConfigVersionCascadeDelete(t *testing.T) {
	db := testDB(t)

	devID, _ := db.CreateDevice("300234063904190", "Unit 1", "rockblock", "")
	db.CreateDeviceConfigVersion(devID, "key: value", "test")

	// Delete the device — config versions should be cascade-deleted
	if err := db.DeleteDevice(devID); err != nil {
		t.Fatalf("delete device: %v", err)
	}

	versions, err := db.GetDeviceConfigVersions(devID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected 0 versions after cascade delete, got %d", len(versions))
	}
}
