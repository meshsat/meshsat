package oob

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// testKey is the fixed vector key: bytes 0x01..0x20.
func testKey() []byte {
	k := make([]byte, KeyLen)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func mustSeal(t *testing.T, f Frame, role Role) []byte {
	t.Helper()
	wire, err := Seal(f, testKey(), role)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return wire
}

func TestSealOpen_RoundTrips(t *testing.T) {
	key := testKey()
	peer := PeerIDFromKey(key)
	args73 := bytes.Repeat([]byte{0xAB}, MaxArgs)

	cases := []struct {
		name     string
		frame    Frame
		wantLen  int
		sender   Role
		receiver Role
	}{
		{"ping_clear_request", Frame{PeerID: peer, Counter: 1, Cmd: CmdPing}, MinFrameLen, RoleIssuer, RoleIssuer},
		{"ping_enc_request", Frame{Enc: true, PeerID: peer, Counter: 2, Cmd: CmdPing}, MinFrameLen, RoleIssuer, RoleIssuer},
		{"reboot_enc_request", Frame{Enc: true, PeerID: peer, Counter: 3, Cmd: CmdReboot, Args: EncodeRebootArgs(10)}, MinFrameLen + 2, RoleImporter, RoleImporter},
		{"reply_ok_enc", Frame{Enc: true, Reply: true, PeerID: peer, Counter: 4, Cmd: CmdPing,
			Args: EncodeReplyArgs(ReplyArgs{RC: RCOK, ReqCounterLo: 3, Seq: 1, Total: 1, Body: []byte("u17h b98A")})}, MinFrameLen + 5 + 9, RoleIssuer, RoleIssuer},
		{"noreply_clear", Frame{NoReply: true, PeerID: peer, Counter: 5, Cmd: CmdRestart}, MinFrameLen, RoleImporter, RoleImporter},
		{"roundtrip_max_args_73_clear", Frame{PeerID: peer, Counter: 6, Cmd: CmdLog, Args: args73}, MaxFrameLen, RoleIssuer, RoleIssuer},
		{"roundtrip_max_args_73_enc", Frame{Enc: true, PeerID: peer, Counter: 7, Cmd: CmdLog, Args: args73}, MaxFrameLen, RoleIssuer, RoleIssuer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wire := mustSeal(t, c.frame, c.sender)
			if len(wire) != c.wantLen {
				t.Fatalf("wire len %d, want %d", len(wire), c.wantLen)
			}
			got, err := Open(wire, key, c.receiver)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if got.Enc != c.frame.Enc || got.Reply != c.frame.Reply || got.NoReply != c.frame.NoReply ||
				got.PeerID != c.frame.PeerID || got.Counter != c.frame.Counter || got.Cmd != c.frame.Cmd {
				t.Fatalf("header mismatch: got %+v want %+v", got, c.frame)
			}
			if !bytes.Equal(got.Args, c.frame.Args) {
				t.Fatalf("args mismatch: got %x want %x", got.Args, c.frame.Args)
			}
			// Clear frames carry the args verbatim on the wire; encrypted ones must not.
			if len(c.frame.Args) > 0 {
				visible := bytes.Contains(wire, c.frame.Args)
				if c.frame.Enc && visible {
					t.Fatalf("encrypted args visible on the wire")
				}
				if !c.frame.Enc && !visible {
					t.Fatalf("clear args not on the wire")
				}
			}
		})
	}
}

func TestSeal_Rejects(t *testing.T) {
	key := testKey()
	peer := PeerIDFromKey(key)
	cases := []struct {
		name  string
		frame Frame
		key   []byte
		err   error
	}{
		{"args_74", Frame{PeerID: peer, Counter: 1, Cmd: CmdPing, Args: make([]byte, MaxArgs+1)}, key, ErrArgsLen},
		{"peer_zero", Frame{PeerID: 0, Counter: 1, Cmd: CmdPing}, key, ErrBadPeer},
		{"counter_zero", Frame{PeerID: peer, Counter: 0, Cmd: CmdPing}, key, ErrBadCounter},
		{"short_key", Frame{PeerID: peer, Counter: 1, Cmd: CmdPing}, key[:16], ErrBadKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Seal(c.frame, c.key, RoleIssuer); !errors.Is(err, c.err) {
				t.Fatalf("got %v want %v", err, c.err)
			}
		})
	}
}

func TestOpen_Rejects(t *testing.T) {
	key := testKey()
	peer := PeerIDFromKey(key)
	enc := mustSeal(t, Frame{Enc: true, PeerID: peer, Counter: 9, Cmd: CmdReboot, Args: EncodeRebootArgs(30)}, RoleIssuer)
	clear := mustSeal(t, Frame{PeerID: peer, Counter: 10, Cmd: CmdReboot, Args: EncodeRebootArgs(30)}, RoleIssuer)

	flip := func(w []byte, i int) []byte {
		out := append([]byte{}, w...)
		out[i] ^= 0x01
		return out
	}
	otherKey := testKey()
	otherKey[0] ^= 0xFF

	cases := []struct {
		name string
		wire []byte
		key  []byte
		role Role
		err  error
	}{
		{"tag_bit_flip_enc", flip(enc, len(enc)-1), key, RoleIssuer, ErrAuth},
		{"tag_bit_flip_clear", flip(clear, len(clear)-1), key, RoleIssuer, ErrAuth},
		{"body_bit_flip_enc", flip(enc, HeaderLen), key, RoleIssuer, ErrAuth},
		{"body_bit_flip_clear", flip(clear, HeaderLen), key, RoleIssuer, ErrAuth},
		{"header_counter_flip", flip(enc, 7), key, RoleIssuer, ErrAuth},
		{"reflected_as_reply", flip(enc, 1) /* toggles ENC bit */, key, RoleIssuer, ErrAuth},
		{"wrong_sender_role", enc, key, RoleImporter, ErrAuth},
		{"wrong_key", enc, otherKey, RoleIssuer, ErrAuth},
		{"too_short_24", enc[:MinFrameLen-1], key, RoleIssuer, ErrTooShort},
		{"bad_magic", flip(enc, 0), key, RoleIssuer, ErrBadMagic},
		{"bad_version", func() []byte { w := append([]byte{}, enc...); w[1] = (2 << 4) | (w[1] & 0x0F); return w }(), key, RoleIssuer, ErrBadVersion},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Open(c.wire, c.key, c.role); !errors.Is(err, c.err) {
				t.Fatalf("got %v want %v", err, c.err)
			}
		})
	}
	// A reply-flag flip is a reflection: the frame must not open as a reply.
	reflected := append([]byte{}, enc...)
	reflected[1] |= FlagReply
	if _, err := Open(reflected, key, RoleIssuer); !errors.Is(err, ErrAuth) {
		t.Fatalf("reflected reply opened: %v", err)
	}
}

func TestEncodeDecode(t *testing.T) {
	key := testKey()
	peer := PeerIDFromKey(key)

	t.Run("encode_length_sms_160", func(t *testing.T) {
		wire := mustSeal(t, Frame{Enc: true, PeerID: peer, Counter: 1, Cmd: CmdLog, Args: make([]byte, MaxArgs)}, RoleIssuer)
		text := Encode(wire)
		if len(text) != 160 {
			t.Fatalf("len %d want 160: %s", len(text), text)
		}
		got, err := Decode(text)
		if err != nil || !bytes.Equal(got, wire) {
			t.Fatalf("decode: %v", err)
		}
	})
	t.Run("encode_length_aprs_67", func(t *testing.T) {
		wire := mustSeal(t, Frame{Enc: true, PeerID: peer, Counter: 2, Cmd: CmdReset, Args: make([]byte, 15)}, RoleIssuer)
		text := Encode(wire)
		if len(text) != 67 {
			t.Fatalf("len %d want 67: %s", len(text), text)
		}
	})
	t.Run("encode_length_min_43", func(t *testing.T) {
		wire := mustSeal(t, Frame{PeerID: peer, Counter: 3, Cmd: CmdPing}, RoleIssuer)
		if text := Encode(wire); len(text) != 43 {
			t.Fatalf("len %d want 43", len(text))
		}
	})
	t.Run("decode_case_fold_and_ilo", func(t *testing.T) {
		wire := mustSeal(t, Frame{PeerID: peer, Counter: 4, Cmd: CmdPing, Args: []byte("hello")}, RoleIssuer)
		text := Encode(wire)
		mangled := strings.ToLower(text)
		mangled = strings.ReplaceAll(mangled, "1", "l")
		mangled = strings.ReplaceAll(mangled, "0", "o")
		// Voice-relay style grouping with hyphens.
		mangled = mangled[:8] + "-" + mangled[8:]
		got, err := Decode(mangled)
		if err != nil {
			t.Fatalf("decode mangled: %v (%s)", err, mangled)
		}
		if !bytes.Equal(got, wire) {
			t.Fatalf("mangled decode differs")
		}
	})
	t.Run("decode_rejects_u", func(t *testing.T) {
		wire := mustSeal(t, Frame{PeerID: peer, Counter: 5, Cmd: CmdPing}, RoleIssuer)
		text := Encode(wire)
		bad := text[:10] + "U" + text[11:]
		if _, err := Decode(bad); !errors.Is(err, ErrBadText) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("decode_rejects_short", func(t *testing.T) {
		if _, err := Decode("MS:ABCDEFG"); !errors.Is(err, ErrBadText) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestExtractFrame(t *testing.T) {
	key := testKey()
	peer := PeerIDFromKey(key)
	wire := mustSeal(t, Frame{Enc: true, PeerID: peer, Counter: 11, Cmd: CmdPing}, RoleIssuer)
	text := Encode(wire)
	body := text[len(Sentinel):]

	// A valid base32 run of the right length whose decoded magic is wrong.
	badMagicWire := append([]byte{}, wire...)
	badMagicWire[0] = 0x4E
	badMagicText := Encode(badMagicWire)

	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"extract_at_start", text, true},
		{"extract_lowercase_sentinel", "ms:" + body, true},
		{"extract_after_aprs_prefix", "[APRS:PD0XYZ-7→MESHSAT-1] " + text, true},
		{"extract_after_colon", "reply:" + text, true},
		{"extract_with_trailing_aprs_msgid", text + "{01", true},
		{"extract_with_trailing_text", text + " thanks", true},
		{"extract_second_token_wins", "SMS:" + body + " " + text, true},
		{"extract_rejects_sms_colon", "SMS:" + body, false},
		{"extract_rejects_ms_space", "MS: hello there", false},
		{"extract_rejects_short_run", "MS:" + body[:20], false},
		{"extract_rejects_glued_prefix", "xMS:" + body, false},
		{"extract_rejects_bad_header", badMagicText, false},
		{"extract_rejects_plain_text", "meeting at 10, bring the MS:paperwork please", false},
		{"extract_empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ExtractFrame(c.in)
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if ok && !bytes.Equal(got, wire) {
				t.Fatalf("extracted wire differs")
			}
		})
	}
}

func TestNonce_DistinctPerRoleAndDirection(t *testing.T) {
	seen := map[[NonceLen]byte]string{}
	for _, role := range []Role{RoleIssuer, RoleImporter} {
		for _, dir := range []Direction{DirRequest, DirReply} {
			n := Nonce(7, role, dir, 1)
			if prev, dup := seen[n]; dup {
				t.Fatalf("nonce collision between %s and role=%d dir=%d", prev, role, dir)
			}
			seen[n] = "role/dir"
		}
	}
	if Nonce(7, RoleIssuer, DirRequest, 1) == Nonce(7, RoleIssuer, DirRequest, 2) {
		t.Fatalf("counter not in nonce")
	}
	if Nonce(7, RoleIssuer, DirRequest, 1) == Nonce(8, RoleIssuer, DirRequest, 1) {
		t.Fatalf("peer id not in nonce")
	}
}

// TestVectors prints the spec section 14 vectors (go test -run TestVectors -v)
// and pins them so a change in the codec is caught.
func TestVectors(t *testing.T) {
	key := testKey()
	peer := PeerIDFromKey(key)
	vectors := []struct {
		name  string
		frame Frame
		role  Role
	}{
		{"ping_clear_request", Frame{PeerID: peer, Counter: 1, Cmd: CmdPing}, RoleIssuer},
		{"ping_enc_request", Frame{Enc: true, PeerID: peer, Counter: 2, Cmd: CmdPing}, RoleIssuer},
		{"reboot_enc_request_delay_10", Frame{Enc: true, PeerID: peer, Counter: 3, Cmd: CmdReboot, Args: EncodeRebootArgs(10)}, RoleIssuer},
		{"reply_ok_enc_to_counter_3", Frame{Enc: true, Reply: true, PeerID: peer, Counter: 1, Cmd: CmdReboot,
			Args: EncodeReplyArgs(ReplyArgs{RC: RCOK, ReqCounterLo: 3, Seq: 1, Total: 1, Body: []byte("rv10s")})}, RoleImporter},
	}
	t.Logf("key=%x peer_id=%d (0x%04x)", key, peer, peer)
	for _, v := range vectors {
		wire := mustSeal(t, v.frame, v.role)
		t.Logf("%s sender_role=%d wire=%x text=%s", v.name, v.role, wire, Encode(wire))
		if _, err := Open(wire, key, v.role); err != nil {
			t.Fatalf("%s: open: %v", v.name, err)
		}
	}
	if got, want := Encode(mustSeal(t, vectors[0].frame, vectors[0].role)), goldenPingClear; want != "" && got != want {
		t.Fatalf("ping_clear_request vector changed:\n got %s\nwant %s", got, want)
	}
}

// goldenPingClear pins the first spec vector (docs/OOB_MANAGEMENT_PROTOCOL.md
// section 14): key 0x01..0x20, peer id 0x94cb, counter 1, PING, clear.
var goldenPingClear = "MS:9W899JR000002098WTQ26XJ7V4DYYXQ28AEY1BVR"
