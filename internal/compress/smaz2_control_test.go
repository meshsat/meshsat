package compress

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/lib-x/smaz2"
)

// Bytes 0x01-0x08 survive the default table now, in any position and run, and
// the plain lib-x decoder reads the result: nothing changes on the receiving
// side. [MESHSAT-1626]
func TestSmaz2DefaultRoundTripsControlBytes(t *testing.T) {
	cases := [][]byte{
		[]byte("a\x01b\x02c\x03d\x04e\x05f\x06g\x07h\x08i"),
		[]byte("\x01\x02\x03\x04\x05\x06\x07\x08"),
		[]byte("\x08the station\x01 reports\x06 all clear\x07"),
		[]byte("caf\xc3\xa9\x05ok"),
		[]byte("\x01"),
	}
	for _, in := range cases {
		c := Compress(in, DictDefault)
		back, err := Decompress(c, DictDefault)
		if err != nil || !bytes.Equal(back, in) {
			t.Fatalf("%q -> %x -> %q (%v)", in, c, back, err)
		}
		if lib, err := smaz2.DecompressToBytes(c); err != nil || !bytes.Equal(lib, in) {
			t.Fatalf("lib-x reads %x as %q (%v), want %q", c, lib, err, in)
		}
	}
}

// Text without such a byte compresses exactly as lib-x does: the SMS receivers
// take a payload as compressed only if compressing its text again gives the
// same bytes, so the senders must not drift. [MESHSAT-1626]
func TestSmaz2DefaultUnchangedWithoutControlBytes(t *testing.T) {
	rng := rand.New(rand.NewSource(1626))
	alphabet := []byte("abcdefghijklmnopqrstuvwxyz ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.,:;!?'\"\t\n\r-\x00\x09\x7f\x80\xc3\xa9\xce\xbb\xff")
	texts := [][]byte{[]byte("the message from the station is that all are well"), []byte("Καλημέρα από το σκάφος"), {}}
	for i := 0; i < 2000; i++ {
		n := rng.Intn(80)
		b := make([]byte, n)
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		texts = append(texts, b)
	}
	for _, in := range texts {
		if got, want := Compress(in, DictDefault), smaz2.CompressBytes(in); len(in) > 0 && !bytes.Equal(got, want) {
			t.Fatalf("%q: %x, lib-x %x", in, got, want)
		}
	}
}
