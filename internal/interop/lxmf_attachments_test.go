package interop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/lxmf"
	"meshsat/internal/rnsstack"
	"meshsat/internal/routing"
)

// The smallest valid WebP: a 1x1 lossless (VP8L) image, 34 bytes.
var interopWebP = []byte{
	0x52, 0x49, 0x46, 0x46, 0x1a, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50, 0x56, 0x50, 0x38, 0x4c,
	0x0d, 0x00, 0x00, 0x00, 0x2f, 0x00, 0x00, 0x00, 0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe, 0x07, 0x00,
}

// A voice note and a photo cross upstream LXMF both ways as FIELD_AUDIO
// [mode, frames] and FIELD_IMAGE ["webp", bytes], the shapes Sideband and
// MeshChat send: stock Python LXMF sends them to the kit's stack, which
// files both under the message and hands them to the rules hook; the kit's
// ledger sender sends both back and Python reads them intact. The
// attachments outgrow one packet, so each way is an RNS resource on a link
// with the fields inside it. [MESHSAT-1735]
func TestInteropLXMFAttachmentsBothWays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	p := startPeer(t, peerOpts{name: "Sideband", listen: port, loglevel: 4})

	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := routing.NewInterfaceRegistry()
	coded := routing.NewCodedIfaces()
	sink := &nodeSink{}
	mgr := routing.NewIfaceManager(routing.IfaceManagerConfig{DB: db, Registry: reg, Coded: coded, Sink: sink})
	if err := mgr.Load(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.StopAll()
	rowID, err := mgr.Create(routing.DynTypeTCPRNS, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d}`, port)), true)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := routing.NewIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	st, err := rnsstack.Build(ctx, rnsstack.Config{Identity: ident, Registry: reg, Coded: coded, DB: db, AcceptLinks: true,
		LXMF: rnsstack.LXMFConfig{Enabled: true, DisplayName: "kit"}})
	if err != nil {
		t.Fatal(err)
	}
	sink.node.Store(st.Node)
	hooked := make(chan []lxmf.Attachment, 4)
	st.SetInboundHook(func(_, _ string, atts []lxmf.Attachment) { hooked <- atts })
	kitLXMF := st.LXMF.HashHex()

	waitFor(t, "row up", 10*time.Second, func() bool { return reg.Get(rowID) != nil && reg.Get(rowID).IsOnline() })
	if err := st.LXMF.Announce(); err != nil {
		t.Fatal(err)
	}
	p.wait("announce", func(e event) bool { return e["dest"] == kitLXMF }, 20*time.Second)
	p.send(event{"cmd": "announce"})
	waitFor(t, "kit learns python", 15*time.Second, func() bool { return st.Node.Paths().Hops(h16(p.Delivery)) == 1 })

	// Python -> kit: 600 bytes of fake Codec2 700C frames and the WebP, in
	// the fields map as umsgpack packs it.
	frames := make([]byte, 600)
	for i := range frames {
		frames[i] = byte(i * 7)
	}
	fields, err := lxmf.WithAttachments(lxmf.MapOf(), lxmf.Attachment{Kind: lxmf.AttachmentAudio, Mode: lxmf.AudioMode700C, Data: frames},
		lxmf.Attachment{Kind: lxmf.AttachmentImage, Format: lxmf.ImageWebP, Data: interopWebP})
	if err != nil {
		t.Fatal(err)
	}
	p.send(event{"cmd": "send_lxm", "dest": kitLXMF, "content": "voice note", "title": "from python", "method": "direct",
		"fields_hex": hex.EncodeToString(lxmf.Pack(fields))})
	sent := p.wait("lxm_sent", nil, 10*time.Second)
	var got []lxmf.Attachment
	select {
	case got = <-hooked:
	case <-time.After(30 * time.Second):
		t.Fatal("the kit did not take the message in")
	}
	if len(got) != 2 || got[0].Kind != lxmf.AttachmentImage || !bytes.Equal(got[0].Data, interopWebP) ||
		got[1].Kind != lxmf.AttachmentAudio || got[1].Mode != lxmf.AudioMode700C || !bytes.Equal(got[1].Data, frames) {
		t.Fatalf("hook got %+v", got)
	}
	p.wait("lxm_delivered", nil, 30*time.Second)
	// Filed under the message, readable by the LXMF hash as the API does.
	msgID, err := db.LXMFMessageIDByHash(sent["hash"].(string))
	if err != nil {
		t.Fatalf("by hash: %v", err)
	}
	list, err := db.ListLXMFAttachments(msgID)
	if err != nil || len(list) != 2 || list[0].Kind != "image" || list[0].Format != "webp" || list[0].Size != len(interopWebP) || list[1].Mode != 3 || list[1].Size != 600 {
		t.Fatalf("filed %+v %v", list, err)
	}
	if _, data, err := db.GetLXMFAttachment(msgID, 1); err != nil || !bytes.Equal(data, frames) {
		t.Fatalf("frames: %v", err)
	}
	var text string
	if err := db.QueryRow(`SELECT decoded_text FROM messages WHERE id = ?`, msgID).Scan(&text); err != nil || text != "from python\nvoice note" {
		t.Fatalf("text %q %v", text, err)
	}

	// Kit -> Python through the ledger sender, the API's body shape.
	body, _ := json.Marshal(rnsstack.LXMFBody{Content: "reply", Title: "kit",
		Audio: &rnsstack.LXMFAudio{Mode: lxmf.AudioMode700C, DataB64: base64.StdEncoding.EncodeToString(frames)},
		Image: &rnsstack.LXMFImage{Format: "webp", DataB64: base64.StdEncoding.EncodeToString(interopWebP)}})
	sctx, scancel := context.WithTimeout(ctx, 60*time.Second)
	defer scancel()
	hash, err := st.Deliver(sctx, database.MessageDelivery{Destination: p.Delivery, Payload: body, TextPreview: "reply"})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	rcv := p.wait("lxm_received", nil, 30*time.Second)
	if rcv["hash"] != hash || rcv["content"] != "reply" || rcv["title"] != "kit" || rcv["signature_validated"] != true {
		t.Fatalf("python got %v", rcv)
	}
	raw, _ := hex.DecodeString(rcv["fields_hex"].(string))
	v, _, err := lxmf.UnpackValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	back := lxmf.AttachmentsOf(v)
	if len(back) != 2 || back[0].Kind != lxmf.AttachmentImage || back[0].Format != "webp" || !bytes.Equal(back[0].Data, interopWebP) ||
		back[1].Kind != lxmf.AttachmentAudio || back[1].Mode != lxmf.AudioMode700C || !bytes.Equal(back[1].Data, frames) {
		t.Fatalf("python read %+v", back)
	}
	if _, ok := lxmf.MidOf(v); !ok {
		t.Fatal("the mid did not travel beside the attachments")
	}
	// The sent message's attachments are filed too.
	outID, err := db.LXMFMessageIDByHash(hash)
	if err != nil {
		t.Fatalf("sent by hash: %v", err)
	}
	if list, _ := db.ListLXMFAttachments(outID); len(list) != 2 {
		t.Fatalf("sent attachments %+v", list)
	}
}
