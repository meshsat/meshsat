package transport

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeviceSupervisor_ExcludePort: an excluded serial port, given as a
// symlink the way /dev/serial/by-id names it, is dropped from the scan list
// and never probed. [MESHSAT-821]
func TestDeviceSupervisor_ExcludePort(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "ttyUSB7")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "usb-Silicon_Labs_CP2102N-if00-port0")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "ttyUSB8")

	s := NewDeviceSupervisor()
	if s.IsExcluded(real) {
		t.Fatal("nothing excluded yet")
	}
	s.ExcludePort(link)
	if !s.IsExcluded(real) || !s.IsExcluded(link) {
		t.Fatal("real path and symlink must both read as excluded")
	}
	if s.IsExcluded(other) {
		t.Fatal("unrelated port excluded")
	}
	got := s.filterExcluded([]string{real, other})
	if len(got) != 1 || got[0] != other {
		t.Fatalf("filterExcluded = %v", got)
	}

	// A path that does not exist yet (device not plugged in) is kept and
	// matched once it appears.
	s2 := NewDeviceSupervisor()
	later := filepath.Join(dir, "later-link")
	s2.ExcludePort(later)
	if !s2.IsExcluded(later) {
		t.Fatal("literal match must hold before the link exists")
	}
	if err := os.Symlink(real, later); err != nil {
		t.Fatal(err)
	}
	if !s2.IsExcluded(real) {
		t.Fatal("must match the real path once the link resolves")
	}
}
