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

	// Establish a link
	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	respData, _ := bobLM.HandleLinkRequest(reqData)
	confirmData, _ := aliceLM.HandleLinkResponse(respData, bob.SigningPublicKey())
	bobLM.HandleLinkConfirm(confirmData)

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
	respData, _ := bobLM.HandleLinkRequest(reqData)
	confirmData, _ := aliceLM.HandleLinkResponse(respData, bob.SigningPublicKey())
	bobLM.HandleLinkConfirm(confirmData)

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
	respData, _ := bobLM.HandleLinkRequest(reqData)
	confirmData, _ := aliceLM.HandleLinkResponse(respData, bob.SigningPublicKey())
	bobLM.HandleLinkConfirm(confirmData)

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

func TestBandwidthPerLink(t *testing.T) {
	bps := BandwidthPerLink()
	if bps != 0.45 {
		t.Errorf("expected 0.45 bps, got %f", bps)
	}
}
