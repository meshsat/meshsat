package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
)

// The row the Hub keeps for the workspace's Private network [MESHSAT-1715].

type hubAccessKit struct {
	m     *IfaceManager
	reg   *InterfaceRegistry
	sink  *fakeSink
	port  int
	asks  int
	askMu sync.Mutex
}

func newHubAccessKit(t *testing.T) *hubAccessKit {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	k := &hubAccessKit{reg: NewInterfaceRegistry(), sink: &fakeSink{senders: map[string]bool{}}, port: ln.Addr().(*net.TCPAddr).Port}
	k.m = NewIfaceManager(IfaceManagerConfig{DB: db, Registry: k.reg, Coded: NewCodedIfaces(), Sink: k.sink})
	if err := k.m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.m.StopAll)
	return k
}

func (k *hubAccessKit) endpoint() string { return fmt.Sprintf("127.0.0.1:%d", k.port) }

func (k *hubAccessKit) access(t *testing.T, enabled bool) *HubAccess {
	t.Helper()
	h, err := NewHubAccess(HubAccessConfig{Ifaces: k.m, Enabled: enabled, Endpoint: k.endpoint(), NodeRunning: true,
		Ask: func() bool { k.askMu.Lock(); k.asks++; k.askMu.Unlock(); return true }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (k *hubAccessKit) msg(mode, host string, port int, netname, pass string) []byte {
	b, _ := json.Marshal(map[string]any{"v": 1, "mode": mode, "host": host, "port": port, "ifac_netname": netname, "ifac_passphrase": pass})
	return b
}

// stored returns every tcp_rns row as stored (passphrase included).
func (k *hubAccessKit) stored(t *testing.T) map[string]TCPRNSConfig {
	t.Helper()
	out := map[string]TCPRNSConfig{}
	k.m.mu.Lock()
	defer k.m.mu.Unlock()
	for id, in := range k.m.inst {
		if in.row.Type != DynTypeTCPRNS {
			continue
		}
		var c TCPRNSConfig
		if err := json.Unmarshal([]byte(in.row.Config), &c); err != nil {
			t.Fatal(err)
		}
		out[id] = c
	}
	return out
}

func (k *hubAccessKit) managed(t *testing.T) (string, TCPRNSConfig, int) {
	t.Helper()
	var id string
	var cfg TCPRNSConfig
	n := 0
	for rid, c := range k.stored(t) {
		if c.Managed == TCPRNSManagedHub {
			id, cfg = rid, c
			n++
		}
	}
	return id, cfg, n
}

func TestHubAccess_KeepsOneManagedLeafRowAsTheHubSays(t *testing.T) {
	k := newHubAccessKit(t)
	h := k.access(t, true)
	h.Apply(k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", "first-passphrase"))
	id, c, n := k.managed(t)
	if n != 1 || !c.Coded() || c.IFACPassphrase != "first-passphrase" || c.TLS {
		t.Fatalf("after the first word: %d managed rows, %+v", n, c)
	}
	// A coded row: never in the registry or the processor (coded.go).
	if k.reg.Get(id) != nil || k.sink.senders[id] {
		t.Fatal("the Hub's row is wired into the legacy layer")
	}
	waitCond(t, "the row comes up", 5*time.Second, func() bool { st, _ := k.m.Get(id); return st != nil && st.Coded })
	if st, _ := k.m.Get(id); strings.Contains(string(st.Config), "first-passphrase") {
		t.Fatal("the status shows the passphrase")
	}
	// The same word again changes nothing.
	if res, _ := k.m.ReconcileManaged(&TCPRNSConfig{Host: "127.0.0.1", Port: k.port, IFACNetname: "meshsat-0a1b2c3d", IFACPassphrase: "first-passphrase"}); res != "unchanged "+id {
		t.Fatalf("the same word: %s", res)
	}
	// A rotation updates the same row.
	h.Apply(k.msg("private", "127.0.0.1", k.port, "meshsat-ffff0000", "second-passphrase"))
	id2, c2, n := k.managed(t)
	if n != 1 || id2 != id || c2.IFACNetname != "meshsat-ffff0000" || c2.IFACPassphrase != "second-passphrase" {
		t.Fatalf("after a rotation: %d rows, %s %+v", n, id2, c2)
	}
	// none removes it.
	h.Apply(k.msg("none", "", 0, "", ""))
	if _, _, n := k.managed(t); n != 0 || len(k.stored(t)) != 0 {
		t.Fatalf("after none: %d managed, %d tcp_rns rows", n, len(k.stored(t)))
	}
}

// Only the code comes from the message: another endpoint, a version this kit
// does not read, an unknown mode or a code that is not one changes nothing.
func TestHubAccess_IgnoresWhatItMustNotApply(t *testing.T) {
	k := newHubAccessKit(t)
	h := k.access(t, true)
	for name, p := range map[string][]byte{
		"another host":    k.msg("private", "collector.example", k.port, "meshsat-0a1b2c3d", "pass"),
		"another port":    k.msg("private", "127.0.0.1", 443, "meshsat-0a1b2c3d", "pass"),
		"no passphrase":   k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", ""),
		"no network":      k.msg("private", "127.0.0.1", k.port, "", "pass"),
		"unknown mode":    k.msg("public", "127.0.0.1", k.port, "meshsat-0a1b2c3d", "pass"),
		"version 2":       []byte(`{"v":2,"mode":"private","host":"127.0.0.1","port":1,"ifac_netname":"n","ifac_passphrase":"p"}`),
		"not json":        []byte(`{`),
		"long netname":    k.msg("private", "127.0.0.1", k.port, strings.Repeat("n", 129), "pass"),
		"long passphrase": k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", strings.Repeat("p", 257)),
	} {
		h.Apply(p)
		if rows := k.stored(t); len(rows) != 0 {
			t.Fatalf("%s: rows %+v", name, rows)
		}
	}
}

// The switch off, or the kit's node off: nothing is applied and the Hub's
// row goes at start; a person's own coded row stays.
func TestHubAccess_OffRemovesTheHubsRowOnly(t *testing.T) {
	k := newHubAccessKit(t)
	k.access(t, true).Apply(k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", "pass"))
	own, err := k.m.Create(DynTypeTCPRNS, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":"someone-else","ifac_passphrase":"theirs"}`, k.port)), true)
	if err != nil {
		t.Fatal(err)
	}
	off := k.access(t, false)
	off.Start(context.Background())
	off.Apply(k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", "pass"))
	rows := k.stored(t)
	if _, _, n := k.managed(t); n != 0 || len(rows) != 1 || rows[own].IFACNetname != "someone-else" {
		t.Fatalf("after off: %+v", rows)
	}
}

// A coded row a person made by hand for the same network is taken over, and
// extra managed rows collapse into one.
func TestHubAccess_AdoptsTheHandMadeRowAndCollapsesDuplicates(t *testing.T) {
	k := newHubAccessKit(t)
	hand, err := k.m.Create(DynTypeTCPRNS, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":"meshsat-0a1b2c3d","ifac_passphrase":"pass"}`, k.port)), true)
	if err != nil {
		t.Fatal(err)
	}
	h := k.access(t, true)
	h.Apply(k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", "pass"))
	if id, _, n := k.managed(t); n != 1 || id != hand || len(k.stored(t)) != 1 {
		t.Fatalf("not adopted: managed %s (%d), rows %d", id, n, len(k.stored(t)))
	}
	// A second managed row (an older kit version, a restore): only the Hub's
	// path can make one.
	k.m.opMu.Lock()
	extra, err := k.m.create(DynTypeTCPRNS, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":"x","ifac_passphrase":"y","managed":"hub"}`, k.port)), true)
	k.m.opMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.Apply(k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", "pass"))
	if id, _, n := k.managed(t); n != 1 || id != hand || k.stored(t)[extra].Host != "" {
		t.Fatalf("duplicates left: managed %s (%d)", id, n)
	}
}

// An edit through the API keeps the mark and cannot take the code off: the
// row would otherwise run on the legacy layer.
func TestHubAccess_AnEditKeepsTheMarkAndTheCode(t *testing.T) {
	k := newHubAccessKit(t)
	k.access(t, true).Apply(k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", "pass"))
	id, _, _ := k.managed(t)
	if err := k.m.Update(id, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":"meshsat-0a1b2c3d"}`, k.port)), true); err != nil {
		t.Fatal(err)
	}
	if _, c, n := k.managed(t); n != 1 || c.IFACPassphrase != "pass" {
		t.Fatalf("the mark or the passphrase was lost: %d %+v", n, c)
	}
	if err := k.m.Update(id, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d}`, k.port)), true); err == nil {
		t.Fatal("the Hub's row was made uncoded")
	}
	if _, err := ValidateDynConfig(DynTypeTCPRNS, json.RawMessage(`{"host":"h","managed":"someone"}`)); err == nil {
		t.Fatal("a managed value other than hub was accepted")
	}
}

// Words that arrive together (a push and a join's answer on a reconnect, paho
// runs each in its own goroutine) leave one row.
func TestHubAccess_ConcurrentWordsLeaveOneRow(t *testing.T) {
	k := newHubAccessKit(t)
	h := k.access(t, true)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.Apply(k.msg("private", "127.0.0.1", k.port, "meshsat-0a1b2c3d", fmt.Sprintf("pass-%d", i%2)))
		}(i)
	}
	wg.Wait()
	if _, _, n := k.managed(t); n != 1 || len(k.stored(t)) != 1 {
		t.Fatalf("%d managed rows, %d rows", n, len(k.stored(t)))
	}
}

// The kit asks at once on a connect, again with backoff until it has an
// answer, then only every so often or when the row stays down.
func TestHubAccess_AsksUntilItHasAnAnswer(t *testing.T) {
	k := newHubAccessKit(t)
	h := k.access(t, true)
	asks := func() int { k.askMu.Lock(); defer k.askMu.Unlock(); return k.asks }
	h.OnConnect()
	if asks() != 1 {
		t.Fatalf("asked %d times on connect", asks())
	}
	now := time.Now()
	h.tick(now.Add(5 * time.Second))
	if asks() != 1 {
		t.Fatal("asked again within the backoff")
	}
	h.tick(now.Add(16 * time.Second))
	h.tick(now.Add(30 * time.Second))
	if asks() != 2 {
		t.Fatalf("asked %d times; the second backoff is 30 s", asks())
	}
	h.tick(now.Add(47 * time.Second))
	if asks() != 3 {
		t.Fatalf("asked %d times after the second backoff", asks())
	}
	h.Apply(k.msg("none", "", 0, "", ""))
	for i := 1; i <= 10; i++ {
		h.tick(now.Add(time.Duration(i) * time.Minute))
	}
	if asks() != 3 {
		t.Fatalf("asked %d times after an answer", asks())
	}
	h.tick(time.Now().Add(7 * time.Hour))
	if asks() != 4 {
		t.Fatalf("asked %d times after 7 h", asks())
	}
}

// The mark is the Hub's: a person can neither create a row with it nor put it
// on their own row, or the Hub's next "none" would remove that row.
func TestHubAccess_APersonCannotSetTheMark(t *testing.T) {
	k := newHubAccessKit(t)
	if _, err := k.m.Create(DynTypeTCPRNS, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":"n","ifac_passphrase":"p","managed":"hub"}`, k.port)), true); err == nil {
		t.Fatal("a person created a managed row")
	}
	own, err := k.m.Create(DynTypeTCPRNS, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":"n","ifac_passphrase":"p"}`, k.port)), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.m.Update(own, json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":"n","ifac_passphrase":"p","managed":"hub"}`, k.port)), true); err == nil {
		t.Fatal("a person marked their own row managed")
	}
	k.access(t, true).Apply(k.msg("none", "", 0, "", ""))
	if _, ok := k.stored(t)[own]; !ok {
		t.Fatal("the Hub's none removed a person's row")
	}
}
