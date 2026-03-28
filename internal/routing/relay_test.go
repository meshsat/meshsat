package routing

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAnnounceRelay_Dedup(t *testing.T) {
	table := NewDestinationTable(nil)
	var relayed int
	var mu sync.Mutex
	relay := NewAnnounceRelay(DefaultRelayConfig(), table, func(data []byte, a *Announce) {
		mu.Lock()
		relayed++
		mu.Unlock()
	})

	id := testIdentity(t)
	relay.RegisterLocal(id.DestHash()) // local identity should not be relayed

	// Create an announce from a different identity
	remoteID := testIdentity(t)
	announce, _ := NewAnnounce(remoteID, []byte("remote"))
	data := announce.Marshal()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First receive: should accept
	if !relay.HandleAnnounce(ctx, data, "mesh_0") {
		t.Fatal("first announce should be accepted")
	}

	// Second receive of same announce: should be deduplicated
	if relay.HandleAnnounce(ctx, data, "iridium_0") {
		t.Fatal("duplicate announce should be rejected")
	}

	// Destination should be in the table
	if table.Count() != 1 {
		t.Errorf("table should have 1 destination, got %d", table.Count())
	}
}

func TestAnnounceRelay_LocalNotRelayed(t *testing.T) {
	table := NewDestinationTable(nil)
	var relayed int
	var mu sync.Mutex
	relay := NewAnnounceRelay(DefaultRelayConfig(), table, func(data []byte, a *Announce) {
		mu.Lock()
		relayed++
		mu.Unlock()
	})

	id := testIdentity(t)
	relay.RegisterLocal(id.DestHash())

	announce, _ := NewAnnounce(id, nil)
	data := announce.Marshal()

	ctx := context.Background()
	relay.HandleAnnounce(ctx, data, "mesh_0")

	// Wait for any scheduled relay to fire
	time.Sleep(3 * time.Second)

	mu.Lock()
	count := relayed
	mu.Unlock()

	if count != 0 {
		t.Errorf("local announce should not be relayed, got %d relays", count)
	}
}

func TestAnnounceRelay_MaxHops(t *testing.T) {
	config := DefaultRelayConfig()
	config.MaxHops = 2

	table := NewDestinationTable(nil)
	var relayed int
	var mu sync.Mutex
	relay := NewAnnounceRelay(config, table, func(data []byte, a *Announce) {
		mu.Lock()
		relayed++
		mu.Unlock()
	})

	remoteID := testIdentity(t)
	announce, _ := NewAnnounce(remoteID, nil)
	announce.HopCount = 2 // at max
	data := announce.Marshal()

	ctx := context.Background()
	result := relay.HandleAnnounce(ctx, data, "mesh_0")

	if !result {
		t.Fatal("announce at max hops should still be accepted (just not relayed)")
	}

	time.Sleep(3 * time.Second)

	mu.Lock()
	count := relayed
	mu.Unlock()

	if count != 0 {
		t.Errorf("announce at max hops should not be relayed, got %d", count)
	}
}

func TestAnnounceRelay_InvalidPacket(t *testing.T) {
	relay := NewAnnounceRelay(DefaultRelayConfig(), nil, nil)
	ctx := context.Background()

	if relay.HandleAnnounce(ctx, []byte{0x01, 0x02}, "mesh_0") {
		t.Fatal("invalid packet should be rejected")
	}
}

func TestAnnounceRelay_SeenCount(t *testing.T) {
	relay := NewAnnounceRelay(DefaultRelayConfig(), NewDestinationTable(nil), nil)

	if relay.SeenCount() != 0 {
		t.Fatal("initial seen count should be 0")
	}

	id := testIdentity(t)
	announce, _ := NewAnnounce(id, nil)
	data := announce.Marshal()

	ctx := context.Background()
	relay.HandleAnnounce(ctx, data, "mesh_0")

	if relay.SeenCount() != 1 {
		t.Errorf("seen count should be 1, got %d", relay.SeenCount())
	}
}

func TestAnnounceRelay_DelayVariance(t *testing.T) {
	config := DefaultRelayConfig()
	config.MinRelayDelay = 50 * time.Millisecond
	config.MaxRelayDelay = 200 * time.Millisecond

	table := NewDestinationTable(nil)
	var mu sync.Mutex
	var relayTimes []time.Time

	relay := NewAnnounceRelay(config, table, func(data []byte, a *Announce) {
		mu.Lock()
		relayTimes = append(relayTimes, time.Now())
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Send multiple announces from different identities
	start := time.Now()
	for i := 0; i < 5; i++ {
		remoteID := testIdentity(t)
		announce, _ := NewAnnounce(remoteID, nil)
		data := announce.Marshal()
		relay.HandleAnnounce(ctx, data, "mesh_0")
	}

	// Wait for all relays to fire
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(relayTimes) != 5 {
		t.Fatalf("expected 5 relays, got %d", len(relayTimes))
	}

	// Check that delays are within bounds
	for _, rt := range relayTimes {
		delay := rt.Sub(start)
		if delay < config.MinRelayDelay {
			t.Errorf("delay %v is less than min %v", delay, config.MinRelayDelay)
		}
		if delay > config.MaxRelayDelay+100*time.Millisecond { // small tolerance
			t.Errorf("delay %v exceeds max %v", delay, config.MaxRelayDelay)
		}
	}
}

func TestAnnounceRelay_SeenAgeBoundary(t *testing.T) {
	config := DefaultRelayConfig()
	config.DedupTTL = 100 * time.Millisecond // very short
	config.MaxDedupEntries = 100

	table := NewDestinationTable(nil)
	var relayed int
	var mu sync.Mutex
	relay := NewAnnounceRelay(config, table, func(data []byte, a *Announce) {
		mu.Lock()
		relayed++
		mu.Unlock()
	})

	ctx := context.Background()

	// First announce
	remoteID := testIdentity(t)
	announce, _ := NewAnnounce(remoteID, nil)
	data := announce.Marshal()

	if !relay.HandleAnnounce(ctx, data, "mesh_0") {
		t.Fatal("first announce should be accepted")
	}

	// Immediately re-send — should be deduped
	if relay.HandleAnnounce(ctx, data, "iridium_0") {
		t.Fatal("immediate re-send should be deduped")
	}

	// Wait for dedup to expire
	time.Sleep(150 * time.Millisecond)
	relay.prune()

	// Same announce should now be accepted again
	if !relay.HandleAnnounce(ctx, data, "mesh_0") {
		t.Fatal("announce should be accepted after dedup TTL expires")
	}
}

func TestAnnounceRelay_MaxDedupEntries(t *testing.T) {
	config := DefaultRelayConfig()
	config.MaxDedupEntries = 3
	config.DedupTTL = 1 * time.Hour // don't expire

	table := NewDestinationTable(nil)
	relay := NewAnnounceRelay(config, table, nil)

	ctx := context.Background()

	// Add 5 announces — exceeds max of 3
	for i := 0; i < 5; i++ {
		id := testIdentity(t)
		ann, _ := NewAnnounce(id, nil)
		relay.HandleAnnounce(ctx, ann.Marshal(), "mesh_0")
	}

	if relay.SeenCount() != 5 {
		t.Fatalf("before prune: seen count = %d, want 5", relay.SeenCount())
	}

	relay.prune()

	// Should be capped at MaxDedupEntries
	if relay.SeenCount() > config.MaxDedupEntries {
		t.Errorf("after prune: seen count = %d, want <= %d", relay.SeenCount(), config.MaxDedupEntries)
	}
}

func TestAnnounceRelay_Prune(t *testing.T) {
	config := DefaultRelayConfig()
	config.DedupTTL = 1 * time.Millisecond

	relay := NewAnnounceRelay(config, NewDestinationTable(nil), nil)

	id := testIdentity(t)
	announce, _ := NewAnnounce(id, nil)
	data := announce.Marshal()

	ctx := context.Background()
	relay.HandleAnnounce(ctx, data, "mesh_0")

	time.Sleep(5 * time.Millisecond)
	relay.prune()

	if relay.SeenCount() != 0 {
		t.Errorf("after prune with expired TTL, seen count should be 0, got %d", relay.SeenCount())
	}
}
