package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"meshsat/internal/crewchat"
)

// MESHSAT-1744: the processor's crew chat reader takes a C1 frame off the
// satellite link as the MT's bytes and off the cellular link as base64 from
// the Hub's number alone, and leaves everything else to the pipeline.

type deliveries struct{ got []string }

func (d *deliveries) DeliverFrame(f crewchat.Chat, bearer string) int {
	d.got = append(d.got, bearer+" "+f.MessageID+" "+f.Callsign+" "+f.Text)
	return 1
}

func TestCrewChatInboundTakesFramesFromTheHubAlone(t *testing.T) {
	const hub = "+3197010258258"
	raw, _ := hex.DecodeString("4305" + "0102030405060708090a0b0c0d0e0f10" + "a1b3c2ec" + "3f24d8f92959c061" + "04416e6e61" + "07c48020f29794")
	b64 := base64.StdEncoding.EncodeToString(raw)
	want := "01020304-0506-0708-090a-0b0c0d0e0f10 Anna check in please"
	d := &deliveries{}
	read := crewChatInbound(d, hub)
	ctx := context.Background()
	cases := []struct {
		name     string
		iface    string
		from     string
		text     string
		consumed bool
		bearer   string
	}{
		{"an SMS from the Hub's number", "cellular_0", hub, b64, true, "sms"},
		{"an SMS from the Hub's number with a stray newline", "cellular_0", hub, b64 + "\n", true, "sms"},
		{"an SMS from another number", "cellular_0", "+31653618463", b64, false, ""},
		{"a plain SMS from the Hub's number", "cellular_0", hub, "[+31653618463] hello", false, ""},
		{"a base64 SMS from the Hub that is not a frame", "cellular_0", hub, base64.StdEncoding.EncodeToString([]byte("MS:9W899")), false, ""},
		{"an SBD MT", "iridium_0", "rock7", string(raw), true, "sbd"},
		{"an IMT MT", "iridium_imt_0", "cloudloop", string(raw), true, "imt"},
		{"an SBD MT that is plain text", "iridium_0", "rock7", "hello from the Hub", false, ""},
		{"a warning digest on SBD", "iridium_0", "rock7", "W" + string(raw[1:]), false, ""},
		{"another link", "aprs_0", "PD1EG-9", string(raw), false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(d.got)
			if got := read(ctx, c.iface, c.from, c.text); got != c.consumed {
				t.Fatalf("consumed %v, want %v", got, c.consumed)
			}
			if !c.consumed {
				if len(d.got) != before {
					t.Fatalf("delivered %q", d.got[before:])
				}
				return
			}
			if len(d.got) != before+1 || d.got[before] != c.bearer+" "+want {
				t.Fatalf("delivered %q, want %q", d.got[before:], c.bearer+" "+want)
			}
		})
	}
	// A frame that starts right but does not read is consumed and logged:
	// it is not text for the mesh either way.
	if !read(ctx, "cellular_0", hub, base64.StdEncoding.EncodeToString(append(raw[:31], 0x05, 0xff, 0xfe))) {
		t.Error("an unreadable frame reached the pipeline")
	}
	// With no Hub number configured, nothing on SMS is a frame.
	none := crewChatInbound(d, "")
	if none(ctx, "cellular_0", hub, b64) {
		t.Error("a frame was taken with no Hub number to check against")
	}
}
