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

// A command that uses its whole execution window still gets its reply out:
// the reply has a context of its own. [MESHSAT-810]
func TestRun_ReplyOutlivesALongExecution(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	old := ExecTimeout
	ExecTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ExecTimeout = old })

	var deadlineLeft time.Duration
	h.svc.d.Actions["mesh"] = map[byte]Action{LevelSoft: func(ctx context.Context) error {
		dl, _ := ctx.Deadline()
		deadlineLeft = time.Until(dl)
		<-ctx.Done() // a handshake that runs to the end of the window
		return nil   // and then succeeds, as the transport does
	}}
	var replyCtxErr error
	send := h.svc.d.Send
	h.svc.d.Send = func(ctx context.Context, iface, addr, text string) (int64, error) {
		replyCtxErr = ctx.Err()
		return send(ctx, iface, addr, text)
	}
	h.svc.HandleInbound(context.Background(), "cellular_0", "+31653207829",
		h.peerFrame(t, 1, CmdReset, EncodeResetArgs(TargetMesh, LevelSoft), true, false))
	waitFor(t, func() bool { return len(h.sends()) == 1 })
	if deadlineLeft > ExecTimeout || deadlineLeft <= 0 {
		t.Fatalf("action got %s of deadline, want the exec timeout", deadlineLeft)
	}
	if replyCtxErr != nil {
		t.Fatalf("the reply was queued on an expired context: %v", replyCtxErr)
	}
	if _, ra := h.openReply(t, h.sends()[0].text); ra.RC != RCOK {
		t.Fatalf("a reset that succeeded replied %s", ra.RC)
	}
}

// A BEARER off that cuts the bearer it arrived on answers on that bearer
// before the cut; parallax's "aprs off rv10m" of 21 Sep 2026 died unsent
// because the gateway was stopped first. [MESHSAT-756]
func TestBearerOff_ReplyLeavesBeforeTheCut(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	old := severingStopDelay
	severingStopDelay = 200 * time.Millisecond
	t.Cleanup(func() { severingStopDelay = old })

	h.svc.HandleInbound(context.Background(), "cellular_0", "+31653207829",
		h.peerFrame(t, 1, CmdBearer, EncodeBearerArgs(TargetCellular, 0), true, false))
	waitFor(t, func() bool { return len(h.sends()) == 1 })
	h.gws.mu.Lock()
	cutBeforeReply := len(h.gws.stopped)
	h.gws.mu.Unlock()
	if cutBeforeReply != 0 {
		t.Fatalf("the bearer was cut before its reply was queued: %v", h.gws.stopped)
	}
	if _, ra := h.openReply(t, h.sends()[0].text); ra.RC != RCOK || !strings.Contains(string(ra.Body), "rv10m") {
		t.Fatalf("reply %s %q", ra.RC, ra.Body)
	}
	waitFor(t, func() bool {
		h.gws.mu.Lock()
		defer h.gws.mu.Unlock()
		return len(h.gws.stopped) == 1
	})
}

// BEARER on inside the delay cancels the pending cut and the revert.
func TestBearerOn_CancelsAPendingCut(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	old := severingStopDelay
	severingStopDelay = 100 * time.Millisecond
	t.Cleanup(func() { severingStopDelay = old })
	origin := Origin{Role: RoleControl, Bearer: "cellular_0"}
	ctx := context.Background()
	if res := h.svc.Execute(ctx, origin, CmdBearer, EncodeBearerArgs(TargetCellular, 0)); res.Code != RCOK {
		t.Fatalf("off: %s %q", res.Code, res.Body)
	}
	if res := h.svc.Execute(ctx, origin, CmdBearer, EncodeBearerArgs(TargetCellular, 1)); res.Code != RCOK {
		t.Fatalf("on: %s %q", res.Code, res.Body)
	}
	time.Sleep(300 * time.Millisecond)
	h.gws.mu.Lock()
	defer h.gws.mu.Unlock()
	if len(h.gws.stopped) != 0 {
		t.Fatalf("a cancelled cut still ran: stopped %v", h.gws.stopped)
	}
	if len(h.svc.PendingReverts()) != 0 {
		t.Fatalf("revert still armed: %v", h.svc.PendingReverts())
	}
}

// A BEARER revert survives a bridge restart: the deadline is persisted, and
// a new service on the same database re-arms it, or runs it at once when it
// fell due while the bridge was down. Before, a restart inside the window
// left the bearer off for good. [MESHSAT-756]
func TestBearerRevert_SurvivesARestart(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	oldStop, oldMin := severingStopDelay, restoredRevertMinDelay
	severingStopDelay, restoredRevertMinDelay = 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { severingStopDelay, restoredRevertMinDelay = oldStop, oldMin })

	origin := Origin{Role: RoleControl, Bearer: "aprs_0"}
	if res := h.svc.Execute(context.Background(), origin, CmdBearer, EncodeBearerArgs(TargetAPRS, 0)); res.Code != RCOK {
		t.Fatalf("off: %s %q", res.Code, res.Body)
	}
	if m := h.svc.loadRevertDeadlines(); m["aprs_0"] == 0 {
		t.Fatalf("revert not persisted: %v", m)
	}

	// The bridge restarts. The old service is gone; a new one opens the same
	// database, and the revert fell due while it was down.
	h.svc.mu.Lock()
	for _, tm := range h.svc.reverts {
		tm.Stop()
	}
	h.svc.mu.Unlock()
	h.svc.persistRevert("aprs_0", time.Now().Add(-time.Minute))
	gws := &fakeGateways{}
	svc2 := New(Config{Enabled: true, ReplyBudgetHour: 12}, Deps{DB: h.db, Keys: h.keys, Gateways: gws})
	if err := svc2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pr := svc2.PendingReverts(); len(pr) != 1 || pr[0] != "aprs_0" {
		t.Fatalf("revert not re-armed after the restart: %v", pr)
	}
	waitFor(t, func() bool {
		gws.mu.Lock()
		defer gws.mu.Unlock()
		return len(gws.started) == 1 && gws.started[0] == "aprs_0"
	})
	if m := svc2.loadRevertDeadlines(); len(m) != 0 {
		t.Fatalf("a revert that ran is still persisted: %v", m)
	}
}

// BEARER on forgets the persisted revert; deleting a peer keeps it.
func TestBearerRevert_OnForgetsItDeletePeerKeepsIt(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	old := severingStopDelay
	severingStopDelay = 10 * time.Millisecond
	t.Cleanup(func() { severingStopDelay = old })
	ctx := context.Background()
	origin := Origin{Role: RoleControl, Bearer: "cellular_0"}

	h.svc.Execute(ctx, origin, CmdBearer, EncodeBearerArgs(TargetCellular, 0))
	h.svc.Execute(ctx, origin, CmdBearer, EncodeBearerArgs(TargetCellular, 1))
	if m := h.svc.loadRevertDeadlines(); len(m) != 0 {
		t.Fatalf("BEARER on left a persisted revert: %v", m)
	}

	h.svc.Execute(ctx, origin, CmdBearer, EncodeBearerArgs(TargetCellular, 0))
	if err := h.svc.DeletePeer(h.peer.PeerID); err != nil {
		t.Fatal(err)
	}
	if pr := h.svc.PendingReverts(); len(pr) != 1 {
		t.Fatalf("deleting a peer cancelled the kit's bearer revert: %v", pr)
	}
	if m := h.svc.loadRevertDeadlines(); m["cellular_0"] == 0 {
		t.Fatalf("deleting a peer dropped the persisted revert: %v", m)
	}
}
