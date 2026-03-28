package routing

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestKeepalivePacket_Roundtrip(t *testing.T) {
	kp := &KeepalivePacket{
		Random: 0x42,
	}
	kp.LinkID[0] = 0xAA
	kp.LinkID[31] = 0xBB

	data := kp.Marshal()
	parsed, err := UnmarshalKeepalive(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.LinkID != kp.LinkID {
		t.Error("link ID mismatch")
	}
	if parsed.Random != 0x42 {
		t.Errorf("random: got %d, want 0x42", parsed.Random)
	}
}

func TestUnmarshalKeepalive_TooShort(t *testing.T) {
	_, err := UnmarshalKeepalive([]byte{PacketKeepalive, 0x00})
	if err == nil {
		t.Fatal("should fail on short data")
	}
}

func TestUnmarshalKeepalive_WrongType(t *testing.T) {
	data := make([]byte, 1+KeepalivePacketLen)
	data[0] = 0xFF // wrong type
	_, err := UnmarshalKeepalive(data)
	if err == nil {
		t.Fatal("should fail on wrong type")
	}
}

func TestLinkKeepalive_SendsForActiveLinks(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	// Establish a link (2-packet handshake)
	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	proofData, _ := bobLM.HandleLinkRequest(reqData)
	aliceLM.HandleLinkProof(proofData, bob.SigningPublicKey())

	var sent int
	var mu sync.Mutex
	ka := NewLinkKeepalive(aliceLM, func(linkID [LinkIDLen]byte, data []byte) {
		mu.Lock()
		sent++
		mu.Unlock()
	})

	// Manually tick
	ka.tick()

	mu.Lock()
	count := sent
	mu.Unlock()

	if count != 1 {
		t.Errorf("should have sent 1 keepalive, got %d", count)
	}
}

func TestLinkKeepalive_HandleKeepalive(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	proofData, _ := bobLM.HandleLinkRequest(reqData)
	aliceLM.HandleLinkProof(proofData, bob.SigningPublicKey())

	link := bobLM.ActiveLinks()[0]
	oldActivity := link.LastActivity

	// Small delay so time advances
	time.Sleep(10 * time.Millisecond)

	ka := NewLinkKeepalive(bobLM, nil)
	kp := &KeepalivePacket{LinkID: link.ID, Random: 0x01}
	if err := ka.HandleKeepalive(kp.Marshal()); err != nil {
		t.Fatalf("handle keepalive: %v", err)
	}

	if !link.LastActivity.After(oldActivity) {
		t.Error("last activity should have been updated")
	}
}

func TestLinkKeepalive_Timeout(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	proofData, _ := bobLM.HandleLinkRequest(reqData)
	aliceLM.HandleLinkProof(proofData, bob.SigningPublicKey())

	// Force the link to look old
	link := aliceLM.ActiveLinks()[0]
	link.LastActivity = time.Now().Add(-2 * time.Minute)

	ka := &LinkKeepalive{
		linkMgr:  aliceLM,
		sendFn:   func(linkID [LinkIDLen]byte, data []byte) {},
		interval: KeepaliveInterval,
		timeout:  KeepaliveTimeout,
	}

	ka.tick()

	if len(aliceLM.ActiveLinks()) != 0 {
		t.Error("timed out link should have been closed")
	}
}

func TestLinkKeepalive_Start(t *testing.T) {
	alice := testIdentity(t)
	lm := NewLinkManager(alice)
	ka := NewLinkKeepalive(lm, nil)

	ctx, cancel := context.WithCancel(context.Background())
	ka.Start(ctx)
	// Just verify it doesn't panic — the goroutine starts and stops cleanly
	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestLinkKeepalive_SimultaneousTimeouts(t *testing.T) {
	alice := testIdentity(t)

	aliceLM := NewLinkManager(alice)

	// Establish 3 links with different identities
	for i := 0; i < 3; i++ {
		remote := testIdentity(t)
		remoteLM := NewLinkManager(remote)
		reqData, _, _ := aliceLM.InitiateLink(remote.DestHash())
		proofData, _ := remoteLM.HandleLinkRequest(reqData)
		aliceLM.HandleLinkProof(proofData, remote.SigningPublicKey())
	}

	if len(aliceLM.ActiveLinks()) != 3 {
		t.Fatalf("should have 3 active links, got %d", len(aliceLM.ActiveLinks()))
	}

	// Force all links to be expired
	for _, link := range aliceLM.ActiveLinks() {
		link.LastActivity = time.Now().Add(-2 * time.Minute)
	}

	ka := &LinkKeepalive{
		linkMgr:  aliceLM,
		sendFn:   func(linkID [LinkIDLen]byte, data []byte) {},
		interval: KeepaliveInterval,
		timeout:  KeepaliveTimeout,
	}

	ka.tick()

	if len(aliceLM.ActiveLinks()) != 0 {
		t.Errorf("all timed-out links should be closed, got %d remaining", len(aliceLM.ActiveLinks()))
	}
}

func TestLinkKeepalive_HandleKeepalive_UnknownLink(t *testing.T) {
	alice := testIdentity(t)
	lm := NewLinkManager(alice)
	ka := NewLinkKeepalive(lm, nil)

	// Keepalive for a link that doesn't exist
	var unknownID [LinkIDLen]byte
	unknownID[0] = 0xFF
	kp := &KeepalivePacket{LinkID: unknownID, Random: 0x42}

	err := ka.HandleKeepalive(kp.Marshal())
	if err == nil {
		t.Fatal("should fail for unknown link")
	}
}

func TestLinkKeepalive_PartialTimeout(t *testing.T) {
	alice := testIdentity(t)
	aliceLM := NewLinkManager(alice)

	// Establish 2 links
	bob := testIdentity(t)
	bobLM := NewLinkManager(bob)
	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	proofData, _ := bobLM.HandleLinkRequest(reqData)
	aliceLM.HandleLinkProof(proofData, bob.SigningPublicKey())

	charlie := testIdentity(t)
	charlieLM := NewLinkManager(charlie)
	reqData2, _, _ := aliceLM.InitiateLink(charlie.DestHash())
	proofData2, _ := charlieLM.HandleLinkRequest(reqData2)
	aliceLM.HandleLinkProof(proofData2, charlie.SigningPublicKey())

	links := aliceLM.ActiveLinks()
	if len(links) != 2 {
		t.Fatalf("should have 2 active links, got %d", len(links))
	}

	// Expire only the first link
	links[0].LastActivity = time.Now().Add(-2 * time.Minute)
	// Keep second link fresh
	links[1].LastActivity = time.Now()

	var sent int
	ka := &LinkKeepalive{
		linkMgr: aliceLM,
		sendFn: func(linkID [LinkIDLen]byte, data []byte) {
			sent++
		},
		interval: KeepaliveInterval,
		timeout:  KeepaliveTimeout,
	}

	ka.tick()

	// One link closed, one kept (and received keepalive)
	remaining := aliceLM.ActiveLinks()
	if len(remaining) != 1 {
		t.Errorf("should have 1 remaining link, got %d", len(remaining))
	}
	if sent != 1 {
		t.Errorf("should have sent 1 keepalive (for active link), sent %d", sent)
	}
}

func TestBandwidthPerLink(t *testing.T) {
	bps := BandwidthPerLink()
	if bps != 0.45 {
		t.Errorf("expected 0.45 bps, got %f", bps)
	}
}
