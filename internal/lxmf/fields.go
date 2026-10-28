package lxmf

import (
	"errors"
	"fmt"
	"strings"
)

// The LXMF fields a voice note and a photo travel in (LXMF/LXMF.py):
// FIELD_AUDIO is [mode, bytes] with the audio mode below, FIELD_IMAGE is
// [format, bytes] with the format name ("webp"). They are what Sideband and
// MeshChat send, so a note or a photo from either plays here and one from
// here plays there, with nothing MeshSat-specific on the wire. The fields
// are part of the packed message: a message that outgrows one packet goes as
// a resource with them inside, as upstream does. [MESHSAT-1735]
const (
	FieldImage = 0x06
	FieldAudio = 0x07
)

// Audio modes (LXMF.py AM_*): Codec2 modes 0x01-0x09 (700C, the default
// for a satellite, is 0x03), Opus modes 0x10-0x19 (the bytes are an Ogg
// Opus stream), and 0xFF for a custom codec the two ends agreed on.
const (
	AudioModeCodec2First = 0x01
	AudioMode700C        = 0x03
	AudioModeCodec2Last  = 0x09
	AudioModeOpusFirst   = 0x10
	AudioModeOpusLast    = 0x19
	AudioModeCustom      = 0xFF
)

// Attachment kinds and the one image format every client writes.
const (
	AttachmentAudio = "audio"
	AttachmentImage = "image"
	ImageWebP       = "webp"
)

// MaxAttachmentBytes bounds what one message's attachments may weigh
// together: a 10 s Codec2 700C note is about 1 KB and a 320 px WebP a few
// KB, so 64 KB holds every note and photo a kit or the Hub should carry and
// keeps a store of them bounded by count.
const MaxAttachmentBytes = 64 * 1024

// ErrAttachmentsTooLarge: the attachments together exceed MaxAttachmentBytes.
var ErrAttachmentsTooLarge = fmt.Errorf("lxmf: attachments exceed %d bytes", MaxAttachmentBytes)

// Attachment is one audio or image field of a message.
type Attachment struct {
	Kind   string // AttachmentAudio or AttachmentImage
	Mode   int    // audio: the LXMF audio mode
	Format string // image: the format name, lower case ("webp")
	Data   []byte
}

var codec2Names = map[int]string{
	0x01: "450PWB", 0x02: "450", 0x03: "700C", 0x04: "1200", 0x05: "1300", 0x06: "1400", 0x07: "1600", 0x08: "2400", 0x09: "3200",
}

var opusNames = map[int]string{
	0x10: "OGG", 0x11: "LBW", 0x12: "MBW", 0x13: "PTT", 0x14: "RT_HDX", 0x15: "RT_FDX", 0x16: "STANDARD", 0x17: "HQ", 0x18: "BROADCAST", 0x19: "LOSSLESS",
}

// AudioModeName is the mode's name as LXMF.py spells it ("Codec2 700C",
// "Opus OGG", "custom"), or "mode 0x.." for one it does not know.
func AudioModeName(mode int) string {
	if n, ok := codec2Names[mode]; ok {
		return "Codec2 " + n
	}
	if n, ok := opusNames[mode]; ok {
		return "Opus " + n
	}
	if mode == AudioModeCustom {
		return "custom"
	}
	return fmt.Sprintf("mode 0x%02x", mode)
}

// AudioModeKnown reports whether mode is one LXMF.py names.
func AudioModeKnown(mode int) bool {
	_, c := codec2Names[mode]
	_, o := opusNames[mode]
	return c || o || mode == AudioModeCustom
}

// Validate checks the attachment's shape: a known kind, a known audio mode
// or a plain image format name, and some bytes.
func (a Attachment) Validate() error {
	if len(a.Data) == 0 {
		return errors.New("lxmf: attachment has no bytes")
	}
	switch a.Kind {
	case AttachmentAudio:
		if !AudioModeKnown(a.Mode) {
			return fmt.Errorf("lxmf: unknown audio mode 0x%02x", a.Mode)
		}
	case AttachmentImage:
		if a.Format == "" || len(a.Format) > 8 {
			return errors.New("lxmf: image format must be 1 to 8 characters")
		}
		for _, r := range a.Format {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return errors.New("lxmf: image format must be lower-case letters and digits")
			}
		}
	default:
		return fmt.Errorf("lxmf: unknown attachment kind %q", a.Kind)
	}
	return nil
}

// ContentType is the media type a client plays or shows the bytes as: Codec2
// frames as audio/codec2 (the mode goes in a header, the bytes carry none),
// Opus as the Ogg stream it is, an image as image/<format>.
func (a Attachment) ContentType() string {
	switch a.Kind {
	case AttachmentAudio:
		switch {
		case a.Mode >= AudioModeCodec2First && a.Mode <= AudioModeCodec2Last:
			return "audio/codec2"
		case a.Mode >= AudioModeOpusFirst && a.Mode <= AudioModeOpusLast:
			return "audio/ogg"
		}
	case AttachmentImage:
		switch a.Format {
		case ImageWebP, "png", "gif", "bmp", "avif":
			return "image/" + a.Format
		case "jpg", "jpeg":
			return "image/jpeg"
		}
	}
	return "application/octet-stream"
}

// Describe is a short line for a log or a ledger row: "audio Codec2 700C
// 1024 B", "image webp 2048 B".
func (a Attachment) Describe() string {
	switch a.Kind {
	case AttachmentAudio:
		return fmt.Sprintf("audio %s %d B", AudioModeName(a.Mode), len(a.Data))
	case AttachmentImage:
		return fmt.Sprintf("image %s %d B", a.Format, len(a.Data))
	}
	return fmt.Sprintf("%s %d B", a.Kind, len(a.Data))
}

// DescribeAttachments joins the descriptions with ", "; "" for none.
func DescribeAttachments(atts []Attachment) string {
	parts := make([]string, 0, len(atts))
	for _, a := range atts {
		parts = append(parts, a.Describe())
	}
	return strings.Join(parts, ", ")
}

// AttachmentBytes is what the attachments weigh together.
func AttachmentBytes(atts []Attachment) int {
	n := 0
	for _, a := range atts {
		n += len(a.Data)
	}
	return n
}

// AttachmentsOf reads the audio and image fields of a received message, the
// image first (field order), each only when it has the documented shape; a
// field of another shape is left where it is and not reported.
func AttachmentsOf(fields Value) []Attachment {
	if fields.Kind != KindMap {
		return nil
	}
	var out []Attachment
	if v, ok := fields.Get(FieldImage); ok && v.Kind == KindArray && len(v.Array) == 2 {
		format := strings.ToLower(string(binOrStr(v.Array[0])))
		if v.Array[1].Kind == KindBin && len(v.Array[1].Bin) > 0 && format != "" {
			out = append(out, Attachment{Kind: AttachmentImage, Format: format, Data: v.Array[1].Bin})
		}
	}
	if v, ok := fields.Get(FieldAudio); ok && v.Kind == KindArray && len(v.Array) == 2 {
		mode, modeOK := v.Array[0].AsInt()
		if modeOK && v.Array[1].Kind == KindBin && len(v.Array[1].Bin) > 0 {
			out = append(out, Attachment{Kind: AttachmentAudio, Mode: int(mode), Data: v.Array[1].Bin})
		}
	}
	return out
}

// WithAttachments puts the attachments into a fields map (nil becomes a
// map), one audio and one image at most, replacing a field of the same
// kind. Every attachment is validated and their total must fit
// MaxAttachmentBytes; an error leaves the input as it was. A value that is
// neither a map nor nil is returned unchanged with an error.
func WithAttachments(fields Value, atts ...Attachment) (Value, error) {
	if len(atts) == 0 {
		return fields, nil
	}
	switch fields.Kind {
	case KindNil:
		fields = MapOf()
	case KindMap:
	default:
		return fields, errors.New("lxmf: fields is not a map")
	}
	if AttachmentBytes(atts) > MaxAttachmentBytes {
		return fields, ErrAttachmentsTooLarge
	}
	seen := map[string]bool{}
	for _, a := range atts {
		if err := a.Validate(); err != nil {
			return fields, err
		}
		if seen[a.Kind] {
			return fields, fmt.Errorf("lxmf: more than one %s attachment", a.Kind)
		}
		seen[a.Kind] = true
	}
	out := WithoutAttachments(fields)
	for _, a := range atts {
		switch a.Kind {
		case AttachmentAudio:
			out.Map = append(out.Map, KV{Key: Int(FieldAudio), Val: Array(Int(int64(a.Mode)), Bin(a.Data))})
		case AttachmentImage:
			out.Map = append(out.Map, KV{Key: Int(FieldImage), Val: Array(Str(a.Format), Bin(a.Data))})
		}
	}
	return out, nil
}

// WithoutAttachments is the fields map without its audio and image fields;
// a value that is not a map is returned as it is.
func WithoutAttachments(fields Value) Value {
	if fields.Kind != KindMap {
		return fields
	}
	out := make([]KV, 0, len(fields.Map))
	for _, kv := range fields.Map {
		if k, ok := kv.Key.AsInt(); ok && (k == FieldAudio || k == FieldImage) {
			continue
		}
		out = append(out, kv)
	}
	return MapOf(out...)
}
