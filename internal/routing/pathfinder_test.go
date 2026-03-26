package routing

import (
	"context"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

// testSendLog records packets sent by the PathFinder.
type testSendLog struct {
	mu      sync.Mutex
	packets []struct {
		ifaceID string
		data    []byte
	}
}

func (l *testSendLog) send(ifaceID string, packet []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := make([]byte, len(packet))
	copy(cp, packet)
	l.packets = append(l.packets, struct {
		ifaceID string
		data    []byte
	}{ifaceID, cp})
	return nil
}

func (l *testSendLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.packets)
}

func (l *testSendLog) last() (string, []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.packets) == 0 {
		return "", nil
	}
	p := l.packets[len(l.packets)-1]
	return p.ifaceID, p.data
}

func newTestPathFinder(t *testing.T) (*PathFinder, *testSendLog) {
	t.Helper()
	router := reticulum.NewRouter(30 * time.Minute)
	registry := NewInterfaceRegistry()

	// Register two test interfaces (both free/floodable for path discovery tests)
	registry.Register(NewReticulumInterface("mesh_0", "mesh", 230, nil))
	registry.Register(NewReticulumInterface("tcp_0", "tcp", 65535, nil))

	identity := generateTestIdentity(t)
	sendLog := &testSendLog{}

	config := DefaultPathFinderConfig()
	config.RequestTimeout = 2 * time.Second

	pf := NewPathFinder(config, router, registry, identity, sendLog.send)
	return pf, sendLog
}

func generateTestIdentity(t *testing.T) *Identity {
	t.Helper()
	return testIdentity(t) // uses in-memory identity from announce_test.go
}

func TestPathFinder_HandlePathRequest_Unknown(t *testing.T) {
	pf, sendLog := newTestPathFinder(t)

	// Send a path request for an unknown destination
	var destHash [reticulum.TruncatedHashLen]byte
	destHash[0] = 0xDE
	destHash[1] = 0xAD

	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0x01

	req := &reticulum.PathRequest{DestHash: destHash, Tag: tag}
	data := reticulum.MarshalPathRequest(req)

	pf.HandlePathRequest(data, "mesh_0")

	// Should flood to all floodable interfaces except the source
	// Registry has mesh_0 and tcp_0, source is mesh_0, so should send to tcp_0
	if sendLog.count() != 1 {
		t.Fatalf("expected 1 flood packet, got %d", sendLog.count())
	}
	ifaceID, _ := sendLog.last()
	if ifaceID != "tcp_0" {
		t.Fatalf("flood target = %q, want %q", ifaceID, "tcp_0")
	}
}

func TestPathFinder_HandlePathRequest_Dedup(t *testing.T) {
	pf, sendLog := newTestPathFinder(t)

	var destHash [reticulum.TruncatedHashLen]byte
	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0x42

	req := &reticulum.PathRequest{DestHash: destHash, Tag: tag}
	data := reticulum.MarshalPathRequest(req)

	// First request should be flooded
	pf.HandlePathRequest(data, "mesh_0")
	if sendLog.count() != 1 {
		t.Fatalf("first request: expected 1 packet, got %d", sendLog.count())
	}

	// Duplicate should be ignored
	pf.HandlePathRequest(data, "tcp_0")
	if sendLog.count() != 1 {
		t.Fatalf("duplicate request: expected still 1 packet, got %d", sendLog.count())
	}
}

func TestPathFinder_PaidInterfacesNotFlooded(t *testing.T) {
	router := reticulum.NewRouter(30 * time.Minute)
	registry := NewInterfaceRegistry()

	// mesh_0 is free (floodable), iridium_0 is paid (not floodable)
	registry.Register(NewReticulumInterface("mesh_0", "mesh", 230, nil))
	registry.Register(NewReticulumInterface("iridium_0", "iridium", 340, nil))

	identity := generateTestIdentity(t)
	sendLog := &testSendLog{}
	config := DefaultPathFinderConfig()
	pf := NewPathFinder(config, router, registry, identity, sendLog.send)

	var destHash [reticulum.TruncatedHashLen]byte
	destHash[0] = 0xBE
	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0x77

	req := &reticulum.PathRequest{DestHash: destHash, Tag: tag}
	data := reticulum.MarshalPathRequest(req)

	// Source is iridium_0, only floodable target is mesh_0
	pf.HandlePathRequest(data, "iridium_0")
	if sendLog.count() != 1 {
		t.Fatalf("expected 1 flood packet (mesh_0 only), got %d", sendLog.count())
	}
	ifaceID, _ := sendLog.last()
	if ifaceID != "mesh_0" {
		t.Fatalf("flood target = %q, want %q (iridium_0 should be excluded)", ifaceID, "mesh_0")
	}
}

func TestPathFinder_FloodableOverride(t *testing.T) {
	router := reticulum.NewRouter(30 * time.Minute)
	registry := NewInterfaceRegistry()

	meshIface := NewReticulumInterface("mesh_0", "mesh", 230, nil)
	iridiumIface := NewReticulumInterface("iridium_0", "iridium", 340, nil)
	registry.Register(meshIface)
	registry.Register(iridiumIface)

	// Verify default: iridium is not floodable
	if iridiumIface.IsFloodable() {
		t.Fatal("iridium should not be floodable by default")
	}

	// Override: user opted in
	iridiumIface.SetFloodable(true)

	identity := generateTestIdentity(t)
	sendLog := &testSendLog{}
	config := DefaultPathFinderConfig()
	pf := NewPathFinder(config, router, registry, identity, sendLog.send)

	var destHash [reticulum.TruncatedHashLen]byte
	destHash[0] = 0xCC
	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0x88

	req := &reticulum.PathRequest{DestHash: destHash, Tag: tag}
	data := reticulum.MarshalPathRequest(req)

	// Source is mesh_0, both mesh_0 and iridium_0 are now floodable
	// So only iridium_0 should receive (mesh_0 excluded as source)
	pf.HandlePathRequest(data, "mesh_0")
	if sendLog.count() != 1 {
		t.Fatalf("expected 1 flood packet after override, got %d", sendLog.count())
	}
	ifaceID, _ := sendLog.last()
	if ifaceID != "iridium_0" {
		t.Fatalf("flood target = %q, want %q after floodable override", ifaceID, "iridium_0")
	}
}

func TestPathFinder_HandlePathRequest_LocalDest(t *testing.T) {
	pf, sendLog := newTestPathFinder(t)

	// Request for our own dest hash — should send response, not flood
	myHash := pf.localID.DestHash()

	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0x99

	req := &reticulum.PathRequest{DestHash: myHash, Tag: tag}
	data := reticulum.MarshalPathRequest(req)

	pf.HandlePathRequest(data, "mesh_0")

	// Should have sent a response back to the source interface
	if sendLog.count() != 1 {
		t.Fatalf("expected 1 response packet, got %d", sendLog.count())
	}
	ifaceID, pktData := sendLog.last()
	if ifaceID != "mesh_0" {
		t.Fatalf("response target = %q, want %q (source interface)", ifaceID, "mesh_0")
	}

	// Verify it's a path response packet
	hdr, err := reticulum.UnmarshalHeader(pktData)
	if err != nil {
		t.Fatalf("unmarshal response header: %v", err)
	}
	if hdr.Context != reticulum.ContextPathResponse {
		t.Fatalf("response context = 0x%02x, want ContextPathResponse", hdr.Context)
	}

	resp, err := reticulum.UnmarshalPathResponse(hdr.Data)
	if err != nil {
		t.Fatalf("unmarshal path response: %v", err)
	}
	if resp.Hops != 0 {
		t.Fatalf("response hops = %d, want 0 (local)", resp.Hops)
	}
	if resp.InterfaceType != "local" {
		t.Fatalf("response iface = %q, want %q", resp.InterfaceType, "local")
	}
}

func TestPathFinder_HandlePathRequest_KnownRoute(t *testing.T) {
	pf, sendLog := newTestPathFinder(t)

	// Add a route to the router
	var destHash [reticulum.TruncatedHashLen]byte
	destHash[0] = 0xBE
	destHash[1] = 0xEF

	announce := &reticulum.Announce{
		DestHash: destHash,
		Hops:     2,
	}
	pf.router.ProcessAnnounce(announce, "iridium")

	// Now send a path request for that destination
	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0x77

	req := &reticulum.PathRequest{DestHash: destHash, Tag: tag}
	data := reticulum.MarshalPathRequest(req)

	pf.HandlePathRequest(data, "mesh_0")

	// Should send a response (not flood)
	if sendLog.count() != 1 {
		t.Fatalf("expected 1 response, got %d", sendLog.count())
	}
	ifaceID, pktData := sendLog.last()
	if ifaceID != "mesh_0" {
		t.Fatalf("response target = %q, want %q", ifaceID, "mesh_0")
	}

	hdr, err := reticulum.UnmarshalHeader(pktData)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	resp, err := reticulum.UnmarshalPathResponse(hdr.Data)
	if err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Hops != 2 {
		t.Fatalf("response hops = %d, want 2", resp.Hops)
	}
	if resp.InterfaceType != "iridium" {
		t.Fatalf("response iface = %q, want %q", resp.InterfaceType, "iridium")
	}
}

func TestPathFinder_HandlePathResponse_Pending(t *testing.T) {
	pf, _ := newTestPathFinder(t)

	var destHash [reticulum.TruncatedHashLen]byte
	destHash[0] = 0xCA
	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0xFE

	// Register a pending request
	pr := &pendingRequest{
		destHash: destHash,
		tag:      tag,
		created:  time.Now(),
		resultCh: make(chan *reticulum.PathResponse, 1),
	}
	pf.mu.Lock()
	pf.pending[tag] = pr
	pf.mu.Unlock()

	// Send a matching response
	resp := &reticulum.PathResponse{
		DestHash:      destHash,
		Tag:           tag,
		Hops:          1,
		InterfaceType: "mesh",
	}
	data := reticulum.MarshalPathResponse(resp)

	pf.HandlePathResponse(data, "iridium_0")

	// Should receive the response on the channel
	select {
	case got := <-pr.resultCh:
		if got.Hops != 1 {
			t.Fatalf("response hops = %d, want 1", got.Hops)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

func TestPathFinder_Prune(t *testing.T) {
	pf, _ := newTestPathFinder(t)

	// Add an old seen entry
	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0x01
	pf.mu.Lock()
	pf.seen[tag] = time.Now().Add(-10 * time.Minute) // older than DedupTTL
	pf.mu.Unlock()

	pf.prune()

	if pf.SeenCount() != 0 {
		t.Fatalf("expected 0 seen entries after prune, got %d", pf.SeenCount())
	}
}

func TestPathFinder_RequestTimeout(t *testing.T) {
	router := reticulum.NewRouter(30 * time.Minute)
	registry := NewInterfaceRegistry()
	registry.Register(NewReticulumInterface("mesh_0", "mesh", 230, nil))

	identity := generateTestIdentity(t)

	config := DefaultPathFinderConfig()
	config.RequestTimeout = 200 * time.Millisecond // very short

	pf := NewPathFinder(config, router, registry, identity, func(ifaceID string, packet []byte) error {
		return nil // send into the void
	})

	var destHash [reticulum.TruncatedHashLen]byte
	destHash[0] = 0xFF

	ctx := context.Background()
	resp := pf.RequestPath(ctx, destHash)
	if resp != nil {
		t.Fatal("expected nil response (timeout)")
	}
}

func TestPathFinder_ConcurrentRequests(t *testing.T) {
	pf, _ := newTestPathFinder(t)

	const numRequests = 10
	var wg sync.WaitGroup
	wg.Add(numRequests)

	for i := 0; i < numRequests; i++ {
		go func(idx int) {
			defer wg.Done()
			var destHash [reticulum.TruncatedHashLen]byte
			destHash[0] = byte(idx)

			var tag [reticulum.TruncatedHashLen]byte
			tag[0] = byte(idx + 100)

			req := &reticulum.PathRequest{DestHash: destHash, Tag: tag}
			data := reticulum.MarshalPathRequest(req)
			pf.HandlePathRequest(data, "mesh_0")
		}(i)
	}
	wg.Wait()

	// All should have been processed (dedup with unique tags)
	if pf.SeenCount() != numRequests {
		t.Errorf("seen count = %d, want %d", pf.SeenCount(), numRequests)
	}
}

func TestPathFinder_EmptyRegistry(t *testing.T) {
	router := reticulum.NewRouter(30 * time.Minute)
	identity := generateTestIdentity(t)
	sendLog := &testSendLog{}

	config := DefaultPathFinderConfig()
	// nil registry
	pf := NewPathFinder(config, router, nil, identity, sendLog.send)

	var destHash [reticulum.TruncatedHashLen]byte
	destHash[0] = 0xAB
	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0xCD

	req := &reticulum.PathRequest{DestHash: destHash, Tag: tag}
	data := reticulum.MarshalPathRequest(req)

	// Should not panic with nil registry — falls back to sendFn("")
	pf.HandlePathRequest(data, "mesh_0")

	// With nil registry, flood goes to sendFn with empty ifaceID
	if sendLog.count() != 1 {
		t.Fatalf("expected 1 packet via fallback, got %d", sendLog.count())
	}
	ifaceID, _ := sendLog.last()
	if ifaceID != "" {
		t.Errorf("expected empty ifaceID, got %q", ifaceID)
	}
}

func TestPathFinder_ContextCancellation(t *testing.T) {
	router := reticulum.NewRouter(30 * time.Minute)
	registry := NewInterfaceRegistry()
	registry.Register(NewReticulumInterface("mesh_0", "mesh", 230, nil))
	identity := generateTestIdentity(t)

	config := DefaultPathFinderConfig()
	config.RequestTimeout = 30 * time.Second // long timeout

	pf := NewPathFinder(config, router, registry, identity, func(string, []byte) error {
		return nil
	})

	var destHash [reticulum.TruncatedHashLen]byte
	destHash[0] = 0xEE

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		resp := pf.RequestPath(ctx, destHash)
		if resp != nil {
			t.Error("expected nil response after cancel")
		}
		close(done)
	}()

	// Cancel quickly
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good — returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPath should return promptly after context cancel")
	}
}

func TestPathFinder_Metrics(t *testing.T) {
	pf, _ := newTestPathFinder(t)

	if pf.SeenCount() != 0 {
		t.Fatalf("initial SeenCount = %d, want 0", pf.SeenCount())
	}
	if pf.PendingCount() != 0 {
		t.Fatalf("initial PendingCount = %d, want 0", pf.PendingCount())
	}

	// Handle a request to populate seen
	var tag [reticulum.TruncatedHashLen]byte
	tag[0] = 0xAA
	req := &reticulum.PathRequest{Tag: tag}
	pf.HandlePathRequest(reticulum.MarshalPathRequest(req), "mesh_0")

	if pf.SeenCount() != 1 {
		t.Fatalf("SeenCount after request = %d, want 1", pf.SeenCount())
	}
}
