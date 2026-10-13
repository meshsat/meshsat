package smpp

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPDURoundTrip(t *testing.T) {
	bindBody, err := EncodeBind(Bind{SystemID: "meshsat-bridge", Password: "s3cret", Version: Version})
	if err != nil {
		t.Fatal(err)
	}
	frame := Encode(CmdBindTransceiver, 0, 7, bindBody)
	h, err := DecodeHeader(frame)
	if err != nil || h.Length != uint32(len(frame)) || h.ID != CmdBindTransceiver || h.Sequence != 7 {
		t.Fatalf("header %+v err %v", h, err)
	}
	b, err := DecodeBind(frame[16:])
	if err != nil || b.SystemID != "meshsat-bridge" || b.Password != "s3cret" || b.Version != Version {
		t.Fatalf("bind %+v err %v", b, err)
	}

	sm := SM{ServiceType: "CMT", SourceTON: TONNetworkSpecific, SourceNPI: NPIISDN, SourceAddr: "2002",
		DestTON: 1, DestNPI: 1, DestAddr: "1000", ESMClass: ESMModeForward | ESMUDHI, DataCoding: DCSUCS2,
		RegisteredDelivery: 1, ShortMessage: []byte{0x05, 0x00, 0x03, 0x2a, 0x02, 0x01, 0x00, 0x59},
		TLVs: []TLV{U16TLV(TagUserMessageReference, 0xBEEF)}}
	body, err := EncodeSM(sm)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSM(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceAddr != "2002" || got.DestAddr != "1000" || got.ESMClass != sm.ESMClass || got.DataCoding != DCSUCS2 ||
		got.RegisteredDelivery != 1 || !bytes.Equal(got.ShortMessage, sm.ShortMessage) || !got.HasUDH() {
		t.Fatalf("sm %+v", got)
	}
	if ref, ok := got.UserMessageReference(); !ok || ref != 0xBEEF {
		t.Fatalf("user_message_reference %x %v", ref, ok)
	}
	if !(SM{ESMClass: ESMTypeDeliveryReceipt}).IsReceipt() || (SM{ESMClass: ESMModeDatagram}).IsReceipt() {
		t.Fatal("receipt bit")
	}

	a, err := DecodeAlert(EncodeAlert(Alert{SourceTON: 3, SourceNPI: 1, SourceAddr: "1001",
		TLVs: []TLV{{Tag: TagMSAvailabilityStatus, Value: []byte{2}}}}))
	if err != nil || a.SourceAddr != "1001" || a.Availability() != 2 {
		t.Fatalf("alert %+v err %v", a, err)
	}
	if (Alert{}).Availability() != -1 {
		t.Fatal("absent availability must be -1")
	}
	if r, err := DecodeSMResp(nil); err != nil || r.MessageID != "" {
		t.Fatalf("empty resp %+v %v", r, err)
	}
}

func TestPDULimits(t *testing.T) {
	if _, err := EncodeBind(Bind{SystemID: "sixteen-chars-xx", Password: "x"}); err == nil {
		t.Fatal("a 16-character system_id must be refused (15 max)")
	}
	if _, err := EncodeBind(Bind{SystemID: "x", Password: "ninechars"}); err == nil {
		t.Fatal("a 9-character password must be refused (8 max)")
	}
	if _, err := EncodeSM(SM{ShortMessage: make([]byte, 255)}); err == nil {
		t.Fatal("255-byte short_message must be refused")
	}
	if _, err := DecodeSM([]byte{0}); err == nil {
		t.Fatal("truncated body must fail")
	}
}

// What osmo-msc puts on the wire for data_coding 0x00/0x01: septet values.
func TestGSM7(t *testing.T) {
	cases := []struct {
		text string
		want []byte
	}{
		{"Yo", []byte{0x59, 0x6F}},
		{"@", []byte{0x00}},
		{"£$¥", []byte{0x01, 0x02, 0x03}},
		{"èéùìòÇ", []byte{0x04, 0x05, 0x06, 0x07, 0x08, 0x09}},
		{"ΔΦΓΛΩΠΨΣΘΞ", []byte{0x10, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A}},
		{"_", []byte{0x11}},
		{"[", []byte{0x1B, 0x3C}},
		{"]{}\\^~|€\f", []byte{0x1B, 0x3E, 0x1B, 0x28, 0x1B, 0x29, 0x1B, 0x2F, 0x1B, 0x14, 0x1B, 0x3D, 0x1B, 0x40, 0x1B, 0x65, 0x1B, 0x0A}},
		{"ÄÖÑÜ§¿äöñüà", []byte{0x5B, 0x5C, 0x5D, 0x5E, 0x5F, 0x60, 0x7B, 0x7C, 0x7D, 0x7E, 0x7F}},
		{"a\nb\r", []byte{0x61, 0x0A, 0x62, 0x0D}},
	}
	for _, c := range cases {
		got, err := EncodeGSM7(c.text)
		if err != nil || !bytes.Equal(got, c.want) {
			t.Errorf("encode %q = % x (%v), want % x", c.text, got, err, c.want)
		}
		if back := DecodeGSM7(c.want); back != c.text {
			t.Errorf("decode % x = %q, want %q", c.want, back, c.text)
		}
	}
	if _, err := EncodeGSM7("αβγ"); err == nil {
		t.Fatal("Greek lowercase is not in GSM 03.38")
	}
	if IsGSM7("snowman ☃") {
		t.Fatal("☃ is not GSM 03.38")
	}
	if n, ok := GSM7Septets("[ab]"); !ok || n != 6 {
		t.Fatalf("septets = %d %v, want 6", n, ok)
	}
	// A dangling escape decodes as a space; an unknown extension code too.
	if got := DecodeGSM7([]byte{0x61, 0x1B}); got != "a " {
		t.Fatalf("dangling escape = %q", got)
	}
	if got := DecodeGSM7([]byte{0x1B, 0x61, 0x62}); got != " ab" {
		t.Fatalf("unknown extension = %q", got)
	}
}

func TestUCS2(t *testing.T) {
	text := "Γειά σου κόσμε 🙂"
	enc := EncodeUCS2(text)
	if enc[0] != 0x03 || enc[1] != 0x93 { // Γ = U+0393, big-endian
		t.Fatalf("not big-endian: % x", enc[:2])
	}
	if DecodeUCS2(enc) != text {
		t.Fatal("round trip")
	}
	if UCS2Units("🙂") != 2 {
		t.Fatal("an emoji is two code units")
	}
	if DecodeText(DCSUCS2, enc) != text || DecodeText(DCSGSM7, []byte{0x00}) != "@" ||
		DecodeText(DCS8Bit, []byte("id:00000001")) != "id:00000001" || DecodeText(0xF0, []byte{0x59, 0x6F}) != "Yo" {
		t.Fatal("DecodeText by data_coding")
	}
}

func TestSplitText(t *testing.T) {
	one := strings.Repeat("a", 160)
	parts, err := SplitText(one, 9)
	if err != nil || len(parts) != 1 || parts[0].UDH || parts[0].DataCoding != DCSDefault || len(parts[0].ShortMessage) != 160 {
		t.Fatalf("160 GSM chars must be one part: %d parts %v", len(parts), err)
	}
	parts, err = SplitText(one+"b", 9)
	if err != nil || len(parts) != 2 || !parts[0].UDH || len(parts[0].ShortMessage) != 6+153 || len(parts[1].ShortMessage) != 6+8 {
		t.Fatalf("161 GSM chars must be two parts of 153+8: %v", err)
	}
	if udh, body, err := SplitUDH(parts[0].ShortMessage); err != nil || !bytes.Equal(udh, []byte{0x00, 0x03, 9, 2, 1}) || len(body) != 153 {
		t.Fatalf("part 1 header: % x body %d err %v", udh, len(body), err)
	}
	if c, ok := ParseConcat([]byte{0x00, 0x03, 9, 2, 2}); !ok || c.Ref != 9 || c.Total != 2 || c.Seq != 2 {
		t.Fatalf("concat %+v %v", c, ok)
	}
	if c, ok := ParseConcat([]byte{0x08, 0x04, 0x12, 0x34, 3, 1}); !ok || c.Ref != 0x1234 || c.Total != 3 {
		t.Fatalf("16-bit concat %+v %v", c, ok)
	}
	if _, ok := ParseConcat([]byte{0x24, 0x01, 0x00}); ok {
		t.Fatal("a header without a concat element has none")
	}
	// An extension character never straddles two parts: 152 'a' + '[' +
	// 10 'b' is 164 septets; the escape would be septet 153 of part 1, so
	// it moves whole to part 2 and part 1 carries 152.
	parts, err = SplitText(strings.Repeat("a", 152)+"["+strings.Repeat("b", 10), 1)
	if err != nil || len(parts) != 2 || len(parts[0].ShortMessage) != 6+152 || !bytes.Equal(parts[1].ShortMessage[6:8], []byte{0x1B, 0x3C}) || len(parts[1].ShortMessage) != 6+12 {
		t.Fatalf("escape straddled: %d parts, %d and %d bytes, %v", len(parts), len(parts[0].ShortMessage), len(parts[len(parts)-1].ShortMessage), err)
	}
	// UCS-2: 70 units single, 71 units two parts of 67 + 4.
	gr := strings.Repeat("Ω", 69) + "ώ"
	parts, err = SplitText(gr, 1)
	if err != nil || len(parts) != 1 || parts[0].DataCoding != DCSUCS2 || len(parts[0].ShortMessage) != 140 {
		t.Fatalf("70 UCS-2 units must be one part: %v", err)
	}
	parts, err = SplitText(gr+"ς", 1)
	if err != nil || len(parts) != 2 || len(parts[0].ShortMessage) != 6+134 || len(parts[1].ShortMessage) != 6+8 {
		t.Fatalf("71 UCS-2 units must be 67+4: %v", err)
	}
	// A surrogate pair is never split: 66 BMP chars, an emoji (units 67
	// and 68) and five more make 73 units; part 1 would end inside the
	// pair, so it carries 66 and the emoji opens part 2.
	parts, err = SplitText(strings.Repeat("ώ", 66)+"🙂"+strings.Repeat("x", 5), 1)
	if err != nil || len(parts) != 2 || len(parts[0].ShortMessage) != 6+132 || len(parts[1].ShortMessage) != 6+14 {
		t.Fatalf("surrogate split: %d parts, first %d bytes, %v", len(parts), len(parts[0].ShortMessage), err)
	}
	if _, err := SplitText("", 1); err == nil {
		t.Fatal("empty text")
	}
	for _, p := range parts {
		if len(p.ShortMessage) > MaxShortMessage {
			t.Fatal("a part over 254 bytes")
		}
	}
}

func TestReassembler(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewReassembler(10 * time.Minute)
	r.now = func() time.Time { return now }
	if _, done := r.Add("1001", Concat{Ref: 5, Total: 3, Seq: 2}, DCSDefault, []byte("B")); done {
		t.Fatal("one of three is not done")
	}
	if _, done := r.Add("1001", Concat{Ref: 5, Total: 3, Seq: 1}, DCSDefault, []byte("A")); done {
		t.Fatal("two of three is not done")
	}
	// Another sender's part with the same reference is a different series.
	if _, done := r.Add("2002", Concat{Ref: 5, Total: 3, Seq: 3}, DCSDefault, []byte("Z")); done {
		t.Fatal("other sender")
	}
	whole, done := r.Add("1001", Concat{Ref: 5, Total: 3, Seq: 3}, DCSDefault, []byte("C"))
	if !done || string(whole) != "ABC" {
		t.Fatalf("reassembled %q %v", whole, done)
	}
	if r.Pending() != 1 {
		t.Fatalf("pending %d, want the other sender's", r.Pending())
	}
	now = now.Add(11 * time.Minute)
	r.Add("3003", Concat{Ref: 1, Total: 2, Seq: 1}, DCSDefault, []byte("x"))
	if r.Pending() != 1 {
		t.Fatalf("the stale series must be dropped, pending %d", r.Pending())
	}
}
