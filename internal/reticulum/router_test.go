package reticulum

import (
	"testing"
	"time"
)

func makeTestAnnounce(t *testing.T, appName string) *Announce {
	t.Helper()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAnnounce(id, appName, []byte("test-node"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRouter_ProcessAnnounce_NewRoute(t *testing.T) {
	rt := NewRouter(5 * time.Minute)
	a := makeTestAnnounce(t, "meshsat.hub")

	if !rt.ProcessAnnounce(a, IfaceMQTT) {
		t.Error("expected new route to be accepted")
	}
	if rt.RouteCount() != 1 {
		t.Errorf("route count: got %d, want 1", rt.RouteCount())
	}

	entry := rt.Lookup(a.DestHash)
	if entry == nil {
		t.Fatal("expected route to be found")
	}
	if entry.Interface != IfaceMQTT {
		t.Errorf("interface: got %s, want %s", entry.Interface, IfaceMQTT)
	}
	if entry.Cost != 0 {
		t.Errorf("cost: got %f, want 0", entry.Cost)
	}
	if entry.Hops != 0 {
		t.Errorf("hops: got %d, want 0", entry.Hops)
	}
}

func TestRouter_ProcessAnnounce_PrefersCheaper(t *testing.T) {
	rt := NewRouter(5 * time.Minute)
	a := makeTestAnnounce(t, "meshsat.hub")

	// Learn via expensive Iridium first.
	rt.ProcessAnnounce(a, IfaceIridium)
	entry := rt.Lookup(a.DestHash)
	if entry.Interface != IfaceIridium {
		t.Fatal("expected iridium route")
	}

	// Same announce arrives via free MQTT — should replace.
	if !rt.ProcessAnnounce(a, IfaceMQTT) {
		t.Error("cheaper route should be accepted")
	}
	entry = rt.Lookup(a.DestHash)
	if entry.Interface != IfaceMQTT {
		t.Errorf("interface: got %s, want %s (cheaper)", entry.Interface, IfaceMQTT)
	}
}

func TestRouter_ProcessAnnounce_RejectsCostlier(t *testing.T) {
	rt := NewRouter(5 * time.Minute)
	a := makeTestAnnounce(t, "meshsat.hub")

	// Learn via free MQTT first.
	rt.ProcessAnnounce(a, IfaceMQTT)

	// Same announce arrives via expensive Iridium — should reject.
	if rt.ProcessAnnounce(a, IfaceIridium) {
		t.Error("costlier route should be rejected")
	}
	entry := rt.Lookup(a.DestHash)
	if entry.Interface != IfaceMQTT {
		t.Error("route should still be MQTT")
	}
}

func TestRouter_ProcessAnnounce_RefreshTimestamp(t *testing.T) {
	rt := NewRouter(5 * time.Minute)
	a := makeTestAnnounce(t, "meshsat.hub")

	rt.ProcessAnnounce(a, IfaceMQTT)
	entry1 := rt.Lookup(a.DestHash)
	firstSeen := entry1.LastSeen

	time.Sleep(2 * time.Millisecond)

	// Same announce, same interface — should refresh timestamp.
	rt.ProcessAnnounce(a, IfaceMQTT)
	entry2 := rt.Lookup(a.DestHash)
	if !entry2.LastSeen.After(firstSeen) {
		t.Error("timestamp should be refreshed")
	}
}

func TestRouter_Lookup_Unknown(t *testing.T) {
	rt := NewRouter(5 * time.Minute)
	var unknown [TruncatedHashLen]byte
	unknown[0] = 0xFF

	if rt.Lookup(unknown) != nil {
		t.Error("expected nil for unknown destination")
	}
}

func TestRouter_Lookup_Expired(t *testing.T) {
	rt := NewRouter(1 * time.Millisecond)
	a := makeTestAnnounce(t, "meshsat.hub")

	rt.ProcessAnnounce(a, IfaceMQTT)
	time.Sleep(5 * time.Millisecond)

	if rt.Lookup(a.DestHash) != nil {
		t.Error("expired route should return nil")
	}
}

func TestRouter_LookupHex(t *testing.T) {
	rt := NewRouter(5 * time.Minute)
	a := makeTestAnnounce(t, "meshsat.hub")
	rt.ProcessAnnounce(a, IfaceMQTT)

	destHex := DestHashHex(a.DestHash)
	entry := rt.LookupHex(destHex)
	if entry == nil {
		t.Fatal("LookupHex should find the route")
	}

	// Invalid hex.
	if rt.LookupHex("zzzz") != nil {
		t.Error("invalid hex should return nil")
	}
	if rt.LookupHex("aabb") != nil {
		t.Error("wrong length should return nil")
	}
}

func TestRouter_AllRoutes(t *testing.T) {
	rt := NewRouter(5 * time.Minute)

	a1 := makeTestAnnounce(t, "meshsat.hub")
	a2 := makeTestAnnounce(t, "meshsat.bridge")

	rt.ProcessAnnounce(a1, IfaceMQTT)
	rt.ProcessAnnounce(a2, IfaceIridium)

	routes := rt.AllRoutes()
	if len(routes) != 2 {
		t.Errorf("route count: got %d, want 2", len(routes))
	}
}

func TestRouter_AllRoutes_ExcludesExpired(t *testing.T) {
	rt := NewRouter(1 * time.Millisecond)
	a := makeTestAnnounce(t, "meshsat.hub")
	rt.ProcessAnnounce(a, IfaceMQTT)

	time.Sleep(5 * time.Millisecond)

	routes := rt.AllRoutes()
	if len(routes) != 0 {
		t.Error("expired routes should not appear in AllRoutes")
	}
}

func TestRouter_ExpireStale(t *testing.T) {
	rt := NewRouter(1 * time.Millisecond)

	a1 := makeTestAnnounce(t, "meshsat.hub")
	a2 := makeTestAnnounce(t, "meshsat.bridge")
	rt.ProcessAnnounce(a1, IfaceMQTT)
	rt.ProcessAnnounce(a2, IfaceIridium)

	time.Sleep(5 * time.Millisecond)

	removed := rt.ExpireStale()
	if removed != 2 {
		t.Errorf("removed: got %d, want 2", removed)
	}
	if rt.RouteCount() != 0 {
		t.Error("no routes should remain")
	}
}

func TestRouter_Remove(t *testing.T) {
	rt := NewRouter(5 * time.Minute)
	a := makeTestAnnounce(t, "meshsat.hub")
	rt.ProcessAnnounce(a, IfaceMQTT)

	if !rt.Remove(a.DestHash) {
		t.Error("Remove should return true for existing route")
	}
	if rt.RouteCount() != 0 {
		t.Error("route should be gone")
	}
	if rt.Remove(a.DestHash) {
		t.Error("Remove should return false for non-existent route")
	}
}

func TestRouter_BestInterface(t *testing.T) {
	rt := NewRouter(5 * time.Minute)
	a := makeTestAnnounce(t, "meshsat.hub")
	rt.ProcessAnnounce(a, IfaceIridium)

	if rt.BestInterface(a.DestHash) != IfaceIridium {
		t.Error("expected iridium")
	}

	var unknown [TruncatedHashLen]byte
	if rt.BestInterface(unknown) != "" {
		t.Error("expected empty for unknown dest")
	}
}

func TestRouter_MultipleDestinations(t *testing.T) {
	rt := NewRouter(5 * time.Minute)

	for range 50 {
		a := makeTestAnnounce(t, "meshsat.hub")
		rt.ProcessAnnounce(a, IfaceMQTT)
	}

	if rt.RouteCount() != 50 {
		t.Errorf("route count: got %d, want 50", rt.RouteCount())
	}
}

func TestInterfaceCost(t *testing.T) {
	if InterfaceCost(IfaceMQTT) != 0 {
		t.Error("MQTT should be free")
	}
	if InterfaceCost(IfaceTor) != 0 {
		t.Error("Tor should be free")
	}
	if InterfaceCost(IfaceWireGuard) != 0 {
		t.Error("WireGuard should be free")
	}
	if InterfaceCost(IfaceIridium) != 0.05 {
		t.Errorf("Iridium cost: got %f, want 0.05", InterfaceCost(IfaceIridium))
	}
	if InterfaceCost(IfaceAstrocast) != 0.01 {
		t.Errorf("Astrocast cost: got %f, want 0.01", InterfaceCost(IfaceAstrocast))
	}
	if InterfaceCost(IfaceGlobalstar) != 0.02 {
		t.Errorf("Globalstar cost: got %f, want 0.02", InterfaceCost(IfaceGlobalstar))
	}
}

func TestRouteEntry_IsExpired(t *testing.T) {
	entry := &RouteEntry{ExpiresAt: time.Now().Add(-1 * time.Second)}
	if !entry.IsExpired() {
		t.Error("past expiry should be expired")
	}

	entry2 := &RouteEntry{ExpiresAt: time.Now().Add(1 * time.Hour)}
	if entry2.IsExpired() {
		t.Error("future expiry should not be expired")
	}
}

func TestDefaultRouteTTL(t *testing.T) {
	rt := NewRouter(0) // should use default
	a := makeTestAnnounce(t, "meshsat.hub")
	rt.ProcessAnnounce(a, IfaceMQTT)

	entry := rt.Lookup(a.DestHash)
	if entry == nil {
		t.Fatal("route not found")
	}
	expectedExpiry := entry.LastSeen.Add(DefaultRouteTTL)
	diff := entry.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("expiry should be ~30min from now, got %v", entry.ExpiresAt)
	}
}
