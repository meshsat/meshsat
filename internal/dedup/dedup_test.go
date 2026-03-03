package dedup

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestIsDuplicate(t *testing.T) {
	d := New(10*time.Minute, 1000)

	// First time — not a duplicate
	if d.IsDuplicate(100, 1) {
		t.Error("expected first occurrence to not be duplicate")
	}

	// Same key — duplicate
	if !d.IsDuplicate(100, 1) {
		t.Error("expected second occurrence to be duplicate")
	}

	// Different from — not duplicate
	if d.IsDuplicate(200, 1) {
		t.Error("expected different from to not be duplicate")
	}

	// Different packetID — not duplicate
	if d.IsDuplicate(100, 2) {
		t.Error("expected different packet_id to not be duplicate")
	}
}

func TestMaxSize(t *testing.T) {
	d := New(10*time.Minute, 5)

	for i := uint32(0); i < 10; i++ {
		d.IsDuplicate(1, i)
	}

	if d.Size() > 5 {
		t.Errorf("expected max 5 entries, got %d", d.Size())
	}
}

func TestPrune(t *testing.T) {
	d := New(50*time.Millisecond, 1000)

	d.IsDuplicate(1, 1)
	d.IsDuplicate(1, 2)

	time.Sleep(100 * time.Millisecond)

	d.mu.Lock()
	pruned := d.prune()
	d.mu.Unlock()

	if pruned != 2 {
		t.Errorf("expected 2 pruned, got %d", pruned)
	}

	// After prune, same keys should not be duplicate
	if d.IsDuplicate(1, 1) {
		t.Error("expected pruned entry to not be duplicate")
	}
}

func TestConcurrent(t *testing.T) {
	d := New(10*time.Minute, 10000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.StartPruner(ctx)

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := uint32(0); i < 100; i++ {
				d.IsDuplicate(uint32(g), i)
			}
		}(g)
	}
	wg.Wait()

	if d.Size() == 0 {
		t.Error("expected non-zero size after concurrent writes")
	}
}
