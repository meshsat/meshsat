package rnsstack

import (
	"path/filepath"
	"testing"
	"time"

	"meshsat/internal/lxmf"
)

// Beyond the per-minute limit an inbound message is neither filed nor handed
// to the access rules; the bucket refills with time. [MESHSAT-1715]
func TestInboundOverTheLimitIsNotFiledNorHandedOn(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "kit.db"))
	defer func() { _ = db.Close() }()
	now := time.Unix(1_800_000_000, 0)
	hooked := 0
	st := &Stack{cfg: Config{DB: db}, now: func() time.Time { return now }, inbound: newInboundLimit(3, now)}
	st.SetInboundHook(func(string, string) { hooked++ })
	var src [lxmf.DestLen]byte
	src[0] = 7
	for i := 0; i < 5; i++ {
		st.storeInbound(&lxmf.Message{Source: src, Content: []byte("flood")})
	}
	if n := countLXMF(t, db); n != 3 || hooked != 3 || st.inbound.droppedCount() != 2 {
		t.Fatalf("filed %d, hooked %d, dropped %d; want 3, 3, 2", n, hooked, st.inbound.droppedCount())
	}
	now = now.Add(20 * time.Second) // one token per 20 s at 3 a minute
	st.storeInbound(&lxmf.Message{Source: src, Content: []byte("later")})
	if n := countLXMF(t, db); n != 4 || hooked != 4 {
		t.Fatalf("after the refill: filed %d, hooked %d; want 4 and 4", n, hooked)
	}
}

func TestInboundLimitDefault(t *testing.T) {
	if b := newInboundLimit(0, time.Now()); b.capacity != defaultInboundPerMinute {
		t.Fatalf("capacity %v, want the default %d", b.capacity, defaultInboundPerMinute)
	}
}
