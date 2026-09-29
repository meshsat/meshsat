package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"google.golang.org/protobuf/proto"

	"meshsat/internal/database"
	pb "meshsat/internal/gateway/takproto"
	"meshsat/internal/transport"
)

// TAK without a TAK server: SA multicast, the Hub export and the Bridge's own
// events. No test here sends multicast: SA goes to a unicast listener on
// 127.0.0.1, which joins no group. [MESHSAT-1421]

// fakeTAKHub stands in for the Hub reporter.
type fakeTAKHub struct {
	mu        sync.Mutex
	connected bool
	docs      [][]byte
	at        []time.Time
}

func (h *fakeTAKHub) IsConnected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connected
}

func (h *fakeTAKHub) PublishTAKCoT(xml []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.docs = append(h.docs, append([]byte(nil), xml...))
	h.at = append(h.at, time.Now())
	return nil
}

// events are the documents published so far, parsed.
func (h *fakeTAKHub) events(t *testing.T) []CotEvent {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]CotEvent, 0, len(h.docs))
	for _, d := range h.docs {
		ev, err := ParseCotEvent(d)
		if err != nil {
			t.Fatalf("the Hub got something that is not CoT XML: %q: %v", d, err)
		}
		out = append(out, *ev)
	}
	return out
}

// waitFor waits for a published event that match accepts.
func (h *fakeTAKHub) waitFor(t *testing.T, what string, match func(CotEvent) bool) CotEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range h.events(t) {
			if match(ev) {
				return ev
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the Hub never got %s; it has %d documents", what, len(h.events(t)))
	return CotEvent{}
}

// saListener is a unicast stand-in for the TAK SA group.
func saListener(t *testing.T) (*net.UDPConn, *net.UDPAddr) {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, c.LocalAddr().(*net.UDPAddr)
}

// readSA reads one datagram, or reports false after wait.
func readSA(t *testing.T, c *net.UDPConn, wait time.Duration) ([]byte, bool) {
	t.Helper()
	buf := make([]byte, 64*1024)
	if err := c.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatal(err)
	}
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		return nil, false
	}
	return buf[:n], true
}

// decodeSA checks the TAK protocol v1 mesh header and decodes the event.
func decodeSA(t *testing.T, frame []byte) *CotEvent {
	t.Helper()
	if len(frame) < 4 || frame[0] != 0xBF || frame[1] != 0x01 || frame[2] != 0xBF {
		t.Fatalf("frame does not start BF 01 BF: % x", frame[:min(len(frame), 8)])
	}
	msg := &pb.TakMessage{}
	if err := proto.Unmarshal(frame[3:], msg); err != nil {
		t.Fatalf("frame payload is not a TakMessage: %v", err)
	}
	ev, err := ProtoToCotEvent(msg)
	if err != nil {
		t.Fatalf("TakMessage to CoT: %v", err)
	}
	return ev
}

func TestTAKConfigValidate_NoServer(t *testing.T) {
	// The defaults: no server, and both new outputs off, so a kit's saved
	// config behaves as it did.
	cfg, err := ParseTAKConfig(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("no tak_host refused: %v", err)
	}
	if cfg.HasServer() || cfg.Multicast || cfg.HubExport {
		t.Fatalf("defaults: server %v, multicast %v, hub_export %v", cfg.HasServer(), cfg.Multicast, cfg.HubExport)
	}
	if cfg.Port != 8087 || cfg.CallsignPrefix != "MESHSAT" || cfg.CotStaleSec != 300 || cfg.CoalesceSeconds != 30 {
		t.Fatalf("defaults changed: %+v", cfg)
	}

	// What the app sends: no host, the outputs explicit.
	cfg, err = ParseTAKConfig(`{"callsign_prefix":"E2E","multicast":true,"multicast_iface":" waydroid0 ","hub_export":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("app config refused: %v", err)
	}
	if !cfg.Multicast || !cfg.HubExport || cfg.MulticastIface != "waydroid0" || cfg.CallsignPrefix != "E2E" {
		t.Fatalf("parsed: %+v", cfg)
	}

	// A blank host is no host, and the TLS rules are about the server link.
	cfg, _ = ParseTAKConfig(`{"tak_host":"   ","tak_ssl":true}`)
	if err := cfg.Validate(); err != nil || cfg.HasServer() {
		t.Fatalf("blank host with tak_ssl: %v, server %v", err, cfg.HasServer())
	}
	// With a host they apply as before.
	cfg, _ = ParseTAKConfig(`{"tak_host":"tak.example.com","tak_ssl":true}`)
	if err := cfg.Validate(); err == nil {
		t.Fatal("a TLS server without a certificate is accepted")
	}

	// GET answers every field, the new one included.
	b, err := json.Marshal(DefaultTAKConfig().Redacted())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"hub_export":false`, `"multicast":false`, `"multicast_iface":""`, `"tak_host":""`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config JSON lacks %s: %s", want, b)
		}
	}
}

func TestTAKGateway_StartsWithoutServer(t *testing.T) {
	cfg, err := ParseTAKConfig(`{"callsign_prefix":"E2E","multicast":false,"hub_export":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	gw := NewTAKGateway(*cfg, nil)
	begun := time.Now()
	if err := gw.Start(context.Background()); err != nil {
		t.Fatalf("start without a server: %v", err)
	}
	if took := time.Since(begun); took > time.Second {
		t.Errorf("start without a server took %v: nothing should be dialled", took)
	}
	if gw.serverConn() != nil {
		t.Error("a server connection exists without a host")
	}
	if !gw.Status().Connected {
		t.Error("no host and multicast off, yet not connected")
	}
	// hub_export on and no Hub link: nothing goes anywhere, and that is not an
	// error (the scratch Bridge of the e2e case has no Hub).
	if err := gw.SendCotEvent(BuildChatEvent("u", "E2E", "hi", 300)); err != nil {
		t.Errorf("send without a Hub link: %v", err)
	}
	if st := gw.Status(); st.Errors != 0 || st.MessagesOut != 0 {
		t.Errorf("without a Hub link: errors %d, out %d", st.Errors, st.MessagesOut)
	}
	if err := gw.Stop(); err != nil {
		t.Fatal(err)
	}
	if gw.Status().Connected {
		t.Error("connected after Stop")
	}

	// Through the manager, as PUT /api/gateways/tak does it.
	db, err := database.New(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer db.Close()
	m := NewManager(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if err := m.ConfigureInstance(ctx, "tak", "tak_0", true, `{"callsign_prefix":"E2E","multicast":false,"hub_export":true}`); err != nil {
		t.Fatalf("PUT without tak_host: %v", err)
	}
	tg := m.GetTAKGateway()
	if tg == nil {
		t.Fatal("the TAK gateway is not running")
	}
	st, err := m.GetSingleStatus("tak")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.Connected {
		t.Errorf("status: enabled %v, connected %v", st.Enabled, st.Connected)
	}
	if !strings.Contains(string(st.Config), `"hub_export":true`) {
		t.Errorf("GET config: %s", st.Config)
	}
}

func TestTAKMulticastFrame(t *testing.T) {
	c, addr := saListener(t)
	m := NewTAKMulticast("")
	m.dst = addr // unicast on 127.0.0.1: no group is joined, nothing leaves the host
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ttl, err := m.pc.MulticastTTL(); err != nil || ttl != 1 {
		t.Errorf("multicast TTL %d, %v; want 1", ttl, err)
	}
	if !m.Joined() {
		t.Fatal("not ready to send")
	}

	ev := BuildPositionEvent("MESHSAT-aabbccdd", "MESHSAT-CCDD", 52.3676, 4.9041, 12, 300)
	if err := m.SendEvent(ev); err != nil {
		t.Fatalf("send: %v", err)
	}
	frame, ok := readSA(t, c, 2*time.Second)
	if !ok {
		t.Fatal("no datagram arrived")
	}
	got := decodeSA(t, frame)
	if got.UID != "MESHSAT-aabbccdd" || got.Type != CotEventTypePosition {
		t.Errorf("decoded uid %q type %q", got.UID, got.Type)
	}
	if got.Detail == nil || got.Detail.Contact == nil || got.Detail.Contact.Callsign != "MESHSAT-CCDD" {
		t.Errorf("decoded detail %+v", got.Detail)
	}
	if got.Point.Lat != 52.3676 || got.Point.Lon != 4.9041 {
		t.Errorf("decoded point %+v", got.Point)
	}
	if m.Sent() != 1 {
		t.Errorf("sent %d", m.Sent())
	}

	m.Stop()
	if m.Joined() {
		t.Error("joined after Stop")
	}
	if err := m.SendEvent(ev); !errors.Is(err, errTAKMulticastDown) {
		t.Errorf("send after Stop: %v", err)
	}
}

// The real join-and-send path, confined to the loopback interface: SA joined on
// "lo" only, a random organisation-local group and port, TTL 1, so nothing
// leaves the host. Where the kernel or container offers no loopback multicast
// the test is skipped; a wrong join set or a wrong frame fails it.
func TestTAKMulticastLoopback(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil || lo.Flags&net.FlagUp == 0 {
		t.Skip("no loopback interface named lo")
	}
	group := net.IPv4(239, 255, byte(100+rand.Intn(100)), byte(1+rand.Intn(200))).To4()

	// A listener joined on lo, as a TAK client on the same host would be.
	lc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer lc.Close()
	if err := ipv4.NewPacketConn(lc).JoinGroup(lo, &net.UDPAddr{IP: group}); err != nil {
		t.Skipf("no loopback multicast here: %v", err)
	}

	m := NewTAKMulticast("lo")
	m.dst = &net.UDPAddr{IP: group, Port: lc.LocalAddr().(*net.UDPAddr).Port}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if names := m.joinedNames(); !reflect.DeepEqual(names, []string{"lo"}) {
		t.Fatalf("joined %v, want only lo", names)
	}
	if !m.Joined() {
		t.Fatal("not joined")
	}

	ev := BuildChatEvent("MESHSAT-aabbccdd", "MESHSAT-CCDD", "over the loopback", 300)
	if err := m.SendEvent(ev); err != nil {
		t.Skipf("no loopback multicast send here: %v", err)
	}
	frame, ok := readSA(t, lc, 2*time.Second)
	if !ok {
		t.Skip("the kernel took the datagram but delivered nothing on lo")
	}
	if got := decodeSA(t, frame); got.UID != ev.UID || got.Type != CotEventTypeChat {
		t.Errorf("decoded uid %q type %q", got.UID, got.Type)
	}
}

// The interfaces SA joins by itself: up, multicast-capable, not loopback, with
// an IPv4 address. A named one is taken when it is up.
func TestTAKMulticastInterfaces(t *testing.T) {
	all := []net.Interface{
		{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback | net.FlagRunning},
		{Index: 2, Name: "wlan0", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast | net.FlagRunning},
		{Index: 3, Name: "eth0", Flags: net.FlagBroadcast | net.FlagMulticast},
		{Index: 4, Name: "wwan0", Flags: net.FlagUp | net.FlagPointToPoint | net.FlagRunning},
		{Index: 5, Name: "waydroid0", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast},
		{Index: 6, Name: "wg6", Flags: net.FlagUp | net.FlagMulticast | net.FlagRunning},
	}
	hasIPv4 := func(ifi net.Interface) bool { return ifi.Name != "wg6" }
	names := func(list []net.Interface) []string {
		out := []string{}
		for _, ifi := range list {
			out = append(out, ifi.Name)
		}
		return out
	}
	cases := []struct {
		named string
		want  []string
	}{
		{"", []string{"wlan0", "waydroid0"}},
		{"waydroid0", []string{"waydroid0"}},
		{"lo", []string{"lo"}},  // named: the caller's choice
		{"eth0", []string{}},    // down
		{"absent0", []string{}}, // not there (yet)
	}
	for _, c := range cases {
		if got := names(takMulticastIfaces(all, c.named, hasIPv4)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("named %q: got %v, want %v", c.named, got, c.want)
		}
	}
}

func TestTAKEmitFanOut(t *testing.T) {
	cases := []struct {
		name                string
		server, mcast, hub  bool
		wantOut, wantErrors int64
	}{
		{"server only", true, false, false, 1, 0},
		{"multicast only", false, true, false, 1, 0},
		{"hub only", false, false, true, 1, 0},
		{"all three", true, true, true, 1, 0},
		{"none", false, false, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			udp, udpAddr := saListener(t)
			hub := &fakeTAKHub{connected: true}
			cfg := TAKConfig{
				CallsignPrefix:  "E2E",
				CotStaleSec:     300,
				CoalesceSeconds: 3600,
				Multicast:       tc.mcast,
				HubExport:       tc.hub,
			}
			var srv *mockTAKServer
			if tc.server {
				srv = newMockTAKServer(t)
				defer srv.close()
				cfg.Host, cfg.Port = splitHostPort(t, srv.addr())
			}
			gw := NewTAKGateway(cfg, nil)
			gw.mcastDst = udpAddr
			gw.SetHooks(TAKHooks{Hub: hub, NodeID: func() string { return "aabbccdd" }})
			if err := gw.Start(context.Background()); err != nil {
				t.Fatalf("start: %v", err)
			}
			defer gw.Stop()

			text := "fan-out: " + tc.name
			if err := gw.SendOwnChat(text); err != nil {
				t.Fatalf("send: %v", err)
			}
			isOurs := func(ev CotEvent) bool {
				return ev.Type == CotEventTypeChat && ev.Detail != nil && ev.Detail.Remarks != nil &&
					ev.Detail.Remarks.Text == text && ev.Detail.Contact != nil && ev.Detail.Contact.Callsign == "E2E-CCDD"
			}

			// The Hub
			if tc.hub {
				hub.waitFor(t, "the chat", isOurs)
			} else if n := len(hub.events(t)); n != 0 {
				t.Errorf("hub_export off, yet the Hub got %d documents", n)
			}

			// SA multicast
			wait := 200 * time.Millisecond
			if tc.mcast {
				wait = 2 * time.Second
			}
			frame, got := readSA(t, udp, wait)
			if tc.mcast {
				if !got {
					t.Fatal("multicast on, yet no SA arrived")
				}
				if ev := decodeSA(t, frame); ev.Type != CotEventTypeChat || !strings.HasPrefix(ev.UID, "MESHSAT-aabbccdd-CHAT-") {
					t.Errorf("SA: type %q uid %q", ev.Type, ev.UID)
				}
			} else if got {
				t.Error("multicast off, yet SA arrived")
			}

			// The TAK server
			if tc.server {
				deadline := time.Now().Add(3 * time.Second)
				for !containsEvent(srv.events(), isOurs) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if !containsEvent(srv.events(), isOurs) {
					t.Errorf("the TAK server never got the chat: %d events", len(srv.events()))
				}
			}

			st := gw.Status()
			if st.MessagesOut != tc.wantOut || st.Errors != tc.wantErrors {
				t.Errorf("out %d errors %d, want %d and %d", st.MessagesOut, st.Errors, tc.wantOut, tc.wantErrors)
			}
			if !st.Connected {
				t.Error("not connected with every switched-on output up")
			}
		})
	}

	// An output without a link is skipped, is no error, and the caller is not
	// told: a Hub that dropped, a hub_export without any Hub.
	t.Run("hub without a session", func(t *testing.T) {
		hub := &fakeTAKHub{connected: false}
		gw := NewTAKGateway(TAKConfig{CallsignPrefix: "E2E", CotStaleSec: 300, HubExport: true}, nil)
		gw.SetHooks(TAKHooks{Hub: hub})
		if err := gw.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer gw.Stop()
		if err := gw.SendOwnChat("nobody listens"); err != nil {
			t.Errorf("send: %v", err)
		}
		if st := gw.Status(); st.MessagesOut != 0 || st.Errors != 0 {
			t.Errorf("out %d errors %d", st.MessagesOut, st.Errors)
		}
		if n := len(hub.events(t)); n != 0 {
			t.Errorf("a disconnected Hub got %d documents", n)
		}
	})
}

func containsEvent(evs []CotEvent, match func(CotEvent) bool) bool {
	for _, ev := range evs {
		if match(ev) {
			return true
		}
	}
	return false
}

// Android's CotBuilderTest "callsign format matches bridge", case for case.
func TestTAKOwnCallsign(t *testing.T) {
	cases := []struct{ nodeID, prefix, want string }{
		{"123456", "", "MESHSAT-3456"},
		{"123456", "MESHSAT", "MESHSAT-3456"},
		{"abcd", "", "MESHSAT-ABCD"},
		{"", "", "MESHSAT"},
		{"123456", "MS", "MS-3456"},
		// The Bridge's node ids: the last four hex digits, upper case.
		{"aabbccdd", "MESHSAT", "MESHSAT-CCDD"},
		{"", "E2E", "E2E"},
	}
	for _, c := range cases {
		if got := OwnTAKCallsign(c.nodeID, c.prefix); got != c.want {
			t.Errorf("OwnTAKCallsign(%q, %q) = %q, want %q", c.nodeID, c.prefix, got, c.want)
		}
	}
	if got := OwnTAKUID("aabbccdd"); got != "MESHSAT-aabbccdd" {
		t.Errorf("OwnTAKUID = %q", got)
	}
}

// The Bridge's own events, Android's list: PLI at most every
// coalesce_seconds, SOS only with a position, dead man, chat; routed nodes
// keep their own callsigns.
func TestTAKOwnEvents(t *testing.T) {
	var mu sync.Mutex
	nodeID, havePos := "aabbccdd", true
	hub := &fakeTAKHub{connected: true}
	gw := NewTAKGateway(TAKConfig{CallsignPrefix: "MESHSAT", CotStaleSec: 300, CoalesceSeconds: 1, HubExport: true}, nil)
	gw.SetHooks(TAKHooks{
		Hub: hub,
		NodeID: func() string {
			mu.Lock()
			defer mu.Unlock()
			return nodeID
		},
		SelfPosition: func() (float64, float64, float64, bool) {
			mu.Lock()
			defer mu.Unlock()
			return 52.3676, 4.9041, 12, havePos
		},
	})
	if err := gw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer gw.Stop()

	// PLI: one at once, the next no sooner than coalesce_seconds.
	isPLI := func(ev CotEvent) bool {
		return ev.Type == CotEventTypePosition && ev.Detail != nil && ev.Detail.Emergency == nil
	}
	pli := hub.waitFor(t, "a PLI", isPLI)
	if pli.UID != "MESHSAT-aabbccdd" || pli.Detail.Contact.Callsign != "MESHSAT-CCDD" || pli.Point.Lat != 52.3676 || pli.Point.Hae != 12 {
		t.Errorf("PLI: uid %q callsign %q point %+v", pli.UID, pli.Detail.Contact.Callsign, pli.Point)
	}
	deadline := time.Now().Add(5 * time.Second)
	var times []time.Time
	for time.Now().Before(deadline) {
		times = times[:0]
		hub.mu.Lock()
		for i, d := range hub.docs {
			if ev, err := ParseCotEvent(d); err == nil && isPLI(*ev) {
				times = append(times, hub.at[i])
			}
		}
		hub.mu.Unlock()
		if len(times) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(times) < 2 {
		t.Fatalf("%d PLIs in 5 s with coalesce_seconds 1", len(times))
	}
	if gap := times[1].Sub(times[0]); gap < 900*time.Millisecond {
		t.Errorf("two PLIs %v apart, coalesce_seconds is 1", gap)
	}

	// SOS at the Bridge's position, the reason as its text.
	if err := gw.SendOwnSOS("help at the booth"); err != nil {
		t.Fatalf("SOS: %v", err)
	}
	sos := hub.waitFor(t, "the SOS", func(ev CotEvent) bool { return ev.Detail != nil && ev.Detail.Emergency != nil })
	if sos.UID != "MESHSAT-aabbccdd" || sos.Detail.Emergency.Type != "911 Alert" || sos.Detail.Emergency.Text != "help at the booth" ||
		sos.Detail.Remarks == nil || sos.Detail.Remarks.Text != "Emergency: help at the booth" || sos.Point.Lon != 4.9041 {
		t.Errorf("SOS: %+v %+v", sos, sos.Detail)
	}

	// Dead man, at the position the switch gives.
	if err := gw.SendOwnDeadman(51.5, -0.12, time.Now().Add(-90*time.Second)); err != nil {
		t.Fatalf("dead man: %v", err)
	}
	dm := hub.waitFor(t, "the dead man", func(ev CotEvent) bool { return ev.Type == CotEventTypeAlarm })
	if dm.UID != "MESHSAT-aabbccdd-DEADMAN" || dm.Point.Lat != 51.5 || dm.Detail.Remarks == nil || !strings.Contains(dm.Detail.Remarks.Text, "no check-in for 90s") {
		t.Errorf("dead man: %+v %+v", dm, dm.Detail)
	}

	// Chat, from the Bridge itself.
	if err := gw.SendOwnChat("hello TAK"); err != nil {
		t.Fatal(err)
	}
	chat := hub.waitFor(t, "the chat", func(ev CotEvent) bool {
		return ev.Type == CotEventTypeChat && ev.Detail.Remarks != nil && ev.Detail.Remarks.Text == "hello TAK"
	})
	if !strings.HasPrefix(chat.UID, "MESHSAT-aabbccdd-CHAT-") || chat.Detail.Remarks.Source != "MESHSAT-CCDD" {
		t.Errorf("chat: uid %q source %q", chat.UID, chat.Detail.Remarks.Source)
	}

	// A routed mesh text keeps the node's own identity.
	if err := gw.Forward(context.Background(), &transport.MeshMessage{From: 0x1234abcd, PortNum: 1, DecodedText: "from a node"}); err != nil {
		t.Fatal(err)
	}
	routed := hub.waitFor(t, "the routed text", func(ev CotEvent) bool {
		return ev.Type == CotEventTypeChat && ev.Detail.Remarks != nil && ev.Detail.Remarks.Text == "from a node"
	})
	if !strings.HasPrefix(routed.UID, "meshsat-1234abcd-CHAT-") || routed.Detail.Contact.Callsign != "MESHSAT-abcd" {
		t.Errorf("routed: uid %q callsign %q", routed.UID, routed.Detail.Contact.Callsign)
	}

	// No position: no SOS, and the caller hears why.
	mu.Lock()
	havePos = false
	nodeID = ""
	mu.Unlock()
	emergencies := func() int {
		n := 0
		for _, ev := range hub.events(t) {
			if ev.Detail != nil && ev.Detail.Emergency != nil {
				n++
			}
		}
		return n
	}
	before := emergencies()
	if err := gw.SendOwnSOS("again"); !errors.Is(err, ErrTAKNoPosition) {
		t.Errorf("SOS without a position: %v", err)
	}
	if n := emergencies(); n != before {
		t.Errorf("SOS without a position published %d emergencies", n-before)
	}
	// No node id: the prefix alone.
	if err := gw.SendOwnChat("anonymous"); err != nil {
		t.Fatal(err)
	}
	anon := hub.waitFor(t, "the chat without a node id", func(ev CotEvent) bool {
		return ev.Type == CotEventTypeChat && ev.Detail.Remarks != nil && ev.Detail.Remarks.Text == "anonymous"
	})
	if anon.Detail.Contact.Callsign != "MESHSAT" {
		t.Errorf("callsign without a node id: %q", anon.Detail.Contact.Callsign)
	}
}

// Stopped, a gateway sends nothing more, not even to the Hub.
func TestTAKGateway_NothingAfterStop(t *testing.T) {
	hub := &fakeTAKHub{connected: true}
	gw := NewTAKGateway(TAKConfig{CallsignPrefix: "E2E", CotStaleSec: 300, HubExport: true}, nil)
	gw.SetHooks(TAKHooks{Hub: hub})
	if err := gw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := gw.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := gw.SendOwnChat("late"); err != nil {
		t.Errorf("send after Stop: %v", err)
	}
	if n := len(hub.events(t)); n != 0 {
		t.Errorf("the Hub got %d documents after Stop", n)
	}
}
