package routing

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/kiss"
)

// Golden values from RNS 1.5.4 AutoInterface (computed in the pinned venv).
func TestAutoInterfaceDerivations(t *testing.T) {
	cases := []struct{ group, mcast, tokenFE80_1 string }{
		{"reticulum", "ff12:0:d70b:fb1c:16e4:5e39:485e:31e1", "97b25576749ea936b0d8a8536ffaf442d157cf47d460dcf13c48b7bd18b6c163"},
		{"meshsat-test", "ff12:0:8e1:5fef:2248:d6fe:b6f6:e0df", "1cdcd0751ef8a697341788be3219a494b5da5095f3279cbd2f4894b536009402"},
	}
	for _, c := range cases {
		if got := AutoMulticastGroup(c.group); !got.Equal(net.ParseIP(c.mcast)) {
			t.Errorf("group %s: %s, want %s", c.group, got, c.mcast)
		}
		tok := AutoDiscoveryToken(c.group, "fe80::1")
		if hex.EncodeToString(tok[:]) != c.tokenFE80_1 {
			t.Errorf("token %s: %x", c.group, tok)
		}
	}
	// Upstream: re.sub("fe80:[0-9a-f]*::", "fe80::", addr.split("%")[0]).
	if DescopeLinkLocal("fe80:3::1234:5678:9abc:def0%eth0") != "fe80::1234:5678:9abc:def0" {
		t.Errorf("descope: %s", DescopeLinkLocal("fe80:3::1234:5678:9abc:def0%eth0"))
	}
	if DescopeLinkLocal("fe80::1%v0") != "fe80::1" {
		t.Errorf("descope simple")
	}
}

func waitCond(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	dl := time.Now().Add(d)
	for time.Now().Before(dl) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func freeUDPPort(t *testing.T) int {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return p
}

func TestUDPInterfaceLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pa, pb := freeUDPPort(t), freeUDPPort(t)
	var mu sync.Mutex
	var gotA, gotB [][]byte
	a := NewUDPInterface(UDPInterfaceConfig{Name: "udp_a", ListenAddr: fmt.Sprintf("127.0.0.1:%d", pa), ForwardAddr: fmt.Sprintf("127.0.0.1:%d", pb)},
		func(p []byte) { mu.Lock(); gotA = append(gotA, p); mu.Unlock() })
	b := NewUDPInterface(UDPInterfaceConfig{Name: "udp_b", ListenAddr: fmt.Sprintf("127.0.0.1:%d", pb), ForwardAddr: fmt.Sprintf("127.0.0.1:%d", pa)},
		func(p []byte) { mu.Lock(); gotB = append(gotB, p); mu.Unlock() })
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	defer b.Stop()
	if err := a.Send(ctx, []byte("a to b")); err != nil {
		t.Fatal(err)
	}
	if err := b.Send(ctx, []byte("b to a")); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "both delivered", 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(gotA) == 1 && len(gotB) == 1
	})
	if string(gotB[0]) != "a to b" || string(gotA[0]) != "b to a" {
		t.Fatalf("got %q %q", gotB[0], gotA[0])
	}
	rx, tx := a.Counters()
	if rx != 6 || tx != 6 {
		t.Fatalf("counters %d %d", rx, tx)
	}
	a.Stop()
	if a.IsOnline() {
		t.Fatal("still online after stop")
	}
}

// A fake KISS TNC over TCP: records the parameter frames, echoes a data
// frame back and accepts one.
func TestKISSInterfaceTCPTNC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	frames := make(chan kiss.Frame, 32)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		sp := &kiss.Splitter{MaxLen: 1024, Timeout: time.Second}
		buf := make([]byte, 1024)
		// One data frame from the TNC to the bridge after the params.
		sent := false
		for {
			n, err := c.Read(buf)
			if n > 0 {
				for _, f := range sp.Feed(buf[:n], time.Now()) {
					frames <- f
					if !sent && f.Cmd == kissCmdReady {
						c.Write(kiss.Encode(kiss.CmdData, []byte("from tnc")))
						sent = true
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var mu sync.Mutex
	var got []string
	k := NewKISSInterface(KISSInterfaceConfig{Name: "kiss_0", Port: "tcp://" + ln.Addr().String(), PreambleMs: 400, TXTailMs: 30, Persistence: 100, SlotTimeMs: 50},
		func(p []byte) { mu.Lock(); got = append(got, string(p)); mu.Unlock() })
	if err := k.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	waitCond(t, "online", 5*time.Second, k.IsOnline)
	// Parameter frames in upstream order, ms/10.
	want := []struct {
		cmd byte
		v   byte
	}{{kissCmdTXDelay, 40}, {kissCmdTXTail, 3}, {kissCmdP, 100}, {kissCmdSlotTime, 5}, {kissCmdReady, 1}}
	for _, w := range want {
		select {
		case f := <-frames:
			if f.Cmd != w.cmd || len(f.Payload) != 1 || f.Payload[0] != w.v {
				t.Fatalf("param frame %02x %x, want %02x %02x", f.Cmd, f.Payload, w.cmd, w.v)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no param frame %02x", w.cmd)
		}
	}
	waitCond(t, "data from tnc", 3*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	if got[0] != "from tnc" {
		t.Fatalf("got %q", got[0])
	}
	if err := k.Send(ctx, []byte("to tnc")); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-frames:
		if f.Cmd != kiss.CmdData || string(f.Payload) != "to tnc" {
			t.Fatalf("data frame %02x %q", f.Cmd, f.Payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no data frame at the tnc")
	}
	if err := k.Send(ctx, make([]byte, KISSHWMTU+1)); err == nil {
		t.Fatal("oversize packet accepted")
	}
}

type fakeSink struct {
	mu      sync.Mutex
	senders map[string]bool
	packets []string
}

func (f *fakeSink) InjectReticulumPacket(p []byte, iface string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.packets = append(f.packets, iface+":"+string(p))
}
func (f *fakeSink) RegisterPacketSender(id string, fn func(ctx context.Context, data []byte) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.senders[id] = true
}
func (f *fakeSink) UnregisterPacketSender(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.senders, id)
}

func TestValidateDynConfig(t *testing.T) {
	if _, err := ValidateDynConfig("bogus", nil); err == nil {
		t.Fatal("unknown type accepted")
	}
	if _, err := ValidateDynConfig(DynTypeAuto, json.RawMessage(`{"devices":["wlan0"]}`)); err == nil {
		t.Fatal("wlan0 accepted for auto")
	}
	if _, err := ValidateDynConfig(DynTypeUDP, json.RawMessage(`{"device":"wlan0"}`)); err == nil {
		t.Fatal("wlan0 accepted for udp")
	}
	if _, err := ValidateDynConfig(DynTypeUDP, json.RawMessage(`{}`)); err == nil {
		t.Fatal("udp without forward accepted")
	}
	norm, err := ValidateDynConfig(DynTypeAuto, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var ac AutoInterfaceConfig
	json.Unmarshal(norm, &ac)
	if ac.GroupID != "reticulum" || len(ac.Devices) != 1 || ac.Devices[0] != "eth0" {
		t.Fatalf("auto defaults %+v", ac)
	}
	norm, err = ValidateDynConfig(DynTypeRNode, json.RawMessage(`{"preset":"eu-868"}`))
	if err != nil {
		t.Fatal(err)
	}
	var rc RNodeInterfaceConfig
	json.Unmarshal(norm, &rc)
	if rc.Port != "auto" || rc.Params.Frequency != 867_200_000 || rc.Params.SF != 8 {
		t.Fatalf("rnode preset fill %+v", rc)
	}
	if _, err := ValidateDynConfig(DynTypeRNode, json.RawMessage(`{"preset":"mars-1"}`)); err == nil {
		t.Fatal("unknown preset accepted")
	}
	if _, err := ValidateDynConfig(DynTypeRNode, json.RawMessage(`{"params":{"frequency":1,"bandwidth":125000,"spreadingfactor":7,"codingrate":5,"txpower":10}}`)); err == nil {
		t.Fatal("out-of-range frequency accepted")
	}
	if _, err := ValidateDynConfig(DynTypeKISS, json.RawMessage(`{}`)); err == nil {
		t.Fatal("kiss without port accepted")
	}

	// tcp_rns: the host is required, the port defaults to 4242 and must be
	// 1-65535, and tls is stored as sent (port 443 forces TLS only when dialling).
	for _, c := range []struct{ in, want, err string }{
		{`{}`, "", "tcp_rns: host required"},
		{``, "", "tcp_rns: host required"},
		{`{"host":"   ","port":4242}`, "", "tcp_rns: host required"},
		{`{"host":"rns.example.org"}`, `{"host":"rns.example.org","port":4242,"tls":false}`, ""},
		{`{"host":" 10.0.0.5 ","port":0,"tls":true}`, `{"host":"10.0.0.5","port":4242,"tls":true}`, ""},
		{`{"host":"reticulum.meshsat.net","port":4243,"tls":true}`, `{"host":"reticulum.meshsat.net","port":4243,"tls":true}`, ""},
		{`{"host":"rns.example.org","port":443}`, `{"host":"rns.example.org","port":443,"tls":false}`, ""},
		{`{"host":"h","port":1}`, `{"host":"h","port":1,"tls":false}`, ""},
		{`{"host":"h","port":65535}`, `{"host":"h","port":65535,"tls":false}`, ""},
		{`{"host":"h","port":65536}`, "", "tcp_rns: port must be 1-65535"},
		{`{"host":"h","port":-1}`, "", "tcp_rns: port must be 1-65535"},
		{`{"host":"h","port":"4242"}`, "", "tcp_rns config: "},
	} {
		norm, err := ValidateDynConfig(DynTypeTCPRNS, json.RawMessage(c.in))
		if c.err != "" {
			if err == nil || !strings.HasPrefix(err.Error(), c.err) {
				t.Errorf("tcp_rns %s: error %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || string(norm) != c.want {
			t.Errorf("tcp_rns %s: %s %v, want %s", c.in, norm, err, c.want)
		}
	}
}

func TestIfaceManagerLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sink := &fakeSink{senders: map[string]bool{}}
	reg := NewInterfaceRegistry()
	m := NewIfaceManager(IfaceManagerConfig{DB: db, Registry: reg, Sink: sink})
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	pa, pb := freeUDPPort(t), freeUDPPort(t)
	cfg := json.RawMessage(fmt.Sprintf(`{"listen_addr":"127.0.0.1:%d","forward_addr":"127.0.0.1:%d"}`, pa, pb))
	id, err := m.Create(DynTypeUDP, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if id != "udp_0" {
		t.Fatalf("id %s", id)
	}
	st, _ := m.Get(id)
	if !st.Running || !st.Online || !sink.senders[id] || reg.Get(id) == nil {
		t.Fatalf("not wired: %+v senders=%v", st, sink.senders)
	}
	if reg.Get(id).MTU() != UDPHWMTU || reg.Get(id).Type() != "udp" || !reg.Get(id).IsFloodable() {
		t.Fatalf("registry entry %+v", reg.Get(id))
	}
	// A packet from the wire reaches the sink tagged with the id.
	c, _ := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: pa})
	c.Write([]byte("hi"))
	c.Close()
	waitCond(t, "inbound via manager", 3*time.Second, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.packets) == 1 && sink.packets[0] == "udp_0:hi"
	})
	// Seed is a no-op once an instance of the type exists.
	if sid, err := m.Seed(DynTypeUDP, UDPInterfaceConfig{ForwardAddr: "127.0.0.1:1"}); err != nil || sid != "" {
		t.Fatalf("seed %q %v", sid, err)
	}
	// Disable: stops, unregisters, keeps the row.
	if err := m.Update(id, nil, false); err != nil {
		t.Fatal(err)
	}
	st, _ = m.Get(id)
	if st.Running || sink.senders[id] || reg.Get(id) != nil || st.Enabled {
		t.Fatalf("still wired after disable: %+v", st)
	}
	// Re-enable with a bad config is rejected and the old config kept.
	if err := m.Update(id, json.RawMessage(`{"device":"wlan0"}`), true); err == nil {
		t.Fatal("wlan0 accepted")
	}
	if err := m.Update(id, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(id); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "online after restart", 3*time.Second, func() bool { s, _ := m.Get(id); return s.Online })
	// Persistence: a fresh manager on the same DB starts it again.
	m.StopAll()
	if reg.Get(id) != nil {
		t.Fatal("registered after StopAll")
	}
	m2 := NewIfaceManager(IfaceManagerConfig{DB: db, Registry: reg, Sink: sink})
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := m2.Get(id); !s.Running {
		t.Fatalf("not restarted from the table: %+v", s)
	}
	if err := m2.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Get(id); err != database.ErrRoutingIfaceNotFound {
		t.Fatalf("still there: %v", err)
	}
	if rows, _ := db.ListRoutingIfaces(); len(rows) != 0 {
		t.Fatalf("row survived delete: %+v", rows)
	}
	// A KISS instance whose TNC is absent stays "running" but offline and reports the error.
	kid, err := m2.Create(DynTypeKISS, json.RawMessage(`{"port":"tcp://127.0.0.1:1"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	waitCond(t, "kiss connect error surfaced", 8*time.Second, func() bool { s, _ := m2.Get(kid); return s.Running && !s.Online && s.LastError != "" })

	// tcp_rns: the first instance is tcp_rns_0 (Android's link id), online
	// means connected, packets reach the sink under its id, disabling stops
	// it and keeps the config, and a refused connection is not online.
	node := startRNSNode(t, nil, []byte(helloFromNode))
	rnsCfg := `{"host":"127.0.0.1","port":` + strconv.Itoa(node.port()) + `,"tls":false}`
	tid, err := m2.Create(DynTypeTCPRNS, json.RawMessage(`{"host":"127.0.0.1","port":`+strconv.Itoa(node.port())+`}`), true)
	if err != nil {
		t.Fatal(err)
	}
	if tid != "tcp_rns_0" {
		t.Fatalf("id %s, want tcp_rns_0", tid)
	}
	waitCond(t, "tcp_rns online", 5*time.Second, func() bool { s, _ := m2.Get(tid); return s.Online })
	st, _ = m2.Get(tid)
	if !st.Running || !st.Enabled || st.LastError != "" || st.Summary != "127.0.0.1:"+strconv.Itoa(node.port()) || string(st.Config) != rnsCfg {
		t.Fatalf("tcp_rns status %+v", st)
	}
	ri := reg.Get(tid)
	if ri == nil || ri.Type() != "tcp" || ri.MTU() != TCPRNSHWMTU || !ri.IsFloodable() || !ri.IsOnline() || !sink.senders[tid] {
		t.Fatalf("tcp_rns not wired: %+v senders=%v", ri, sink.senders)
	}
	waitCond(t, "inbound via tcp_rns", 3*time.Second, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for _, p := range sink.packets {
			if p == "tcp_rns_0:"+helloFromNode {
				return true
			}
		}
		return false
	})
	if err := ri.Send(ctx, []byte(helloToNode)); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "outbound via tcp_rns", 3*time.Second, func() bool { got := node.view().got; return len(got) == 1 && got[0] == helloToNode })
	// PUT enabled:false (no config): stopped, unwired, config kept, connection closed.
	if err := m2.Update(tid, nil, false); err != nil {
		t.Fatal(err)
	}
	st, _ = m2.Get(tid)
	if st.Enabled || st.Running || st.Online || reg.Get(tid) != nil || sink.senders[tid] || string(st.Config) != rnsCfg {
		t.Fatalf("tcp_rns after disable: %+v", st)
	}
	waitCond(t, "the node sees the connection closed", 3*time.Second, func() bool { return node.view().ends == 1 })
	if n := node.view().accepts; n != 1 {
		t.Fatalf("%d connections to the node, want 1", n)
	}
	// Enabled again towards a port nobody listens on: running, not online, the refusal in last_error.
	if err := m2.Update(tid, json.RawMessage(`{"host":"127.0.0.1","port":`+strconv.Itoa(freePortNum(t))+`}`), true); err != nil {
		t.Fatal(err)
	}
	waitCond(t, "refused connection surfaced", 8*time.Second, func() bool { s, _ := m2.Get(tid); return s.LastError != "" })
	st, _ = m2.Get(tid)
	if !st.Running || st.Online || !strings.Contains(st.LastError, "refused") || reg.Get(tid) == nil || reg.Get(tid).IsOnline() {
		t.Fatalf("tcp_rns refused: %+v", st)
	}
	if err := reg.Get(tid).Send(ctx, []byte(helloToNode)); err == nil {
		t.Fatal("sent while not connected")
	}
	m2.StopAll()
}
