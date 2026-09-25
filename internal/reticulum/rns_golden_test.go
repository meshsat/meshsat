package reticulum_test

// Golden tests against the pinned upstream Python RNS (see
// internal/interop/rnsenv). Every wire format the bridge emits is fed to
// the reference implementation and vice versa. They skip when the venv is
// absent and MESHSAT_INTEROP is not 1.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"meshsat/internal/interop/rnsenv"
	"meshsat/internal/reticulum"
)

func TestRNSGolden_TokenBothWays(t *testing.T) {
	key := make([]byte, 64)
	rand.Read(key)
	tok, err := reticulum.NewToken(key)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("token golden test payload, 40 bytes long!")
	ct, err := tok.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Plain string `json:"plain"`
		CT    string `json:"ct"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json
from RNS.Cryptography import Token
key = bytes.fromhex(sys.argv[1]); ct = bytes.fromhex(sys.argv[2]); plain = bytes.fromhex(sys.argv[3])
tok = Token(key)
print(json.dumps({"plain": tok.decrypt(ct).hex(), "ct": tok.encrypt(plain).hex()}))
`, hex.EncodeToString(key), hex.EncodeToString(ct), hex.EncodeToString(plain))
	if res.Plain != hex.EncodeToString(plain) {
		t.Fatalf("python could not decrypt the Go token: got %s", res.Plain)
	}
	pyCT, _ := hex.DecodeString(res.CT)
	got, err := tok.Decrypt(pyCT)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Go could not decrypt the python token: %v %x", err, got)
	}
}

func TestRNSGolden_HKDF(t *testing.T) {
	ikm := []byte("input keying material")
	salt := []byte("0123456789abcdef")
	got := reticulum.HKDF(64, ikm, salt, nil)
	got2 := reticulum.HKDF(48, ikm, nil, nil)
	var res struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json
from RNS.Cryptography import hkdf
ikm = bytes.fromhex(sys.argv[1]); salt = bytes.fromhex(sys.argv[2])
print(json.dumps({"a": hkdf(length=64, derive_from=ikm, salt=salt).hex(), "b": hkdf(length=48, derive_from=ikm, salt=None).hex()}))
`, hex.EncodeToString(ikm), hex.EncodeToString(salt))
	if res.A != hex.EncodeToString(got) || res.B != hex.EncodeToString(got2) {
		t.Fatalf("HKDF mismatch:\n go %x / %x\n py %s / %s", got, got2, res.A, res.B)
	}
}

func TestRNSGolden_IdentityEncryptBothWays(t *testing.T) {
	id, _ := reticulum.GenerateIdentity()
	plain := []byte("single packet plaintext for lxmf.delivery")
	ct, err := id.Encrypt(plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Plain string `json:"plain"`
		CT    string `json:"ct"`
		Hash  string `json:"hash"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS
prv = bytes.fromhex(sys.argv[1]); ct = bytes.fromhex(sys.argv[2]); plain = bytes.fromhex(sys.argv[3])
ident = RNS.Identity.from_bytes(prv)
pub = RNS.Identity(create_keys=False); pub.load_public_key(ident.get_public_key())
print(json.dumps({"plain": ident.decrypt(ct).hex(), "ct": pub.encrypt(plain).hex(), "hash": ident.hash.hex()}))
`, hex.EncodeToString(id.PrivateBytes()), hex.EncodeToString(ct), hex.EncodeToString(plain))
	if res.Plain != hex.EncodeToString(plain) {
		t.Fatalf("python could not decrypt Go single-packet ciphertext: %q", res.Plain)
	}
	ih := id.IdentityHash()
	if res.Hash != hex.EncodeToString(ih[:]) {
		t.Fatalf("identity hash mismatch go=%x py=%s", ih, res.Hash)
	}
	pyCT, _ := hex.DecodeString(res.CT)
	got, idx, err := id.Decrypt(pyCT, nil)
	if err != nil || idx != -1 || !bytes.Equal(got, plain) {
		t.Fatalf("Go could not decrypt python ciphertext: %v idx=%d", err, idx)
	}
}

func TestRNSGolden_RatchetEncrypt(t *testing.T) {
	id, _ := reticulum.GenerateIdentity()
	ratchet, _ := reticulum.GenerateRatchet()
	rpub, _ := reticulum.RatchetPublic(ratchet)
	plain := []byte("ratcheted")
	ct, _ := id.Encrypt(plain, rpub)
	var res struct {
		Plain string `json:"plain"`
		CT    string `json:"ct"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS
prv = bytes.fromhex(sys.argv[1]); ct = bytes.fromhex(sys.argv[2]); ratchet = bytes.fromhex(sys.argv[3]); rpub = bytes.fromhex(sys.argv[4]); plain = bytes.fromhex(sys.argv[5])
ident = RNS.Identity.from_bytes(prv)
pub = RNS.Identity(create_keys=False); pub.load_public_key(ident.get_public_key())
print(json.dumps({"plain": ident.decrypt(ct, ratchets=[ratchet], enforce_ratchets=True).hex(), "ct": pub.encrypt(plain, ratchet=rpub).hex()}))
`, hex.EncodeToString(id.PrivateBytes()), hex.EncodeToString(ct), hex.EncodeToString(ratchet), hex.EncodeToString(rpub.Bytes()), hex.EncodeToString(plain))
	if res.Plain != hex.EncodeToString(plain) {
		t.Fatalf("python ratchet decrypt failed: %q", res.Plain)
	}
	pyCT, _ := hex.DecodeString(res.CT)
	got, idx, err := id.Decrypt(pyCT, [][]byte{ratchet})
	if err != nil || idx != 0 || !bytes.Equal(got, plain) {
		t.Fatalf("Go ratchet decrypt failed: %v idx=%d", err, idx)
	}
}

// pyPacketHash asks RNS to unpack a raw packet and report its hashes.
func pyPacketHash(t *testing.T, raw []byte) (full, trunc string) {
	var res struct {
		Hash  string `json:"hash"`
		Trunc string `json:"trunc"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS
p = RNS.Packet(None, None); p.raw = bytes.fromhex(sys.argv[1])
assert p.unpack(), "unpack failed"
print(json.dumps({"hash": p.packet_hash.hex(), "trunc": p.truncated_packet_hash.hex()}))
`, hex.EncodeToString(raw))
	return res.Hash, res.Trunc
}

func TestRNSGolden_PacketHash(t *testing.T) {
	id, _ := reticulum.GenerateIdentity()
	h := &reticulum.Header{HeaderType: reticulum.HeaderType1, DestType: reticulum.DestSingle, PacketType: reticulum.PacketData,
		Hops: 3, DestHash: id.DestHash("lxmf.delivery"), Context: 0, Data: []byte("payload bytes")}
	raw := h.Marshal()
	full := reticulum.PacketHash(raw)
	pyFull, pyTrunc := pyPacketHash(t, raw)
	if pyFull != hex.EncodeToString(full[:]) {
		t.Fatalf("HEADER_1 packet hash mismatch go=%x py=%s", full, pyFull)
	}
	tr := reticulum.TruncatedPacketHash(raw)
	if pyTrunc != hex.EncodeToString(tr[:]) {
		t.Fatalf("truncated hash mismatch")
	}
	// The same packet rewritten for transport keeps its hash.
	var tid [16]byte
	rand.Read(tid[:])
	raw2 := reticulum.RewriteForTransport(raw, tid)
	full2 := reticulum.PacketHash(raw2)
	if full2 != full {
		t.Fatalf("hash changed under HEADER_2 rewrite")
	}
	pyFull2, _ := pyPacketHash(t, raw2)
	if pyFull2 != pyFull {
		t.Fatalf("python hashes the transport form differently: %s", pyFull2)
	}
	if !bytes.Equal(reticulum.StripTransport(raw2), raw) {
		t.Fatalf("StripTransport did not restore the HEADER_1 packet")
	}
}

func TestRNSGolden_LinkIDAndSignalling(t *testing.T) {
	id, _ := reticulum.GenerateIdentity()
	eph, _ := ecdh.X25519().GenerateKey(rand.Reader)
	sigPub, _, _ := ed25519.GenerateKey(rand.Reader)
	lr := &reticulum.RNSLinkRequest{EphPub: eph.PublicKey(), EphSigPub: sigPub, MTU: reticulum.MTU, Mode: reticulum.LinkModeAES256CBC}
	h := &reticulum.Header{HeaderType: reticulum.HeaderType1, DestType: reticulum.DestSingle, PacketType: reticulum.PacketLinkRequest,
		DestHash: id.DestHash("lxmf.delivery"), Data: lr.Marshal()}
	raw := h.Marshal()
	linkID, err := reticulum.LinkIDFromRequestPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	sig := reticulum.SignallingBytes(reticulum.MTU, reticulum.LinkModeAES256CBC)
	var res struct {
		LinkID string `json:"link_id"`
		Sig    string `json:"sig"`
		MTU    int    `json:"mtu"`
		Mode   int    `json:"mode"`
		Key    string `json:"key"`
	}
	shared := make([]byte, 32)
	rand.Read(shared)
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS
from RNS.Link import Link
p = RNS.Packet(None, None); p.raw = bytes.fromhex(sys.argv[1]); assert p.unpack()
lid = Link.link_id_from_lr_packet(p)
shared = bytes.fromhex(sys.argv[2])
print(json.dumps({"link_id": lid.hex(), "sig": Link.signalling_bytes(500, Link.MODE_AES256_CBC).hex(),
  "mtu": Link.mtu_from_lr_packet(p), "mode": Link.mode_from_lr_packet(p),
  "key": RNS.Cryptography.hkdf(length=64, derive_from=shared, salt=lid).hex()}))
`, hex.EncodeToString(raw), hex.EncodeToString(shared))
	if res.LinkID != hex.EncodeToString(linkID[:]) {
		t.Fatalf("link id mismatch go=%x py=%s", linkID, res.LinkID)
	}
	if res.Sig != hex.EncodeToString(sig[:]) || res.MTU != reticulum.MTU || res.Mode != int(reticulum.LinkModeAES256CBC) {
		t.Fatalf("signalling mismatch go=%x py=%s mtu=%d mode=%d", sig, res.Sig, res.MTU, res.Mode)
	}
	if res.Key != hex.EncodeToString(reticulum.DeriveLinkKey(shared, linkID)) {
		t.Fatalf("link key derivation mismatch")
	}
	if lr2, err := reticulum.UnmarshalRNSLinkRequest(h.Data); err != nil || lr2.MTU != reticulum.MTU || !lr2.Signalled {
		t.Fatalf("round trip: %v %+v", err, lr2)
	}
}

func TestRNSGolden_LinkProofSignature(t *testing.T) {
	// The responder identity signs link_id || eph pub || identity sig pub || signalling;
	// the initiator verifies with the destination identity exactly as Link.validate_proof.
	id, _ := reticulum.GenerateIdentity()
	eph, _ := ecdh.X25519().GenerateKey(rand.Reader)
	var linkID [16]byte
	rand.Read(linkID[:])
	signed := reticulum.LinkProofSignedData(linkID, eph.PublicKey(), id.SigningPublicKey(), reticulum.MTU, reticulum.LinkModeAES256CBC)
	lp := &reticulum.RNSLinkProof{Signature: id.Sign(signed), EphPub: eph.PublicKey(), MTU: reticulum.MTU, Mode: reticulum.LinkModeAES256CBC}
	data := lp.Marshal()
	var res struct {
		Valid bool `json:"valid"`
		Len   int  `json:"len"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS
from RNS.Link import Link
pub = bytes.fromhex(sys.argv[1]); data = bytes.fromhex(sys.argv[2]); lid = bytes.fromhex(sys.argv[3])
ident = RNS.Identity(create_keys=False); ident.load_public_key(pub)
SIG = RNS.Identity.SIGLENGTH//8; EC = Link.ECPUBSIZE//2
sig = data[:SIG]; peer_pub = data[SIG:SIG+EC]; mtu_bytes = data[SIG+EC:SIG+EC+3]
mtu = ((mtu_bytes[0] << 16) + (mtu_bytes[1] << 8) + mtu_bytes[2]) & Link.MTU_BYTEMASK
mode = mtu_bytes[0] >> 5
signed = lid + peer_pub + ident.get_public_key()[EC:] + Link.signalling_bytes(mtu, mode)
print(json.dumps({"valid": bool(ident.validate(sig, signed)), "len": len(data)}))
`, hex.EncodeToString(id.PublicBytes()), hex.EncodeToString(data), hex.EncodeToString(linkID[:]))
	if !res.Valid || res.Len != 99 {
		t.Fatalf("python rejected the Go link proof (valid=%v len=%d)", res.Valid, res.Len)
	}
	lp2, err := reticulum.UnmarshalRNSLinkProof(data)
	if err != nil || lp2.MTU != reticulum.MTU || !bytes.Equal(lp2.Signature, lp.Signature) {
		t.Fatalf("round trip: %v", err)
	}
}

func TestRNSGolden_RTTMsgpack(t *testing.T) {
	var res struct {
		Packed string  `json:"packed"`
		Value  float64 `json:"value"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json
import RNS.vendor.umsgpack as umsgpack
v = 0.734
print(json.dumps({"packed": umsgpack.packb(v).hex(), "value": umsgpack.unpackb(bytes.fromhex(sys.argv[1]))}))
`, hex.EncodeToString(reticulum.RTTData(1.25)))
	if res.Packed != hex.EncodeToString(reticulum.RTTData(0.734)) || res.Value != 1.25 {
		t.Fatalf("RTT msgpack mismatch: %+v", res)
	}
	pk, _ := hex.DecodeString(res.Packed)
	if v, err := reticulum.ParseRTTData(pk); err != nil || v != 0.734 {
		t.Fatalf("parse: %v %v", v, err)
	}
}

func TestRNSGolden_ProofValidation(t *testing.T) {
	id, _ := reticulum.GenerateIdentity()
	h := &reticulum.Header{DestType: reticulum.DestSingle, PacketType: reticulum.PacketData, DestHash: id.DestHash("lxmf.delivery"), Data: []byte("x")}
	raw := h.Marshal()
	ph := reticulum.PacketHash(raw)
	impl := id.ImplicitProof(ph)
	expl := id.ExplicitProof(ph)
	proofPkt := reticulum.BuildProofPacket(raw, impl)
	var res struct {
		Impl bool   `json:"impl"`
		Expl bool   `json:"expl"`
		Dest string `json:"dest"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS
pub = bytes.fromhex(sys.argv[1]); raw = bytes.fromhex(sys.argv[2]); impl = bytes.fromhex(sys.argv[3]); expl = bytes.fromhex(sys.argv[4]); proof = bytes.fromhex(sys.argv[5])
ident = RNS.Identity(create_keys=False); ident.load_public_key(pub)
p = RNS.Packet(None, None); p.raw = raw; assert p.unpack()
pp = RNS.Packet(None, None); pp.raw = proof; assert pp.unpack()
ok_impl = ident.validate(impl, p.packet_hash)
ok_expl = expl[:32] == p.packet_hash and ident.validate(expl[32:], p.packet_hash)
print(json.dumps({"impl": bool(ok_impl), "expl": bool(ok_expl), "dest": pp.destination_hash.hex()}))
`, hex.EncodeToString(id.PublicBytes()), hex.EncodeToString(raw), hex.EncodeToString(impl), hex.EncodeToString(expl), hex.EncodeToString(proofPkt))
	tr := reticulum.TruncatedPacketHash(raw)
	if !res.Impl || !res.Expl || res.Dest != hex.EncodeToString(tr[:]) {
		t.Fatalf("proof golden failed: %+v", res)
	}
	if err := reticulum.ValidateProof(impl, ph, id.SigningPublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := reticulum.ValidateProof(expl, ph, id.SigningPublicKey()); err != nil {
		t.Fatal(err)
	}
}

func TestRNSGolden_AnnounceTimestampAndPathResponse(t *testing.T) {
	id, _ := reticulum.GenerateIdentity()
	ann, err := reticulum.NewAnnounce(id, "lxmf.delivery", []byte("app"))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(ann.EmittedAt()); d < 0 || d > 5*time.Second {
		t.Fatalf("EmittedAt not now: %v", ann.EmittedAt())
	}
	var tid [16]byte
	rand.Read(tid[:])
	raw := ann.MarshalPacketTransport(tid, 2, reticulum.ContextPathResponse)
	var res struct {
		Valid     bool   `json:"valid"`
		Context   int    `json:"context"`
		Transport string `json:"transport"`
		Hops      int    `json:"hops"`
		TS        int64  `json:"ts"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS
p = RNS.Packet(None, None); p.raw = bytes.fromhex(sys.argv[1]); assert p.unpack()
valid = RNS.Identity.validate_announce(p, only_validate_signature=True)
ts = int.from_bytes(p.data[64+10+5:64+10+10], "big")
print(json.dumps({"valid": bool(valid), "context": p.context, "transport": p.transport_id.hex(), "hops": p.hops, "ts": ts}))
`, hex.EncodeToString(raw))
	if !res.Valid || res.Context != int(reticulum.ContextPathResponse) || res.Transport != hex.EncodeToString(tid[:]) || res.Hops != 2 {
		t.Fatalf("transport announce rejected: %+v", res)
	}
	if res.TS != ann.EmittedAt().Unix() {
		t.Fatalf("timestamp mismatch py=%d go=%d", res.TS, ann.EmittedAt().Unix())
	}
	back, err := reticulum.UnmarshalAnnouncePacket(raw)
	if err != nil || back.Context != reticulum.ContextPathResponse || back.Hops != 2 {
		t.Fatalf("round trip: %v", err)
	}
}

func TestRNSGolden_PlainDestHash(t *testing.T) {
	var res struct {
		Hash string `json:"hash"`
	}
	rnsenv.RunJSON(t, &res, `
import json, RNS
print(json.dumps({"hash": RNS.Destination.hash(None, "rnstransport", "path", "request").hex()}))
`)
	got := reticulum.PathRequestDestHash()
	if res.Hash != hex.EncodeToString(got[:]) {
		t.Fatalf("path request dest mismatch go=%x py=%s", got, res.Hash)
	}
}

func TestRNSGolden_IFACBothWays(t *testing.T) {
	f, err := reticulum.NewIFAC("meshsat-test", "hunter2", reticulum.DefaultIFACSize)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := reticulum.GenerateIdentity()
	ann, _ := reticulum.NewAnnounce(id, "meshsat.bridge", nil)
	raw := ann.MarshalPacket()
	wrapped := f.Wrap(raw)
	var res struct {
		Wrapped string `json:"wrapped"`
		Valid   bool   `json:"valid"`
		Clean   string `json:"clean"`
		Key     string `json:"key"`
	}
	rnsenv.RunJSON(t, &res, `
import sys, json, RNS
class Iface:
    def ifac_violation(self, *a): pass
i = Iface()
origin = RNS.Identity.full_hash(b"meshsat-test") + RNS.Identity.full_hash(b"hunter2")
i.ifac_key = RNS.Cryptography.hkdf(length=64, derive_from=RNS.Identity.full_hash(origin), salt=RNS.Reticulum.IFAC_SALT, context=None)
i.ifac_identity = RNS.Identity.from_bytes(i.ifac_key); i.ifac_size = 16
raw = bytes.fromhex(sys.argv[1]); wrapped_go = bytes.fromhex(sys.argv[2])
valid, clean = RNS.Transport.handle_ifac(wrapped_go, i)
print(json.dumps({"wrapped": RNS.Transport.handle_outgoing_ifac(i, raw).hex(), "valid": bool(valid), "clean": clean.hex() if clean else "", "key": i.ifac_key.hex()}))
`, hex.EncodeToString(raw), hex.EncodeToString(wrapped))
	if res.Key != hex.EncodeToString(reticulum.IFACKey("meshsat-test", "hunter2")) {
		t.Fatalf("IFAC key mismatch")
	}
	if !res.Valid || res.Clean != hex.EncodeToString(raw) {
		t.Fatalf("python rejected Go IFAC packet: %+v", res)
	}
	if res.Wrapped != hex.EncodeToString(wrapped) {
		t.Fatalf("IFAC wrap differs from python (deterministic signature expected)")
	}
	clean, err := f.Unwrap(wrapped)
	if err != nil || !bytes.Equal(clean, raw) {
		t.Fatalf("Go unwrap failed: %v", err)
	}
	bad, _ := reticulum.NewIFAC("other", "", 16)
	if _, err := bad.Unwrap(wrapped); err == nil {
		t.Fatalf("wrong network accepted")
	}
}
