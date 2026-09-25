package interop

// The IP-mesh and TNC interface types against stock rnsd 1.5.4:
// UDPInterface on loopback, KISSInterface over a software TNC pair, and
// AutoInterface on a veth pair inside a user network namespace (the
// runner's own interfaces carry no link-local IPv6). [MESHSAT-1350]

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"meshsat/internal/interop/rnsenv"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/routing"
)

// dynIface is the part of the interface types the tests drive.
type dynIface interface {
	Start(ctx context.Context) error
	Stop()
	Send(ctx context.Context, packet []byte) error
	IsOnline() bool
}

// dynTx routes everything to one dynamic interface.
type dynTx struct {
	id    string
	iface dynIface
	mtu   int
}

func (x *dynTx) Transmit(ifaceID string, raw []byte) error {
	return x.iface.Send(context.Background(), raw)
}
func (x *dynTx) Floodable() []string {
	if x.iface.IsOnline() {
		return []string{x.id}
	}
	return nil
}
func (x *dynTx) HWMTU(string) int   { return x.mtu }
func (x *dynTx) Bitrate(string) int { return 0 }

// bridgeOn builds an in-process node whose only interface is iface.
func bridgeOn(t *testing.T, ctx context.Context, id string, mtu int, mk func(cb func([]byte)) dynIface) (*rns.Node, *rns.Destination, dynIface, chan string) {
	t.Helper()
	ident, _ := reticulum.GenerateIdentity()
	var node *rns.Node
	iface := mk(func(packet []byte) { node.Inbound(packet, id) })
	n, err := rns.New(rns.Config{Identity: ident}, &dynTx{id: id, iface: iface, mtu: mtu})
	if err != nil {
		t.Fatal(err)
	}
	node = n
	got := make(chan string, 8)
	dest := n.AddDestination(&rns.Destination{Name: "interop.raw", AcceptLinks: true, ProveAll: true,
		OnPacket: func(plain []byte, pkt *rns.Packet) { got <- string(plain) }})
	if err := iface.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(iface.Stop)
	n.Start(ctx)
	return n, dest, iface, got
}

// exchange proves announces, a packet with proof and a receipt both ways.
func exchange(t *testing.T, n *rns.Node, dest *rns.Destination, got chan string, p *peer, ifaceID string, wait time.Duration) {
	t.Helper()
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge hears python", wait, func() bool { return n.Paths().Hops(h16(p.Raw)) == 1 })
	if e := n.Paths().Get(h16(p.Raw)); e.Iface != ifaceID {
		t.Fatalf("path via %s", e.Iface)
	}
	n.Announce(dest, "", false)
	dh := hex.EncodeToString(dest.Hash[:])
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, wait)

	p.send(event{"cmd": "send_packet", "dest": dh, "data": "hello over " + ifaceID})
	select {
	case s := <-got:
		if s != "hello over "+ifaceID {
			t.Fatalf("got %q", s)
		}
	case <-time.After(wait):
		t.Fatalf("no packet over %s", ifaceID)
	}
	p.wait("packet_proved", nil, wait)
	r, err := n.SendData(h16(p.Raw), []byte("kit says hi"), true)
	if err != nil {
		t.Fatal(err)
	}
	p.wait("packet_received", func(e event) bool { return e["data"] == "kit says hi" }, wait)
	select {
	case res := <-r.Done():
		if !res.Delivered {
			t.Fatalf("no proof over %s", ifaceID)
		}
	case <-time.After(wait):
		t.Fatalf("proof timeout over %s", ifaceID)
	}
}

func TestInteropUDPAgainstRNSD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pyPort, brPort := freePort(t), freePort(t)
	py := rnsenv.Python(t)
	p := startPeerWithConfig(t, py, t.TempDir(), fmt.Sprintf(`[reticulum]
  enable_transport = No
  share_instance = No
  panic_on_interface_error = No

[logging]
  loglevel = 4

[interfaces]
  [[udp]]
    type = UDPInterface
    enabled = yes
    listen_ip = 127.0.0.1
    listen_port = %d
    forward_ip = 127.0.0.1
    forward_port = %d
`, pyPort, brPort), "U")
	n, dest, _, got := bridgeOn(t, ctx, "udp_0", routing.UDPHWMTU, func(cb func([]byte)) dynIface {
		return routing.NewUDPInterface(routing.UDPInterfaceConfig{Name: "udp_0",
			ListenAddr: fmt.Sprintf("127.0.0.1:%d", brPort), ForwardAddr: fmt.Sprintf("127.0.0.1:%d", pyPort)}, cb)
	})
	exchange(t, n, dest, got, p, "udp_0", 15*time.Second)
}

// startFakeTNCs runs scripts/fake_kiss_tnc.py and returns the pty paths
// and a channel of PARAM lines.
func startFakeTNCs(t *testing.T) (string, string, chan string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "fake_kiss_tnc.py")
	cmd := exec.Command(rnsenv.Python(t), script, "--ready")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("fake_kiss_tnc.py: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	sc := bufio.NewScanner(out)
	var a, b string
	for (a == "" || b == "") && sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[0] == "A" {
			a = f[1]
		}
		if len(f) == 2 && f[0] == "B" {
			b = f[1]
		}
	}
	if a == "" || b == "" {
		t.Fatalf("fake_kiss_tnc.py printed no pty paths")
	}
	params := make(chan string, 64)
	go func() {
		for sc.Scan() {
			params <- sc.Text()
		}
	}()
	return a, b, params
}

func TestInteropKISSAgainstRNSD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ptyA, ptyB, params := startFakeTNCs(t)
	py := rnsenv.Python(t)
	p := startPeerWithConfig(t, py, t.TempDir(), fmt.Sprintf(`[reticulum]
  enable_transport = No
  share_instance = No
  panic_on_interface_error = No

[logging]
  loglevel = 4

[interfaces]
  [[tnc]]
    type = KISSInterface
    enabled = yes
    port = %s
    speed = 115200
    preamble = 350
    txtail = 20
    persistence = 64
    slottime = 20
    flow_control = True
`, ptyA), "K")
	n, dest, _, got := bridgeOn(t, ctx, "kiss_0", routing.KISSHWMTU, func(cb func([]byte)) dynIface {
		return routing.NewKISSInterface(routing.KISSInterfaceConfig{Name: "kiss_0", Port: ptyB, Baud: 115200,
			PreambleMs: 350, TXTailMs: 20, Persistence: 64, SlotTimeMs: 20, FlowControl: true}, cb)
	})
	exchange(t, n, dest, got, p, "kiss_0", 20*time.Second)

	// Both hosts configured their TNC the same way: same command bytes,
	// same ms/10 values, in upstream's order.
	seen := map[string][]string{}
	deadline := time.After(2 * time.Second)
collect:
	for {
		select {
		case l := <-params:
			f := strings.Fields(l)
			if len(f) == 4 && f[0] == "PARAM" {
				seen[f[1]] = append(seen[f[1]], f[2]+":"+f[3])
			}
		case <-deadline:
			break collect
		}
	}
	want := []string{"01:23", "04:02", "02:40", "03:02", "0f:01"}
	for _, side := range []string{"A", "B"} {
		if len(seen[side]) < len(want) {
			t.Fatalf("side %s saw %v", side, seen[side])
		}
		for i, w := range want {
			if seen[side][i] != w {
				t.Fatalf("side %s param %d = %s, want %s (all %v)", side, i, seen[side][i], w, seen[side])
			}
		}
	}
}

// TestInteropAutoIfaceAgainstRNSD runs inside `unshare -rn` with a veth
// pair: the bridge on v0, rnsd's AutoInterface on v1. The parent test
// re-executes the test binary in the namespace and reports its output.
func TestInteropAutoIfaceAgainstRNSD(t *testing.T) {
	if os.Getenv("MESHSAT_NETNS_CHILD") == "" {
		if _, err := exec.LookPath("unshare"); err != nil {
			t.Skip("unshare not available")
		}
		if out, err := exec.Command("unshare", "-rn", "true").CombinedOutput(); err != nil {
			t.Skipf("user network namespaces unavailable: %v %s", err, out)
		}
		rnsenv.Python(t) // resolve (and create) the venv outside the namespace
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		script := `ip link set lo up && ip link add v0 type veth peer name v1 && ip link set v0 up && ip link set v1 up && sleep 2 && exec "$0" -test.run '^TestInteropAutoIfaceAgainstRNSD$' -test.v -test.count=1 -test.timeout=120s`
		cmd := exec.Command("unshare", "-rn", "sh", "-c", script, exe)
		cmd.Env = append(os.Environ(), "MESHSAT_NETNS_CHILD=1")
		wd, _ := os.Getwd()
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		t.Logf("namespace child:\n%s", out)
		if err != nil {
			t.Fatalf("child failed: %v", err)
		}
		if !strings.Contains(string(out), "--- PASS: TestInteropAutoIfaceAgainstRNSD") {
			t.Fatalf("child did not report PASS")
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	py := rnsenv.Python(t)
	p := startPeerWithConfig(t, py, t.TempDir(), `[reticulum]
  enable_transport = No
  share_instance = No
  panic_on_interface_error = No

[logging]
  loglevel = 4

[interfaces]
  [[auto]]
    type = AutoInterface
    enabled = yes
    devices = v1
    group_id = meshsat-test
`, "A")
	n, dest, iface, got := bridgeOn(t, ctx, "auto_0", routing.AutoHWMTU, func(cb func([]byte)) dynIface {
		return routing.NewAutoInterface(routing.AutoInterfaceConfig{Name: "auto_0", GroupID: "meshsat-test", Devices: []string{"v0"}}, cb)
	})
	auto := iface.(*routing.AutoInterface)
	waitFor(t, "python discovered as a peer", 30*time.Second, func() bool { return auto.PeerCount() == 1 })
	exchange(t, n, dest, got, p, "auto_0", 30*time.Second)
	if peers := auto.Peers(); len(peers) != 1 || peers[0].Device != "v0" || !strings.HasPrefix(peers[0].Address, "fe80::") {
		t.Fatalf("peers %+v", peers)
	}
}
