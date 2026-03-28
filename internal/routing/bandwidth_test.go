package routing

import (
	"sync"
	"testing"
	"time"
)

func TestBandwidthLimiter_Unlimited(t *testing.T) {
	l := NewAnnounceBandwidthLimiter()
	// No bandwidth configured = unlimited
	if !l.Allow("mesh_0", 1000) {
		t.Fatal("unconfigured interface should be unlimited")
	}
	if l.Available("mesh_0") != -1 {
		t.Fatal("unconfigured interface should return -1 for available")
	}
}

func TestBandwidthLimiter_SetBandwidth(t *testing.T) {
	l := NewAnnounceBandwidthLimiter()
	// 1200 bps mesh, 2% budget = 24 bps = 3 bytes/sec
	l.SetBandwidth("mesh_0", 1200, 2)

	// A small announce (~163 bytes = 1304 bits) should exceed the 1-second bucket (24 bits)
	if l.Allow("mesh_0", 163) {
		t.Fatal("163-byte announce should exceed 24-bit budget")
	}

	// A 2-byte packet (16 bits) should fit within 24 bits
	if !l.Allow("mesh_0", 2) {
		t.Fatal("2-byte packet should fit within 24-bit budget")
	}
}

func TestBandwidthLimiter_Refill(t *testing.T) {
	l := NewAnnounceBandwidthLimiter()
	// 10000 bps, 100% budget = 10000 bps = 1250 bytes/sec
	l.SetBandwidth("test_0", 10000, 100)

	// Drain the bucket
	l.Allow("test_0", 1250)

	// Should be empty now
	if l.Allow("test_0", 100) {
		t.Fatal("bucket should be empty")
	}

	// Wait for refill
	time.Sleep(200 * time.Millisecond)

	// Should have ~250 bytes refilled (1250 * 0.2)
	if !l.Allow("test_0", 200) {
		t.Fatal("bucket should have refilled enough for 200 bytes")
	}
}

func TestBandwidthLimiter_SetDefaultBandwidth(t *testing.T) {
	l := NewAnnounceBandwidthLimiter()
	l.SetDefaultBandwidth("mesh_0", "mesh")

	avail := l.Available("mesh_0")
	if avail == -1 {
		t.Fatal("mesh_0 should have a configured budget")
	}
	// 1200 bps * 2% = 24 bps = 3 bytes
	if avail != 3 {
		t.Errorf("available: got %d, want 3 bytes", avail)
	}
}

func TestBandwidthLimiter_UnlimitedChannel(t *testing.T) {
	l := NewAnnounceBandwidthLimiter()
	l.SetDefaultBandwidth("webhook_0", "webhook")

	// Webhook has 0 bandwidth = no limiter registered
	if l.Available("webhook_0") != -1 {
		t.Fatal("webhook should be unlimited")
	}
}

func TestBandwidthLimiter_ZeroBandwidth(t *testing.T) {
	l := NewAnnounceBandwidthLimiter()
	l.SetBandwidth("test_0", 0, 2)
	// Should not register a bucket
	if l.Available("test_0") != -1 {
		t.Fatal("zero bandwidth should be unlimited")
	}
}

func TestBandwidthLimiter_ConcurrentAllow(t *testing.T) {
	l := NewAnnounceBandwidthLimiter()
	// High bandwidth so we don't exhaust budget
	l.SetBandwidth("mesh_0", 1000000, 100) // 1Mbps, 100%

	const goroutines = 20
	const allowsPerGoroutine = 50

	var wg sync.WaitGroup
	var allowed int64
	var mu sync.Mutex
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < allowsPerGoroutine; j++ {
				if l.Allow("mesh_0", 1) { // 1 byte = 8 bits
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	// With 1Mbps and 100% budget, all should succeed
	mu.Lock()
	if allowed != goroutines*allowsPerGoroutine {
		t.Errorf("expected all %d to be allowed, got %d", goroutines*allowsPerGoroutine, allowed)
	}
	mu.Unlock()
}

func TestBandwidthLimiter_DynamicReconfig(t *testing.T) {
	l := NewAnnounceBandwidthLimiter()

	// Start with low bandwidth
	l.SetBandwidth("mesh_0", 100, 100) // 100 bps, 100% budget = 12.5 bytes/sec

	// A 20-byte packet should fail (160 bits > 100 bits capacity)
	if l.Allow("mesh_0", 20) {
		t.Fatal("20 bytes should exceed 100-bit budget")
	}

	// Reconfigure to high bandwidth
	l.SetBandwidth("mesh_0", 1000000, 100) // 1Mbps

	// Same packet should now succeed
	if !l.Allow("mesh_0", 20) {
		t.Fatal("20 bytes should fit in 1Mbps budget")
	}

	// Reconfigure to unlimited (0 bps)
	l.SetBandwidth("mesh_0", 0, 100)
	if l.Available("mesh_0") != -1 {
		t.Fatal("0 bps should be unlimited")
	}
}

func TestMaxLinksForBandwidth(t *testing.T) {
	// 1200 bps mesh, 4% capacity for keepalives
	max := MaxLinksForBandwidth(1200, 4)
	// 1200 * 0.04 = 48 bps budget / 0.45 bps per link ≈ 106
	if max < 100 || max > 110 {
		t.Errorf("expected ~106 links for 1200bps/4%%, got %d", max)
	}

	// Edge cases
	if MaxLinksForBandwidth(0, 4) != 0 {
		t.Error("zero bandwidth should return 0")
	}
	if MaxLinksForBandwidth(1200, 0) != 0 {
		t.Error("zero capacity should return 0")
	}
}
