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
	"meshsat/internal/reticulum"
)

// fakeCodedNode records what the coded set asks of the node.
type fakeCodedNode struct {
	mu      sync.Mutex
	ifac    map[string]*reticulum.IFAC
	leaf    map[string]bool
	inbound int
}

func newFakeCodedNode() *fakeCodedNode {
	return &fakeCodedNode{ifac: map[string]*reticulum.IFAC{}, leaf: map[string]bool{}}
}

func (f *fakeCodedNode) SetIFAC(id string, x *reticulum.IFAC) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x == nil {
		delete(f.ifac, id)
		return
	}
	f.ifac[id] = x
}
func (f *fakeCodedNode) SetLeaf(id string, leaf bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaf[id] = leaf
}
func (f *fakeCodedNode) Inbound([]byte, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbound++
	return true
}

func TestValidateTCPRNSAccessCode(t *testing.T) {
	for _, tc := range []struct {
		cfg string
		ok  bool
	}{
		{`{"host":"rns.meshsat.net","ifac_netname":"meshsat-0a1b2c3d","ifac_passphrase":"QX7ZK2M4PAN6EHV3"}`, true},
		{`{"host":"rns.meshsat.net","ifac_netname":"meshsat-0a1b2c3d"}`, false},
		{`{"host":"rns.meshsat.net","ifac_passphrase":"QX7ZK2M4PAN6EHV3"}`, false},
		{`{"host":"rns.meshsat.net","ifac_netname":"   ","ifac_passphrase":"QX7ZK2M4PAN6EHV3"}`, false},
		{`{"host":"rns.meshsat.net"}`, true},
	} {
		_, err := ValidateDynConfig(DynTypeTCPRNS, json.RawMessage(tc.cfg))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v, want ok=%v", tc.cfg, err, tc.ok)
		}
	}
}

// The stored passphrase is kept only for the same network at the same
// address: a row pointed elsewhere must be given the passphrase again.
func TestKeepTCPRNSPassphrase(t *testing.T) {
	stored := json.RawMessage(`{"host":"rns.meshsat.net","port":4242,"tls":false,"ifac_netname":"meshsat-0a1b2c3d","ifac_passphrase":"secret-passphrase"}`)
	for _, tc := range []struct {
		next string
		keep bool
	}{
		{`{"host":"rns.meshsat.net","port":4242,"ifac_netname":"meshsat-0a1b2c3d","ifac_passphrase":""}`, true},
		{`{"host":"rns.meshsat.net","ifac_netname":"meshsat-0a1b2c3d"}`, true},
		{`{"host":"evil.example","port":4242,"ifac_netname":"meshsat-0a1b2c3d"}`, false},
		{`{"host":"rns.meshsat.net","port":4243,"ifac_netname":"meshsat-0a1b2c3d"}`, false},
		{`{"host":"rns.meshsat.net","port":4242,"tls":true,"ifac_netname":"meshsat-0a1b2c3d"}`, false},
		{`{"host":"rns.meshsat.net","port":4242,"ifac_netname":"meshsat-ffffffff"}`, false},
		{`{"host":"rns.meshsat.net","port":4242}`, false},
	} {
		got := string(keepTCPRNSPassphrase(stored, json.RawMessage(tc.next)))
		if strings.Contains(got, "secret-passphrase") != tc.keep {
			t.Errorf("%s -> %s, want keep=%v", tc.next, got, tc.keep)
		}
	}
}

// A frame from a stopped connection never reaches a row restarted under
// the same id, and nothing reaches the node before the row is armed.
func TestCodedIfacesBindFramesToTheirEntry(t *testing.T) {
	c := NewCodedIfaces()
	f, err := reticulum.NewIFAC("meshsat-0a1b2c3d", "secret-passphrase", reticulum.DefaultIFACSize)
	if err != nil {
		t.Fatal(err)
	}
	online := func() bool { return true }
	send := func(context.Context, []byte) error { return nil }
	e1 := &codedEntry{ifac: f, send: send, online: online, mtu: TCPRNSHWMTU}
	c.add("tcp_rns_0", e1)
	c.deliver("tcp_rns_0", e1, []byte{0x80, 0x00, 0x01})
	if c.Dropped() != 1 || len(c.Floodable()) != 0 || c.Armed("tcp_rns_0") {
		t.Fatalf("an unarmed row let a frame through or is floodable (dropped %d)", c.Dropped())
	}
	n := newFakeCodedNode()
	c.Attach(n, nil)
	if !n.leaf["tcp_rns_0"] || n.ifac["tcp_rns_0"] == nil || !c.Armed("tcp_rns_0") {
		t.Fatal("Attach did not arm the row")
	}
	c.deliver("tcp_rns_0", e1, []byte{0x80, 0x00, 0x01})
	if n.inbound != 1 {
		t.Fatalf("an armed row's frame did not reach the node (%d)", n.inbound)
	}
	c.remove("tcp_rns_0")
	if n.leaf["tcp_rns_0"] || n.ifac["tcp_rns_0"] != nil {
		t.Fatal("remove left the code or the leaf mark on the node")
	}
	e2 := &codedEntry{ifac: f, send: send, online: online, mtu: TCPRNSHWMTU}
	c.add("tcp_rns_0", e2)
	c.deliver("tcp_rns_0", e1, []byte{0x80, 0x00, 0x01})
	if n.inbound != 1 {
		t.Fatal("a frame of the stopped connection reached the restarted row")
	}
	if err := c.Send("tcp_rns_0", []byte{0x00, 0x00}); err != ErrUncodedFrame {
		t.Fatalf("an uncoded frame: %v", err)
	}
}

// With no Reticulum node the row may connect, but it says nothing crosses.
func TestCodedRowWithoutTheNodeIsNotOnline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sink := &fakeSink{senders: map[string]bool{}}
	reg := NewInterfaceRegistry()
	coded := NewCodedIfaces()
	m := NewIfaceManager(IfaceManagerConfig{DB: db, Registry: reg, Coded: coded, Sink: sink})
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	cfg := fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"ifac_netname":"meshsat-0a1b2c3d","ifac_passphrase":"secret-passphrase"}`, ln.Addr().(*net.TCPAddr).Port)
	id, err := m.Create(DynTypeTCPRNS, json.RawMessage(cfg), true)
	if err != nil {
		t.Fatal(err)
	}
	waitCond(t, "the row reports the missing node", 5*time.Second, func() bool {
		st, _ := m.Get(id)
		return st != nil && strings.Contains(st.LastError, "not running")
	})
	st, _ := m.Get(id)
	if st.Online || !st.Coded || strings.Contains(string(st.Config), "secret-passphrase") {
		t.Fatalf("status %+v", st)
	}
	if reg.Get(id) != nil || sink.senders[id] {
		t.Fatal("the coded row is wired into the registry or the processor")
	}
}

// A stored config that does not parse is never shown as it is.
func TestCodedRowStatusFailsClosedOnABadConfig(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveRoutingIface(database.RoutingIface{ID: "tcp_rns_0", Type: DynTypeTCPRNS, Config: `{"ifac_passphrase":"secret-passphrase",`}); err != nil {
		t.Fatal(err)
	}
	m := NewIfaceManager(IfaceManagerConfig{DB: db, Coded: NewCodedIfaces()})
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := m.Get("tcp_rns_0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(st.Config), "secret") {
		t.Fatalf("an unparsable config was shown: %s", st.Config)
	}
}
