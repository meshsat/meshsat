package lxmf

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// The smallest valid WebP: a 1x1 lossless (VP8L) image, 34 bytes.
var testWebP = []byte{
	0x52, 0x49, 0x46, 0x46, 0x1a, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50, 0x56, 0x50, 0x38, 0x4c,
	0x0d, 0x00, 0x00, 0x00, 0x2f, 0x00, 0x00, 0x00, 0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe, 0x07, 0x00,
}

var testFrames = []byte{0, 1, 2, 3, 4, 5, 6, 7, 0, 1, 2, 3, 4, 5, 6, 7}

// What LXMF.py's umsgpack packs for {FIELD_IMAGE: ["webp", webp],
// FIELD_AUDIO: [AM_CODEC2_700C, frames]} (computed under the pinned venv):
// the bytes this code must write and must read. [MESHSAT-1735]
const goldenImageThenAudio = "820692a477656270c422524946461a000000574542505650384c0d0000002f00000010071011118888fe0700079203c41000010203040506070001020304050607"

// The same two fields the other way round, as a sender that adds audio first
// packs them.
const goldenAudioThenImage = "82079203c410000102030405060700010203040506070692a477656270c422524946461a000000574542505650384c0d0000002f00000010071011118888fe0700"

func TestAttachmentsPackLikeUpstream(t *testing.T) {
	fields, err := WithAttachments(Nil(), Attachment{Kind: AttachmentImage, Format: "webp", Data: testWebP}, Attachment{Kind: AttachmentAudio, Mode: AudioMode700C, Data: testFrames})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(Pack(fields)); got != goldenImageThenAudio {
		t.Fatalf("packed\n%s\nwant\n%s", got, goldenImageThenAudio)
	}
	for _, golden := range []string{goldenImageThenAudio, goldenAudioThenImage} {
		raw, _ := hex.DecodeString(golden)
		v, n, err := UnpackValue(raw)
		if err != nil || n != len(raw) {
			t.Fatalf("unpack: %v (%d of %d)", err, n, len(raw))
		}
		atts := AttachmentsOf(v)
		if len(atts) != 2 || atts[0].Kind != AttachmentImage || atts[0].Format != "webp" || !bytes.Equal(atts[0].Data, testWebP) ||
			atts[1].Kind != AttachmentAudio || atts[1].Mode != AudioMode700C || !bytes.Equal(atts[1].Data, testFrames) {
			t.Fatalf("read %+v", atts)
		}
	}
}

func TestAttachmentsKeepTheOtherFields(t *testing.T) {
	var mid [16]byte
	mid[0] = 7
	fields, err := WithAttachments(MidFields(mid), Attachment{Kind: AttachmentAudio, Mode: AudioMode700C, Data: testFrames})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := MidOf(fields); !ok || got != mid {
		t.Fatal("the mid did not survive")
	}
	// Replacing the audio keeps one audio field.
	fields, err = WithAttachments(fields, Attachment{Kind: AttachmentAudio, Mode: 0x10, Data: []byte("ogg")})
	if err != nil {
		t.Fatal(err)
	}
	atts := AttachmentsOf(fields)
	if len(atts) != 1 || atts[0].Mode != 0x10 || len(fields.Map) != 3 {
		t.Fatalf("after replace: %+v, %d fields", atts, len(fields.Map))
	}
	bare := WithoutAttachments(fields)
	if len(AttachmentsOf(bare)) != 0 || len(bare.Map) != 2 {
		t.Fatalf("without: %d fields", len(bare.Map))
	}
	if _, ok := MidOf(bare); !ok {
		t.Fatal("the mid went with the attachments")
	}
	// The input was not touched.
	if len(AttachmentsOf(fields)) != 1 {
		t.Fatal("WithoutAttachments changed its input")
	}
}

func TestAttachmentsRefused(t *testing.T) {
	ok := Attachment{Kind: AttachmentAudio, Mode: AudioMode700C, Data: testFrames}
	for name, tc := range map[string]struct {
		fields Value
		atts   []Attachment
		want   error
	}{
		"too large":  {Nil(), []Attachment{{Kind: AttachmentImage, Format: "webp", Data: make([]byte, MaxAttachmentBytes+1)}}, ErrAttachmentsTooLarge},
		"together":   {Nil(), []Attachment{{Kind: AttachmentImage, Format: "webp", Data: make([]byte, MaxAttachmentBytes-1)}, {Kind: AttachmentAudio, Mode: 3, Data: []byte{1, 2}}}, ErrAttachmentsTooLarge},
		"two audios": {Nil(), []Attachment{ok, ok}, nil},
		"bad mode":   {Nil(), []Attachment{{Kind: AttachmentAudio, Mode: 0x0a, Data: testFrames}}, nil},
		"no bytes":   {Nil(), []Attachment{{Kind: AttachmentAudio, Mode: 3}}, nil},
		"bad format": {Nil(), []Attachment{{Kind: AttachmentImage, Format: "WebP", Data: testWebP}}, nil},
		"long":       {Nil(), []Attachment{{Kind: AttachmentImage, Format: strings.Repeat("a", 9), Data: testWebP}}, nil},
		"kind":       {Nil(), []Attachment{{Kind: "file", Data: testWebP}}, nil},
		"not a map":  {Str("x"), []Attachment{ok}, nil},
	} {
		_, err := WithAttachments(tc.fields, tc.atts...)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Exactly the cap is fine, and no attachments leaves the input alone.
	if _, err := WithAttachments(Nil(), Attachment{Kind: AttachmentImage, Format: "webp", Data: make([]byte, MaxAttachmentBytes)}); err != nil {
		t.Fatal(err)
	}
	if v, err := WithAttachments(Str("x")); err != nil || v.Kind != KindStr {
		t.Fatal("no attachments must be a no-op")
	}
}

// A field of another shape is not an attachment: it stays in the map and is
// not reported.
func TestAttachmentsOfIgnoresOtherShapes(t *testing.T) {
	fields := MapOf(
		KV{Key: Int(FieldAudio), Val: Bin(testFrames)},
		KV{Key: Int(FieldImage), Val: Array(Str("webp"))},
		KV{Key: Str("7"), Val: Array(Int(3), Bin(testFrames))},
	)
	if atts := AttachmentsOf(fields); len(atts) != 0 {
		t.Fatalf("read %+v", atts)
	}
	if AttachmentsOf(Nil()) != nil || AttachmentsOf(Str("x")) != nil {
		t.Fatal("non-map fields")
	}
	// An image format sent as bytes, upper case, is read all the same.
	v := MapOf(KV{Key: Int(FieldImage), Val: Array(Bin([]byte("WEBP")), Bin(testWebP))})
	if atts := AttachmentsOf(v); len(atts) != 1 || atts[0].Format != "webp" {
		t.Fatalf("bytes format: %+v", atts)
	}
}

func TestAttachmentNames(t *testing.T) {
	a := Attachment{Kind: AttachmentAudio, Mode: AudioMode700C, Data: testFrames}
	if a.ContentType() != "audio/codec2" || a.Describe() != "audio Codec2 700C 16 B" {
		t.Fatalf("%s %s", a.ContentType(), a.Describe())
	}
	o := Attachment{Kind: AttachmentAudio, Mode: 0x16, Data: testFrames}
	if o.ContentType() != "audio/ogg" || AudioModeName(0x16) != "Opus STANDARD" {
		t.Fatalf("%s %s", o.ContentType(), AudioModeName(0x16))
	}
	i := Attachment{Kind: AttachmentImage, Format: "webp", Data: testWebP}
	if i.ContentType() != "image/webp" || i.Describe() != "image webp 34 B" {
		t.Fatalf("%s %s", i.ContentType(), i.Describe())
	}
	if (Attachment{Kind: AttachmentImage, Format: "jpg"}).ContentType() != "image/jpeg" || (Attachment{Kind: AttachmentImage, Format: "tiff"}).ContentType() != "application/octet-stream" {
		t.Fatal("image content types")
	}
	if AudioModeName(0xff) != "custom" || AudioModeName(0x0a) != "mode 0x0a" || AudioModeKnown(0x0a) {
		t.Fatal("mode names")
	}
	if DescribeAttachments([]Attachment{i, a}) != "image webp 34 B, audio Codec2 700C 16 B" || DescribeAttachments(nil) != "" || AttachmentBytes([]Attachment{i, a}) != 50 {
		t.Fatal("describe")
	}
}
