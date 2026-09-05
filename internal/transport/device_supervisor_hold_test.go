package transport

import (
	"sync/atomic"
	"testing"
	"time"
)

// A held role gets its port claimed at once but the driver is only told
// when the hold expires. [MESHSAT-817]
func TestSupervisorHoldDefersNotify(t *testing.T) {
	s := NewDeviceSupervisor()
	var found atomic.Int32
	s.SetCallbacks(RoleCellular, &DriverCallbacks{
		InstanceID:  "cellular_0",
		OnPortFound: func(string) { found.Add(1) },
		OnPortLost:  func(string) {},
		HasPort:     func() bool { return false },
	})
	const port = "/dev/ttyHOLD0"
	if !s.registry.ClaimPort(port, RoleCellular) {
		t.Fatal("claim failed")
	}

	s.HoldRole(RoleCellular, 100*time.Millisecond)
	if rem := s.holdRemaining(RoleCellular); rem <= 0 {
		t.Fatalf("holdRemaining %v", rem)
	}
	s.notifyPortFoundHeld(RoleCellular, port)
	if found.Load() != 0 {
		t.Fatal("driver notified inside the hold")
	}
	deadline := time.Now().Add(2 * time.Second)
	for found.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if found.Load() != 1 {
		t.Fatalf("driver notified %d times after the hold, want 1", found.Load())
	}
	if s.GetPortInstance(port) != "cellular_0" {
		t.Fatalf("port instance %q", s.GetPortInstance(port))
	}

	// Other roles are not held.
	if rem := s.holdRemaining(RoleMeshtastic); rem != 0 {
		t.Fatalf("mesh held: %v", rem)
	}
	s.notifyPortFoundHeld(RoleCellular, port)
	if found.Load() != 2 {
		t.Fatalf("immediate notify after the hold expired: %d", found.Load())
	}
}
