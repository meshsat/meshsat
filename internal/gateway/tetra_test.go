package gateway

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/tetra"
	"meshsat/internal/transport"
)

// tetraFake answers the PEI like the MTP3550 did on 17 Oct 2026.
type tetraFake struct {
	mu       sync.Mutex
	cmds     []string
	pdus     []string
	mode     int
	refuse   bool
	toGW     *io.PipeWriter
	closeAll func()
}

type tetraPipe struct {
	io.Reader
	io.Writer
	close func() error
}

func (p tetraPipe) Close() error { return p.close() }

func newTetraFake(mode int) *tetraFake { return &tetraFake{mode: mode} }

// open is the gateway's open func: each call is a fresh port.
func (f *tetraFake) open(string) (io.ReadWriteCloser, error) {
	gr, fw := io.Pipe() // fake -> gateway
	fr, gw := io.Pipe() // gateway -> fake
	var once sync.Once
	closeAll := func() {
		once.Do(func() { gr.Close(); fw.Close(); fr.Close(); gw.Close() })
	}
	f.mu.Lock()
	f.toGW, f.closeAll = fw, closeAll
	f.mu.Unlock()
	go func() {
		// A pipe write waits for the reader, which starts after open.
		fw.Write([]byte("+MCCFG: 0\r\n"))
		f.serve(fr, fw)
	}()
	return tetraPipe{Reader: gr, Writer: gw, close: func() error { closeAll(); return nil }}, nil
}

func (f *tetraFake) say(w io.Writer, lines ...string) {
	for _, l := range lines {
		w.Write([]byte(l + "\r\n"))
	}
}

func (f *tetraFake) serve(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	for {
		cmd, err := br.ReadString('\r')
		if err != nil {
			return
		}
		cmd = strings.TrimSpace(cmd)
		f.mu.Lock()
		f.cmds = append(f.cmds, cmd)
		mode, refuse := f.mode, f.refuse
		f.mu.Unlock()
		switch {
		case strings.HasPrefix(cmd, "AT+CMGS=") && !strings.HasSuffix(cmd, "?"):
			data, err := br.ReadString('\x1a')
			if err != nil {
				return
			}
			f.mu.Lock()
			f.pdus = append(f.pdus, strings.TrimSpace(strings.TrimSuffix(data, "\x1a")))
			f.mu.Unlock()
			if refuse {
				f.say(w, "+CME ERROR: 3")
				continue
			}
			// The MTP3550 adds its 18-byte ID inside the 148-byte budget:
			// a PDU over 130 bytes is "too long" (measured 17 Oct 2026).
			if pdu, err := hex.DecodeString(strings.TrimSpace(strings.TrimSuffix(data, "\x1a"))); err == nil && len(pdu) > 130 {
				f.say(w, "+CME ERROR: 24")
				continue
			}
			// The late report carries the PDU's message reference.
			ref := "0"
			if raw, err := hex.DecodeString(strings.TrimSpace(strings.TrimSuffix(data, "\x1a"))); err == nil && len(raw) > 2 {
				ref = itoa(int(raw[2]))
			}
			f.say(w, "+CMGS: 0", "", "OK", "+CMGS: 0,4,"+ref)
		case cmd == "ATI":
			itsi, gtsi := "2340239200000100", "2340239200003000"
			if mode == 1 {
				itsi, gtsi = "7530236100000100", "7530236100002000"
			}
			f.say(w, "Manufacturer: Motorola Solutions", "Model Type: MTP3550 350-470MHz PTBP952HE", "TEI: 00016B2B2264210",
				"ITSI: "+itsi, "GTSI: "+gtsi, "Radio Ver: R35.000.9426", "", "OK")
		case cmd == "AT+CTOM?":
			f.say(w, "+CTOM: "+string(rune('0'+mode)), "", "OK")
		case strings.HasPrefix(cmd, "AT+CTOM="):
			f.mu.Lock()
			f.mode = int(cmd[len(cmd)-1] - '0')
			f.mu.Unlock()
			f.say(w, "OK")
		case cmd == "AT+CMGS=?":
			f.say(w, "+CMGS: (0-16777214,00000001-10231638316777214,1-255,0-999999999999999999999999),(8-1184)", "", "OK")
		case cmd == "AT+CBC?":
			f.say(w, "+CBC: 1,100", "", "OK")
		default:
			f.say(w, "OK")
		}
	}
}

func (f *tetraFake) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.cmds...)
}

func (f *tetraFake) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.pdus...)
}

func (f *tetraFake) incoming(header, hexPDU string) {
	f.mu.Lock()
	w := f.toGW
	f.mu.Unlock()
	f.say(w, header, hexPDU)
}

func (f *tetraFake) unplug() {
	f.mu.Lock()
	c := f.closeAll
	f.mu.Unlock()
	c()
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func startTetra(t *testing.T, f *tetraFake, cfg TetraConfig) *TetraGateway {
	t.Helper()
	g := NewTetraGateway(cfg, "tetra_0", func() string { return "/dev/ttyUSB1" })
	g.open = f.open
	g.SetNodeNameResolver(func(n uint32) string {
		if n == 0x4370c1d8 {
			return "tess"
		}
		return ""
	})
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Stop() })
	return g
}

func TestTetraStartPutsTheRadioInDMOAndTakesItsTexts(t *testing.T) {
	f := newTetraFake(0) // the radio comes up in TMO
	g := startTetra(t, f, DefaultTetraConfig())
	st := g.TetraStatus()
	if st.State != tetraConnected || st.Mode != "dmo" || st.Group != "2000" || st.Self != "100" || st.Destination != "2000" ||
		st.MaxTextBytes != 130 || st.BatteryPct != 100 || st.Radio.Firmware != "R35.000.9426" || st.Port != "/dev/ttyUSB1" {
		t.Fatalf("status %+v", st)
	}
	cmds := f.commands()
	for _, want := range []string{"ATE0", "AT+CTOM=1", "AT+CTSP=1,3,130", "AT+CTSP=1,3,137"} {
		if !has(cmds, want) {
			t.Fatalf("radio never got %s: %q", want, cmds)
		}
	}
	if !g.Status().Connected || g.Status().Mode != "dmo" {
		t.Fatalf("gateway status %+v", g.Status())
	}
}

func TestTetraKeepModeLeavesTheRadioAlone(t *testing.T) {
	f := newTetraFake(0)
	cfg := DefaultTetraConfig()
	cfg.Mode = "keep"
	g := startTetra(t, f, cfg)
	if has(f.commands(), "AT+CTOM=1") || g.TetraStatus().Mode != "tmo" {
		t.Fatalf("keep switched the radio: %q", f.commands())
	}
}

func TestTetraForward(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	ctx := context.Background()

	// A text relayed from the mesh goes to the radio's group with its sender.
	if err := g.Forward(ctx, &transport.MeshMessage{From: 0x4370c1d8, DecodedText: "hello over DMO"}); err != nil {
		t.Fatal(err)
	}
	// A text with its own address goes there, as written.
	if err := g.Forward(ctx, &transport.MeshMessage{DecodedText: "to one radio", Destination: "tetra:101", AsWritten: true}); err != nil {
		t.Fatal(err)
	}
	cmds := f.commands()
	if !has(cmds, "AT+CMGS=2000,"+itoa(8*(4+len("tess: hello over DMO")))) || !has(cmds, "AT+CMGS=101,"+itoa(8*(4+len("to one radio")))) {
		t.Fatalf("radio got %q", cmds)
	}
	sent := f.sent()
	r := tetra.Received{}
	if err := r.DecodePDU(sent[0]); err != nil || r.Text != "tess: hello over DMO" {
		t.Fatalf("first PDU %q: %+v %v", sent[0], r, err)
	}
	if g.Status().MessagesOut != 2 {
		t.Fatalf("messages out %d", g.Status().MessagesOut)
	}

	// A bad address is no recipient: no retry can help.
	if err := g.Forward(ctx, &transport.MeshMessage{DecodedText: "x", Destination: "cell:1001"}); !errors.Is(err, transport.ErrNoRecipient) {
		t.Fatalf("bad address: %v", err)
	}
}

func TestTetraForwardLongText(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	ctx := context.Background()
	long := strings.Repeat("words that need more than one SDS ", 8) // 272 characters
	if err := g.Forward(ctx, &transport.MeshMessage{DecodedText: long, AsWritten: true}); err != nil {
		t.Fatal(err)
	}
	sent := f.sent()
	if len(sent) < 2 {
		t.Fatalf("%d parts", len(sent))
	}
	var joined []string
	for i, h := range sent {
		raw, _ := hex.DecodeString(h)
		if len(raw) > 130 {
			t.Fatalf("part %d is %d bytes, the radio takes 130", i+1, len(raw))
		}
		r := tetra.Received{}
		if err := r.DecodePDU(h); err != nil {
			t.Fatal(err)
		}
		prefix := itoa(i+1) + "/" + itoa(len(sent)) + " "
		if !strings.HasPrefix(r.Text, prefix) {
			t.Fatalf("part %d %q has no %q", i+1, r.Text, prefix)
		}
		joined = append(joined, strings.TrimPrefix(r.Text, prefix))
	}
	if strings.Join(strings.Fields(strings.Join(joined, " ")), " ") != strings.Join(strings.Fields(long), " ") {
		t.Fatalf("parts lost words: %q", joined)
	}

	// A sealed payload that does not fit is refused whole, never cut.
	before := len(f.sent())
	err := g.Forward(ctx, &transport.MeshMessage{DecodedText: strings.Repeat("Z", 300), Encrypted: true})
	if !errors.Is(err, transport.ErrPayloadTooLong) || len(f.sent()) != before {
		t.Fatalf("sealed long: %v, sent %d", err, len(f.sent())-before)
	}
}

func TestTetraForwardRefusedAndNotConnected(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	f.mu.Lock()
	f.refuse = true
	f.mu.Unlock()
	err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: "x", AsWritten: true})
	if !errors.Is(err, transport.ErrNotSubmitted) {
		t.Fatalf("refused: %v", err)
	}

	idle := NewTetraGateway(DefaultTetraConfig(), "tetra_0", func() string { return "" })
	if err := idle.Forward(context.Background(), &transport.MeshMessage{DecodedText: "x"}); !errors.Is(err, transport.ErrNotConnected) {
		t.Fatalf("no radio: %v", err)
	}
	if err := idle.Start(context.Background()); !errors.Is(err, transport.ErrNotConnected) {
		t.Fatalf("start with no radio: %v", err)
	}
}

func TestTetraInbound(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	// What parallax received from tesseract on 17 Oct 2026 21:22.
	f.incoming("+CTSDSR: 12,100,0,2000,0,496",
		"8202F8014D65736853617420444D4F207465737420322074657373657261637420746F20706172616C6C61780D0D37353330323336313030303030313030")
	// A report and a non-text SDS are not messages.
	f.incoming("+CTSDSR: 12,100,0,2000,0,24", "821031")
	f.incoming("+CTSDSR: 12,100,0,2000,0,24", "0A0102")
	select {
	case m := <-g.Receive():
		if m.Text != "MeshSat DMO test 2 tesseract to parallax" || m.Source != "tetra" || m.FromAddr != "tetra:100" || m.To != "tetra:2000" {
			t.Fatalf("inbound %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message")
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case m := <-g.Receive():
		t.Fatalf("a report or binary SDS became a message: %+v", m)
	default:
	}
	st := g.TetraStatus()
	if st.MessagesIn != 1 || st.Reports != 1 || st.NonTextIn != 1 {
		t.Fatalf("counters %+v", st)
	}
}

func TestTetraStopGivesTheScreenBackAndUnplugReconnects(t *testing.T) {
	old := tetraHealthEvery
	tetraHealthEvery = time.Hour
	defer func() { tetraHealthEvery = old }()
	f := newTetraFake(1)
	var opens atomic.Int32
	g := NewTetraGateway(DefaultTetraConfig(), "tetra_0", func() string { return "/dev/ttyUSB1" })
	g.open = func(p string) (io.ReadWriteCloser, error) { opens.Add(1); return f.open(p) }
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	f.unplug()
	deadline := time.Now().Add(6 * time.Second)
	for opens.Load() < 2 || !g.Status().Connected {
		if time.Now().After(deadline) {
			t.Fatalf("no reconnect: opens %d, status %+v", opens.Load(), g.Status())
		}
		time.Sleep(50 * time.Millisecond)
	}

	g.Stop()
	cmds := f.commands()
	if !has(cmds, "AT+CTSP=0,3,130") || !has(cmds, "AT+CTSP=0,3,137") {
		t.Fatalf("stop did not give texts back to the screen: %q", cmds)
	}
	if g.Status().Connected {
		t.Fatal("still connected after Stop")
	}
}

func TestTetraConfig(t *testing.T) {
	cfg, err := ParseTetraConfig("{}")
	if err != nil || cfg.Mode != "dmo" || cfg.Validate() != nil {
		t.Fatalf("defaults %+v %v", cfg, err)
	}
	for _, bad := range []string{`{"mode":"repeater"}`, `{"destination":"abc"}`, `{"destination":"0"}`, `{"prefix":"` + strings.Repeat("x", 21) + `"}`} {
		c, err := ParseTetraConfig(bad)
		if err == nil && c.Validate() == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestTetraManagerWiring(t *testing.T) {
	if roleToGatewayType(transport.RoleTetra) != "tetra" || gatewayTypeToRole("tetra") != transport.RoleTetra {
		t.Fatal("role and gateway type do not map")
	}
	if roleToGatewayType(transport.RoleTetraAux) != "" {
		t.Fatal("the aux port must start no gateway")
	}
	if _, known := channelLabel("tetra"); !known {
		t.Fatal("no channel label: the plugged-in radio would get no tetra_0 link")
	}
	m := &Manager{}
	gw, err := m.createGatewayForInstance("tetra", "tetra_0", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := gw.(*TetraGateway); !ok || gw.Type() != "tetra" {
		t.Fatalf("created %T", gw)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// A split text arrives at the far kit as one message, not one per part.
func TestTetraSplitTextIsOneMessageAtTheFarEnd(t *testing.T) {
	sender := newTetraFake(1)
	tx := startTetra(t, sender, DefaultTetraConfig())
	long := strings.Repeat("words that need more than one SDS ", 8)
	if err := tx.Forward(context.Background(), &transport.MeshMessage{DecodedText: long, AsWritten: true}); err != nil {
		t.Fatal(err)
	}
	parts := sender.sent()
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}

	receiver := newTetraFake(1)
	rx := startTetra(t, receiver, DefaultTetraConfig())
	// The radio appends its sender id to each part, as the MTP3550 does.
	for _, p := range parts {
		receiver.incoming("+CTSDSR: 12,100,0,2000,0,0", p+hex.EncodeToString([]byte("\r\r7530236100000100")))
	}
	select {
	case m := <-rx.Receive():
		if strings.Join(strings.Fields(m.Text), " ") != strings.Join(strings.Fields(long), " ") {
			t.Fatalf("reassembled %q", m.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reassembled message")
	}
	select {
	case m := <-rx.Receive():
		t.Fatalf("a second message: %q", m.Text)
	case <-time.After(200 * time.Millisecond):
	}
	if rx.Status().MessagesIn != 1 {
		t.Fatalf("messages in %d", rx.Status().MessagesIn)
	}

	// A short text that only looks like a part goes on at once.
	pdu, _ := tetra.EncodeText(9, "1/2 cup of sugar")
	receiver.incoming("+CTSDSR: 12,100,0,2000,0,0", hex.EncodeToString(pdu))
	select {
	case m := <-rx.Receive():
		if m.Text != "1/2 cup of sugar" {
			t.Fatalf("got %q", m.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a text that looks like a part was held")
	}
}

func TestTetraMissingPartGoesOnWithTheGapMarked(t *testing.T) {
	old := tetraAssemblyWait
	tetraAssemblyWait = 300 * time.Millisecond
	defer func() { tetraAssemblyWait = old }()
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	first := "1/2 " + strings.Repeat("a", 120)
	pdu, _ := tetra.EncodeText(40, first)
	f.incoming("+CTSDSR: 12,100,0,2000,0,0", hex.EncodeToString(pdu))
	select {
	case m := <-g.Receive():
		if m.Text != strings.Repeat("a", 120)+" [...]" {
			t.Fatalf("got %q", m.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the part never went on")
	}
}

// A first open that fails (the 17 Oct 2026 boot: the driver refused the
// modem-line ioctl) is retried in the background, because the reconcile at
// boot starts a gateway once and never again.
func TestTetraFirstOpenFailsThenConnectsInTheBackground(t *testing.T) {
	f := newTetraFake(1)
	var opens atomic.Int32
	g := NewTetraGateway(DefaultTetraConfig(), "tetra_0", func() string { return "/dev/ttyUSB1" })
	g.open = func(p string) (io.ReadWriteCloser, error) {
		if opens.Add(1) == 1 {
			return nil, errors.New("open /dev/ttyUSB1: inappropriate ioctl for device")
		}
		return f.open(p)
	}
	if err := g.Start(context.Background()); err != nil {
		t.Fatalf("Start returned %v; it must keep trying", err)
	}
	defer g.Stop()
	if g.Status().Connected {
		t.Fatal("connected without a radio")
	}
	deadline := time.Now().Add(6 * time.Second)
	for !g.Status().Connected {
		if time.Now().After(deadline) {
			t.Fatalf("never connected: %+v", g.TetraStatus())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A PDU the radio calls too long (+CME ERROR: 24) is refused for good, and
// a text right at the measured edge goes in one SDS.
func TestTetraTooLongIsPermanentAndTheEdgeFits(t *testing.T) {
	f := newTetraFake(1)
	g := startTetra(t, f, DefaultTetraConfig())
	edge := strings.Repeat("e", 126) // 126 + 4 = 130 bytes
	if err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: edge, AsWritten: true}); err != nil {
		t.Fatalf("126 characters: %v", err)
	}
	if n := len(f.sent()); n != 1 {
		t.Fatalf("126 characters went as %d SDS", n)
	}
	// A destination the gateway's own limit cannot see past: force a
	// 131-byte PDU through the session to check the error mapping.
	g.mu.Lock()
	g.maxBits = 2000 // pretend the radio advertised more than it takes
	g.mu.Unlock()
	err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: strings.Repeat("e", 127), AsWritten: true})
	if !errors.Is(err, transport.ErrPayloadTooLong) {
		t.Fatalf("+CME ERROR: 24 mapped to %v", err)
	}
}
