package database

import (
	"database/sql"
	"testing"
)

func TestDeviceConfigCreateAndList(t *testing.T) {
	db := testDB(t)
	devID, err := db.CreateDevice("300234063904190", "Unit 1", "rockblock", "", "default")
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	// Create 3 versions
	for i, yml := range []string{"key: v1", "key: v2", "key: v3"} {
		v, err := db.CreateDeviceConfigVersion(devID, yml, "comment "+string(rune('1'+i)))
		if err != nil {
			t.Fatalf("create config %d: %v", i+1, err)
		}
		if v.Version != i+1 {
			t.Errorf("version: got %d, want %d", v.Version, i+1)
		}
	}

	// List returns newest first
	versions, err := db.GetDeviceConfigVersions(devID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("list len: got %d, want 3", len(versions))
	}
	if versions[0].Version != 3 {
		t.Errorf("first in list: got version %d, want 3", versions[0].Version)
	}
	if versions[2].Version != 1 {
		t.Errorf("last in list: got version %d, want 1", versions[2].Version)
	}
}

func TestDeviceConfigGetLatest(t *testing.T) {
	db := testDB(t)
	devID, _ := db.CreateDevice("300234063904190", "Unit 1", "rockblock", "", "default")

	db.CreateDeviceConfigVersion(devID, "key: v1", "first")
	db.CreateDeviceConfigVersion(devID, "key: v2", "second")

	latest, err := db.GetDeviceConfigLatest(devID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest.Version != 2 {
		t.Errorf("version: got %d, want 2", latest.Version)
	}
	if latest.YAML != "key: v2" {
		t.Errorf("yaml: got %q, want %q", latest.YAML, "key: v2")
	}
}

func TestDeviceConfigGetByVersion(t *testing.T) {
	db := testDB(t)
	devID, _ := db.CreateDevice("300234063904190", "Unit 1", "rockblock", "", "default")

	db.CreateDeviceConfigVersion(devID, "key: v1", "first")
	db.CreateDeviceConfigVersion(devID, "key: v2", "second")

	v, err := db.GetDeviceConfigVersion(devID, 1)
	if err != nil {
		t.Fatalf("get version 1: %v", err)
	}
	if v.YAML != "key: v1" {
		t.Errorf("yaml: got %q, want %q", v.YAML, "key: v1")
	}
}

func TestDeviceConfigAutoIncrement(t *testing.T) {
	db := testDB(t)
	devID, _ := db.CreateDevice("300234063904190", "Unit 1", "rockblock", "", "default")

	for i := 1; i <= 3; i++ {
		v, err := db.CreateDeviceConfigVersion(devID, "key: val", "")
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if v.Version != i {
			t.Errorf("version %d: got %d", i, v.Version)
		}
	}
}

func TestDeviceConfigCascadeDelete(t *testing.T) {
	db := testDB(t)
	devID, _ := db.CreateDevice("300234063904190", "Unit 1", "rockblock", "", "default")

	db.CreateDeviceConfigVersion(devID, "key: v1", "first")
	db.CreateDeviceConfigVersion(devID, "key: v2", "second")

	// Delete device — configs should cascade
	if err := db.DeleteDevice(devID, "default"); err != nil {
		t.Fatalf("delete device: %v", err)
	}

	versions, err := db.GetDeviceConfigVersions(devID)
	if err != nil {
		t.Fatalf("list after cascade: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected 0 versions after cascade delete, got %d", len(versions))
	}

	_, err = db.GetDeviceConfigLatest(devID)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestDeviceConfigDeleteVersion(t *testing.T) {
	db := testDB(t)
	devID, _ := db.CreateDevice("300234063904190", "Unit 1", "rockblock", "", "default")

	db.CreateDeviceConfigVersion(devID, "key: v1", "first")
	db.CreateDeviceConfigVersion(devID, "key: v2", "second")
	db.CreateDeviceConfigVersion(devID, "key: v3", "third")

	// Delete version 2
	if err := db.DeleteDeviceConfig(devID, 2); err != nil {
		t.Fatalf("delete version: %v", err)
	}

	versions, err := db.GetDeviceConfigVersions(devID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("list len: got %d, want 2", len(versions))
	}

	// Versions 1 and 3 remain
	_, err = db.GetDeviceConfigVersion(devID, 1)
	if err != nil {
		t.Errorf("version 1 should exist: %v", err)
	}
	_, err = db.GetDeviceConfigVersion(devID, 3)
	if err != nil {
		t.Errorf("version 3 should exist: %v", err)
	}
	_, err = db.GetDeviceConfigVersion(devID, 2)
	if err == nil {
		t.Error("version 2 should be deleted")
	}
}
