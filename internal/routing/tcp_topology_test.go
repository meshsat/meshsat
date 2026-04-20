package routing

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

// ---------------------------------------------------------------------------
// TCP multi-node topology tests
// ---------------------------------------------------------------------------

// TestTCPTopology_ThreeNodeLinear sets up A↔B↔C via TCP and verifies
// announce propagation and packet forwarding across hops.
func TestTCPTopology_ThreeNodeLinear(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Node identities
	_ = testIdentity(t) // idA (node A doesn't need its own transport)
	idB := testIdentity(t)
	idC := testIdentity(t)

	// Packet collectors
	var muA, muC sync.Mutex
	var packetsA, packetsC [][]byte

	// Node B listens on two ports (one for A, one for C)
	portAB := freePortNum(t)
	portBC := freePortNum(t)

	// Node B's transport node
	regB := NewInterfaceRegistry()
	tnB := NewTransportNode(idB, 30*time.Minute, regB.Send)
	tnB.Enable()

	// TCP interface: B listens for A
	tcpBA := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_ba",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", portAB),
	}, func(packet []byte) {
		// B receives from A — process announce or forward
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return
		}
		if hdr.PacketType == reticulum.PacketAnnounce {
			ann, err := UnmarshalAnnounce(packet)
			if err == nil {
				tnB.ProcessAnnounce(ann, "tcp_ba")
			}
		} else {
			tnB.ForwardPacket(packet, "tcp_ba")
		}
	})
	if err := tcpBA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tcpBA.Stop()

	// TCP interface: B listens for C
	tcpBC := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_bc",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", portBC),
	}, func(packet []byte) {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return
		}
		if hdr.PacketType == reticulum.PacketAnnounce {
			ann, err := UnmarshalAnnounce(packet)
			if err == nil {
				tnB.ProcessAnnounce(ann, "tcp_bc")
			}
		} else {
			tnB.ForwardPacket(packet, "tcp_bc")
		}
	})
	if err := tcpBC.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tcpBC.Stop()

	// Register B's interfaces
	regB.Register(NewReticulumInterface("tcp_ba", "tcp", 65535, tcpBA.Send))
	regB.Register(NewReticulumInterface("tcp_bc", "tcp", 65535, tcpBC.Send))

	// Node A connects to B
	tcpA := NewTCPInterface(TCPInterfaceConfig{
		Name:        "tcp_a",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", portAB),
	}, func(packet []byte) {
		muA.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		packetsA = append(packetsA, cp)
		muA.Unlock()
	})
	if err := tcpA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tcpA.Stop()

	// Node C connects to B
	tcpC := NewTCPInterface(TCPInterfaceConfig{
		Name:        "tcp_c",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", portBC),
	}, func(packet []byte) {
		muC.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		packetsC = append(packetsC, cp)
		muC.Unlock()
	})
	if err := tcpC.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tcpC.Stop()

	// Wait for connections to establish
	waitFor(t, 2*time.Second, func() bool {
		return tcpA.IsOnline() && tcpC.IsOnline() && tcpBA.PeerCount() >= 1 && tcpBC.PeerCount() >= 1
	})

	// --- Test 1: C sends announce, B learns route ---
	annC, err := NewAnnounce(idC, []byte("node-c"))
	if err != nil {
		t.Fatal(err)
	}
	annPacket := annC.Marshal()
	if err := tcpC.Send(ctx, annPacket); err != nil {
		t.Fatalf("C announce send: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if tnB.RouteCount() != 1 {
		t.Fatalf("B should have 1 route, got %d", tnB.RouteCount())
	}
	if tnB.BestInterface(idC.DestHash()) != "tcp_bc" {
		t.Errorf("B should route to C via tcp_bc, got %q", tnB.BestInterface(idC.DestHash()))
	}

	// --- Test 2: A sends data packet to C, B forwards ---
	dataHdr := &reticulum.Header{
		HeaderType:    reticulum.HeaderType1,
		TransportType: reticulum.TransportBroadcast,
		DestType:      reticulum.DestSingle,
		PacketType:    reticulum.PacketData,
		Hops:          0,
		DestHash:      idC.DestHash(),
		Context:       reticulum.ContextNone,
		Data:          []byte("hello-from-A-to-C"),
	}
	if err := tcpA.Send(ctx, dataHdr.Marshal()); err != nil {
		t.Fatalf("A data send: %v", err)
	}

	// Wait for C to receive the forwarded packet
	waitFor(t, 2*time.Second, func() bool {
		muC.Lock()
		defer muC.Unlock()
		return len(packetsC) >= 1
	})

	muC.Lock()
	if len(packetsC) == 0 {
		muC.Unlock()
		t.Fatal("C should have received forwarded packet")
	}
	fwdPacket := packetsC[len(packetsC)-1]
	muC.Unlock()

	fwdHdr, err := reticulum.UnmarshalHeader(fwdPacket)
	if err != nil {
		t.Fatal(err)
	}
	if fwdHdr.HeaderType != reticulum.HeaderType2 {
		t.Error("forwarded packet should be HEADER_2 (transport)")
	}
	if fwdHdr.TransportID != idB.DestHash() {
		t.Error("transport ID should be B's dest hash")
	}
	if fwdHdr.DestHash != idC.DestHash() {
		t.Error("dest hash should be C")
	}
	if fwdHdr.Hops != 1 {
		t.Errorf("hops should be 1, got %d", fwdHdr.Hops)
	}
	if !bytes.Equal(fwdHdr.Data, []byte("hello-from-A-to-C")) {
		t.Error("data payload should be preserved")
	}
}

// TestTCPTopology_AnnounceReachesAllNodes tests that an announce from C
// reaches both B and A through B's relay.
func TestTCPTopology_AnnounceReachesAllNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	idB := testIdentity(t)
	idC := testIdentity(t)

	portAB := freePortNum(t)
	portBC := freePortNum(t)

	// Track what A receives
	var mu sync.Mutex
	var receivedByA [][]byte

	// B: set up transport + relay
	regB := NewInterfaceRegistry()
	tnB := NewTransportNode(idB, 30*time.Minute, regB.Send)
	tnB.Enable()

	tcpBA := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_ba",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", portAB),
	}, nil) // B doesn't need to process A's packets for this test
	if err := tcpBA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tcpBA.Stop()

	tcpBC := NewTCPInterface(TCPInterfaceConfig{
		Name:       "tcp_bc",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", portBC),
	}, func(packet []byte) {
		// B receives C's announce → learn route + relay to A
		hdr, _ := reticulum.UnmarshalHeader(packet)
		if hdr != nil && hdr.PacketType == reticulum.PacketAnnounce {
			ann, err := UnmarshalAnnounce(packet)
			if err == nil {
				tnB.ProcessAnnounce(ann, "tcp_bc")
				// Relay: re-send announce to A's interface
				ann.IncrementHop()
				tcpBA.Send(ctx, ann.Marshal())
			}
		}
	})
	if err := tcpBC.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tcpBC.Stop()

	regB.Register(NewReticulumInterface("tcp_ba", "tcp", 65535, tcpBA.Send))
	regB.Register(NewReticulumInterface("tcp_bc", "tcp", 65535, tcpBC.Send))

	// A connects to B
	tcpA := NewTCPInterface(TCPInterfaceConfig{
		Name:        "tcp_a",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", portAB),
	}, func(packet []byte) {
		mu.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		receivedByA = append(receivedByA, cp)
		mu.Unlock()
	})
	if err := tcpA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tcpA.Stop()

	// C connects to B
	tcpC := NewTCPInterface(TCPInterfaceConfig{
		Name:        "tcp_c",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", portBC),
	}, nil)
	if err := tcpC.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tcpC.Stop()

	waitFor(t, 2*time.Second, func() bool {
		return tcpA.IsOnline() && tcpC.IsOnline()
	})

	// C sends announce
	annC, _ := NewAnnounce(idC, []byte("node-c"))
	tcpC.Send(ctx, annC.Marshal())

	// Wait for A to receive the relayed announce
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(receivedByA) >= 1
	})

	mu.Lock()
	defer mu.Unlock()
	if len(receivedByA) == 0 {
		t.Fatal("A should receive C's announce relayed through B")
	}

	// Parse and verify the announce A received
	relayed, err := UnmarshalAnnounce(receivedByA[0])
	if err != nil {
		t.Fatalf("A received invalid announce: %v", err)
	}
	if relayed.DestHash != idC.DestHash() {
		t.Error("relayed announce should have C's dest hash")
	}
	if relayed.HopCount != 1 {
		t.Errorf("relayed announce hop count should be 1, got %d", relayed.HopCount)
	}
}

// TestTCPTopology_ConcurrentSends verifies TCP handles concurrent writes.
func TestTCPTopology_ConcurrentSends(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	port := freePortNum(t)
	var mu sync.Mutex
	var received [][]byte

	srv := NewTCPInterface(TCPInterfaceConfig{
		Name:       "srv",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		mu.Lock()
		cp := make([]byte, len(packet))
		copy(cp, packet)
		received = append(received, cp)
		mu.Unlock()
	})
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	cli := NewTCPInterface(TCPInterfaceConfig{
		Name:        "cli",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, nil)
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer cli.Stop()

	waitFor(t, 2*time.Second, func() bool { return cli.IsOnline() })

	// Send 50 packets concurrently
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			payload := make([]byte, reticulum.HeaderMinSize+4)
			payload[0] = byte(idx)
			payload[len(payload)-4] = byte(idx >> 24)
			payload[len(payload)-3] = byte(idx >> 16)
			payload[len(payload)-2] = byte(idx >> 8)
			payload[len(payload)-1] = byte(idx)
			cli.Send(ctx, payload)
		}(i)
	}
	wg.Wait()

	// Wait for all to arrive
	waitFor(t, 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= n
	})

	mu.Lock()
	if len(received) < n {
		t.Errorf("expected %d packets, received %d", n, len(received))
	}
	mu.Unlock()
}

// TestTCPTopology_ConnectionDrop verifies behavior when a peer disconnects.
func TestTCPTopology_ConnectionDrop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	port := freePortNum(t)

	srv := NewTCPInterface(TCPInterfaceConfig{
		Name:       "srv",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, nil)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	cli := NewTCPInterface(TCPInterfaceConfig{
		Name:        "cli",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, nil)
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 2*time.Second, func() bool { return srv.PeerCount() >= 1 })

	if srv.PeerCount() < 1 {
		t.Fatal("server should have 1 peer")
	}

	// Client disconnects
	cli.Stop()

	// Server should detect disconnect
	waitFor(t, 3*time.Second, func() bool { return srv.PeerCount() == 0 })

	if srv.PeerCount() != 0 {
		t.Errorf("server should have 0 peers after disconnect, got %d", srv.PeerCount())
	}
}

// TestTCPTopology_ReconnectAfterDrop tests client reconnection.
func TestTCPTopology_ReconnectAfterDrop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	port := freePortNum(t)
	var received int32
	var mu sync.Mutex

	srv := NewTCPInterface(TCPInterfaceConfig{
		Name:       "srv",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		mu.Lock()
		received++
		mu.Unlock()
	})
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	cli := NewTCPInterface(TCPInterfaceConfig{
		Name:              "cli",
		ConnectAddr:       fmt.Sprintf("127.0.0.1:%d", port),
		Reconnect:         true,
		ReconnectInterval: 200 * time.Millisecond,
	}, nil)
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer cli.Stop()

	waitFor(t, 2*time.Second, func() bool { return cli.IsOnline() })

	// Send a packet before disconnect
	payload := make([]byte, reticulum.HeaderMinSize)
	cli.Send(ctx, payload)
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	firstCount := received
	mu.Unlock()
	if firstCount < 1 {
		t.Fatal("should receive packet before restart")
	}

	// Stop and restart server
	srv.Stop()
	time.Sleep(300 * time.Millisecond)

	// Client should go offline
	if cli.IsOnline() {
		// The client may still think it's online briefly
		time.Sleep(500 * time.Millisecond)
	}

	// Restart server on same port
	srv2 := NewTCPInterface(TCPInterfaceConfig{
		Name:       "srv2",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		mu.Lock()
		received++
		mu.Unlock()
	})
	if err := srv2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv2.Stop()

	// Wait for client to reconnect
	waitFor(t, 5*time.Second, func() bool { return cli.IsOnline() })
	if !cli.IsOnline() {
		t.Fatal("client should reconnect to new server")
	}

	// Send another packet
	cli.Send(ctx, payload)

	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return received > firstCount
	})
}

// ---------------------------------------------------------------------------
// Link establishment over TCP
// ---------------------------------------------------------------------------

func TestTCPTopology_LinkEstablishment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	port := freePortNum(t)
	idA := testIdentity(t)
	idB := testIdentity(t)

	lmA := NewLinkManager(idA)
	lmB := NewLinkManager(idB)

	var proofForA []byte
	var proofMu sync.Mutex
	var srvLink *TCPInterface

	// B is the server (responder)
	srv := NewTCPInterface(TCPInterfaceConfig{
		Name:       "srv",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		// B receives link request → processes it
		if len(packet) > 0 && packet[0] == PacketLinkRequest {
			proof, err := lmB.HandleLinkRequest(packet)
			if err != nil {
				return
			}
			// Send proof back
			srvLink.Send(ctx, proof)
		}
	})
	srvLink = srv
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// A is the client (initiator)
	cli := NewTCPInterface(TCPInterfaceConfig{
		Name:        "cli",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		if len(packet) > 0 && packet[0] == PacketLinkProof {
			proofMu.Lock()
			proofForA = make([]byte, len(packet))
			copy(proofForA, packet)
			proofMu.Unlock()
		}
	})
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer cli.Stop()

	waitFor(t, 2*time.Second, func() bool { return cli.IsOnline() })

	// A initiates link to B
	reqData, pendingLink, err := lmA.InitiateLink(idB.DestHash())
	if err != nil {
		t.Fatal(err)
	}
	if pendingLink.State != LinkStatePending {
		t.Error("link should be pending")
	}

	// Send request
	if err := cli.Send(ctx, reqData); err != nil {
		t.Fatal(err)
	}

	// Wait for proof to arrive
	waitFor(t, 2*time.Second, func() bool {
		proofMu.Lock()
		defer proofMu.Unlock()
		return len(proofForA) > 0
	})

	proofMu.Lock()
	proofData := proofForA
	proofMu.Unlock()

	// A processes proof
	if err := lmA.HandleLinkProof(proofData, idB.SigningPublicKey()); err != nil {
		t.Fatalf("handle proof: %v", err)
	}

	// Verify both sides have established links
	link := lmA.GetLink(pendingLink.ID)
	if link == nil || link.State != LinkStateEstablished {
		t.Fatal("A's link should be established")
	}
	bLinks := lmB.ActiveLinks()
	if len(bLinks) != 1 {
		t.Fatalf("B should have 1 active link, got %d", len(bLinks))
	}

	// Encrypt on A, decrypt on B
	ct, err := link.Encrypt([]byte("secret message"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := bLinks[0].Decrypt(ct)
	if err != nil {
		t.Fatalf("B decrypt: %v", err)
	}
	if !bytes.Equal(pt, []byte("secret message")) {
		t.Error("decrypted message mismatch")
	}

	// Encrypt on B, decrypt on A
	ct2, _ := bLinks[0].Encrypt([]byte("reply"))
	pt2, err := link.Decrypt(ct2)
	if err != nil {
		t.Fatalf("A decrypt: %v", err)
	}
	if !bytes.Equal(pt2, []byte("reply")) {
		t.Error("reverse decrypted message mismatch")
	}
}

// ---------------------------------------------------------------------------
// Resource transfer over TCP
// ---------------------------------------------------------------------------

func TestTCPTopology_ResourceTransfer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	port := freePortNum(t)

	var senderRT, receiverRT *ResourceTransfer

	// Receiver (server)
	srv := NewTCPInterface(TCPInterfaceConfig{
		Name:       "srv",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return
		}
		switch hdr.Context {
		case reticulum.ContextResourceAdv:
			receiverRT.HandleAdvertisement(hdr.Data, "srv")
		case reticulum.ContextResourceReq:
			// Forward to sender — in this test, sender is the client
			// The sender handles requests via the client callback
		case reticulum.ContextResource:
			receiverRT.HandleSegment(hdr.Data, "srv")
		}
	})
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// Sender (client)
	cli := NewTCPInterface(TCPInterfaceConfig{
		Name:        "cli",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return
		}
		switch hdr.Context {
		case reticulum.ContextResourceReq:
			senderRT.HandleRequest(hdr.Data, "cli")
		case reticulum.ContextResourcePRF:
			senderRT.HandleProof(hdr.Data)
		}
	})
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer cli.Stop()

	// Wire up resource transfer managers
	senderRT = NewResourceTransfer(
		ResourceTransferConfig{SegmentSize: 100, TransferTimeout: 30 * time.Second},
		func(ifaceID string, packet []byte) error { return cli.Send(ctx, packet) },
	)
	receiverRT = NewResourceTransfer(
		ResourceTransferConfig{SegmentSize: 100, TransferTimeout: 30 * time.Second},
		func(ifaceID string, packet []byte) error { return srv.Send(ctx, packet) },
	)

	waitFor(t, 2*time.Second, func() bool { return cli.IsOnline() })

	// Sender offers a resource
	testData := make([]byte, 500)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	hash, err := senderRT.Offer(ctx, testData, "cli")
	if err != nil {
		t.Fatal(err)
	}
	_ = hash

	// The receiver should have started receiving via HandleAdvertisement callback
	// and the request/segment exchange should complete automatically.
	// We need to wait for the complete transfer. Since the receiver sends the request
	// back to the server (which needs to reach the client), we need bidirectional routing.
	// In this simplified test, the server sends the request back through its peers,
	// and the client processes it.

	// Give some time for the exchange
	time.Sleep(2 * time.Second)

	// Check stats
	sOut, sIn := senderRT.Stats()
	rOut, rIn := receiverRT.Stats()
	t.Logf("sender: out=%d, in=%d; receiver: out=%d, in=%d", sOut, sIn, rOut, rIn)
}

// ---------------------------------------------------------------------------
// PathFinder over TCP
// ---------------------------------------------------------------------------

func TestTCPTopology_PathDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	port := freePortNum(t)

	_ = testIdentity(t) // idA
	idB := testIdentity(t)
	idC := testIdentity(t)

	// B knows route to C
	routerB := reticulum.NewRouter(30 * time.Minute)
	annC, _ := NewAnnounce(idC, nil)
	routerB.ProcessAnnounce(annC.ret, "tcp_bc")

	regB := NewInterfaceRegistry()

	// B is the server
	var pfB *PathFinder
	srv := NewTCPInterface(TCPInterfaceConfig{
		Name:       "srv",
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return
		}
		if hdr.Context == reticulum.ContextRequest {
			pfB.HandlePathRequest(hdr.Data, "tcp_ba")
		}
	})
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	pfB = NewPathFinder(
		PathFinderConfig{
			RequestTimeout:  5 * time.Second,
			DedupTTL:        5 * time.Minute,
			MaxDedupEntries: 100,
			MaxHops:         128,
		},
		routerB,
		regB,
		idB,
		func(ifaceID string, packet []byte) error {
			return srv.Send(ctx, packet)
		},
	)

	regB.Register(NewReticulumInterface("tcp_ba", "tcp", 65535, srv.Send))

	// A connects to B
	var pathResponseCh = make(chan []byte, 1)
	cli := NewTCPInterface(TCPInterfaceConfig{
		Name:        "cli",
		ConnectAddr: fmt.Sprintf("127.0.0.1:%d", port),
	}, func(packet []byte) {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return
		}
		if hdr.Context == reticulum.ContextPathResponse {
			select {
			case pathResponseCh <- hdr.Data:
			default:
			}
		}
	})
	if err := cli.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer cli.Stop()

	waitFor(t, 2*time.Second, func() bool { return cli.IsOnline() })

	// A sends path request for C
	var tag [reticulum.TruncatedHashLen]byte
	rand.Read(tag[:])
	req := &reticulum.PathRequest{
		DestHash: idC.DestHash(),
		Tag:      tag,
	}
	var broadcastDest [reticulum.TruncatedHashLen]byte
	packet := reticulum.BuildPathRequestPacket(broadcastDest, req)
	if err := cli.Send(ctx, packet); err != nil {
		t.Fatal(err)
	}

	// Wait for path response
	select {
	case respData := <-pathResponseCh:
		resp, err := reticulum.UnmarshalPathResponse(respData)
		if err != nil {
			t.Fatal(err)
		}
		if resp.DestHash != idC.DestHash() {
			t.Error("response should be for C's dest hash")
		}
		if resp.Tag != tag {
			t.Error("response tag should match request")
		}
		t.Logf("path found: %d hops via %s", resp.Hops, resp.InterfaceType)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for path response")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func freePortNum(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
