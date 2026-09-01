package database

import (
	"errors"
	"testing"
)

func TestOOBSchemaVersion(t *testing.T) {
	db := testDB(t)
	var v int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v < 54 {
		t.Fatalf("schema version %d, want >= 54", v)
	}
}

func TestOOBPeerCRUD(t *testing.T) {
	db := testDB(t)

	p := &OOBPeer{PeerID: 0x94cb, Alias: "parallax", KeyRef: "mgmt:parallax", KeySource: "ecdh", LocalRole: 1,
		Role: "control", Enabled: true, Addresses: `{"cellular_0":"+31653207829","aprs_0":"PD0XYZ-7"}`, EncPolicy: `{"aprs_0":false}`}
	if err := db.InsertOOBPeer(p); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.InsertOOBPeer(&OOBPeer{PeerID: 0, Alias: "zero", KeyRef: "mgmt:zero"}); err == nil {
		t.Fatal("peer id 0 accepted")
	}
	if err := db.InsertOOBPeer(&OOBPeer{PeerID: 0x94cb, Alias: "dup", KeyRef: "mgmt:dup"}); err == nil {
		t.Fatal("duplicate peer id accepted")
	}

	got, err := db.GetOOBPeer(0x94cb)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Alias != "parallax" || got.KeySource != "ecdh" || got.LocalRole != 1 || got.Role != "control" || !got.Enabled ||
		got.Addresses != p.Addresses || got.EncPolicy != p.EncPolicy || got.TxCounter != 0 || got.RxHigh != 0 || got.RxWindow != 0 {
		t.Fatalf("round trip: %+v", got)
	}
	if byAlias, err := db.GetOOBPeerByAlias("parallax"); err != nil || byAlias.PeerID != 0x94cb {
		t.Fatalf("by alias: %+v %v", byAlias, err)
	}
	if _, err := db.GetOOBPeer(1); !errors.Is(err, ErrOOBPeerNotFound) {
		t.Fatalf("missing peer: %v", err)
	}

	// Defaults on a minimal insert.
	if err := db.InsertOOBPeer(&OOBPeer{PeerID: 7, Alias: "phone", KeyRef: "mgmt:phone"}); err != nil {
		t.Fatal(err)
	}
	minimal, _ := db.GetOOBPeer(7)
	if minimal.Role != "readonly" || minimal.KeySource != "bundle" || minimal.Addresses != "{}" || minimal.EncPolicy != "{}" {
		t.Fatalf("defaults: %+v", minimal)
	}

	list, err := db.ListOOBPeers()
	if err != nil || len(list) != 2 || list[0].Alias != "parallax" || list[1].Alias != "phone" {
		t.Fatalf("list: %+v %v", list, err)
	}

	got.Role = "readonly"
	got.Enabled = false
	got.Addresses = `{"cellular_0":"+31653207829"}`
	if err := db.UpdateOOBPeer(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := db.GetOOBPeer(0x94cb)
	if again.Role != "readonly" || again.Enabled || again.Addresses != got.Addresses {
		t.Fatalf("after update: %+v", again)
	}
	if err := db.UpdateOOBPeer(&OOBPeer{PeerID: 999}); !errors.Is(err, ErrOOBPeerNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	if err := db.DeleteOOBPeer(7); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.DeleteOOBPeer(7); !errors.Is(err, ErrOOBPeerNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestOOBCounters(t *testing.T) {
	db := testDB(t)
	if err := db.InsertOOBPeer(&OOBPeer{PeerID: 10, Alias: "a", KeyRef: "mgmt:a"}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertOOBPeer(&OOBPeer{PeerID: 11, Alias: "b", KeyRef: "mgmt:b"}); err != nil {
		t.Fatal(err)
	}

	t.Run("next_tx_counter_monotonic", func(t *testing.T) {
		var last uint32
		for i := 1; i <= 5; i++ {
			n, err := db.NextOOBTxCounter(10)
			if err != nil {
				t.Fatal(err)
			}
			if n != uint32(i) || n <= last {
				t.Fatalf("counter %d at step %d", n, i)
			}
			last = n
		}
		if _, err := db.NextOOBTxCounter(999); !errors.Is(err, ErrOOBPeerNotFound) {
			t.Fatalf("missing: %v", err)
		}
	})

	t.Run("bump_tx_counters_only_used_peers", func(t *testing.T) {
		if err := db.BumpOOBTxCounters(1 << 16); err != nil {
			t.Fatal(err)
		}
		a, _ := db.GetOOBPeer(10)
		b, _ := db.GetOOBPeer(11)
		if a.TxCounter != 5+(1<<16) {
			t.Fatalf("a bumped to %d", a.TxCounter)
		}
		if b.TxCounter != 0 {
			t.Fatalf("unused peer bumped to %d", b.TxCounter)
		}
	})

	t.Run("rx_window_persist_full_64_bits", func(t *testing.T) {
		const window = uint64(0x8000_0000_0000_0001) // high bit set: int64 bit pattern is negative
		if err := db.SaveOOBRxWindow(11, 4_000_000_000, window); err != nil {
			t.Fatal(err)
		}
		b, _ := db.GetOOBPeer(11)
		if b.RxHigh != 4_000_000_000 || b.RxWindow != window {
			t.Fatalf("window round trip: high=%d window=%x", b.RxHigh, b.RxWindow)
		}
		if b.LastSeenAt == nil || *b.LastSeenAt == "" {
			t.Fatal("last_seen_at not set")
		}
		if err := db.SaveOOBRxWindow(999, 1, 1); !errors.Is(err, ErrOOBPeerNotFound) {
			t.Fatalf("missing: %v", err)
		}
	})
}

func TestOOBLog(t *testing.T) {
	db := testDB(t)
	delID := int64(42)
	entries := []OOBLogEntry{
		{PeerID: 10, Direction: "in", Kind: "request", Bearer: "cellular_0", FromAddr: "+316", Cmd: 1, Counter: 1, Result: "ok"},
		{PeerID: 10, Direction: "out", Kind: "reply", Bearer: "cellular_0", FromAddr: "+316", Cmd: 1, Counter: 65537, Result: "ok", DeliveryID: &delID},
		{PeerID: 0, Direction: "in", Kind: "reject", Bearer: "aprs_0", Cmd: 0, Counter: 0, Result: "unknown_peer", Detail: `{"peer":99}`},
	}
	for i := range entries {
		id, err := db.InsertOOBLog(&entries[i])
		if err != nil || id == 0 {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	all, err := db.ListOOBLog(10, -1)
	if err != nil || len(all) != 3 {
		t.Fatalf("list all: %d %v", len(all), err)
	}
	if all[0].Kind != "reject" || all[2].Kind != "request" {
		t.Fatalf("order newest first: %+v", all)
	}
	if all[1].DeliveryID == nil || *all[1].DeliveryID != 42 || all[1].Counter != 65537 {
		t.Fatalf("reply row: %+v", all[1])
	}

	peer, err := db.ListOOBLog(10, 10)
	if err != nil || len(peer) != 2 {
		t.Fatalf("list peer 10: %d %v", len(peer), err)
	}
	if hub, _ := db.ListOOBLog(10, 0); len(hub) != 1 || hub[0].Result != "unknown_peer" {
		t.Fatalf("list peer 0: %+v", hub)
	}
	if limited, _ := db.ListOOBLog(1, -1); len(limited) != 1 {
		t.Fatalf("limit: %d", len(limited))
	}

	if err := db.PruneOOBLog(1); err != nil {
		t.Fatal(err)
	}
	if left, _ := db.ListOOBLog(10, -1); len(left) != 1 || left[0].Kind != "reject" {
		t.Fatalf("after prune: %+v", left)
	}
}

func TestDeliveryDestinationAndClass(t *testing.T) {
	db := testDB(t)

	// Default class on an ordinary insert.
	plainID, err := db.InsertDelivery(MessageDelivery{MsgRef: "m1", Channel: "cellular_0", Status: "queued", Payload: []byte("hello"), TextPreview: "hello", MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := db.GetDelivery(plainID)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Class != DeliveryClassMessage || plain.Destination != "" {
		t.Fatalf("defaults: class=%q destination=%q", plain.Class, plain.Destination)
	}

	// OOB row with a destination, read back through every full-column query.
	oobID, err := db.InsertDelivery(MessageDelivery{MsgRef: "m2", Channel: "cellular_0", Status: "queued", Payload: []byte("MS:frame"),
		TextPreview: "MS:frame", MaxRetries: 5, Destination: "+31653207829", Class: DeliveryClassOOB})
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, d *MessageDelivery) {
		t.Helper()
		if d == nil {
			t.Fatalf("%s: oob row missing", name)
		}
		if d.Class != DeliveryClassOOB || d.Destination != "+31653207829" {
			t.Fatalf("%s: class=%q destination=%q", name, d.Class, d.Destination)
		}
	}
	got, err := db.GetDelivery(oobID)
	if err != nil {
		t.Fatal(err)
	}
	check("GetDelivery", got)

	list, err := db.GetDeliveries(DeliveryFilter{MsgRef: "m2"})
	if err != nil || len(list) != 1 {
		t.Fatalf("GetDeliveries: %d %v", len(list), err)
	}
	check("GetDeliveries", &list[0])

	pending, err := db.GetPendingDeliveries("cellular_0", 10)
	if err != nil {
		t.Fatal(err)
	}
	var found *MessageDelivery
	for i := range pending {
		if pending[i].ID == oobID {
			found = &pending[i]
		}
	}
	check("GetPendingDeliveries", found)

	if err := db.SetDeliveryAck(oobID, "pending"); err != nil {
		t.Fatal(err)
	}
	acks, err := db.GetPendingAcks("cellular_0", 0)
	if err != nil {
		t.Fatal(err)
	}
	found = nil
	for i := range acks {
		if acks[i].ID == oobID {
			found = &acks[i]
		}
	}
	check("GetPendingAcks", found)
}
