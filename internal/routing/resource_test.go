package routing

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

// peerPair simulates two resource transfer managers connected back-to-back.
type peerPair struct {
	sender   *ResourceTransfer
	receiver *ResourceTransfer
	mu       sync.Mutex
}

func newPeerPair(t *testing.T) *peerPair {
	t.Helper()
	pp := &peerPair{}

	// Sender's sendFn delivers to receiver's handlers
	senderSend := func(ifaceID string, packet []byte) error {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return err
		}
		// Dispatch based on context
		switch hdr.Context {
		case reticulum.ContextResourceAdv:
			pp.receiver.HandleAdvertisement(hdr.Data, "test_0")
		case reticulum.ContextResource:
			pp.receiver.HandleSegment(hdr.Data, "test_0")
		case reticulum.ContextResourcePRF:
			pp.sender.HandleProof(hdr.Data)
		}
		return nil
	}

	// Receiver's sendFn delivers to sender's handlers
	receiverSend := func(ifaceID string, packet []byte) error {
		hdr, err := reticulum.UnmarshalHeader(packet)
		if err != nil {
			return err
		}
		switch hdr.Context {
		case reticulum.ContextResourceReq:
			pp.sender.HandleRequest(hdr.Data, "test_0")
		case reticulum.ContextResourcePRF:
			pp.sender.HandleProof(hdr.Data)
		}
		return nil
	}

	config := DefaultResourceTransferConfig()
	config.SegmentSize = 50 // small for testing

	pp.sender = NewResourceTransfer(config, senderSend)
	pp.receiver = NewResourceTransfer(config, receiverSend)
	return pp
}

func TestResourceTransfer_SmallData(t *testing.T) {
	pp := newPeerPair(t)

	data := []byte("Hello, this is a small resource transfer test!")

	// Offer the resource (sender side)
	hash, err := pp.sender.Offer(context.Background(), data, "test_0")
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if hash == [reticulum.FullHashLen]byte{} {
		t.Fatal("hash should not be zero")
	}

	// Wait for the transfer to complete
	// The Offer triggers: adv → receiver.HandleAdvertisement → req → sender.HandleRequest → segments → receiver.HandleSegment → proof
	time.Sleep(100 * time.Millisecond)

	// Verify sender cleaned up (proof received)
	out, _ := pp.sender.Stats()
	if out != 0 {
		t.Fatalf("sender outbound count = %d, want 0 (should be cleaned up after proof)", out)
	}
}

func TestResourceTransfer_MultiSegment(t *testing.T) {
	pp := newPeerPair(t)

	// 250 bytes with 50-byte segments = 5 segments
	data := bytes.Repeat([]byte("ABCDE"), 50)

	hash, err := pp.sender.Offer(context.Background(), data, "test_0")
	if err != nil {
		t.Fatalf("offer: %v", err)
	}

	_ = hash
	time.Sleep(200 * time.Millisecond)

	out, in := pp.sender.Stats()
	if out != 0 {
		t.Fatalf("sender should have 0 outbound after proof, got %d", out)
	}
	_, _ = in, in // receiver cleans up too
}

func TestResourceTransfer_LargeData(t *testing.T) {
	pp := newPeerPair(t)

	// 10KB with 50-byte segments = 200 segments
	data := bytes.Repeat([]byte("X"), 10000)

	_, err := pp.sender.Offer(context.Background(), data, "test_0")
	if err != nil {
		t.Fatalf("offer: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	out, _ := pp.sender.Stats()
	if out != 0 {
		t.Fatalf("sender outbound = %d, want 0", out)
	}
}

func TestResourceTransfer_TooLarge(t *testing.T) {
	config := DefaultResourceTransferConfig()
	rt := NewResourceTransfer(config, nil)

	data := make([]byte, reticulum.MaxResourceSize+1)
	_, err := rt.Offer(context.Background(), data, "test_0")
	if err == nil {
		t.Fatal("expected error for oversized resource")
	}
}

func TestResourceTransfer_Stats(t *testing.T) {
	config := DefaultResourceTransferConfig()
	rt := NewResourceTransfer(config, func(string, []byte) error { return nil })

	out, in := rt.Stats()
	if out != 0 || in != 0 {
		t.Fatalf("initial stats: out=%d, in=%d, want 0,0", out, in)
	}
}

func TestResourceTransfer_Prune(t *testing.T) {
	config := DefaultResourceTransferConfig()
	config.TransferTimeout = 1 * time.Millisecond

	rt := NewResourceTransfer(config, func(string, []byte) error { return nil })

	// Add a stale outbound transfer
	var hash [reticulum.FullHashLen]byte
	hash[0] = 0xAA
	rt.mu.Lock()
	rt.outbound[hash] = &outboundTransfer{
		hash:    hash,
		data:    []byte("test"),
		created: time.Now().Add(-1 * time.Hour),
	}
	rt.mu.Unlock()

	time.Sleep(10 * time.Millisecond)
	rt.prune()

	out, _ := rt.Stats()
	if out != 0 {
		t.Fatalf("outbound = %d after prune, want 0", out)
	}
}
