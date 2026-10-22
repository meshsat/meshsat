package rnsstack

import (
	"path/filepath"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/lxmf"
)

func countLXMF(t *testing.T, db *database.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE portnum_name = 'LXMF' AND direction = 'rx'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A message that comes again with the same mid, under another LXMF hash
// (a retry) and after a restart, is filed and handed on once; the same mid
// from another sender is another message. [MESHSAT-1641]
func TestInboundRepeatOfAMidIsDroppedAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.db")
	db, err := database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	hooked := 0
	st := &Stack{cfg: Config{DB: db}}
	st.SetInboundHook(func(string, string) { hooked++ })
	mid, _ := lxmf.ParseMid("01a14f31-6680-7a0b-8c0d-0e0f10111213")
	var src, other [lxmf.DestLen]byte
	src[0], other[0] = 1, 2
	msg := func(from [lxmf.DestLen]byte, hashByte byte) *lxmf.Message {
		m := &lxmf.Message{Source: from, Content: []byte("one message"), Fields: lxmf.MidFields(mid)}
		m.Hash[0] = hashByte
		return m
	}
	st.storeInbound(msg(src, 1))
	st.storeInbound(msg(src, 2))
	if n := countLXMF(t, db); n != 1 || hooked != 1 {
		t.Fatalf("filed %d, hooked %d, want 1 and 1", n, hooked)
	}
	_ = db.Close()

	db, err = database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	st = &Stack{cfg: Config{DB: db}}
	st.SetInboundHook(func(string, string) { hooked++ })
	st.storeInbound(msg(src, 3))
	if n := countLXMF(t, db); n != 1 || hooked != 1 {
		t.Fatalf("after a restart: filed %d, hooked %d, want 1 and 1", n, hooked)
	}
	st.storeInbound(msg(other, 4))
	// A message with no mid is never held back.
	st.storeInbound(&lxmf.Message{Source: src, Content: []byte("no id")})
	st.storeInbound(&lxmf.Message{Source: src, Content: []byte("no id")})
	if n := countLXMF(t, db); n != 4 || hooked != 4 {
		t.Fatalf("filed %d, hooked %d, want 4 and 4", n, hooked)
	}
}

// An intent's attempts carry its id; a row of no intent gets a UUIDv7 of
// its own that every retry of the row repeats.
func TestDeliveryMid(t *testing.T) {
	var self, other [lxmf.DestLen]byte
	other[0] = 9
	intent := database.MessageDelivery{ID: 7, IntentID: "01a14f31-6680-7a0b-8c0d-0e0f10111213", CreatedAt: "2026-10-22 18:00:00"}
	if got := lxmf.FormatMid(deliveryMid(intent, self)); got != intent.IntentID {
		t.Fatalf("intent mid %s", got)
	}
	row := database.MessageDelivery{ID: 7, CreatedAt: "2026-10-22 18:00:00"}
	a, b := deliveryMid(row, self), deliveryMid(row, self)
	if a != b {
		t.Fatal("a row's retries got two mids")
	}
	if a[6]>>4 != 7 || a[8]>>6 != 2 {
		t.Fatalf("not a UUIDv7: %s", lxmf.FormatMid(a))
	}
	if ms := uint64(a[0])<<40 | uint64(a[1])<<32 | uint64(a[2])<<24 | uint64(a[3])<<16 | uint64(a[4])<<8 | uint64(a[5]); ms != 1792692000000 {
		t.Fatalf("time %d", ms)
	}
	row8 := row
	row8.ID = 8
	if deliveryMid(row8, self) == a || deliveryMid(row, other) == a {
		t.Fatal("two rows, or two nodes, share a mid")
	}
}
