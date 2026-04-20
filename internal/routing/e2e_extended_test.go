package routing

import (
	"bytes"
	"context"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

// ---------------------------------------------------------------------------
// Resource Transfer: out-of-order segment delivery
// ---------------------------------------------------------------------------

func TestE2E_ResourceTransfer_OutOfOrder(t *testing.T) {
	var mu sync.Mutex
	var sentSegments [][]byte

	// Sender captures all outgoing packets
	senderRT := NewResourceTransfer(
		ResourceTransferConfig{SegmentSize: 30, TransferTimeout: 30 * time.Second},
		func(ifaceID string, packet []byte) error {
			mu.Lock()
			cp := make([]byte, len(packet))
			copy(cp, packet)
			sentSegments = append(sentSegments, cp)
			mu.Unlock()
			return nil
		},
	)

	receiverRT := NewResourceTransfer(
		ResourceTransferConfig{SegmentSize: 30, TransferTimeout: 30 * time.Second},
		func(ifaceID string, packet []byte) error {
			hdr, _ := reticulum.UnmarshalHeader(packet)
			if hdr != nil && hdr.Context == reticulum.ContextResourceReq {
				senderRT.HandleRequest(hdr.Data, "test")
			}
			return nil
		},
	)

	// Offer data: 100 bytes = 4 segments (30+30+30+10)
	testData := make([]byte, 100)
	rand.Read(testData)

	senderRT.Offer(context.Background(), testData, "test")
	time.Sleep(100 * time.Millisecond)

	// Collect advertisement and segment packets
	mu.Lock()
	allPackets := make([][]byte, len(sentSegments))
	copy(allPackets, sentSegments)
	mu.Unlock()

	var advHdrData []byte
	var segHdrDatas [][]byte
	for _, pkt := range allPackets {
		hdr, _ := reticulum.UnmarshalHeader(pkt)
		if hdr == nil {
			continue
		}
		switch hdr.Context {
		case reticulum.ContextResourceAdv:
			advHdrData = hdr.Data
		case reticulum.ContextResource:
			cp := make([]byte, len(hdr.Data))
			copy(cp, hdr.Data)
			segHdrDatas = append(segHdrDatas, cp)
		}
	}

	if advHdrData == nil {
		t.Fatal("no advertisement found")
	}

	// Process advertisement
	resultCh := receiverRT.HandleAdvertisement(advHdrData, "test")
	if resultCh == nil {
		t.Fatal("should get result channel")
	}

	// Wait for request to trigger segment sending
	time.Sleep(100 * time.Millisecond)

	// Gather ALL segment packets (initial + request-triggered)
	mu.Lock()
	allPackets = make([][]byte, len(sentSegments))
	copy(allPackets, sentSegments)
	mu.Unlock()

	segHdrDatas = nil
	for _, pkt := range allPackets {
		hdr, _ := reticulum.UnmarshalHeader(pkt)
		if hdr != nil && hdr.Context == reticulum.ContextResource {
			cp := make([]byte, len(hdr.Data))
			copy(cp, hdr.Data)
			segHdrDatas = append(segHdrDatas, cp)
		}
	}

	if len(segHdrDatas) == 0 {
		t.Fatal("no segments found")
	}

	// Feed segments in REVERSE order
	for i := len(segHdrDatas) - 1; i >= 0; i-- {
		receiverRT.HandleSegment(segHdrDatas[i], "test")
	}

	select {
	case result := <-resultCh:
		if !bytes.Equal(result, testData) {
			t.Errorf("data mismatch: got %d bytes, want %d", len(result), len(testData))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: out-of-order segments should still complete")
	}
}

// ---------------------------------------------------------------------------
// Resource Transfer: duplicate segments
// ---------------------------------------------------------------------------

func TestE2E_ResourceTransfer_DuplicateSegments(t *testing.T) {
	var mu sync.Mutex
	var sentPackets [][]byte

	senderRT := NewResourceTransfer(
		ResourceTransferConfig{SegmentSize: 50, TransferTimeout: 30 * time.Second},
		func(ifaceID string, packet []byte) error {
			mu.Lock()
			cp := make([]byte, len(packet))
			copy(cp, packet)
			sentPackets = append(sentPackets, cp)
			mu.Unlock()
			return nil
		},
	)

	receiverRT := NewResourceTransfer(
		ResourceTransferConfig{SegmentSize: 50, TransferTimeout: 30 * time.Second},
		func(ifaceID string, packet []byte) error {
			hdr, _ := reticulum.UnmarshalHeader(packet)
			if hdr != nil && hdr.Context == reticulum.ContextResourceReq {
				senderRT.HandleRequest(hdr.Data, "test")
			}
			return nil
		},
	)

	testData := make([]byte, 150)
	rand.Read(testData)

	senderRT.Offer(context.Background(), testData, "test")
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	all := make([][]byte, len(sentPackets))
	copy(all, sentPackets)
	mu.Unlock()

	var advData []byte
	var segDatas [][]byte
	for _, pkt := range all {
		hdr, _ := reticulum.UnmarshalHeader(pkt)
		if hdr == nil {
			continue
		}
		switch hdr.Context {
		case reticulum.ContextResourceAdv:
			advData = hdr.Data
		case reticulum.ContextResource:
			cp := make([]byte, len(hdr.Data))
			copy(cp, hdr.Data)
			segDatas = append(segDatas, cp)
		}
	}

	resultCh := receiverRT.HandleAdvertisement(advData, "test")

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	all = make([][]byte, len(sentPackets))
	copy(all, sentPackets)
	mu.Unlock()
	segDatas = nil
	for _, pkt := range all {
		hdr, _ := reticulum.UnmarshalHeader(pkt)
		if hdr != nil && hdr.Context == reticulum.ContextResource {
			cp := make([]byte, len(hdr.Data))
			copy(cp, hdr.Data)
			segDatas = append(segDatas, cp)
		}
	}

	// Feed each segment TWICE (simulating duplicates)
	for _, seg := range segDatas {
		receiverRT.HandleSegment(seg, "test")
		receiverRT.HandleSegment(seg, "test") // duplicate
	}

	select {
	case result := <-resultCh:
		if !bytes.Equal(result, testData) {
			t.Error("duplicate segments should not corrupt data")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: duplicate segments should not prevent completion")
	}
}

// ---------------------------------------------------------------------------
// Bandwidth limiter under concurrent contention
// ---------------------------------------------------------------------------

func TestE2E_BandwidthLimiter_Concurrent(t *testing.T) {
	bwl := NewAnnounceBandwidthLimiter()
	bwl.SetBandwidth("mesh_0", 1200, 100) // 1200 bps

	var wg sync.WaitGroup
	var allowed, denied int64
	var mu sync.Mutex

	const goroutines = 20
	const iterations = 50

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if bwl.Allow("mesh_0", 10) {
					mu.Lock()
					allowed++
					mu.Unlock()
				} else {
					mu.Lock()
					denied++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	total := allowed + denied
	mu.Unlock()

	if total != goroutines*iterations {
		t.Errorf("total should be %d, got %d", goroutines*iterations, total)
	}
	t.Logf("allowed=%d, denied=%d (of %d total)", allowed, denied, total)
}

// ---------------------------------------------------------------------------
// Announce relay dedup across hops
// ---------------------------------------------------------------------------

func TestE2E_AnnounceRelayDedup(t *testing.T) {
	idA := testIdentity(t)

	var mu sync.Mutex
	var relayCount int

	destTable := NewDestinationTable(nil)
	relay := NewAnnounceRelay(
		DefaultRelayConfig(),
		destTable,
		func(data []byte, ann *Announce, sourceIface string) {
			mu.Lock()
			relayCount++
			mu.Unlock()
		},
	)

	ann, _ := NewAnnounce(idA, []byte("test"))
	annData := ann.Marshal()

	// Feed same announce 5 times
	for i := 0; i < 5; i++ {
		relay.HandleAnnounce(context.Background(), annData, "mesh_0")
	}
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := relayCount
	mu.Unlock()

	if count > 1 {
		t.Errorf("should relay at most once, relayed %d times", count)
	}
	if relay.SeenCount() != 1 {
		t.Errorf("seen count: got %d, want 1", relay.SeenCount())
	}
}

// ---------------------------------------------------------------------------
// Announce relay: max hops enforcement
// ---------------------------------------------------------------------------

func TestE2E_AnnounceRelay_MaxHops(t *testing.T) {
	idA := testIdentity(t)

	var mu sync.Mutex
	var relayCount int

	destTable := NewDestinationTable(nil)
	relay := NewAnnounceRelay(
		DefaultRelayConfig(),
		destTable,
		func(data []byte, ann *Announce, sourceIface string) {
			mu.Lock()
			relayCount++
			mu.Unlock()
		},
	)

	ann, _ := NewAnnounce(idA, nil)
	// Set hops to max
	for i := 0; i < MaxAnnounceHops; i++ {
		ann.IncrementHop()
	}
	annData := ann.Marshal()

	relay.HandleAnnounce(context.Background(), annData, "mesh_0")
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := relayCount
	mu.Unlock()

	if count != 0 {
		t.Errorf("should NOT relay at max hops, relayed %d times", count)
	}
}

// ---------------------------------------------------------------------------
// PathFinder: request + response via in-memory channels
// ---------------------------------------------------------------------------

func TestE2E_PathFinder_ChannelFlow(t *testing.T) {
	idA := testIdentity(t)
	idB := testIdentity(t)
	idC := testIdentity(t)

	routerA := reticulum.NewRouter(30 * time.Minute)
	routerB := reticulum.NewRouter(30 * time.Minute)

	// B knows route to C
	annC, _ := NewAnnounce(idC, nil)
	routerB.ProcessAnnounce(annC.ret, "mesh")

	regA := NewInterfaceRegistry()
	regA.Register(NewReticulumInterface("link_b", "tcp", 65535, nil))
	regB := NewInterfaceRegistry()
	regB.Register(NewReticulumInterface("link_a", "tcp", 65535, nil))

	// A↔B channels
	aToB := make(chan []byte, 10)
	bToA := make(chan []byte, 10)

	pfB := NewPathFinder(
		DefaultPathFinderConfig(),
		routerB, regB, idB,
		func(ifaceID string, packet []byte) error {
			cp := make([]byte, len(packet))
			copy(cp, packet)
			bToA <- cp
			return nil
		},
	)

	pfA := NewPathFinder(
		PathFinderConfig{
			RequestTimeout:  3 * time.Second,
			DedupTTL:        5 * time.Minute,
			MaxDedupEntries: 100,
			MaxHops:         128,
		},
		routerA, regA, idA,
		func(ifaceID string, packet []byte) error {
			cp := make([]byte, len(packet))
			copy(cp, packet)
			aToB <- cp
			return nil
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Message pump
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case pkt := <-aToB:
				hdr, _ := reticulum.UnmarshalHeader(pkt)
				if hdr != nil && hdr.Context == reticulum.ContextRequest {
					pfB.HandlePathRequest(hdr.Data, "link_a")
				}
			case pkt := <-bToA:
				hdr, _ := reticulum.UnmarshalHeader(pkt)
				if hdr != nil && hdr.Context == reticulum.ContextPathResponse {
					pfA.HandlePathResponse(hdr.Data, "link_b")
				}
			}
		}
	}()

	// A requests path to C
	resp := pfA.RequestPath(ctx, idC.DestHash())
	if resp == nil {
		t.Fatal("should discover path to C via B")
	}
	if resp.DestHash != idC.DestHash() {
		t.Error("response dest hash mismatch")
	}
	if resp.InterfaceType != "mesh" {
		t.Errorf("interface type: got %q, want mesh", resp.InterfaceType)
	}
}

// ---------------------------------------------------------------------------
// PathFinder: request timeout
// ---------------------------------------------------------------------------

func TestE2E_PathFinder_Timeout(t *testing.T) {
	idA := testIdentity(t)
	unknown := testIdentity(t)

	routerA := reticulum.NewRouter(30 * time.Minute)
	regA := NewInterfaceRegistry()
	regA.Register(NewReticulumInterface("link_b", "tcp", 65535, nil))

	pfA := NewPathFinder(
		PathFinderConfig{
			RequestTimeout:  200 * time.Millisecond, // very short timeout
			DedupTTL:        5 * time.Minute,
			MaxDedupEntries: 100,
			MaxHops:         128,
		},
		routerA, regA, idA,
		func(ifaceID string, packet []byte) error {
			return nil // send into the void — no one responds
		},
	)

	ctx := context.Background()
	start := time.Now()
	resp := pfA.RequestPath(ctx, unknown.DestHash())
	elapsed := time.Since(start)

	if resp != nil {
		t.Error("should timeout (nil response) for unknown destination")
	}
	if elapsed > 1*time.Second {
		t.Errorf("timeout took too long: %v (expected ~200ms)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Multi-hop forwarding: 3 transport nodes
// ---------------------------------------------------------------------------

func TestE2E_ThreeHopForwarding(t *testing.T) {
	idA := testIdentity(t)
	idB := testIdentity(t)
	idC := testIdentity(t)
	dest := testIdentity(t)

	type forwardResult struct {
		iface  string
		packet []byte
	}

	makeForwarder := func(id *Identity, routeTarget *Identity, routeIface string) (*TransportNode, *[]forwardResult, *sync.Mutex) {
		var results []forwardResult
		var mu sync.Mutex
		sendFn := func(ifaceID string, packet []byte) error {
			mu.Lock()
			cp := make([]byte, len(packet))
			copy(cp, packet)
			results = append(results, forwardResult{ifaceID, cp})
			mu.Unlock()
			return nil
		}
		tn := NewTransportNode(id, 30*time.Minute, sendFn)
		tn.Enable()
		ann, _ := NewAnnounce(routeTarget, nil)
		tn.ProcessAnnounce(ann, routeIface)
		return tn, &results, &mu
	}

	tnA, resultsA, muA := makeForwarder(idA, dest, "link_b")
	tnB, resultsB, muB := makeForwarder(idB, dest, "link_c")
	tnC, resultsC, muC := makeForwarder(idC, dest, "iridium_0")

	// Original packet from external source
	origHdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestType:   reticulum.DestSingle,
		Hops:       0,
		DestHash:   dest.DestHash(),
		Data:       []byte("three-hop-test"),
	}

	// Hop 1: A forwards
	if !tnA.ForwardPacket(origHdr.Marshal(), "external") {
		t.Fatal("A should forward")
	}
	muA.Lock()
	aPacket := (*resultsA)[0].packet
	muA.Unlock()

	// Hop 2: B forwards what A sent
	if !tnB.ForwardPacket(aPacket, "link_a") {
		t.Fatal("B should forward")
	}
	muB.Lock()
	bPacket := (*resultsB)[0].packet
	muB.Unlock()

	// Hop 3: C forwards what B sent
	if !tnC.ForwardPacket(bPacket, "link_b") {
		t.Fatal("C should forward")
	}
	muC.Lock()
	cPacket := (*resultsC)[0].packet
	muC.Unlock()

	// Verify final packet
	finalHdr, err := reticulum.UnmarshalHeader(cPacket)
	if err != nil {
		t.Fatal(err)
	}
	if finalHdr.Hops != 3 {
		t.Errorf("final hops: got %d, want 3", finalHdr.Hops)
	}
	if finalHdr.DestHash != dest.DestHash() {
		t.Error("dest hash should be preserved")
	}
	if !bytes.Equal(finalHdr.Data, []byte("three-hop-test")) {
		t.Error("payload should be preserved through 3 hops")
	}
	// Transport ID should be the last forwarder (C)
	if finalHdr.TransportID != idC.DestHash() {
		t.Error("transport ID should be C's hash")
	}
}

// ---------------------------------------------------------------------------
// Link: encrypt without established state
// ---------------------------------------------------------------------------

func TestE2E_Link_EncryptDecryptState(t *testing.T) {
	link := &Link{State: LinkStatePending}
	_, err := link.Encrypt([]byte("test"))
	if err == nil {
		t.Error("encrypt on pending link should fail")
	}

	_, err = link.Decrypt([]byte("test"))
	if err == nil {
		t.Error("decrypt on pending link should fail")
	}

	link.State = LinkStateClosed
	_, err = link.Encrypt([]byte("test"))
	if err == nil {
		t.Error("encrypt on closed link should fail")
	}
}

// ---------------------------------------------------------------------------
// Link: close cleans up both maps
// ---------------------------------------------------------------------------

func TestE2E_LinkManager_CloseCleanup(t *testing.T) {
	id := testIdentity(t)
	remote := testIdentity(t)
	lm := NewLinkManager(id)

	// Initiate a link (goes to pending)
	_, pendingLink, err := lm.InitiateLink(remote.DestHash())
	if err != nil {
		t.Fatal(err)
	}

	if lm.GetPendingLink(pendingLink.ID) == nil {
		t.Fatal("should have pending link")
	}

	// Close it
	lm.CloseLink(pendingLink.ID)

	if lm.GetPendingLink(pendingLink.ID) != nil {
		t.Error("pending link should be gone after close")
	}
	if lm.GetLink(pendingLink.ID) != nil {
		t.Error("established link should be gone after close")
	}
	if len(lm.ActiveLinks()) != 0 {
		t.Error("active links should be empty")
	}
}
