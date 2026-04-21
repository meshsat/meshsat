package api

import (
	"context"
	"sync"
	"testing"
)

// The reconciler's behaviour is driven entirely by readP2PStatus +
// three ip-helper calls. These tests inject fakes via the package-
// level p2pReadStatus/p2pAssignIP/p2pRemoveIP/p2pIfaceHasIP seams so
// the state machine can be exercised end-to-end without actually
// shelling out to nsenter. Each test restores the originals via
// t.Cleanup.

type p2pFakes struct {
	mu        sync.Mutex
	next      WiFiP2PStatus
	nextErr   error
	assigned  map[string]string // iface -> ip last assigned
	removed   map[string]string // iface -> ip last removed
	hasIP     bool
	assignErr error
}

func (f *p2pFakes) SetStatus(st WiFiP2PStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = st
	f.nextErr = nil
}

func (f *p2pFakes) SetHasIP(b bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hasIP = b
}

func (f *p2pFakes) Assigned() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.assigned {
		out[k] = v
	}
	return out
}

func (f *p2pFakes) Removed() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.removed {
		out[k] = v
	}
	return out
}

func installP2PFakes(t *testing.T) *p2pFakes {
	t.Helper()
	f := &p2pFakes{
		assigned: map[string]string{},
		removed:  map[string]string{},
		hasIP:    true,
	}
	origStatus := p2pReadStatus
	origAssign := p2pAssignIP
	origRemove := p2pRemoveIP
	origHasIP := p2pIfaceHasIP
	p2pReadStatus = func(_ context.Context, _ string) (WiFiP2PStatus, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.next, f.nextErr
	}
	p2pAssignIP = func(_ context.Context, iface, ip string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.assignErr != nil {
			return f.assignErr
		}
		f.assigned[iface] = ip
		return nil
	}
	p2pRemoveIP = func(_ context.Context, iface, ip string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.removed[iface] = ip
		return nil
	}
	p2pIfaceHasIP = func(_ context.Context, iface, ip string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.hasIP && f.assigned[iface] == ip
	}
	t.Cleanup(func() {
		p2pReadStatus = origStatus
		p2pAssignIP = origAssign
		p2pRemoveIP = origRemove
		p2pIfaceHasIP = origHasIP
	})
	return f
}

// TestReconcileInactive: no P2P group active → reconciler is a no-op
// and nothing is assigned.
func TestReconcileInactive(t *testing.T) {
	f := installP2PFakes(t)
	f.SetStatus(WiFiP2PStatus{Active: false})
	r := NewP2PReconciler(&Server{})
	r.reconcileOnce(context.Background())
	if got := r.Snapshot(); got.Iface != "" || got.LocalIP != "" {
		t.Errorf("expected empty snapshot, got %+v", got)
	}
	if len(f.Assigned()) != 0 {
		t.Errorf("no assign expected: %v", f.Assigned())
	}
}

// TestReconcileGORoleAssigns: group comes up with role=go → 10.42.43.1
// is assigned to the group iface.
func TestReconcileGORoleAssigns(t *testing.T) {
	f := installP2PFakes(t)
	f.SetStatus(WiFiP2PStatus{Active: true, Role: "go", GroupIface: "p2p-0"})
	r := NewP2PReconciler(&Server{})
	r.reconcileOnce(context.Background())
	snap := r.Snapshot()
	if snap.Iface != "p2p-0" {
		t.Errorf("iface: got %q want p2p-0", snap.Iface)
	}
	if snap.LocalIP != P2POverlayGOIP {
		t.Errorf("local_ip: got %q want %q", snap.LocalIP, P2POverlayGOIP)
	}
	if snap.PeerAddr != P2POverlayClientIP+":4242" {
		t.Errorf("peer_addr: got %q want %s:4242", snap.PeerAddr, P2POverlayClientIP)
	}
	if f.Assigned()["p2p-0"] != P2POverlayGOIP {
		t.Errorf("assign record: %v", f.Assigned())
	}
}

// TestReconcileClientRoleAssigns: group comes up with role=client →
// 10.42.43.2 is assigned.
func TestReconcileClientRoleAssigns(t *testing.T) {
	f := installP2PFakes(t)
	f.SetStatus(WiFiP2PStatus{Active: true, Role: "client", GroupIface: "p2p-wlx-0"})
	r := NewP2PReconciler(&Server{})
	r.reconcileOnce(context.Background())
	snap := r.Snapshot()
	if snap.LocalIP != P2POverlayClientIP {
		t.Errorf("local_ip: got %q want %q", snap.LocalIP, P2POverlayClientIP)
	}
	if snap.PeerAddr != P2POverlayGOIP+":4242" {
		t.Errorf("peer_addr: got %q want %s:4242", snap.PeerAddr, P2POverlayGOIP)
	}
}

// TestReconcileIfaceRename: group iface was p2p-0, later wpa_supplicant
// renames it to p2p-wlx-0 (observed live). Reconciler must remove the
// old binding and reapply on the new iface without dropping the TCP
// peer address (peer IP is stable, so the same peerAddr is fine).
func TestReconcileIfaceRename(t *testing.T) {
	f := installP2PFakes(t)
	r := NewP2PReconciler(&Server{})

	// T0: group up as GO on p2p-0.
	f.SetStatus(WiFiP2PStatus{Active: true, Role: "go", GroupIface: "p2p-0"})
	r.reconcileOnce(context.Background())
	if r.Snapshot().Iface != "p2p-0" {
		t.Fatalf("T0 iface: got %q", r.Snapshot().Iface)
	}

	// T1: wpa_supplicant renamed the group iface.
	f.SetStatus(WiFiP2PStatus{Active: true, Role: "go", GroupIface: "p2p-wlx-0"})
	r.reconcileOnce(context.Background())
	snap := r.Snapshot()
	if snap.Iface != "p2p-wlx-0" {
		t.Errorf("T1 iface: got %q want p2p-wlx-0", snap.Iface)
	}
	// Old iface had its IP removed, new iface has it.
	if f.Removed()["p2p-0"] != P2POverlayGOIP {
		t.Errorf("expected remove on p2p-0: %v", f.Removed())
	}
	if f.Assigned()["p2p-wlx-0"] != P2POverlayGOIP {
		t.Errorf("expected assign on p2p-wlx-0: %v", f.Assigned())
	}
}

// TestReconcileTeardownOnInactive: group goes inactive → reconciler
// strips the overlay IP it owned. Future reconciles should not leak
// state.
func TestReconcileTeardownOnInactive(t *testing.T) {
	f := installP2PFakes(t)
	r := NewP2PReconciler(&Server{})

	f.SetStatus(WiFiP2PStatus{Active: true, Role: "client", GroupIface: "p2p-0"})
	r.reconcileOnce(context.Background())
	f.SetStatus(WiFiP2PStatus{Active: false})
	r.reconcileOnce(context.Background())

	snap := r.Snapshot()
	if snap.Iface != "" || snap.LocalIP != "" || snap.PeerAddr != "" {
		t.Errorf("snapshot not cleared: %+v", snap)
	}
	if f.Removed()["p2p-0"] != P2POverlayClientIP {
		t.Errorf("expected removal on teardown: %v", f.Removed())
	}
}

// TestReconcileSelfHealIPStripped: IP was stripped externally (e.g.
// NetworkManager re-scanned) but the group is still up — reconciler
// re-applies on the next tick.
func TestReconcileSelfHealIPStripped(t *testing.T) {
	f := installP2PFakes(t)
	r := NewP2PReconciler(&Server{})

	f.SetStatus(WiFiP2PStatus{Active: true, Role: "go", GroupIface: "p2p-0"})
	r.reconcileOnce(context.Background())

	// Simulate external strip.
	f.SetHasIP(false)
	// Clear the "assigned" record so next ifaceHasIP lookup misses.
	f.mu.Lock()
	delete(f.assigned, "p2p-0")
	f.mu.Unlock()

	r.reconcileOnce(context.Background())
	if f.Assigned()["p2p-0"] != P2POverlayGOIP {
		t.Errorf("expected re-apply after strip: %v", f.Assigned())
	}
}

// TestReconcileRoleUnknown: wpa_cli hasn't populated role= yet
// (happens briefly during group formation). Reconciler must not
// assign an arbitrary IP — it waits for the next tick.
func TestReconcileRoleUnknown(t *testing.T) {
	f := installP2PFakes(t)
	f.SetStatus(WiFiP2PStatus{Active: true, Role: "", GroupIface: "p2p-0"})
	r := NewP2PReconciler(&Server{})
	r.reconcileOnce(context.Background())
	if len(f.Assigned()) != 0 {
		t.Errorf("should not assign with unknown role: %v", f.Assigned())
	}
	if r.Snapshot().Iface != "" {
		t.Errorf("snapshot should stay empty until role known: %+v", r.Snapshot())
	}
}

// TestReconcileReadStatusError: wpa_cli failed (not running yet).
// Reconciler must not touch state.
func TestReconcileReadStatusError(t *testing.T) {
	f := installP2PFakes(t)
	f.mu.Lock()
	f.nextErr = context.DeadlineExceeded
	f.mu.Unlock()
	r := NewP2PReconciler(&Server{})
	r.reconcileOnce(context.Background())
	if len(f.Assigned()) != 0 || len(f.Removed()) != 0 {
		t.Errorf("no state change expected on read error")
	}
}
