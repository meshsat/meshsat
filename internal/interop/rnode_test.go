package interop

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"meshsat/internal/interop/rnsenv"
	"meshsat/internal/reticulum"
	"meshsat/internal/rnode"
	"meshsat/internal/rns"
	"meshsat/internal/routing"
)

// startFakeRNodes runs scripts/fake_rnode.py and returns the two pty paths.
func startFakeRNodes(t *testing.T) (string, string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "fake_rnode.py")
	cmd := exec.Command(rnsenv.Python(t), script)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("fake_rnode.py: %v", err)
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
		t.Fatalf("fake_rnode.py printed no pty paths")
	}
	return a, b
}

// The bridge's RNode interface on one software RNode, a stock rnsd
// RNodeInterface on the other, with CrossTalk's EU preset on both.
func TestInteropRNodeAgainstRNSD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ptyA, ptyB := startFakeRNodes(t)

	// Python peer on pty A with an RNodeInterface.
	preset := rnode.PresetByID("eu-868")
	py := rnsenv.Python(t)
	cfgDir := t.TempDir()
	p := startPeerWithConfig(t, py, cfgDir, fmt.Sprintf(`[reticulum]
  enable_transport = No
  share_instance = No
  panic_on_interface_error = No

[logging]
  loglevel = 4

[interfaces]
  [[rnode]]
    type = RNodeInterface
    enabled = yes
    port = %s
    frequency = %d
    bandwidth = %d
    txpower = %d
    spreadingfactor = %d
    codingrate = %d
    flow_control = True
`, ptyA, preset.Frequency, preset.Bandwidth, preset.TXPower, preset.SF, preset.CR), "R")

	// Bridge node on pty B.
	id, _ := reticulum.GenerateIdentity()
	var node *rns.Node
	iface := routing.NewRNodeInterface(routing.RNodeInterfaceConfig{Name: "rnode_0", Port: ptyB, Params: preset.Params, FlowControl: true},
		func(packet []byte) { node.Inbound(packet, "rnode_0") })
	tx := &rnodeTx{iface: iface}
	n, err := rns.New(rns.Config{Identity: id}, tx)
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
	waitFor(t, "rnode_0 online", 20*time.Second, iface.IsOnline)
	st := iface.Stats()
	if st.Frequency != preset.Frequency || st.SF != preset.SF || !st.RadioOn {
		t.Fatalf("radio state %+v", st)
	}

	// Announce both ways over the "air".
	p.send(event{"cmd": "announce"})
	waitFor(t, "bridge hears python over rnode", 20*time.Second, func() bool { return n.Paths().Hops(h16(p.Raw)) == 1 })
	if e := n.Paths().Get(h16(p.Raw)); e.Iface != "rnode_0" {
		t.Fatalf("path via %s", e.Iface)
	}
	n.Announce(dest, "", false)
	dh := hex.EncodeToString(dest.Hash[:])
	p.wait("announce", func(e event) bool { return e["dest"] == dh }, 20*time.Second)

	// Packets with proofs both ways.
	p.send(event{"cmd": "send_packet", "dest": dh, "data": "lora hello"})
	select {
	case s := <-got:
		if s != "lora hello" {
			t.Fatalf("got %q", s)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no packet over rnode")
	}
	p.wait("packet_proved", nil, 20*time.Second)
	r, err := n.SendData(h16(p.Raw), []byte("hello from the kit"), true)
	if err != nil {
		t.Fatal(err)
	}
	p.wait("packet_received", func(e event) bool { return e["data"] == "hello from the kit" }, 20*time.Second)
	select {
	case res := <-r.Done():
		if !res.Delivered {
			t.Fatalf("no proof over rnode")
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("proof timeout")
	}
	if s := iface.Stats(); s.TXPackets < 2 || s.RXPackets < 2 {
		t.Fatalf("counters %+v", s)
	}
}

// rnodeTx routes everything to the one RNode interface.
type rnodeTx struct{ iface *routing.RNodeInterface }

func (x *rnodeTx) Transmit(ifaceID string, raw []byte) error {
	return x.iface.Send(context.Background(), raw)
}
func (x *rnodeTx) Floodable() []string {
	if x.iface.IsOnline() {
		return []string{"rnode_0"}
	}
	return nil
}
func (x *rnodeTx) HWMTU(string) int   { return 508 }
func (x *rnodeTx) Bitrate(string) int { return x.iface.Bitrate() }
