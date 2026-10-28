package database

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func openAttachmentDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "kit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func fileLXMF(t *testing.T, db *DB, hash string) int64 {
	t.Helper()
	id, err := db.InsertMessageWithStatus(&Message{FromNode: "lxmf:aa", ToNode: "lxmf:bb", PortNum: 1, PortNumName: "LXMF", DecodedText: "note", Direction: "rx", Transport: "reticulum"}, "received")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Attachments are filed under the message row in order, read back by index
// or through the LXMF hash, a second filing changes nothing, and eviction
// drops the oldest until the cap holds. [MESHSAT-1735]
func TestLXMFAttachmentsRoundTripAndEviction(t *testing.T) {
	db := openAttachmentDB(t)
	first := fileLXMF(t, db, "h1")
	atts := []LXMFAttachment{{Kind: "image", Format: "webp"}, {Kind: "audio", Mode: 3}}
	data := [][]byte{bytes.Repeat([]byte{1}, 300), bytes.Repeat([]byte{2}, 200)}
	if err := db.InsertLXMFAttachments(first, "h1", atts, data); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertLXMFAttachments(first, "h1", atts[:1], [][]byte{[]byte("replaced?")}); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListLXMFAttachments(first)
	if err != nil || len(list) != 2 || list[0].Kind != "image" || list[0].Format != "webp" || list[0].Size != 300 ||
		list[1].Kind != "audio" || list[1].Mode != 3 || list[1].Index != 1 || list[0].LXMFHash != "h1" || list[0].ReceivedAt == "" {
		t.Fatalf("list %+v %v", list, err)
	}
	a, got, err := db.GetLXMFAttachment(first, 0)
	if err != nil || a.Kind != "image" || !bytes.Equal(got, data[0]) {
		t.Fatalf("get: %+v %d %v", a, len(got), err)
	}
	if _, _, err := db.GetLXMFAttachment(first, 2); !errors.Is(err, ErrNoAttachment) {
		t.Fatalf("index 2: %v", err)
	}
	if id, err := db.LXMFMessageIDByHash("h1"); err != nil || id != first {
		t.Fatalf("by hash: %d %v", id, err)
	}
	if _, err := db.LXMFMessageIDByHash("h9"); !errors.Is(err, ErrNoAttachment) {
		t.Fatalf("unknown hash: %v", err)
	}
	if n, _ := db.LXMFAttachmentBytes(); n != 500 {
		t.Fatalf("bytes %d", n)
	}
	if err := db.InsertLXMFAttachments(first, "h1", atts[:1], nil); err == nil {
		t.Fatal("mismatched data accepted")
	}
	if err := db.InsertLXMFAttachments(first, "h1", nil, nil); err != nil {
		t.Fatal(err)
	}

	// Two newer messages; a cap of 450 keeps the newest two attachments
	// (200 + 200) and drops the first message's both.
	second := fileLXMF(t, db, "h2")
	if err := db.InsertLXMFAttachments(second, "h2", atts[1:], [][]byte{bytes.Repeat([]byte{3}, 200)}); err != nil {
		t.Fatal(err)
	}
	third := fileLXMF(t, db, "h3")
	if err := db.InsertLXMFAttachments(third, "h3", atts[1:], [][]byte{bytes.Repeat([]byte{4}, 200)}); err != nil {
		t.Fatal(err)
	}
	if n, err := db.EvictLXMFAttachments(0); err != nil || n != 0 {
		t.Fatalf("cap 0 evicted %d %v", n, err)
	}
	gone, err := db.EvictLXMFAttachments(450)
	if err != nil || gone != 2 {
		t.Fatalf("evicted %d %v", gone, err)
	}
	if left, _ := db.ListLXMFAttachments(first); len(left) != 0 {
		t.Fatalf("first message kept %d", len(left))
	}
	if n, _ := db.LXMFAttachmentBytes(); n != 400 {
		t.Fatalf("after eviction %d", n)
	}
	if gone, _ := db.EvictLXMFAttachments(450); gone != 0 {
		t.Fatal("eviction under the cap removed rows")
	}
}
