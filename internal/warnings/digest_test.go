package warnings

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"meshsat/internal/compress"
)

// MESHSAT-1734. The test key and the vector are the Hub's
// (meshsat-hub docs/warning-relay.md "Test vector"); the Hub's encoder test
// holds the same bytes, so the two decoders cannot drift apart unseen.

const testKeyD = "1f2d3c4b5a69788796a5b4c3d2e1f00112233445566778899aabbccddeeff001"

const testKeyPKIXHex = "3059301306072a8648ce3d020106082a8648ce3d03010703420004" +
	"8255ccf3e18d398e17a3aa927965387335c09fcea6e05c7671f134b2f6b4a799" +
	"588325ca016280e097d515e21218b6528bf1b7cd8ddde016baee3cecc2c95445"

const vectorHex = "" +
	"57010102f161d743dd14e97400000000000000006ae1ca5d6ae31bdd2241016e6c11024e4c383034000000000b4b857320e820dc918cbb8d" +
	"f76c877ecedb20f0442bf905ec8e55b41bc8a7701b47892201770a5e189e163e" +
	"2d81e77d1bfe52e766d5c90c2748cdcc0d31ccd360eb731e8e2fc75b4eba865b"

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	d, ok := new(big.Int).SetString(testKeyD, 16)
	if !ok {
		t.Fatal("test key")
	}
	key := &ecdsa.PrivateKey{D: d, PublicKey: ecdsa.PublicKey{Curve: elliptic.P256()}}
	key.PublicKey.X, key.PublicKey.Y = key.PublicKey.Curve.ScalarBaseMult(d.Bytes())
	return key
}

func testPub(t *testing.T) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&testKey(t).PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// encode is a test-only encoder of the Hub's layout, so the relay tests can
// make digests of every kind.
func encode(t *testing.T, d *Digest, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	b := []byte{Magic, Version}
	tf := byte(d.MsgType & 0x0F)
	if d.Test {
		tf |= flagTest
	}
	b = append(b, tf, byte(d.Source))
	b = append(b, d.ID[:]...)
	b = append(b, d.Ref[:]...)
	b = binary.BigEndian.AppendUint32(b, unix32(d.Sent))
	b = binary.BigEndian.AppendUint32(b, unix32(d.Expires))
	b = append(b, byte(d.Severity<<4|d.Urgency), byte(d.Certainty<<4|d.Category), byte(d.Event))
	lang := [2]byte{}
	if len(d.Language) >= 2 {
		lang[0], lang[1] = d.Language[0], d.Language[1]
	}
	b = append(b, lang[:]...)
	switch {
	case len(d.Geocodes) > 0:
		b = append(b, byte(AreaGeocodes<<4|len(d.Geocodes)))
		for _, g := range d.Geocodes {
			slot := make([]byte, geocodeBytes)
			slot[0] = byte(g.Type)
			copy(slot[1:], g.Value)
			b = append(b, slot...)
		}
	case len(d.Polygon) > 0:
		b = append(b, byte(AreaPolygon<<4|len(d.Polygon)))
		for _, p := range d.Polygon {
			b = append(b, int24(p.Lat)...)
			b = append(b, int24(p.Lon)...)
		}
	default:
		b = append(b, 0)
	}
	packed := compress.Compress([]byte(d.Text), compress.DictMeshtastic)
	b = append(b, byte(len(packed)))
	b = append(b, packed...)
	h := sha256.Sum256(b)
	der, err := ecdsa.SignASN1(rand.Reader, key, h[:])
	if err != nil {
		t.Fatal(err)
	}
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, SignatureBytes)
	rb, sb := sig.R.Bytes(), sig.S.Bytes()
	copy(raw[32-len(rb):32], rb)
	copy(raw[64-len(sb):], sb)
	return append(b, raw...)
}

func unix32(t time.Time) uint32 {
	if t.IsZero() {
		return 0
	}
	return uint32(t.Unix())
}

func int24(deg float64) []byte {
	v := int64(deg*10000) & 0xFFFFFF
	return []byte{byte(v >> 16), byte(v >> 8), byte(v)}
}

// IDOf mirrors the Hub's: the first 8 bytes of SHA-256 of the identifier.
func idOf(identifier string) [8]byte {
	var id [8]byte
	sum := sha256.Sum256([]byte(identifier))
	copy(id[:], sum[:8])
	return id
}

func TestTheDocumentedVectorVerifiesAndDecodes(t *testing.T) {
	if got := hex.EncodeToString(testPub(t)); got != testKeyPKIXHex {
		t.Fatalf("the test key's public key is %s, the documented one is %s", got, testKeyPKIXHex)
	}
	wire, err := hex.DecodeString(vectorHex)
	if err != nil {
		t.Fatal(err)
	}
	if !IsDigest(wire) || len(wire) != 120 {
		t.Fatalf("not seen as a digest: %d bytes", len(wire))
	}
	pub, _ := hex.DecodeString(testKeyPKIXHex)
	d, err := Verify(pub, wire)
	if err != nil {
		t.Fatalf("the vector does not verify: %v", err)
	}
	want := &Digest{MsgType: TypeAlert, Source: SourceMeteoAlarmNL,
		ID:   idOf("2.49.0.0.528.0.NL.20261028101141.6ddd5a99-421a-4c94-b9ea-25fa0ddcdd48_2"),
		Sent: time.Date(2026, 10, 28, 10, 11, 41, 0, time.UTC), Expires: time.Date(2026, 10, 29, 10, 11, 41, 0, time.UTC),
		Severity: 2, Urgency: 2, Certainty: 4, Category: 1, Event: 1, Language: "nl",
		Geocodes: []Geocode{{Type: GeocodeEMMAID, Value: "NL804"}}, Text: "Kans op windstoten"}
	if d.MsgType != want.MsgType || d.Test || d.Source != want.Source || d.ID != want.ID || d.Ref != [8]byte{} ||
		!d.Sent.Equal(want.Sent) || !d.Expires.Equal(want.Expires) || d.Severity != 2 || d.Urgency != 2 || d.Certainty != 4 ||
		d.Category != 1 || d.Event != 1 || d.Language != "nl" || len(d.Geocodes) != 1 || d.Geocodes[0] != want.Geocodes[0] ||
		d.Text != want.Text || len(d.Polygon) != 0 {
		t.Fatalf("decoded %+v\nwant    %+v", d, want)
	}
	if d.IDHex() != "f161d743dd14e974" || d.RefHex() != "" || d.TypeName() != "Alert" {
		t.Fatalf("names: %s %q %s", d.IDHex(), d.RefHex(), d.TypeName())
	}
}

func TestARefusedDigestNeverDecodes(t *testing.T) {
	wire, _ := hex.DecodeString(vectorHex)
	pub, _ := hex.DecodeString(testKeyPKIXHex)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherPub, _ := x509.MarshalPKIXPublicKey(&other.PublicKey)
	cases := []struct {
		name string
		pub  []byte
		mut  func([]byte) []byte
		want error
	}{
		{"no key pinned", nil, func(b []byte) []byte { return b }, ErrNoKey},
		{"another key", otherPub, func(b []byte) []byte { return b }, ErrSignature},
		{"a changed body byte", pub, func(b []byte) []byte { b[40] ^= 1; return b }, ErrSignature},
		{"a changed signature byte", pub, func(b []byte) []byte { b[len(b)-5] ^= 1; return b }, ErrSignature},
		{"no signature", pub, func(b []byte) []byte { return b[:len(b)-SignatureBytes] }, ErrBadDigest},
		{"too long", pub, func(b []byte) []byte { return append(b, make([]byte, MaxDigestBytes)...) }, ErrBadDigest},
		// The signature covers the magic, so a changed one fails there first.
		{"wrong magic", pub, func(b []byte) []byte { b[0] = 0x01; return b }, ErrSignature},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := c.mut(append([]byte(nil), wire...))
			d, err := Verify(c.pub, b)
			if d != nil || err == nil {
				t.Fatalf("decoded: %+v %v", d, err)
			}
			if c.want != nil && err != c.want && !isWrapped(err, c.want) {
				t.Fatalf("error %v, want %v", err, c.want)
			}
		})
	}
}

func isWrapped(err, target error) bool {
	for e := err; e != nil; {
		if e == target {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

func TestEveryShapeRoundTrips(t *testing.T) {
	key := testKey(t)
	pub := testPub(t)
	cases := []*Digest{
		{MsgType: TypeUpdate, Test: true, Source: SourceDWD, ID: idOf("a"), Ref: idOf("b"), Sent: time.Unix(1793187780, 0).UTC(),
			Severity: 4, Urgency: 3, Certainty: 3, Category: 1, Event: 4, Language: "de", Text: "Amtliche Warnung vor Gewitter",
			Geocodes: []Geocode{{1, "501000005"}, {1, "105758000"}}},
		{MsgType: TypeCancel, Source: SourceMeteoAlarmNL, ID: idOf("c"), Ref: idOf("a"), Sent: time.Unix(1793189000, 0).UTC(), Event: 1, Language: "en",
			Polygon: []Point{{52.9967, 4.757}, {52.767, 4.6428}, {53.795, 4.579}, {53.4071, 5.3781}}},
		{MsgType: TypeAlert, Source: SourceTest, Sent: time.Unix(1, 0).UTC()},
	}
	for i, c := range cases {
		wire := encode(t, c, key)
		if len(wire) > MaxDigestBytes {
			t.Fatalf("%d: %d bytes", i, len(wire))
		}
		d, err := Verify(pub, wire)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if d.MsgType != c.MsgType || d.Test != c.Test || d.Source != c.Source || d.ID != c.ID || d.Ref != c.Ref || !d.Sent.Equal(c.Sent) ||
			d.Event != c.Event || d.Language != c.Language || d.Text != c.Text || len(d.Geocodes) != len(c.Geocodes) || len(d.Polygon) != len(c.Polygon) {
			t.Fatalf("%d: %+v", i, d)
		}
		for j, p := range d.Polygon {
			if dl, dn := p.Lat-c.Polygon[j].Lat, p.Lon-c.Polygon[j].Lon; dl > 0.0001 || dl < -0.0001 || dn > 0.0001 || dn < -0.0001 {
				t.Fatalf("%d: point %d moved: %v", i, j, p)
			}
		}
	}
}
