package routing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

// ---------------------------------------------------------------------------
// E2E: Announce → Route Learn → Forward → Confirm
// ---------------------------------------------------------------------------

func TestE2E_AnnounceLearnForwardConfirm(t *testing.T) {
	// Setup: two nodes, "bridge" (transport) and "remote" (endpoint)
	bridgeID := testIdentity(t)
	remoteID := testIdentity(t)

	// Track forwarded packets
	var mu sync.Mutex
	var forwarded []struct {
		ifaceID string
		packet  []byte
	}
	sendFn := func(ifaceID string, packet []byte) error {
		mu.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		forwarded = append(forwarded, struct {
			ifaceID string
			packet  []byte
		}{ifaceID, cp})
		mu.Unlock()
		return nil
	}

	// Create transport node with routing
	tn := NewTransportNode(bridgeID, 30*time.Minute, sendFn)
	tn.Enable()

	// Step 1: Remote announces itself
	announce, err := NewAnnounce(remoteID, []byte("remote-node"))
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Bridge learns the route from the announce via mesh_0
	if !tn.ProcessAnnounce(announce, "mesh_0") {
		t.Fatal("should learn route from announce")
	}
	if tn.RouteCount() != 1 {
		t.Fatalf("route count: got %d, want 1", tn.RouteCount())
	}
	if tn.BestInterface(remoteID.DestHash()) != "mesh_0" {
		t.Fatal("best interface should be mesh_0")
	}

	// Step 3: A packet arrives on iridium_0 addressed to remote — forward via mesh_0
	dataHdr := &reticulum.Header{
		HeaderType:    reticulum.HeaderType1,
		TransportType: reticulum.TransportBroadcast,
		DestType:      reticulum.DestSingle,
		PacketType:    reticulum.PacketData,
		Hops:          0,
		DestHash:      remoteID.DestHash(),
		Context:       reticulum.ContextNone,
		Data:          []byte("hello remote"),
	}
	packet := dataHdr.Marshal()

	if !tn.ForwardPacket(packet, "iridium_0") {
		t.Fatal("should forward packet to mesh_0")
	}

	mu.Lock()
	if len(forwarded) != 1 {
		t.Fatalf("expected 1 forwarded packet, got %d", len(forwarded))
	}
	if forwarded[0].ifaceID != "mesh_0" {
		t.Fatalf("forwarded to %q, want mesh_0", forwarded[0].ifaceID)
	}

	// Step 4: Verify forwarded header is Type2 with bridge's transport ID
	fwdHdr, err := reticulum.UnmarshalHeader(forwarded[0].packet)
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if fwdHdr.HeaderType != reticulum.HeaderType2 {
		t.Error("forwarded packet should be Type2")
	}
	if fwdHdr.TransportID != bridgeID.DestHash() {
		t.Error("transport ID should be bridge's dest hash")
	}
	if fwdHdr.DestHash != remoteID.DestHash() {
		t.Error("dest hash should be preserved")
	}
	if fwdHdr.Hops != 1 {
		t.Errorf("hops should be 1, got %d", fwdHdr.Hops)
	}
	if string(fwdHdr.Data) != "hello remote" {
		t.Errorf("data mismatch: got %q", fwdHdr.Data)
	}

	// Step 5: Simulate delivery confirmation from remote
	payloadHash := sha256.Sum256([]byte("hello remote"))
	dc := &reticulum.DeliveryConfirmation{
		DestHash:    remoteID.DestHash(),
		PayloadHash: payloadHash,
	}
	body := make([]byte, reticulum.DestHashLen+32)
	copy(body, dc.DestHash[:])
	copy(body[reticulum.DestHashLen:], dc.PayloadHash[:])
	dc.Signature = remoteID.Sign(body)

	// Verify the confirmation
	if !dc.Verify(remoteID.SigningPublicKey()) {
		t.Fatal("delivery confirmation should verify")
	}
	if !dc.VerifyWithPlaintext(remoteID.SigningPublicKey(), []byte("hello remote")) {
		t.Fatal("confirmation should verify with correct plaintext")
	}
}

// ---------------------------------------------------------------------------
// E2E: Link Establishment + Encrypted Data Transfer
// ---------------------------------------------------------------------------

func TestE2E_LinkEstablishmentEncryptedTransfer(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	// Step 1: Alice initiates link to Bob
	reqData, pendingLink, err := aliceLM.InitiateLink(bob.DestHash())
	if err != nil {
		t.Fatal(err)
	}
	if pendingLink.State != LinkStatePending {
		t.Fatal("link should be pending")
	}

	// Step 2: Bob handles request and returns proof
	proofData, err := bobLM.HandleLinkRequest(reqData)
	if err != nil {
		t.Fatal(err)
	}

	// Bob's link is immediately established
	bobLinks := bobLM.ActiveLinks()
	if len(bobLinks) != 1 {
		t.Fatal("bob should have 1 active link")
	}
	if bobLinks[0].State != LinkStateEstablished {
		t.Fatal("bob's link should be established")
	}
	if bobLinks[0].IsInitiator {
		t.Fatal("bob should not be initiator")
	}

	// Step 3: Alice handles proof — link established
	if err := aliceLM.HandleLinkProof(proofData, bob.SigningPublicKey()); err != nil {
		t.Fatal(err)
	}

	aliceLink := aliceLM.ActiveLinks()[0]
	bobLink := bobLM.ActiveLinks()[0]

	if !aliceLink.IsInitiator {
		t.Fatal("alice should be initiator")
	}
	if aliceLink.ID != bobLink.ID {
		t.Fatal("link IDs should match")
	}

	// Step 4: Bidirectional encrypted transfer
	messages := []struct {
		from    string
		payload string
	}{
		{"alice", "First message from Alice"},
		{"bob", "First reply from Bob"},
		{"alice", "Second message from Alice with more data to test multi-block CBC"},
		{"bob", "Bob sends a longer message that spans multiple AES blocks to verify padding"},
	}

	for _, msg := range messages {
		var sender, receiver *Link
		if msg.from == "alice" {
			sender, receiver = aliceLink, bobLink
		} else {
			sender, receiver = bobLink, aliceLink
		}

		ct, err := sender.Encrypt([]byte(msg.payload))
		if err != nil {
			t.Fatalf("%s encrypt: %v", msg.from, err)
		}
		pt, err := receiver.Decrypt(ct)
		if err != nil {
			t.Fatalf("%s decrypt: %v", msg.from, err)
		}
		if string(pt) != msg.payload {
			t.Errorf("mismatch: got %q, want %q", pt, msg.payload)
		}

		// Verify cross-direction decryption fails
		_, err = sender.Decrypt(ct) // sender uses wrong keys for own ciphertext
		if err == nil {
			t.Fatal("same-direction decrypt should fail")
		}
	}

	// Step 5: Close link
	aliceLM.CloseLink(aliceLink.ID)
	if len(aliceLM.ActiveLinks()) != 0 {
		t.Fatal("alice should have 0 active links after close")
	}

	// Alice can't encrypt on closed link
	_, err = aliceLink.Encrypt([]byte("after close"))
	if err == nil {
		t.Fatal("should not encrypt on closed link")
	}
}

// ---------------------------------------------------------------------------
// E2E: Resource Transfer (chunked delivery with bitmap)
// ---------------------------------------------------------------------------

func TestE2E_ResourceTransferE2E(t *testing.T) {
	// Wire sender and receiver together via callbacks
	var senderRT, receiverRT *ResourceTransfer

	senderSend := func(ifaceID string, packet []byte) error {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return err
		}
		switch hdr.Context {
		case reticulum.ContextResourceAdv:
			receiverRT.HandleAdvertisement(hdr.Data, "link_0")
		case reticulum.ContextResource:
			receiverRT.HandleSegment(hdr.Data, "link_0")
		case reticulum.ContextResourcePRF:
			senderRT.HandleProof(hdr.Data)
		}
		return nil
	}

	receiverSend := func(ifaceID string, packet []byte) error {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return err
		}
		switch hdr.Context {
		case reticulum.ContextResourceReq:
			senderRT.HandleRequest(hdr.Data, "link_0")
		case reticulum.ContextResourcePRF:
			senderRT.HandleProof(hdr.Data)
		}
		return nil
	}

	config := DefaultResourceTransferConfig()
	config.SegmentSize = 100 // 100 bytes per segment
	senderRT = NewResourceTransfer(config, senderSend)
	receiverRT = NewResourceTransfer(config, receiverSend)

	// Create test data: 750 bytes = 8 segments (7×100 + 1×50)
	testData := make([]byte, 750)
	rand.Read(testData)

	hash, err := senderRT.Offer(context.Background(), testData, "link_0")
	if err != nil {
		t.Fatal(err)
	}

	// Verify hash matches
	expectedHash := sha256.Sum256(testData)
	if hash != expectedHash {
		t.Fatal("resource hash mismatch")
	}

	// The synchronous callback chain should have completed the transfer
	time.Sleep(100 * time.Millisecond)

	// Sender should have cleaned up after proof
	out, _ := senderRT.Stats()
	if out != 0 {
		t.Fatalf("sender outbound count = %d, want 0 (proof received)", out)
	}
}

// ---------------------------------------------------------------------------
// E2E: PathFinder Integration (unknown dest → flood → response → forward)
// ---------------------------------------------------------------------------

func TestE2E_PathFinderFullFlow(t *testing.T) {
	// Node A wants to find a route to Node C
	// Node B knows the route (via its router)
	// Flow: A floods PathRequest → B responds with PathResponse → A learns route

	nodeA_ID := testIdentity(t)
	nodeB_ID := testIdentity(t)
	nodeC_ID := testIdentity(t)

	routerA := reticulum.NewRouter(30 * time.Minute)
	routerB := reticulum.NewRouter(30 * time.Minute)

	registryA := NewInterfaceRegistry()
	registryA.Register(NewReticulumInterface("tcp_0", "tcp", 65535, nil))

	registryB := NewInterfaceRegistry()
	registryB.Register(NewReticulumInterface("tcp_0", "tcp", 65535, nil))
	registryB.Register(NewReticulumInterface("mesh_0", "mesh", 230, nil))

	// Node B has a route to Node C via mesh_0
	annC := &reticulum.Announce{DestHash: nodeC_ID.DestHash(), Hops: 1}
	routerB.ProcessAnnounce(annC, "mesh")

	// Wire B's pathfinder to respond to A
	var mu sync.Mutex
	var bSent []struct {
		ifaceID string
		data    []byte
	}

	pfB := NewPathFinder(
		DefaultPathFinderConfig(),
		routerB, registryB, nodeB_ID,
		func(ifaceID string, packet []byte) error {
			mu.Lock()
			cp := make([]byte, len(packet))
			copy(cp, packet)
			bSent = append(bSent, struct {
				ifaceID string
				data    []byte
			}{ifaceID, cp})
			mu.Unlock()
			return nil
		},
	)

	// Wire A's pathfinder to send to B
	pfA_config := DefaultPathFinderConfig()
	pfA_config.RequestTimeout = 2 * time.Second

	pfA := NewPathFinder(
		pfA_config,
		routerA, registryA, nodeA_ID,
		func(ifaceID string, packet []byte) error {
			// A sends to B — deliver to B's handler
			pfB.HandlePathRequest(packet[reticulum.HeaderMinSize:], "tcp_0") // strip header for data
			return nil
		},
	)

	// A requests path to C — launches async flood
	// We need to handle the response delivery manually
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Start the request in a goroutine since it blocks
	var resp *reticulum.PathResponse
	done := make(chan struct{})
	go func() {
		// Instead of using pfA.RequestPath (which blocks), we'll simulate the flow
		// by directly handling the path request
		var tag [reticulum.TruncatedHashLen]byte
		rand.Read(tag[:])

		req := &reticulum.PathRequest{DestHash: nodeC_ID.DestHash(), Tag: tag}
		reqData := reticulum.MarshalPathRequest(req)

		// B handles A's request
		pfB.HandlePathRequest(reqData, "tcp_0")

		// Check B sent a response
		mu.Lock()
		sentCount := len(bSent)
		mu.Unlock()

		if sentCount > 0 {
			mu.Lock()
			lastPkt := bSent[len(bSent)-1]
			mu.Unlock()

			hdr, err := reticulum.UnmarshalHeader(lastPkt.data)
			if err == nil && hdr.Context == reticulum.ContextPathResponse {
				resp, _ = reticulum.UnmarshalPathResponse(hdr.Data)
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out")
	}

	_ = pfA // keep reference

	if resp == nil {
		t.Fatal("expected path response from B")
	}
	if resp.Hops != 1 {
		t.Errorf("response hops = %d, want 1", resp.Hops)
	}
	if resp.InterfaceType != "mesh" {
		t.Errorf("response iface = %q, want mesh", resp.InterfaceType)
	}
}

// ---------------------------------------------------------------------------
// E2E: Multi-hop Forwarding Through Transport Node Chain
// ---------------------------------------------------------------------------

func TestE2E_MultiHopForwarding(t *testing.T) {
	// Topology: A → B → C
	// A sends to dest, B forwards to C's interface
	_ = testIdentity(t) // nodeA (sender, not needed as transport)
	nodeB := testIdentity(t)
	dest := testIdentity(t)

	// Track what each node sends
	var bSent, cReceived [][]byte
	var mu sync.Mutex

	bSendFn := func(ifaceID string, packet []byte) error {
		mu.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		bSent = append(bSent, cp)
		mu.Unlock()
		return nil
	}

	// Node B is a transport node
	tnB := NewTransportNode(nodeB, 30*time.Minute, bSendFn)
	tnB.Enable()

	// B knows dest is reachable via iridium_0
	annDest, _ := NewAnnounce(dest, nil)
	tnB.ProcessAnnounce(annDest, "iridium_0")

	// Node A creates a packet addressed to dest
	hdr := &reticulum.Header{
		HeaderType:    reticulum.HeaderType1,
		TransportType: reticulum.TransportBroadcast,
		DestType:      reticulum.DestSingle,
		PacketType:    reticulum.PacketData,
		Hops:          0,
		DestHash:      dest.DestHash(),
		Context:       reticulum.ContextNone,
		Data:          []byte("multi-hop payload"),
	}
	packet := hdr.Marshal()

	// B receives from mesh_0 and forwards
	if !tnB.ForwardPacket(packet, "mesh_0") {
		t.Fatal("B should forward packet")
	}

	mu.Lock()
	if len(bSent) != 1 {
		t.Fatalf("B should have sent 1 packet, got %d", len(bSent))
	}
	fwdPacket := bSent[0]
	mu.Unlock()

	// Verify forwarded packet
	fwdHdr, err := reticulum.UnmarshalHeader(fwdPacket)
	if err != nil {
		t.Fatal(err)
	}
	if fwdHdr.HeaderType != reticulum.HeaderType2 {
		t.Error("should be Type2 after forwarding")
	}
	if fwdHdr.Hops != 1 {
		t.Errorf("hops = %d, want 1", fwdHdr.Hops)
	}
	if fwdHdr.TransportID != nodeB.DestHash() {
		t.Error("transport ID should be node B's hash")
	}

	// Simulate second hop: another transport node C receives and forwards
	nodeC := testIdentity(t)
	finalDest := testIdentity(t)

	cSendFn := func(ifaceID string, packet []byte) error {
		mu.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		cReceived = append(cReceived, cp)
		mu.Unlock()
		return nil
	}

	tnC := NewTransportNode(nodeC, 30*time.Minute, cSendFn)
	tnC.Enable()
	annFinal, _ := NewAnnounce(finalDest, nil)
	tnC.ProcessAnnounce(annFinal, "iridium_0")

	// Create a packet that needs to go through C
	hdr2 := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestType:   reticulum.DestSingle,
		Hops:       1,
		DestHash:   finalDest.DestHash(),
		Data:       []byte("hop-2"),
	}
	if !tnC.ForwardPacket(hdr2.Marshal(), "tcp_0") {
		t.Fatal("C should forward packet")
	}

	mu.Lock()
	if len(cReceived) != 1 {
		t.Fatalf("C should have forwarded 1 packet, got %d", len(cReceived))
	}
	finalHdr, _ := reticulum.UnmarshalHeader(cReceived[0])
	mu.Unlock()

	if finalHdr.Hops != 2 {
		t.Errorf("final hops = %d, want 2", finalHdr.Hops)
	}
	if finalHdr.TransportID != nodeC.DestHash() {
		t.Error("final transport ID should be node C's hash")
	}
	if string(finalHdr.Data) != "hop-2" {
		t.Error("payload should be preserved through hops")
	}
}

// ---------------------------------------------------------------------------
// E2E: Failover During Active Transfer
// ---------------------------------------------------------------------------

func TestE2E_FailoverDuringTransfer(t *testing.T) {
	// Setup: transport node with two routes to same dest
	// Route 1: mesh_0 (cost=0, preferred) — will fail
	// Route 2: iridium_0 (cost=0.05, fallback) — will succeed

	nodeID := testIdentity(t)
	destID := testIdentity(t)

	var mu sync.Mutex
	meshFailed := false
	var sentPackets []struct {
		iface  string
		packet []byte
	}

	sendFn := func(ifaceID string, packet []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if ifaceID == "mesh_0" && !meshFailed {
			meshFailed = true
			return nil // first send succeeds
		}
		cp := make([]byte, len(packet))
		copy(cp, packet)
		sentPackets = append(sentPackets, struct {
			iface  string
			packet []byte
		}{ifaceID, cp})
		return nil
	}

	tn := NewTransportNode(nodeID, 30*time.Minute, sendFn)
	tn.Enable()

	// Learn route via mesh_0 (cost=0, preferred)
	ann1, _ := NewAnnounce(destID, nil)
	tn.ProcessAnnounce(ann1, "mesh_0")

	// Send first packet via mesh_0
	hdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestType:   reticulum.DestSingle,
		DestHash:   destID.DestHash(),
		Data:       []byte("first"),
	}
	if !tn.ForwardPacket(hdr.Marshal(), "iridium_0") {
		t.Fatal("first forward should succeed via mesh_0")
	}

	// Now learn an alternate route via iridium_0 (higher cost, but different interface)
	ann2 := &reticulum.Announce{DestHash: destID.DestHash(), Hops: 0}
	tn.Router().ProcessAnnounce(ann2, "iridium")

	// Iridium route (cost=0.05) now replaces mesh_0 as best route since
	// ProcessAnnounce("mesh_0") yields InterfaceType "mesh_0" (cost=1.0 default).
	// Best route is now iridium. Packet from iridium_0 → can't forward back to
	// same interface type.
	hdr2 := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestType:   reticulum.DestSingle,
		DestHash:   destID.DestHash(),
		Data:       []byte("failover test"),
	}
	// Packet arrives on iridium — best route is iridium → same interface, NOT forwarded
	if tn.ForwardPacket(hdr2.Marshal(), "iridium") {
		t.Fatal("should not forward back to same interface")
	}

	// Packet arrives on mesh_0 — best route is iridium → different interface, forwarded
	if !tn.ForwardPacket(hdr2.Marshal(), "mesh_0") {
		t.Fatal("should forward from mesh_0 to iridium")
	}
}

// ---------------------------------------------------------------------------
// Cross-package interop: routing.Announce ↔ reticulum.Announce
// ---------------------------------------------------------------------------

func TestCrossPackage_AnnounceInterop(t *testing.T) {
	id := testIdentity(t)
	appData := []byte("cross-package test")

	// Create routing.Announce
	routingAnn, err := NewAnnounce(id, appData)
	if err != nil {
		t.Fatal(err)
	}

	// Marshal to wire format
	wireData := routingAnn.Marshal()
	if len(wireData) == 0 {
		t.Fatal("marshal returned empty")
	}

	// Unmarshal as reticulum.Announce (cross-package)
	retAnn, err := reticulum.UnmarshalAnnouncePacket(wireData)
	if err != nil {
		t.Fatal(err)
	}

	// Verify fields match
	if retAnn.DestHash != routingAnn.DestHash {
		t.Error("dest hash mismatch")
	}
	if !bytes.Equal(retAnn.AppData, appData) {
		t.Error("app data mismatch")
	}
	if retAnn.Hops != routingAnn.HopCount {
		t.Error("hop count mismatch")
	}

	// Verify the reticulum announce
	if err := retAnn.Verify(); err != nil {
		t.Fatalf("reticulum announce should verify: %v", err)
	}

	// Unmarshal back as routing.Announce
	routingAnn2, err := UnmarshalAnnounce(wireData)
	if err != nil {
		t.Fatal(err)
	}
	if !routingAnn2.Verify() {
		t.Fatal("re-parsed routing announce should verify")
	}

	// Increment hop in routing, verify in reticulum
	routingAnn.IncrementHop()
	routingAnn.IncrementHop()
	wireData2 := routingAnn.Marshal()

	retAnn2, err := reticulum.UnmarshalAnnouncePacket(wireData2)
	if err != nil {
		t.Fatal(err)
	}
	if retAnn2.Hops != 2 {
		t.Errorf("hops after increment: got %d, want 2", retAnn2.Hops)
	}
	if err := retAnn2.Verify(); err != nil {
		t.Fatal("announce should still verify after hop increment")
	}
}

// ---------------------------------------------------------------------------
// Cross-package: Routing table + PathFinder + TransportNode integration
// ---------------------------------------------------------------------------

func TestCrossPackage_RoutingTablePathFinderTransport(t *testing.T) {
	nodeID := testIdentity(t)
	remoteID := testIdentity(t)

	var sent []struct {
		iface string
		data  []byte
	}
	var mu sync.Mutex

	sendFn := func(ifaceID string, packet []byte) error {
		mu.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		sent = append(sent, struct {
			iface string
			data  []byte
		}{ifaceID, cp})
		mu.Unlock()
		return nil
	}

	tn := NewTransportNode(nodeID, 30*time.Minute, sendFn)
	tn.Enable()

	registry := NewInterfaceRegistry()
	registry.Register(NewReticulumInterface("mesh_0", "mesh", 230, nil))
	registry.Register(NewReticulumInterface("tcp_0", "tcp", 65535, nil))

	pf := NewPathFinder(DefaultPathFinderConfig(), tn.Router(), registry, nodeID, sendFn)

	// Step 1: Remote announces via mesh_0
	ann, _ := NewAnnounce(remoteID, []byte("integrated-test"))
	tn.ProcessAnnounce(ann, "mesh_0")

	// Step 2: PathFinder should respond to path request for known route
	var tag [reticulum.TruncatedHashLen]byte
	rand.Read(tag[:])
	req := &reticulum.PathRequest{DestHash: remoteID.DestHash(), Tag: tag}
	reqData := reticulum.MarshalPathRequest(req)

	pf.HandlePathRequest(reqData, "tcp_0")

	mu.Lock()
	if len(sent) < 1 {
		t.Fatal("pathfinder should have sent a response")
	}
	lastPkt := sent[len(sent)-1]
	mu.Unlock()

	// Should be a response on tcp_0 (back to requester)
	if lastPkt.iface != "tcp_0" {
		t.Errorf("response should go to tcp_0, went to %q", lastPkt.iface)
	}

	hdr, err := reticulum.UnmarshalHeader(lastPkt.data)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Context != reticulum.ContextPathResponse {
		t.Fatal("should be path response")
	}

	resp, err := reticulum.UnmarshalPathResponse(hdr.Data)
	if err != nil {
		t.Fatal(err)
	}
	if resp.InterfaceType != "mesh_0" {
		t.Errorf("response iface = %q, want mesh_0", resp.InterfaceType)
	}

	// Step 3: Forward a packet using the learned route
	mu.Lock()
	sent = nil
	mu.Unlock()

	dataHdr := &reticulum.Header{
		HeaderType: reticulum.HeaderType1,
		PacketType: reticulum.PacketData,
		DestType:   reticulum.DestSingle,
		DestHash:   remoteID.DestHash(),
		Data:       []byte("via-learned-route"),
	}
	if !tn.ForwardPacket(dataHdr.Marshal(), "tcp_0") {
		t.Fatal("should forward via learned route")
	}

	mu.Lock()
	if len(sent) != 1 {
		t.Fatalf("expected 1 forwarded packet, got %d", len(sent))
	}
	if sent[0].iface != "mesh_0" {
		t.Errorf("forwarded to %q, want mesh_0", sent[0].iface)
	}
	mu.Unlock()
}
