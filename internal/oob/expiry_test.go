package oob

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// A satellite command the Hub had given up on was delivered and executed at
// the kit's next pass, 69 minutes late (21 Sep 2026). A request now carries
// its own expiry inside the authenticated body, and a kit refuses it once
// that time has passed. [MESHSAT-1293]

func TestFrame_ExpiryRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeyLen)
	for _, enc := range []bool{true, false} {
		in := Frame{Enc: enc, PeerID: 9393, Counter: 42, Cmd: CmdReset, Args: EncodeResetArgs(4, 3), ExpiresAt: 1789960000}
		wire, err := Seal(in, key, RoleIssuer)
		if err != nil {
			t.Fatal(err)
		}
		h, _ := ParseHeader(wire)
		if !h.HasExpiry() {
			t.Fatalf("enc=%v: expiry flag not set", enc)
		}
		out, err := Open(wire, key, RoleIssuer)
		if err != nil {
			t.Fatalf("enc=%v: %v", enc, err)
		}
		if out.ExpiresAt != in.ExpiresAt || !bytes.Equal(out.Args, in.Args) {
			t.Fatalf("enc=%v: got expires=%d args=%x, want %d %x", enc, out.ExpiresAt, out.Args, in.ExpiresAt, in.Args)
		}
		// The expiry cannot be moved: any change to the body breaks the tag.
		tampered := append([]byte{}, wire...)
		tampered[HeaderLen+1] ^= 0x01
		if _, err := Open(tampered, key, RoleIssuer); err == nil {
			t.Fatalf("enc=%v: a changed expiry still opened", enc)
		}
		// Nor can it be stripped: clearing the flag changes the header, which is authenticated.
		stripped := append([]byte{}, wire...)
		stripped[1] &^= FlagExpiry
		if _, err := Open(stripped, key, RoleIssuer); err == nil {
			t.Fatalf("enc=%v: a frame with its expiry flag cleared still opened", enc)
		}
	}
}

// A frame from a sender that predates the expiry opens exactly as before.
func TestFrame_NoExpiryIsUnchanged(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeyLen)
	wire, err := Seal(Frame{Enc: true, PeerID: 1, Counter: 1, Cmd: CmdPing}, key, RoleIssuer)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := ParseHeader(wire); h.HasExpiry() || len(wire) != MinFrameLen {
		t.Fatalf("a frame without an expiry changed on the wire: flag=%v len=%d", h.HasExpiry(), len(wire))
	}
	f, err := Open(wire, key, RoleIssuer)
	if err != nil || f.ExpiresAt != 0 || len(f.Args) != 0 {
		t.Fatalf("open: %v %+v", err, f)
	}
	if f.Expired(time.Now().Add(1000*time.Hour), 0) {
		t.Fatal("a frame without an expiry expired")
	}
}

// The expiry comes out of the args budget, so a frame never outgrows one SMS.
func TestFrame_ExpiryKeepsTheSizeLimit(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeyLen)
	if _, err := Seal(Frame{PeerID: 1, Counter: 1, Cmd: CmdLog, Args: make([]byte, MaxArgs-ExpiryLen), ExpiresAt: 1}, key, RoleIssuer); err != nil {
		t.Fatalf("a full-size frame with an expiry was refused: %v", err)
	}
	if _, err := Seal(Frame{PeerID: 1, Counter: 1, Cmd: CmdLog, Args: make([]byte, MaxArgs-ExpiryLen+1), ExpiresAt: 1}, key, RoleIssuer); err != ErrArgsLen {
		t.Fatalf("an oversize frame with an expiry was not refused: %v", err)
	}
}

func (h *harness) peerFrameExpiring(t *testing.T, counter uint32, cmd byte, args []byte, expires time.Time) string {
	t.Helper()
	wire, err := Seal(Frame{Enc: true, PeerID: h.peer.PeerID, Counter: counter, Cmd: cmd, Args: args, ExpiresAt: uint32(expires.Unix())}, h.key, Role(h.peer.LocalRole).Other())
	if err != nil {
		t.Fatal(err)
	}
	return Encode(wire)
}

func TestHandleInbound_ExpiredRequestIsNotExecuted(t *testing.T) {
	h := newHarness(t, RoleReadonly, false)
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 5, 6, 18, 0, time.UTC)
	h.svc.d.Now = func() time.Time { return now }

	// Sent at 03:56:32 with a ten minute window, delivered at 05:06:18.
	late := h.peerFrameExpiring(t, 8, CmdPing, nil, time.Date(2026, 9, 21, 4, 6, 32, 0, time.UTC))
	frames, _ := Global.Snapshot()
	before := frames["expired"]
	if !h.svc.HandleInbound(ctx, "iridium_imt_0", "cloudloop", late) {
		t.Fatal("expired frame not consumed")
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(h.sends()); n != 0 {
		t.Fatalf("an expired request was answered (%d sends): it was executed", n)
	}
	frames, _ = Global.Snapshot()
	if frames["expired"] != before+1 {
		t.Fatalf("expired counter %d, want %d", frames["expired"], before+1)
	}
	entries, _ := h.db.ListOOBLog(10, int(h.peer.PeerID))
	var logged bool
	for _, e := range entries {
		if e.Kind == "reject" && e.Result == "expired" && strings.Contains(e.Detail, "late_by") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("no 'expired' reject row with the lateness: %+v", entries)
	}

	// The counter is spent: the same frame cannot come back as fresh.
	if !h.svc.HandleInbound(ctx, "iridium_imt_0", "cloudloop", late) {
		t.Fatal("replayed frame not consumed")
	}
	frames2, _ := Global.Snapshot()
	if frames2["replay"] < frames["replay"]+1 {
		t.Fatal("an expired frame's counter was not marked as used")
	}

	// Within its window (and within the clock skew) a request runs.
	fresh := h.peerFrameExpiring(t, 10, CmdPing, nil, now.Add(-time.Minute))
	h.svc.HandleInbound(ctx, "iridium_imt_0", "cloudloop", fresh)
	waitFor(t, func() bool { return len(h.sends()) == 1 })
}

// On a kit whose clock is not established the expiry cannot be judged, and
// the management path must not lock itself out: the request runs.
func TestHandleInbound_UntrustedClockDoesNotLockOut(t *testing.T) {
	h := newHarness(t, RoleReadonly, false)
	h.svc.d.Now = func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }
	h.svc.d.ClockTrusted = func() bool { return false }
	text := h.peerFrameExpiring(t, 1, CmdPing, nil, time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC))
	h.svc.HandleInbound(context.Background(), "cellular_0", "+31653207829", text)
	waitFor(t, func() bool { return len(h.sends()) == 1 })
}

// Everything this kit originates carries an expiry: the default, or the
// caller's own.
func TestSend_StampsAnExpiry(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	h.svc.d.Now = func() time.Time { return now }
	for _, tc := range []struct {
		ttl  time.Duration
		want time.Time
	}{
		{0, now.Add(DefaultRequestTTL)},
		{10 * time.Minute, now.Add(10 * time.Minute)},
	} {
		res, err := h.svc.Send(context.Background(), SendRequest{PeerID: h.peer.PeerID, Via: "cellular_0", Cmd: CmdReset, Args: EncodeResetArgs(4, 3), TTL: tc.ttl})
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := ExtractFrame(res.Text)
		f, err := Open(wire, h.key, Role(h.peer.LocalRole))
		if err != nil {
			t.Fatal(err)
		}
		if got := time.Unix(int64(f.ExpiresAt), 0).UTC(); !got.Equal(tc.want) {
			t.Fatalf("ttl %s: expires %s, want %s", tc.ttl, got, tc.want)
		}
		if tg, lv, err := ParseResetArgs(f.Args); err != nil || tg != 4 || lv != 3 {
			t.Fatalf("command args damaged by the expiry: %v %d %d", err, tg, lv)
		}
		if len(res.Text) > 160 {
			t.Fatalf("request no longer fits one SMS: %d", len(res.Text))
		}
	}
}
