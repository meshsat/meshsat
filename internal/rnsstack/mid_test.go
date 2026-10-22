package rnsstack

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"

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

func openDB(t *testing.T, path string) *database.DB {
	t.Helper()
	db, err := database.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

var testMid, _ = lxmf.ParseMid("01a14f31-6680-7a0b-8c0d-0e0f10111213")

func midMsg(from [lxmf.DestLen]byte, hashByte byte) *lxmf.Message {
	m := &lxmf.Message{Source: from, Content: []byte("one message"), Fields: lxmf.MidFields(testMid)}
	m.Hash[0] = hashByte
	return m
}

// A message that comes again with the same mid, under another LXMF hash
// (a retry) and after a restart, is filed and handed on once; the same mid
// from another sender is another message. [MESHSAT-1641]
func TestInboundRepeatOfAMidIsDroppedAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.db")
	db := openDB(t, path)
	hooked := 0
	st := &Stack{cfg: Config{DB: db}}
	st.SetInboundHook(func(string, string) { hooked++ })
	var src, other [lxmf.DestLen]byte
	src[0], other[0] = 1, 2
	st.storeInbound(midMsg(src, 1))
	st.storeInbound(midMsg(src, 2))
	if n := countLXMF(t, db); n != 1 || hooked != 1 {
		t.Fatalf("filed %d, hooked %d, want 1 and 1", n, hooked)
	}
	_ = db.Close()

	db = openDB(t, path)
	defer func() { _ = db.Close() }()
	st = &Stack{cfg: Config{DB: db}}
	st.SetInboundHook(func(string, string) { hooked++ })
	st.storeInbound(midMsg(src, 3))
	if n := countLXMF(t, db); n != 1 || hooked != 1 {
		t.Fatalf("after a restart: filed %d, hooked %d, want 1 and 1", n, hooked)
	}
	st.storeInbound(midMsg(other, 4))
	// A message with no mid is never held back.
	st.storeInbound(&lxmf.Message{Source: src, Content: []byte("no id")})
	st.storeInbound(&lxmf.Message{Source: src, Content: []byte("no id")})
	if n := countLXMF(t, db); n != 4 || hooked != 4 {
		t.Fatalf("filed %d, hooked %d, want 4 and 4", n, hooked)
	}
}

// The r40 review's counterexample (R40-R2): the inbox write fails, the kit
// restarts, the message comes again with a new LXMF hash. It must be filed
// then, exactly once, and nothing is handed on while it is not filed.
func TestReviewR40RetryAfterInboxInsertFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.db")
	db := openDB(t, path)
	hooked := 0
	st := &Stack{cfg: Config{DB: db}}
	st.SetInboundHook(func(string, string) { hooked++ })
	if _, err := db.Exec(`CREATE TRIGGER r40_fail BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'test: inbox write fails'); END;`); err != nil {
		t.Fatal(err)
	}
	var src [lxmf.DestLen]byte
	src[0] = 7
	st.storeInbound(midMsg(src, 1))
	var claims int
	_ = db.QueryRow(`SELECT COUNT(*) FROM lxmf_mids`).Scan(&claims)
	if claims != 0 || countLXMF(t, db) != 0 || hooked != 0 {
		t.Fatalf("after the failed write: claims %d, inbox %d, hooked %d; want 0, 0, 0", claims, countLXMF(t, db), hooked)
	}
	if _, err := db.Exec(`DROP TRIGGER r40_fail`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db = openDB(t, path)
	defer func() { _ = db.Close() }()
	st = &Stack{cfg: Config{DB: db}}
	st.SetInboundHook(func(string, string) { hooked++ })
	st.storeInbound(midMsg(src, 2))
	st.storeInbound(midMsg(src, 3))
	if n := countLXMF(t, db); n != 1 || hooked != 1 {
		t.Fatalf("after the restart and the retries: inbox %d, hooked %d; want 1 and 1", n, hooked)
	}
}

// Deliveries of one message at the same time file it once and hand it on once.
func TestInboundConcurrentRepeatsFileOnce(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "kit.db"))
	defer func() { _ = db.Close() }()
	var mu sync.Mutex
	hooked := 0
	st := &Stack{cfg: Config{DB: db}}
	st.SetInboundHook(func(string, string) { mu.Lock(); hooked++; mu.Unlock() })
	var src [lxmf.DestLen]byte
	src[0] = 9
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); st.storeInbound(midMsg(src, byte(i))) }(i)
	}
	wg.Wait()
	if n := countLXMF(t, db); n != 1 || hooked != 1 {
		t.Fatalf("filed %d, hooked %d, want 1 and 1", n, hooked)
	}
}

// An intent's attempts carry its id; a row of no intent the UUIDv7 it was
// given once, the same on every retry and after a restart (r40 review,
// decision 2).
func TestDeliveryMid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.db")
	db := openDB(t, path)
	st := &Stack{cfg: Config{DB: db}}
	intent := database.MessageDelivery{ID: 7, IntentID: "01a14f31-6680-7a0b-8c0d-0e0f10111213"}
	if m, err := st.deliveryMid(intent); err != nil || lxmf.FormatMid(m) != intent.IntentID {
		t.Fatalf("intent mid %s %v", lxmf.FormatMid(m), err)
	}
	id, err := db.InsertDelivery(database.MessageDelivery{Channel: "lxmf_0", Status: "queued", Class: database.DeliveryClassLXMF, Destination: "00112233445566778899aabbccddeeff", TextPreview: "x"})
	if err != nil {
		t.Fatal(err)
	}
	row := database.MessageDelivery{ID: id}
	a, err := st.deliveryMid(row)
	if err != nil {
		t.Fatal(err)
	}
	if u := uuid.UUID(a); u.Version() != 7 {
		t.Fatalf("%s is not a UUIDv7", lxmf.FormatMid(a))
	}
	if b, _ := st.deliveryMid(row); b != a {
		t.Fatal("a retry of the row got another mid")
	}
	_ = db.Close()
	db = openDB(t, path)
	defer func() { _ = db.Close() }()
	st = &Stack{cfg: Config{DB: db}}
	if c, _ := st.deliveryMid(row); c != a {
		t.Fatal("after a restart the row's mid changed")
	}
}
