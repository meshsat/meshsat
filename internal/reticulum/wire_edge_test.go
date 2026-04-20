package reticulum

import (
	"bytes"
	"crypto/aes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Link state machine: full lifecycle (Pending→Established→Closed)
// ---------------------------------------------------------------------------

func TestLinkHandshake_FullLifecycle(t *testing.T) {
	// Generate identities
	_, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	responder, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	// Step 1: Initiator creates LinkRequest
	initiatorEph, _ := ecdh.X25519().GenerateKey(rand.Reader)
	destHash := responder.DestHash("test.app")
	req := &LinkRequest{
		DestHash:     destHash,
		EphemeralPub: initiatorEph.PublicKey(),
	}
	io.ReadFull(rand.Reader, req.Random[:])
	linkID := req.ComputeLinkID()

	// Marshal + unmarshal round-trip
	data := req.Marshal()
	parsed, err := UnmarshalLinkRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.DestHash != destHash {
		t.Error("dest hash mismatch after round-trip")
	}
	if !bytes.Equal(parsed.EphemeralPub.Bytes(), initiatorEph.PublicKey().Bytes()) {
		t.Error("ephemeral pub mismatch")
	}
	if parsed.ComputeLinkID() != linkID {
		t.Error("link ID mismatch after round-trip")
	}

	// Step 2: Responder creates LinkProof
	responderEph, _ := ecdh.X25519().GenerateKey(rand.Reader)
	signable := LinkProofSignable(linkID, responderEph.PublicKey())
	signature := responder.Sign(signable)

	proof := &LinkProof{
		LinkID:       linkID,
		EphemeralPub: responderEph.PublicKey(),
		Signature:    signature,
	}

	// Verify proof signature
	if !proof.Verify(responder.SigningPublicKey()) {
		t.Fatal("proof signature should be valid")
	}

	// Step 3: Both derive shared secret
	initiatorSecret, err := initiatorEph.ECDH(responderEph.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	responderSecret, err := responderEph.ECDH(initiatorEph.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(initiatorSecret, responderSecret) {
		t.Fatal("ECDH shared secrets should match")
	}

	// Step 4: Derive symmetric keys
	iEncKey1, iHmacKey1, iEncKey2, iHmacKey2 := DeriveSymKeys(initiatorSecret, linkID)
	rEncKey1, rHmacKey1, rEncKey2, rHmacKey2 := DeriveSymKeys(responderSecret, linkID)

	// Initiator sends on key1, responder receives on key1
	if !bytes.Equal(iEncKey1, rEncKey1) || !bytes.Equal(iHmacKey1, rHmacKey1) {
		t.Error("key1 pair must match")
	}
	// Responder sends on key2, initiator receives on key2
	if !bytes.Equal(iEncKey2, rEncKey2) || !bytes.Equal(iHmacKey2, rHmacKey2) {
		t.Error("key2 pair must match")
	}

	// Step 5: Encrypt/decrypt (initiator→responder)
	plaintext := []byte("hello from initiator to responder over link")
	ct, err := CBCHMACEncrypt(iEncKey1, iHmacKey1, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := CBCHMACDecrypt(rEncKey1, rHmacKey1, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Error("decrypted plaintext should match")
	}

	// Step 6: Encrypt/decrypt (responder→initiator)
	plaintext2 := []byte("reply from responder")
	ct2, err := CBCHMACEncrypt(rEncKey2, rHmacKey2, plaintext2)
	if err != nil {
		t.Fatal(err)
	}
	pt2, err := CBCHMACDecrypt(iEncKey2, iHmacKey2, ct2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt2, plaintext2) {
		t.Error("reverse direction decrypted plaintext should match")
	}

	// Verify wrong direction keys fail
	_, err = CBCHMACDecrypt(rEncKey2, rHmacKey2, ct) // wrong keys for this direction
	if err == nil {
		t.Error("decryption with wrong direction keys should fail")
	}
}

func TestLinkProof_VerifyWrongKey(t *testing.T) {
	ephKey, _ := ecdh.X25519().GenerateKey(rand.Reader)
	id, _ := GenerateIdentity()
	wrong, _ := GenerateIdentity()

	var linkID [LinkIDLen]byte
	rand.Read(linkID[:])

	signable := LinkProofSignable(linkID, ephKey.PublicKey())
	sig := id.Sign(signable)

	proof := &LinkProof{
		LinkID:       linkID,
		EphemeralPub: ephKey.PublicKey(),
		Signature:    sig,
	}

	if proof.Verify(wrong.SigningPublicKey()) {
		t.Error("should not verify with wrong key")
	}
	if !proof.Verify(id.SigningPublicKey()) {
		t.Error("should verify with correct key")
	}
}

func TestLinkProof_TamperedLinkID(t *testing.T) {
	ephKey, _ := ecdh.X25519().GenerateKey(rand.Reader)
	id, _ := GenerateIdentity()

	var linkID [LinkIDLen]byte
	rand.Read(linkID[:])

	signable := LinkProofSignable(linkID, ephKey.PublicKey())
	sig := id.Sign(signable)

	// Tamper with link ID
	linkID[0] ^= 0xFF
	proof := &LinkProof{
		LinkID:       linkID,
		EphemeralPub: ephKey.PublicKey(),
		Signature:    sig,
	}

	if proof.Verify(id.SigningPublicKey()) {
		t.Error("should not verify with tampered link ID")
	}
}

// ---------------------------------------------------------------------------
// CBC+HMAC edge cases
// ---------------------------------------------------------------------------

func TestCBCHMAC_ExactBlockSize(t *testing.T) {
	key := make([]byte, 32)
	hmacKey := make([]byte, 32)
	rand.Read(key)
	rand.Read(hmacKey)

	// Plaintext exactly 1 block (16 bytes) — PKCS7 adds a full block of padding
	plaintext := make([]byte, aes.BlockSize)
	rand.Read(plaintext)

	ct, err := CBCHMACEncrypt(key, hmacKey, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := CBCHMACDecrypt(key, hmacKey, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Error("round-trip failed for exact block size")
	}
}

func TestCBCHMAC_EmptyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	hmacKey := make([]byte, 32)
	rand.Read(key)
	rand.Read(hmacKey)

	// Empty plaintext — PKCS7 pads to full block
	ct, err := CBCHMACEncrypt(key, hmacKey, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	pt, err := CBCHMACDecrypt(key, hmacKey, ct)
	if err != nil {
		t.Fatal(err)
	}
	if len(pt) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(pt))
	}
}

func TestCBCHMAC_LargePlaintext(t *testing.T) {
	key := make([]byte, 32)
	hmacKey := make([]byte, 32)
	rand.Read(key)
	rand.Read(hmacKey)

	plaintext := make([]byte, 10000) // ~10KB
	rand.Read(plaintext)

	ct, err := CBCHMACEncrypt(key, hmacKey, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := CBCHMACDecrypt(key, hmacKey, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Error("large plaintext round-trip failed")
	}
}

func TestCBCHMAC_TamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	hmacKey := make([]byte, 32)
	rand.Read(key)
	rand.Read(hmacKey)

	ct, _ := CBCHMACEncrypt(key, hmacKey, []byte("test data"))

	// Flip a bit in the ciphertext (after IV, before HMAC)
	ct[aes.BlockSize+1] ^= 0x01

	_, err := CBCHMACDecrypt(key, hmacKey, ct)
	if err == nil {
		t.Error("tampered ciphertext should fail HMAC check")
	}
}

func TestCBCHMAC_TruncatedData(t *testing.T) {
	key := make([]byte, 32)
	hmacKey := make([]byte, 32)
	rand.Read(key)
	rand.Read(hmacKey)

	// Various short lengths
	for _, length := range []int{0, 1, 15, 16, 31, 47, 63} {
		data := make([]byte, length)
		_, err := CBCHMACDecrypt(key, hmacKey, data)
		if err == nil {
			t.Errorf("should fail for data length %d", length)
		}
	}
}

// ---------------------------------------------------------------------------
// Resource wire-format edge cases
// ---------------------------------------------------------------------------

func TestBitmap_AllOperations(t *testing.T) {
	tests := []struct {
		name     string
		segments int
	}{
		{"1 segment", 1},
		{"7 segments", 7},
		{"8 segments", 8},
		{"9 segments", 9},
		{"16 segments", 16},
		{"17 segments", 17},
		{"100 segments", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bm := NewBitmap(tt.segments)

			// All bits should be set initially
			for i := 0; i < tt.segments; i++ {
				if !BitmapGet(bm, i) {
					t.Errorf("bit %d should be set", i)
				}
			}
			if BitmapAllClear(bm) {
				t.Error("bitmap should not be all-clear initially")
			}

			// Clear all bits one by one
			for i := 0; i < tt.segments; i++ {
				BitmapClear(bm, i)
			}
			if !BitmapAllClear(bm) {
				t.Error("bitmap should be all-clear after clearing all")
			}

			// Bits beyond segment count should always be 0
			if BitmapGet(bm, tt.segments) {
				t.Error("bit beyond range should be false")
			}
			if BitmapGet(bm, tt.segments+100) {
				t.Error("far-out-of-range bit should be false")
			}
		})
	}
}

func TestBitmap_ClearIdempotent(t *testing.T) {
	bm := NewBitmap(10)
	BitmapClear(bm, 5)
	BitmapClear(bm, 5) // double clear
	if BitmapGet(bm, 5) {
		t.Error("double-cleared bit should still be clear")
	}
	// Other bits still set
	if !BitmapGet(bm, 4) {
		t.Error("adjacent bit should still be set")
	}
}

func TestResourceSegment_EmptyData(t *testing.T) {
	// Segment with minimal data (1 byte)
	seg := &ResourceSegment{
		SegmentIndex: 42,
		Data:         []byte{0xFF},
	}
	rand.Read(seg.ResourceHash[:])
	data := MarshalResourceSegment(seg)
	parsed, err := UnmarshalResourceSegment(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SegmentIndex != 42 {
		t.Errorf("index: got %d, want 42", parsed.SegmentIndex)
	}
	if !bytes.Equal(parsed.Data, []byte{0xFF}) {
		t.Error("data mismatch")
	}
}

func TestResourceSegment_MaxIndex(t *testing.T) {
	seg := &ResourceSegment{
		SegmentIndex: 0xFFFF,
		Data:         []byte("test"),
	}
	rand.Read(seg.ResourceHash[:])
	data := MarshalResourceSegment(seg)
	parsed, _ := UnmarshalResourceSegment(data)
	if parsed.SegmentIndex != 0xFFFF {
		t.Errorf("max index: got %d, want %d", parsed.SegmentIndex, 0xFFFF)
	}
}

func TestComputeSegmentCount_EdgeCases(t *testing.T) {
	tests := []struct {
		total, seg, want int
	}{
		{0, 100, 0},    // zero total
		{100, 0, 0},    // zero segment size
		{-1, 100, 0},   // negative total
		{100, -1, 0},   // negative segment
		{1, 1, 1},      // exact fit, minimal
		{180, 180, 1},  // exact fit
		{181, 180, 2},  // off by one
		{360, 180, 2},  // exact double
		{361, 180, 3},  // just over double
		{1024, 256, 4}, // power of 2
	}
	for _, tt := range tests {
		got := ComputeSegmentCount(tt.total, tt.seg)
		if got != tt.want {
			t.Errorf("ComputeSegmentCount(%d, %d) = %d, want %d", tt.total, tt.seg, got, tt.want)
		}
	}
}

func TestResourceAdv_ZeroValues(t *testing.T) {
	adv := &ResourceAdvertisement{}
	data := MarshalResourceAdv(adv)
	parsed, err := UnmarshalResourceAdv(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.TotalSize != 0 || parsed.SegmentSize != 0 || parsed.SegmentCount != 0 {
		t.Error("zero advertisement should round-trip")
	}
}

// ---------------------------------------------------------------------------
// Packet parsing edge cases
// ---------------------------------------------------------------------------

func TestPacketFlags_AllCombinations(t *testing.T) {
	// Test all valid flag combinations
	for ht := byte(0); ht <= 1; ht++ {
		for cf := byte(0); cf <= 1; cf++ {
			for tt := byte(0); tt <= 1; tt++ {
				for dt := byte(0); dt <= 3; dt++ {
					for pt := byte(0); pt <= 3; pt++ {
						h := &Header{
							HeaderType:    ht,
							ContextFlag:   cf,
							TransportType: tt,
							DestType:      dt,
							PacketType:    pt,
						}
						flags := h.PackFlags()
						h2 := &Header{}
						h2.UnpackFlags(flags)
						if h2.HeaderType != ht || h2.ContextFlag != cf || h2.TransportType != tt || h2.DestType != dt || h2.PacketType != pt {
							t.Errorf("flags roundtrip failed for ht=%d cf=%d tt=%d dt=%d pt=%d", ht, cf, tt, dt, pt)
						}
					}
				}
			}
		}
	}
}

func TestHeader_MTUBoundary(t *testing.T) {
	// Data exactly at MTU - header size
	maxData := make([]byte, MTU-HeaderMinSize)
	rand.Read(maxData)

	h := &Header{
		HeaderType: HeaderType1,
		PacketType: PacketData,
		DestType:   DestSingle,
		Data:       maxData,
	}
	raw := h.Marshal()
	if len(raw) != MTU {
		t.Errorf("expected MTU-size packet (%d), got %d", MTU, len(raw))
	}

	parsed, err := UnmarshalHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed.Data, maxData) {
		t.Error("data mismatch at MTU boundary")
	}
}

func TestHeader_HopBoundary(t *testing.T) {
	h := &Header{Hops: PathfinderM - 1}
	if !h.IncrementHop() {
		t.Error("should allow increment to PathfinderM")
	}
	if h.Hops != PathfinderM {
		t.Errorf("hops should be %d, got %d", PathfinderM, h.Hops)
	}
	if h.IncrementHop() {
		t.Error("should not allow increment beyond PathfinderM")
	}
}

func TestHeader_Type2WithData(t *testing.T) {
	h := &Header{
		HeaderType:    HeaderType2,
		TransportType: TransportTransport,
		DestType:      DestSingle,
		PacketType:    PacketData,
		Hops:          5,
		Context:       ContextResource,
		Data:          []byte("payload data"),
	}
	rand.Read(h.TransportID[:])
	rand.Read(h.DestHash[:])

	raw := h.Marshal()
	parsed, err := UnmarshalHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.HeaderType != HeaderType2 {
		t.Error("header type mismatch")
	}
	if parsed.TransportID != h.TransportID {
		t.Error("transport ID mismatch")
	}
	if parsed.DestHash != h.DestHash {
		t.Error("dest hash mismatch")
	}
	if parsed.Context != ContextResource {
		t.Error("context mismatch")
	}
	if !bytes.Equal(parsed.Data, h.Data) {
		t.Error("data mismatch")
	}
}

func TestHeader_EmptyData(t *testing.T) {
	h := &Header{
		HeaderType: HeaderType1,
		PacketType: PacketData,
	}
	raw := h.Marshal()
	parsed, err := UnmarshalHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Data) != 0 {
		t.Errorf("expected no data, got %d bytes", len(parsed.Data))
	}
}

func TestPacketTypeString_AllValues(t *testing.T) {
	tests := map[byte]string{
		PacketData:        "DATA",
		PacketAnnounce:    "ANNOUNCE",
		PacketLinkRequest: "LINKREQUEST",
		PacketProof:       "PROOF",
		0x04:              "UNKNOWN(04)",
		0xFF:              "UNKNOWN(ff)",
	}
	for pt, want := range tests {
		got := PacketTypeString(pt)
		if got != want {
			t.Errorf("PacketTypeString(%02x) = %q, want %q", pt, got, want)
		}
	}
}

func TestDestTypeString_AllValues(t *testing.T) {
	tests := map[byte]string{
		DestSingle: "SINGLE",
		DestGroup:  "GROUP",
		DestPlain:  "PLAIN",
		DestLink:   "LINK",
		0x04:       "UNKNOWN(04)",
	}
	for dt, want := range tests {
		got := DestTypeString(dt)
		if got != want {
			t.Errorf("DestTypeString(%02x) = %q, want %q", dt, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Announce edge cases
// ---------------------------------------------------------------------------

func TestAnnounce_WithRatchet(t *testing.T) {
	id, _ := GenerateIdentity()
	ann, err := NewAnnounce(id, "test.ratchet", []byte("app-data"))
	if err != nil {
		t.Fatal(err)
	}

	// Manually set ratchet
	ann.ContextFlag = 1
	rand.Read(ann.Ratchet[:])

	// Re-sign with ratchet included
	// (NewAnnounce doesn't set ratchet, but MarshalPayload/MarshalPacket use ContextFlag)
	payload := ann.MarshalPayload()
	if len(payload) < AnnounceRatchetPayload {
		t.Errorf("ratchet payload should be at least %d bytes, got %d", AnnounceRatchetPayload, len(payload))
	}
}

func TestAnnounce_HopIncrementPreservesVerifiability(t *testing.T) {
	id, _ := GenerateIdentity()
	ann, err := NewAnnounce(id, "test.hop", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Verify before incrementing
	if err := ann.Verify(); err != nil {
		t.Fatalf("should verify before hop: %v", err)
	}

	// Increment hops multiple times
	for i := 0; i < 10; i++ {
		if !ann.IncrementHop() {
			t.Fatalf("should allow increment at hop %d", i+1)
		}
	}

	// Should still verify — hops are NOT in the signed body
	if err := ann.Verify(); err != nil {
		t.Fatalf("should still verify after hop increments: %v", err)
	}
}

func TestAnnounce_MaxPacketRoundtrip(t *testing.T) {
	id, _ := GenerateIdentity()
	// Large app data (but within reason)
	appData := make([]byte, 200)
	rand.Read(appData)

	ann, err := NewAnnounce(id, "test.large", appData)
	if err != nil {
		t.Fatal(err)
	}

	raw := ann.MarshalPacket()
	parsed, err := UnmarshalAnnouncePacket(raw)
	if err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !bytes.Equal(parsed.AppData, appData) {
		t.Error("app data mismatch after round-trip")
	}
	if err := parsed.Verify(); err != nil {
		t.Fatalf("parsed announce should verify: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Confirmation edge cases
// ---------------------------------------------------------------------------

func TestConfirmation_SignAndVerify(t *testing.T) {
	id, _ := GenerateIdentity()
	destHash := id.DestHash("test.conf")
	plaintext := []byte("the payload that was received")

	dc := NewSignedConfirmation(id, destHash, plaintext)

	if !dc.Verify(id.SigningPublicKey()) {
		t.Error("should verify with correct key")
	}
	if !dc.VerifyWithPlaintext(id.SigningPublicKey(), plaintext) {
		t.Error("should verify with correct plaintext")
	}
	if dc.VerifyWithPlaintext(id.SigningPublicKey(), []byte("wrong")) {
		t.Error("should fail with wrong plaintext")
	}

	// Wrong key
	other, _ := GenerateIdentity()
	if dc.Verify(other.SigningPublicKey()) {
		t.Error("should fail with wrong key")
	}
}

func TestConfirmationPacket_RoundTrip(t *testing.T) {
	id, _ := GenerateIdentity()
	destHash := id.DestHash("test.cp")

	dc := NewSignedConfirmation(id, destHash, []byte("payload"))
	cp := &ConfirmationPacket{
		MsgRef:       0xDEADBEEFCAFE,
		Confirmation: dc,
	}

	data := cp.Marshal()
	parsed, err := UnmarshalConfirmationPacket(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.MsgRef != 0xDEADBEEFCAFE {
		t.Errorf("msg ref: got %x, want %x", parsed.MsgRef, 0xDEADBEEFCAFE)
	}
	if !parsed.Confirmation.Verify(id.SigningPublicKey()) {
		t.Error("parsed confirmation should verify")
	}
}

// ---------------------------------------------------------------------------
// HDLC edge cases
// ---------------------------------------------------------------------------

func TestHDLC_EmptyPayload(t *testing.T) {
	frame := HDLCFrame([]byte{})
	// Empty payload between two flags
	if len(frame) != 2 {
		t.Errorf("empty frame should be 2 bytes (two flags), got %d", len(frame))
	}
}

func TestHDLC_AllSpecialBytes(t *testing.T) {
	// Payload entirely of special bytes
	data := bytes.Repeat([]byte{HDLCFlag, HDLCEsc}, 50)
	escaped := HDLCEscape(data)
	unescaped := HDLCUnescape(escaped)
	if !bytes.Equal(unescaped, data) {
		t.Error("all-special-bytes round-trip failed")
	}
}

func TestHDLC_AllByteValues(t *testing.T) {
	// Every possible byte value 0x00-0xFF
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	escaped := HDLCEscape(data)
	unescaped := HDLCUnescape(escaped)
	if !bytes.Equal(unescaped, data) {
		t.Error("all-byte-values round-trip failed")
	}
}

func TestHDLCReader_BackToBackFrames(t *testing.T) {
	reader := NewHDLCFrameReader()

	// Build 3 frames packed tightly (no gap)
	var buf []byte
	payloads := [][]byte{
		make([]byte, HeaderMinSize),
		make([]byte, HeaderMinSize+10),
		make([]byte, HeaderMinSize+20),
	}
	for i, p := range payloads {
		rand.Read(p)
		// Need valid header to pass min-size check
		p[0] = byte(i) // distinguish them
		buf = append(buf, HDLCFrame(p)...)
	}

	// Feed all at once
	frames := reader.Feed(buf)
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}
}

func TestHDLCReader_ByteByByte(t *testing.T) {
	reader := NewHDLCFrameReader()
	payload := make([]byte, HeaderMinSize+5)
	rand.Read(payload)
	frame := HDLCFrame(payload)

	var collected [][]byte
	for _, b := range frame {
		result := reader.Feed([]byte{b})
		collected = append(collected, result...)
	}
	if len(collected) != 1 {
		t.Fatalf("expected 1 frame from byte-by-byte feed, got %d", len(collected))
	}
	if !bytes.Equal(collected[0], payload) {
		t.Error("payload mismatch from byte-by-byte feed")
	}
}

func TestHDLCReader_CorruptedFrame(t *testing.T) {
	reader := NewHDLCFrameReader()

	// Frame with too-short payload (< HeaderMinSize) should be silently dropped
	short := []byte{HDLCFlag, 0x01, 0x02, HDLCFlag}
	frames := reader.Feed(short)
	if len(frames) != 0 {
		t.Errorf("too-short frame should be dropped, got %d frames", len(frames))
	}

	// Valid frame after corruption should still work
	payload := make([]byte, HeaderMinSize)
	rand.Read(payload)
	validFrame := HDLCFrame(payload)
	frames = reader.Feed(validFrame)
	if len(frames) != 1 {
		t.Fatal("valid frame after corruption should be received")
	}
}

func TestHDLCReader_GarbageBetweenFrames(t *testing.T) {
	reader := NewHDLCFrameReader()
	payload := make([]byte, HeaderMinSize)
	rand.Read(payload)

	// Garbage + valid frame
	garbage := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	buf := append(garbage, HDLCFrame(payload)...)

	frames := reader.Feed(buf)
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if !bytes.Equal(frames[0], payload) {
		t.Error("payload mismatch after garbage prefix")
	}
}

// ---------------------------------------------------------------------------
// Identity edge cases
// ---------------------------------------------------------------------------

func TestIdentity_DifferentAppNames_DifferentDestHash(t *testing.T) {
	id, _ := GenerateIdentity()
	hash1 := id.DestHash("app.one")
	hash2 := id.DestHash("app.two")
	if hash1 == hash2 {
		t.Error("different app names should produce different dest hashes")
	}
}

func TestIdentity_LoadSaveRoundtrip(t *testing.T) {
	original, _ := GenerateIdentity()
	enc := original.EncryptionPrivateBytes()
	sig := original.SigningPrivateBytes()

	loaded, err := LoadIdentity(enc, sig)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(loaded.PublicBytes(), original.PublicBytes()) {
		t.Error("loaded public bytes should match original")
	}
	if loaded.DestHash("test") != original.DestHash("test") {
		t.Error("loaded dest hash should match original")
	}

	// Sign with loaded, verify with original
	msg := []byte("test message")
	sig2 := loaded.Sign(msg)
	if !VerifySignature(original.SigningPublicKey(), msg, sig2) {
		t.Error("signature from loaded identity should verify with original key")
	}
}

func TestVerifySignature_EdgeCases(t *testing.T) {
	if VerifySignature(nil, []byte("data"), make([]byte, ed25519.SignatureSize)) {
		t.Error("nil pubkey should fail")
	}
	id, _ := GenerateIdentity()
	if VerifySignature(id.SigningPublicKey(), []byte("data"), nil) {
		t.Error("nil signature should fail")
	}
	if VerifySignature(id.SigningPublicKey(), []byte("data"), make([]byte, 10)) {
		t.Error("short signature should fail")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func NewSignedConfirmation(id *Identity, destHash [TruncatedHashLen]byte, plaintext []byte) *DeliveryConfirmation {
	dc := &DeliveryConfirmation{
		DestHash: destHash,
	}
	h := make([]byte, 32)
	copy(h, func() []byte { v := [32]byte(ComputeResourceHash(plaintext)); return v[:] }())
	copy(dc.PayloadHash[:], h)
	body := dc.signableBody()
	dc.Signature = id.Sign(body)
	return dc
}

// ---------------------------------------------------------------------------
// Path request/response edge cases
// ---------------------------------------------------------------------------

func TestPathResponse_LongInterfaceType(t *testing.T) {
	resp := &PathResponse{
		Hops:          3,
		InterfaceType: strings.Repeat("x", 300), // exceeds 255 limit
		AnnounceData:  []byte("test"),
	}
	rand.Read(resp.DestHash[:])
	rand.Read(resp.Tag[:])

	data := MarshalPathResponse(resp)
	parsed, err := UnmarshalPathResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.InterfaceType) > 255 {
		t.Error("interface type should be truncated to 255")
	}
}

func TestPathResponse_EmptyInterfaceType(t *testing.T) {
	resp := &PathResponse{
		Hops:          1,
		InterfaceType: "",
	}
	rand.Read(resp.DestHash[:])
	rand.Read(resp.Tag[:])

	data := MarshalPathResponse(resp)
	parsed, err := UnmarshalPathResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.InterfaceType != "" {
		t.Errorf("expected empty interface type, got %q", parsed.InterfaceType)
	}
}
