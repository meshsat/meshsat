package oob

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/keystore"
)

// --- fakes ---

type fakeKeys struct {
	mu   sync.Mutex
	keys map[string][]byte
}

func newFakeKeys() *fakeKeys { return &fakeKeys{keys: map[string][]byte{}} }
func (f *fakeKeys) GetKey(ct, addr string) ([]byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[ct+":"+addr]
	if !ok {
		return nil, 0, errors.New("no key")
	}
	return k, 1, nil
}
func (f *fakeKeys) StoreKey(ct, addr string, raw []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[ct+":"+addr] = append([]byte{}, raw...)
	return 1, nil
}
func (f *fakeKeys) RevokeKey(ct, addr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, ct+":"+addr)
	return nil
}
func (f *fakeKeys) CreateBundleFromEntries(entries []keystore.BundleEntry) ([]byte, string, error) {
	return []byte("bundle"), "meshsat://key/fake", nil
}

type fakeGateways struct {
	mu      sync.Mutex
	started []string
	stopped []string
	fail    bool
}

func (g *fakeGateways) StartGatewayInstance(ctx context.Context, id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fail {
		return errors.New("no such instance")
	}
	g.started = append(g.started, id)
	return nil
}
func (g *fakeGateways) StopGatewayInstance(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopped = append(g.stopped, id)
	return nil
}

type sentItem struct {
	iface, addr, text string
}

type harness struct {
	svc    *Service
	db     *database.DB
	keys   *fakeKeys
	gws    *fakeGateways
	up     map[string]bool
	mu     sync.Mutex
	sent   []sentItem
	nextID int64
	audits []string
	events []string
	peer   *database.OOBPeer
	key    []byte
	agent  *fakeAgent
}

func (h *harness) sends() []sentItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]sentItem{}, h.sent...)
}

// fakeAgent serves the host agent protocol on a temp Unix socket.
type fakeAgent struct {
	mu    sync.Mutex
	calls []string
	ln    net.Listener
	path  string
	lines int
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	a := &fakeAgent{ln: ln, path: path, lines: 20}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go a.serve(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return a
}

func (a *fakeAgent) serve(conn net.Conn) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var req agentRequest
	_ = json.Unmarshal(line, &req)
	a.mu.Lock()
	a.calls = append(a.calls, req.Action)
	a.mu.Unlock()
	var reply agentReply
	switch req.Action {
	case "ping":
		reply = agentReply{OK: true, Version: "test"}
	case "journal_tail":
		n := int(req.Args["lines"].(float64))
		lines := make([]any, 0, n)
		for i := 0; i < n && i < a.lines; i++ {
			lines = append(lines, "2026-09-01T21:00:00+02:00 host unit[1]: line number "+strings.Repeat("x", 40))
		}
		reply = agentReply{OK: true, Result: map[string]any{"lines": lines}}
	case "net_status":
		reply = agentReply{OK: true, Result: map[string]any{"ip": "192.168.181.211", "gw": "Y", "wpa": "COMPLETED", "usb": "down"}}
	case "reboot", "wifi_reassociate", "wifi_restart", "service_restart", "usb_rebind", "p2p_restart":
		reply = agentReply{OK: true, Result: map[string]any{}}
	default:
		reply = agentReply{OK: false, Error: "unknown action"}
	}
	b, _ := json.Marshal(reply)
	_, _ = conn.Write(append(b, '\n'))
}

func (a *fakeAgent) called() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.calls...)
}

func newHarness(t *testing.T, role string, withAgent bool) *harness {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &harness{db: db, keys: newFakeKeys(), gws: &fakeGateways{}, up: map[string]bool{"cellular_0": true, "aprs_0": true, "mesh_0": true}}
	host := NewHostClient(filepath.Join(t.TempDir(), "missing.sock"))
	if withAgent {
		h.agent = newFakeAgent(t)
		host = NewHostClient(h.agent.path)
	}
	h.svc = New(Config{Enabled: true, ReplyBudgetHour: 12}, Deps{
		DB:        db,
		Keys:      h.keys,
		Gateways:  h.gws,
		BearersUp: func() map[string]bool { return h.up },
		Host:      host,
		Send: func(ctx context.Context, iface, addr, text string) (int64, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.nextID++
			h.sent = append(h.sent, sentItem{iface, addr, text})
			return h.nextID, nil
		},
		Audit: func(ev string, iface, dir *string, del *int64, detail string) {
			h.mu.Lock()
			h.audits = append(h.audits, ev)
			h.mu.Unlock()
		},
		Emit: func(ev string, data any) {
			h.mu.Lock()
			h.events = append(h.events, ev)
			h.mu.Unlock()
		},
		Actions: map[string]map[byte]Action{},
		Status: StatusSources{
			Uptime:  func() time.Duration { return 17*time.Hour + 5*time.Minute },
			Battery: func() (float64, bool, bool) { return 98.3, true, true },
			Queued:  func() int { return 0 },
			WLAN:    func() (string, bool) { return "up", true },
		},
		LocalAlias: "tesseract",
	})
	if err := h.svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One peer, provisioned on the bundle path (this kit is the issuer).
	p, err := h.svc.CreatePeer(PeerSpec{Alias: "parallax", Role: role, Addresses: map[string]string{"cellular_0": "+31653207829", "aprs_0": "PD0XYZ-7"}})
	if err != nil {
		t.Fatal(err)
	}
	h.peer = p
	h.key, _, _ = h.keys.GetKey("mgmt", "parallax")
	return h
}

// peerFrame seals a request as the remote peer (importer role).
func (h *harness) peerFrame(t *testing.T, counter uint32, cmd byte, args []byte, enc, noReply bool) string {
	t.Helper()
	wire, err := Seal(Frame{Enc: enc, NoReply: noReply, PeerID: h.peer.PeerID, Counter: counter, Cmd: cmd, Args: args}, h.key, Role(h.peer.LocalRole).Other())
	if err != nil {
		t.Fatal(err)
	}
	return Encode(wire)
}

// openReply decodes a reply text as the remote peer would.
func (h *harness) openReply(t *testing.T, text string) (Frame, ReplyArgs) {
	t.Helper()
	wire, ok := ExtractFrame(text)
	if !ok {
		t.Fatalf("reply text is not a frame: %s", text)
	}
	f, err := Open(wire, h.key, Role(h.peer.LocalRole))
	if err != nil {
		t.Fatalf("open reply: %v", err)
	}
	ra, err := ParseReplyArgs(f.Args)
	if err != nil {
		t.Fatalf("reply args: %v", err)
	}
	return f, ra
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 3 s")
}

// --- executor ---

func TestExecute_RolesAndArgs(t *testing.T) {
	ro := newHarness(t, RoleReadonly, false)
	ctx := context.Background()
	origin := Origin{PeerID: ro.peer.PeerID, Alias: "parallax", Role: RoleReadonly, Bearer: "cellular_0"}

	cases := []struct {
		name string
		cmd  byte
		args []byte
		want ResultCode
	}{
		{"readonly_ping_ok", CmdPing, nil, RCOK},
		{"readonly_reboot_denied", CmdReboot, nil, RCDenied},
		{"readonly_reset_denied", CmdReset, EncodeResetArgs(TargetMesh, LevelSoft), RCDenied},
		{"unknown_cmd", 0x55, nil, RCUnknownCmd},
		{"ping_with_args_bad", CmdPing, []byte{1}, RCBadArgs},
		{"log_without_agent_unavailable", CmdLog, EncodeLogArgs(1, 5), RCUnavailable},
		{"log_bad_unit", CmdLog, EncodeLogArgs(200, 5), RCBadArgs},
		{"status_net_fallback", CmdStatusNet, nil, RCOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ro.svc.Execute(ctx, origin, c.cmd, c.args)
			if res.Code != c.want {
				t.Fatalf("got %s (%q), want %s", res.Code, res.Body, c.want)
			}
		})
	}
	ping := ro.svc.Execute(ctx, origin, CmdPing, nil)
	for _, want := range []string{"u17h", "b98A", "q0", "wU", "Z", "agent:none"} {
		if !strings.Contains(ping.Body, want) {
			t.Fatalf("ping body %q lacks %q", ping.Body, want)
		}
	}
	if sn := ro.svc.Execute(ctx, origin, CmdStatusNet, nil); !strings.HasPrefix(sn.Body, "wlan0:up") {
		t.Fatalf("status-net fallback body %q", sn.Body)
	}
}

func TestExecute_ResetAndBearer(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	ctx := context.Background()
	origin := Origin{PeerID: h.peer.PeerID, Alias: "parallax", Role: RoleControl, Bearer: "cellular_0"}

	t.Run("reset_soft_restarts_gateway_instance", func(t *testing.T) {
		res := h.svc.Execute(ctx, origin, CmdReset, EncodeResetArgs(TargetAPRS, LevelSoft))
		if res.Code != RCOK || !strings.HasPrefix(res.Body, "aprs L1") {
			t.Fatalf("%s %q", res.Code, res.Body)
		}
		if len(h.gws.stopped) != 1 || h.gws.stopped[0] != "aprs_0" || len(h.gws.started) != 1 || h.gws.started[0] != "aprs_0" {
			t.Fatalf("gateway calls: stopped=%v started=%v", h.gws.stopped, h.gws.started)
		}
	})
	t.Run("reset_registered_action", func(t *testing.T) {
		called := false
		h.svc.d.Actions["mesh"] = map[byte]Action{LevelDevice: func(ctx context.Context) error { called = true; return nil }}
		res := h.svc.Execute(ctx, origin, CmdReset, EncodeResetArgs(TargetMesh, LevelDevice))
		if res.Code != RCOK || !called {
			t.Fatalf("%s %q called=%v", res.Code, res.Body, called)
		}
	})
	t.Run("reset_unsupported_level", func(t *testing.T) {
		res := h.svc.Execute(ctx, origin, CmdReset, EncodeResetArgs(TargetGPS, LevelDevice))
		if res.Code != RCUnavailable {
			t.Fatalf("%s %q", res.Code, res.Body)
		}
	})
	t.Run("reset_hard_arriving_bearer_refused_when_sole", func(t *testing.T) {
		scans := 0
		h.svc.d.TriggerScan = func() { scans++ }
		h.svc.d.Actions["cellular"] = map[byte]Action{LevelHard: func(ctx context.Context) error { return nil }}
		h.up = map[string]bool{"cellular_0": true}
		res := h.svc.Execute(ctx, origin, CmdReset, EncodeResetArgs(TargetCellular, LevelHard))
		if res.Code != RCRefused || scans != 0 {
			t.Fatalf("%s %q scans=%d", res.Code, res.Body, scans)
		}
		h.up = map[string]bool{"cellular_0": true, "aprs_0": true}
		res = h.svc.Execute(ctx, origin, CmdReset, EncodeResetArgs(TargetCellular, LevelHard))
		if res.Code != RCOK || scans != 1 {
			t.Fatalf("%s %q scans=%d", res.Code, res.Body, scans)
		}
	})
	t.Run("reset_hub_origin_never_severs", func(t *testing.T) {
		h.up = map[string]bool{"cellular_0": true}
		res := h.svc.Execute(ctx, Origin{Role: RoleControl, Bearer: OriginHub}, CmdReset, EncodeResetArgs(TargetCellular, LevelHard))
		if res.Code != RCOK {
			t.Fatalf("%s %q", res.Code, res.Body)
		}
	})
	t.Run("bearer_off_arriving_arms_revert_and_on_cancels", func(t *testing.T) {
		h.gws.stopped, h.gws.started = nil, nil
		res := h.svc.Execute(ctx, origin, CmdBearer, EncodeBearerArgs(TargetCellular, 0))
		if res.Code != RCOK || !strings.Contains(res.Body, "rv10m") {
			t.Fatalf("%s %q", res.Code, res.Body)
		}
		if pr := h.svc.PendingReverts(); len(pr) != 1 || pr[0] != "cellular_0" {
			t.Fatalf("pending reverts %v", pr)
		}
		if len(h.gws.stopped) != 1 || h.gws.stopped[0] != "cellular_0" {
			t.Fatalf("stopped %v", h.gws.stopped)
		}
		res = h.svc.Execute(ctx, origin, CmdBearer, EncodeBearerArgs(TargetCellular, 1))
		if res.Code != RCOK || len(h.svc.PendingReverts()) != 0 || len(h.gws.started) != 1 {
			t.Fatalf("%s %q reverts=%v started=%v", res.Code, res.Body, h.svc.PendingReverts(), h.gws.started)
		}
		// A different bearer arms nothing.
		res = h.svc.Execute(ctx, origin, CmdBearer, EncodeBearerArgs(TargetAPRS, 0))
		if res.Code != RCOK || strings.Contains(res.Body, "rv10m") || len(h.svc.PendingReverts()) != 0 {
			t.Fatalf("%s %q reverts=%v", res.Code, res.Body, h.svc.PendingReverts())
		}
	})
	t.Run("restart_and_reboot_without_hooks", func(t *testing.T) {
		h.up = map[string]bool{"cellular_0": true, "aprs_0": true}
		if res := h.svc.Execute(ctx, origin, CmdRestart, nil); res.Code != RCUnavailable {
			t.Fatalf("restart without hook: %s", res.Code)
		}
		if res := h.svc.Execute(ctx, origin, CmdReboot, EncodeRebootArgs(5)); res.Code != RCUnavailable {
			t.Fatalf("reboot without agent: %s", res.Code)
		}
		h.up = map[string]bool{"cellular_0": true}
		if res := h.svc.Execute(ctx, origin, CmdRestart, nil); res.Code != RCRefused {
			t.Fatalf("restart on sole bearer: %s", res.Code)
		}
	})
}

func TestExecute_WithAgent(t *testing.T) {
	h := newHarness(t, RoleControl, true)
	ctx := context.Background()
	origin := Origin{PeerID: h.peer.PeerID, Alias: "parallax", Role: RoleControl, Bearer: "cellular_0"}

	if avail, ver, _ := h.svc.AgentStatus(); !avail || ver != "test" {
		t.Fatalf("agent status %v %q", avail, ver)
	}
	res := h.svc.Execute(ctx, origin, CmdLog, EncodeLogArgs(1, 3))
	if res.Code != RCOK {
		t.Fatalf("log: %s %q", res.Code, res.Body)
	}
	lines := strings.Split(res.Body, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "21:00:00 ") || len(lines[0]) > 60 {
		t.Fatalf("log lines %q", lines)
	}
	res = h.svc.Execute(ctx, origin, CmdStatusNet, nil)
	if res.Code != RCOK || !strings.Contains(res.Body, "ip:192.168.181.211") || !strings.Contains(res.Body, "wpa:COMPLETED") {
		t.Fatalf("status-net: %s %q", res.Code, res.Body)
	}
	res = h.svc.Execute(ctx, origin, CmdReset, EncodeResetArgs(TargetWiFi, LevelSoft))
	if res.Code != RCOK || !res.FollowUp {
		t.Fatalf("wifi reset: %s %q followup=%v", res.Code, res.Body, res.FollowUp)
	}
	res = h.svc.Execute(ctx, origin, CmdReboot, EncodeRebootArgs(7))
	if res.Code != RCOK || res.Body != "rb7s" {
		t.Fatalf("reboot: %s %q", res.Code, res.Body)
	}
	waitFor(t, func() bool {
		for _, c := range h.agent.called() {
			if c == "reboot" {
				return true
			}
		}
		return false
	})
	calls := h.agent.called()
	if !contains(calls, "wifi_reassociate") || !contains(calls, "journal_tail") || !contains(calls, "net_status") {
		t.Fatalf("agent calls %v", calls)
	}
	info := h.svc.TargetsInfo()
	for _, ti := range info {
		if ti.Name == "host" && !contains3(ti.Levels, LevelHard) {
			t.Fatalf("host levels %v", ti.Levels)
		}
		if ti.Name == "wifi" && (!contains3(ti.Levels, LevelSoft) || contains3(ti.Levels, LevelHard)) {
			t.Fatalf("wifi levels %v", ti.Levels)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func contains3(list []byte, b byte) bool {
	for _, v := range list {
		if v == b {
			return true
		}
	}
	return false
}

// --- inbound and replies ---

func TestHandleInbound_RequestReplyRoundTrip(t *testing.T) {
	h := newHarness(t, RoleReadonly, false)
	ctx := context.Background()

	text := h.peerFrame(t, 1, CmdPing, nil, true, false)
	if !h.svc.HandleInbound(ctx, "cellular_0", "+31653207829", "[APRS:X→Y] "+text) {
		t.Fatal("frame not consumed")
	}
	waitFor(t, func() bool { return len(h.sends()) == 1 })
	s := h.sends()[0]
	if s.iface != "cellular_0" || s.addr != "+31653207829" {
		t.Fatalf("reply routed to %s/%s", s.iface, s.addr)
	}
	f, ra := h.openReply(t, s.text)
	if !f.Reply || !f.Enc || f.Cmd != CmdPing || ra.RC != RCOK || ra.ReqCounterLo != 1 || ra.Seq != 1 || ra.Total != 1 {
		t.Fatalf("reply %+v %+v", f, ra)
	}
	if !strings.HasPrefix(string(ra.Body), "u17h") {
		t.Fatalf("reply body %q", ra.Body)
	}
	if len(s.text) > 160 {
		t.Fatalf("SMS reply too long: %d", len(s.text))
	}

	// Replay: silent, counted, no second send.
	frames, _ := Global.Snapshot()
	before := frames["replay"]
	if !h.svc.HandleInbound(ctx, "cellular_0", "+31653207829", text) {
		t.Fatal("replay not consumed")
	}
	time.Sleep(50 * time.Millisecond)
	frames, _ = Global.Snapshot()
	if frames["replay"] != before+1 || len(h.sends()) != 1 {
		t.Fatalf("replay handling: counter %d sends %d", frames["replay"], len(h.sends()))
	}

	// Log and audit trail.
	entries, _ := h.db.ListOOBLog(10, int(h.peer.PeerID))
	kinds := map[string]int{}
	for _, e := range entries {
		kinds[e.Direction+"/"+e.Kind]++
	}
	if kinds["in/request"] < 1 || kinds["out/reply"] != 1 || kinds["in/reject"] != 1 {
		t.Fatalf("log kinds %v", kinds)
	}
	h.mu.Lock()
	audits := append([]string{}, h.audits...)
	h.mu.Unlock()
	if !contains(audits, "oob_command") || !contains(audits, "oob_reject") {
		t.Fatalf("audits %v", audits)
	}
}

func TestHandleInbound_Silence(t *testing.T) {
	h := newHarness(t, RoleReadonly, false)
	ctx := context.Background()

	t.Run("not_a_frame", func(t *testing.T) {
		if h.svc.HandleInbound(ctx, "cellular_0", "+316", "SMS: hello there") {
			t.Fatal("plain text consumed")
		}
	})
	t.Run("unknown_peer_silent", func(t *testing.T) {
		other := testKey()
		wire, _ := Seal(Frame{PeerID: PeerIDFromKey(other), Counter: 1, Cmd: CmdPing}, other, RoleIssuer)
		if !h.svc.HandleInbound(ctx, "cellular_0", "+316", Encode(wire)) {
			t.Fatal("unknown-peer frame not consumed")
		}
		time.Sleep(30 * time.Millisecond)
		if len(h.sends()) != 0 {
			t.Fatal("unknown peer got a reply")
		}
	})
	t.Run("bad_tag_silent", func(t *testing.T) {
		text := h.peerFrame(t, 5, CmdPing, nil, false, false)
		mangled := text[:len(text)-3] + "ZZZ"
		if !h.svc.HandleInbound(ctx, "cellular_0", "+316", mangled) {
			t.Fatal("bad-tag frame not consumed")
		}
		time.Sleep(30 * time.Millisecond)
		if len(h.sends()) != 0 {
			t.Fatal("bad tag got a reply")
		}
	})
	t.Run("disabled_peer_silent", func(t *testing.T) {
		off := false
		if _, err := h.svc.UpdatePeer(h.peer.PeerID, PeerSpec{}, &off); err != nil {
			t.Fatal(err)
		}
		text := h.peerFrame(t, 6, CmdPing, nil, false, false)
		h.svc.HandleInbound(ctx, "cellular_0", "+316", text)
		time.Sleep(30 * time.Millisecond)
		if len(h.sends()) != 0 {
			t.Fatal("disabled peer got a reply")
		}
		on := true
		h.svc.UpdatePeer(h.peer.PeerID, PeerSpec{}, &on)
	})
	t.Run("service_disabled_drops_frame", func(t *testing.T) {
		cfg := h.svc.Config()
		cfg.Enabled = false
		if err := h.svc.SetConfig(cfg); err != nil {
			t.Fatal(err)
		}
		text := h.peerFrame(t, 7, CmdPing, nil, false, false)
		if !h.svc.HandleInbound(ctx, "cellular_0", "+316", text) {
			t.Fatal("frame must be consumed even when disabled")
		}
		time.Sleep(30 * time.Millisecond)
		if len(h.sends()) != 0 {
			t.Fatal("disabled service replied")
		}
		cfg.Enabled = true
		h.svc.SetConfig(cfg)
	})
	t.Run("noreply_flag", func(t *testing.T) {
		text := h.peerFrame(t, 8, CmdPing, nil, false, true)
		h.svc.HandleInbound(ctx, "cellular_0", "+316", text)
		time.Sleep(50 * time.Millisecond)
		if len(h.sends()) != 0 {
			t.Fatal("NOREPLY frame was answered")
		}
	})
	t.Run("clear_request_gets_clear_reply", func(t *testing.T) {
		text := h.peerFrame(t, 9, CmdPing, nil, false, false)
		h.svc.HandleInbound(ctx, "aprs_0", "PD0XYZ-7", text)
		waitFor(t, func() bool { return len(h.sends()) >= 1 })
		time.Sleep(50 * time.Millisecond)
		sends := h.sends()
		if len(sends) != 1 {
			t.Fatalf("a PING reply is one frame even on APRS, got %d", len(sends))
		}
		s := sends[0]
		f, ra := h.openReply(t, s.text)
		if f.Enc || s.iface != "aprs_0" || s.addr != "PD0XYZ-7" {
			t.Fatalf("reply enc=%v iface=%s addr=%s", f.Enc, s.iface, s.addr)
		}
		if len(s.text) > 67 || len(ra.Body) > BearerBudget("aprs_0")-ReplyHeaderLen || !strings.HasPrefix(string(ra.Body), "u17h") {
			t.Fatalf("aprs reply: %d chars, body %q", len(s.text), ra.Body)
		}
	})
}

func TestHandleInbound_BudgetAndChunks(t *testing.T) {
	h := newHarness(t, RoleControl, true)
	ctx := context.Background()
	cfg := h.svc.Config()
	cfg.ReplyBudgetHour = 3
	if err := h.svc.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}

	// LOG over APRS: 20 long lines cannot fit; chunks are capped at 4 and
	// each spends one reply token, so only 3 go out under a budget of 3.
	text := h.peerFrame(t, 1, CmdLog, EncodeLogArgs(1, 20), false, false)
	h.svc.HandleInbound(ctx, "aprs_0", "PD0XYZ-7", text)
	waitFor(t, func() bool { return len(h.sends()) == 3 })
	time.Sleep(50 * time.Millisecond)
	sends := h.sends()
	if len(sends) != 3 {
		t.Fatalf("sends %d", len(sends))
	}
	for i, s := range sends {
		_, ra := h.openReply(t, s.text)
		if ra.Seq != byte(i+1) || ra.Total != MaxReplyChunks || len(ra.Body) > BearerBudget("aprs_0")-ReplyHeaderLen {
			t.Fatalf("chunk %d: seq=%d total=%d body=%d", i, ra.Seq, ra.Total, len(ra.Body))
		}
	}
	entries, _ := h.db.ListOOBLog(20, int(h.peer.PeerID))
	budgetHits := 0
	for _, e := range entries {
		if e.Result == "budget" {
			budgetHits++
		}
	}
	if budgetHits != 1 {
		t.Fatalf("budget log entries %d", budgetHits)
	}
}

func TestSend_OriginatesRequest(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	ctx := context.Background()

	res, err := h.svc.Send(ctx, SendRequest{PeerID: h.peer.PeerID, Via: "cellular_0", Cmd: CmdReboot, Args: EncodeRebootArgs(15)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Address != "+31653207829" || res.DeliveryID != 1 || res.Counter != 1 {
		t.Fatalf("send result %+v", res)
	}
	// A restart bumps every used counter so a restored database can never
	// rewind into a nonce that was already used.
	if err := h.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	bumped, err := h.svc.Send(ctx, SendRequest{PeerID: h.peer.PeerID, Via: "cellular_0", Cmd: CmdPing})
	if err != nil {
		t.Fatal(err)
	}
	if bumped.Counter != 1+BootCounterBump+1 {
		t.Fatalf("counter after restart %d, want %d", bumped.Counter, 1+BootCounterBump+1)
	}
	wire, ok := ExtractFrame(res.Text)
	if !ok {
		t.Fatal("sent text is not a frame")
	}
	// The peer opens it with the sender's role (this kit is the issuer).
	f, err := Open(wire, h.key, Role(h.peer.LocalRole))
	if err != nil {
		t.Fatal(err)
	}
	if f.Cmd != CmdReboot || !f.Enc || f.Reply {
		t.Fatalf("frame %+v", f)
	}
	if d, _ := ParseRebootArgs(f.Args); d != 15 {
		t.Fatalf("delay %d", d)
	}

	// Per-bearer encryption policy off for APRS, and an override wins.
	if _, err := h.svc.UpdatePeer(h.peer.PeerID, PeerSpec{EncPolicy: map[string]bool{"aprs_0": false}}, nil); err != nil {
		t.Fatal(err)
	}
	res, err = h.svc.Send(ctx, SendRequest{PeerID: h.peer.PeerID, Via: "aprs_0", Cmd: CmdPing})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ = ExtractFrame(res.Text)
	if f, _ := Open(wire, h.key, Role(h.peer.LocalRole)); f.Enc {
		t.Fatal("aprs policy off but frame encrypted")
	}
	on := true
	res, _ = h.svc.Send(ctx, SendRequest{PeerID: h.peer.PeerID, Via: "aprs_0", Cmd: CmdPing, Encrypt: &on})
	wire, _ = ExtractFrame(res.Text)
	if f, _ := Open(wire, h.key, Role(h.peer.LocalRole)); !f.Enc {
		t.Fatal("override on but frame clear")
	}
	if len(res.Text) > 67 {
		t.Fatalf("aprs request too long: %d", len(res.Text))
	}
}

func TestPeers_BundleAndImport(t *testing.T) {
	h := newHarness(t, RoleControl, false)

	url, err := h.svc.IssueBundle(h.peer.PeerID, "")
	if err != nil || url != "meshsat://key/fake" {
		t.Fatalf("issue: %q %v", url, err)
	}
	// The importing side registers the peer from the bundle entry.
	other := newHarness(t, RoleReadonly, false)
	imported, err := other.svc.RegisterImportedPeer("tesseract", h.key)
	if err != nil {
		t.Fatal(err)
	}
	if imported.PeerID != h.peer.PeerID || imported.LocalRole != int(RoleImporter) || imported.Role != RoleReadonly || imported.KeySource != KeySourceBundle {
		t.Fatalf("imported %+v", imported)
	}
	if again, err := other.svc.RegisterImportedPeer("tesseract", h.key); err != nil || again.PeerID != imported.PeerID {
		t.Fatalf("re-import: %+v %v", again, err)
	}
	if _, err := h.svc.CreatePeer(PeerSpec{Alias: "parallax"}); err == nil {
		t.Fatal("duplicate alias accepted")
	}
	if _, err := h.svc.CreatePeer(PeerSpec{Alias: "x", Source: "ecdh"}); err == nil {
		t.Fatal("ecdh without resolver accepted")
	}
	if err := h.svc.DeletePeer(h.peer.PeerID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.keys.GetKey("mgmt", "parallax"); err == nil {
		t.Fatal("key not revoked on delete")
	}
	if _, err := h.svc.IssueBundle(h.peer.PeerID, ""); err == nil {
		t.Fatal("bundle for a deleted peer")
	}
}

func TestExecuteLocal_HubOrigin(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	res := h.svc.ExecuteLocal(context.Background(), Origin{}, CmdPing, nil)
	if res.Code != RCOK {
		t.Fatalf("%s", res.Code)
	}
	entries, _ := h.db.ListOOBLog(5, 0)
	if len(entries) != 1 || entries[0].Bearer != OriginHub || entries[0].Result != "ok" {
		t.Fatalf("hub log %+v", entries)
	}
}
