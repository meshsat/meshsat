package lxmf_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"meshsat/internal/interop/rnsenv"
	"meshsat/internal/lxmf"
	"meshsat/internal/reticulum"
)

func TestGolden_MsgpackMatchesUmsgpack(t *testing.T) {
	v := lxmf.Array(
		lxmf.Float(1758814000.123), lxmf.Bin([]byte("title")), lxmf.Bin(bytes.Repeat([]byte("x"), 300)),
		lxmf.MapOf(lxmf.KV{Key: lxmf.Int(1), Val: lxmf.Str("hello")}, lxmf.KV{Key: lxmf.Int(0x10), Val: lxmf.Array(lxmf.Int(-5), lxmf.Int(200), lxmf.Int(70000), lxmf.Int(-200), lxmf.Bool(true), lxmf.Nil())}),
		lxmf.Int(5000000000), lxmf.Int(-5000000000), lxmf.Str(string(bytes.Repeat([]byte("s"), 40))),
	)
	got := lxmf.Pack(v)
	var res struct {
		Packed string `json:"packed"`
		Same   bool   `json:"same"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json
import RNS.vendor.umsgpack as m
b = bytes.fromhex(sys.argv[1])
v = m.unpackb(b)
print(json.dumps({"packed": m.packb(v).hex(), "same": m.packb(v) == b}))
`, hex.EncodeToString(got))
	if !res.Same {
		t.Fatalf("umsgpack re-encodes differently:\n go %x\n py %s", got, res.Packed)
	}
	back, n, err := lxmf.UnpackValue(got)
	if err != nil || n != len(got) || !bytes.Equal(lxmf.Pack(back), got) {
		t.Fatalf("Go round trip failed: %v", err)
	}
}

func TestGolden_MessagePackedVerifiedByPython(t *testing.T) {
	src, _ := reticulum.GenerateIdentity()
	dst, _ := reticulum.GenerateIdentity()
	m := &lxmf.Message{Dest: dst.DestHash(lxmf.DeliveryName), Source: src.DestHash(lxmf.DeliveryName),
		Title: []byte("hi"), Content: []byte("hello from the bridge"), Fields: lxmf.MapOf()}
	if err := m.Pack(src, 0, nil); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Hash   string `json:"hash"`
		Valid  bool   `json:"valid"`
		Cont   string `json:"content"`
		Packed string `json:"packed"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS, LXMF
RNS.loglevel = 0
src = RNS.Identity.from_bytes(bytes.fromhex(sys.argv[1])); dst = RNS.Identity.from_bytes(bytes.fromhex(sys.argv[2]))
# make the identities recallable
for i in (src, dst):
    RNS.Identity.known_destinations[RNS.Destination.hash_from_name_and_identity("lxmf.delivery", i)] = [0, None, i.get_public_key(), None]
packed = bytes.fromhex(sys.argv[3])
lxm = LXMF.LXMessage.unpack_from_bytes(packed)
# python-packed reply from dst to src
d_src = RNS.Destination(src, RNS.Destination.OUT, RNS.Destination.SINGLE, "lxmf", "delivery")
d_dst = RNS.Destination(dst, RNS.Destination.OUT, RNS.Destination.SINGLE, "lxmf", "delivery")
reply = LXMF.LXMessage(d_src, d_dst, b"reply from python", title=b"re", desired_method=LXMF.LXMessage.OPPORTUNISTIC)
reply.pack()
print(json.dumps({"hash": lxm.hash.hex(), "valid": bool(lxm.signature_validated), "content": lxm.content.decode(), "packed": reply.packed.hex()}))
`, hex.EncodeToString(src.PrivateBytes()), hex.EncodeToString(dst.PrivateBytes()), hex.EncodeToString(m.Packed))
	if res.Hash != hex.EncodeToString(m.Hash[:]) || !res.Valid || res.Cont != "hello from the bridge" {
		t.Fatalf("python rejected the Go message: %+v", res)
	}
	// ContentSize is Python's content_size: the packed payload minus the
	// timestamp and structure overhead (it counts the title too).
	if want := len(lxmf.Pack(lxmf.Array(lxmf.Float(m.Timestamp), lxmf.Bin(m.Title), lxmf.Bin(m.Content), lxmf.MapOf()))) - 16; m.ContentSize() != want {
		t.Fatalf("content size %d != %d", m.ContentSize(), want)
	}
	pk, _ := hex.DecodeString(res.Packed)
	back, err := lxmf.Unpack(pk, func(source [16]byte) []byte {
		if source == dst.DestHash(lxmf.DeliveryName) {
			return dst.SigningPublicKey()
		}
		return nil
	})
	if err != nil || !back.SourceKnown || !back.SignatureValid || string(back.Content) != "reply from python" || string(back.Title) != "re" {
		t.Fatalf("Go could not verify the python message: %v %+v", err, back)
	}
}

func TestGolden_StampAndAnnounceAppData(t *testing.T) {
	src, _ := reticulum.GenerateIdentity()
	dst, _ := reticulum.GenerateIdentity()
	m := &lxmf.Message{Dest: dst.DestHash(lxmf.DeliveryName), Source: src.DestHash(lxmf.DeliveryName), Content: []byte("stamped")}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := m.Pack(src, 8, func(h [32]byte, cost int) []byte { s, _ := lxmf.GenerateStamp(ctx, h, cost); return s })
	if err != nil {
		t.Fatal(err)
	}
	app := lxmf.EncodeAnnounceAppData("MeshSat kit", 8)
	var res struct {
		StampOK bool   `json:"stamp_ok"`
		Name    string `json:"name"`
		Cost    int    `json:"cost"`
		App     string `json:"app"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS, LXMF
RNS.loglevel = 0
from LXMF import LXStamper
src = RNS.Identity.from_bytes(bytes.fromhex(sys.argv[1])); dst = RNS.Identity.from_bytes(bytes.fromhex(sys.argv[2]))
for i in (src, dst):
    RNS.Identity.known_destinations[RNS.Destination.hash_from_name_and_identity("lxmf.delivery", i)] = [0, None, i.get_public_key(), None]
lxm = LXMF.LXMessage.unpack_from_bytes(bytes.fromhex(sys.argv[3]))
ok = lxm.validate_stamp(8)
app = bytes.fromhex(sys.argv[4])
import RNS.vendor.umsgpack as m
print(json.dumps({"stamp_ok": bool(ok) and bool(lxm.signature_validated), "name": LXMF.display_name_from_app_data(app), "cost": LXMF.stamp_cost_from_app_data(app),
  "app": m.packb([b"Python peer", 12, [0]]).hex()}))
`, hex.EncodeToString(src.PrivateBytes()), hex.EncodeToString(dst.PrivateBytes()), hex.EncodeToString(m.Packed), hex.EncodeToString(app))
	if !res.StampOK || res.Name != "MeshSat kit" || res.Cost != 8 {
		t.Fatalf("stamp/app data golden failed: %+v", res)
	}
	pa, _ := hex.DecodeString(res.App)
	info := lxmf.ParseAnnounceAppData(pa)
	if info.DisplayName != "Python peer" || info.StampCost != 12 || !info.Compression {
		t.Fatalf("parse python app data: %+v", info)
	}
	// The workblock/validity check agrees with what Python accepted.
	wb := lxmf.Workblock(m.Hash)
	if !lxmf.StampValid(wb, m.Stamp, 8) {
		t.Fatalf("Go rejects its own stamp")
	}
}
