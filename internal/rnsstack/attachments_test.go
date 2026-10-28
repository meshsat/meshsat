package rnsstack

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"testing"

	"meshsat/internal/lxmf"
)

func attachedMsg(t *testing.T, hashByte byte, atts ...lxmf.Attachment) *lxmf.Message {
	t.Helper()
	fields, err := lxmf.WithAttachments(lxmf.MapOf(), atts...)
	if err != nil {
		t.Fatal(err)
	}
	m := &lxmf.Message{Content: []byte("listen to this"), Fields: fields}
	m.Source[0], m.Hash[0] = 9, hashByte
	return m
}

func lxmfRowIDs(t *testing.T, st *Stack) []int64 {
	t.Helper()
	rows, err := st.cfg.DB.Query(`SELECT id FROM messages WHERE portnum_name = 'LXMF' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// A received voice note and photo are filed under the message's row, in
// the message's order, and handed to the hook with the text; a message
// whose attachments break the cap keeps its text and nothing else; the
// store evicts its oldest rows past the configured size. [MESHSAT-1735]
func TestInboundAttachmentsAreFiledCappedAndEvicted(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "kit.db"))
	defer func() { _ = db.Close() }()
	st := &Stack{cfg: Config{DB: db, LXMF: LXMFConfig{AttachmentStoreBytes: 700}}}
	var hooked [][]lxmf.Attachment
	st.SetInboundHook(func(_, _ string, atts []lxmf.Attachment) { hooked = append(hooked, atts) })

	image := lxmf.Attachment{Kind: lxmf.AttachmentImage, Format: "webp", Data: bytes.Repeat([]byte{7}, 300)}
	audio := lxmf.Attachment{Kind: lxmf.AttachmentAudio, Mode: lxmf.AudioMode700C, Data: bytes.Repeat([]byte{8}, 200)}
	st.storeInbound(attachedMsg(t, 1, audio, image))
	ids := lxmfRowIDs(t, st)
	if len(ids) != 1 {
		t.Fatalf("filed %d messages", len(ids))
	}
	list, err := db.ListLXMFAttachments(ids[0])
	if err != nil || len(list) != 2 || list[0].Kind != "image" || list[0].Format != "webp" || list[1].Kind != "audio" || list[1].Mode != 3 {
		t.Fatalf("filed %+v %v", list, err)
	}
	if _, data, err := db.GetLXMFAttachment(ids[0], 1); err != nil || !bytes.Equal(data, audio.Data) {
		t.Fatalf("audio bytes: %v", err)
	}
	if len(hooked) != 1 || len(hooked[0]) != 2 || hooked[0][0].Kind != "image" {
		t.Fatalf("hook got %+v", hooked)
	}

	// Over the cap: the text is filed, the attachments are not.
	big := lxmf.Attachment{Kind: lxmf.AttachmentImage, Format: "webp", Data: make([]byte, lxmf.MaxAttachmentBytes+1)}
	m := &lxmf.Message{Content: []byte("too big"), Fields: lxmf.MapOf(lxmf.KV{Key: lxmf.Int(lxmf.FieldImage), Val: lxmf.Array(lxmf.Str("webp"), lxmf.Bin(big.Data))})}
	m.Source[0], m.Hash[0] = 9, 2
	st.storeInbound(m)
	ids = lxmfRowIDs(t, st)
	if len(ids) != 2 {
		t.Fatalf("filed %d messages", len(ids))
	}
	if list, _ := db.ListLXMFAttachments(ids[1]); len(list) != 0 {
		t.Fatalf("over the cap and still filed: %+v", list)
	}
	if len(hooked) != 2 || len(hooked[1]) != 0 {
		t.Fatalf("hook got %+v", hooked)
	}

	// A third message's 300 B pushes the store (500 + 300) past 700: the
	// first message's oldest rows go, newest first is kept.
	st.storeInbound(attachedMsg(t, 3, lxmf.Attachment{Kind: lxmf.AttachmentAudio, Mode: 3, Data: bytes.Repeat([]byte{9}, 300)}))
	ids = lxmfRowIDs(t, st)
	if n, _ := db.LXMFAttachmentBytes(); n > 700 {
		t.Fatalf("store holds %d bytes", n)
	}
	if list, _ := db.ListLXMFAttachments(ids[2]); len(list) != 1 {
		t.Fatalf("the newest message lost its attachment: %+v", list)
	}
	if list, _ := db.ListLXMFAttachments(ids[0]); len(list) != 1 || list[0].Kind != "audio" {
		t.Fatalf("the oldest image should have gone first: %+v", list)
	}
}

// The ledger body's voice note and photo decode in the message's order;
// bad base64 is refused.
func TestLXMFBodyAttachments(t *testing.T) {
	b := LXMFBody{Content: "x", Audio: &LXMFAudio{Mode: 3, DataB64: base64.StdEncoding.EncodeToString([]byte("frames"))},
		Image: &LXMFImage{Format: " WebP ", DataB64: base64.StdEncoding.EncodeToString([]byte("pixels"))}}
	atts, err := b.Attachments()
	if err != nil || len(atts) != 2 || atts[0].Kind != "image" || atts[0].Format != "webp" || string(atts[0].Data) != "pixels" ||
		atts[1].Kind != "audio" || atts[1].Mode != 3 || string(atts[1].Data) != "frames" {
		t.Fatalf("%+v %v", atts, err)
	}
	if atts, err := (LXMFBody{Content: "x"}).Attachments(); err != nil || len(atts) != 0 {
		t.Fatalf("no attachments: %+v %v", atts, err)
	}
	if _, err := (LXMFBody{Audio: &LXMFAudio{Mode: 3, DataB64: "***"}}).Attachments(); err == nil {
		t.Fatal("bad base64 accepted")
	}
	if _, err := (LXMFBody{Image: &LXMFImage{Format: "webp", DataB64: "***"}}).Attachments(); err == nil {
		t.Fatal("bad base64 accepted")
	}
}
