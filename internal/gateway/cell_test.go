package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/smpp"
	"meshsat/internal/smpp/smpptest"
	"meshsat/internal/transport"
)

// The MeshSat 2G cell gateway against a fake osmo-msc. [MESHSAT-1557]

func TestCellConfigAndAddresses(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"1001", "1001", true}, {"cell:1001", "1001", true}, {"CELL:2002 ", "2002", true}, {"+31612345678", "+31612345678", true},
		{"", "", false}, {"cell:", "", false}, {"abc", "", false}, {"10 01", "", false}, {"+", "", false},
	} {
		got, ok := ParseCellAddress(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseCellAddress(%q) = %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
	if CellAddress("1001") != "cell:1001" {
		t.Fatal("CellAddress")
	}

	cfg, err := ParseCellConfig(`{"host":"10.0.0.5","destinations":["cell:1001","2002"],"short_code":"cell:1000","password":"pw"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 2775 || cfg.SystemID != "meshsat-bridge" || cfg.ShortCode != "1000" || cfg.ReplyWindowHours != 24 ||
		len(cfg.Destinations) != 2 || cfg.Destinations[0] != "1001" || cfg.Destinations[1] != "2002" {
		t.Fatalf("defaults and normalisation: %+v", cfg)
	}
	if cfg.Redacted().Password != secretMask || cfg.Password != "pw" {
		t.Fatal("the password must be masked on read and kept in the config")
	}
	for name, bad := range map[string]string{
		"long system id":    `{"host":"h","system_id":"sixteen-chars-xx"}`,
		"long password":     `{"host":"h","password":"ninechars"}`,
		"bad short code":    `{"host":"h","short_code":"kit"}`,
		"bad destination":   `{"host":"h","destinations":["+31 6"]}`,
		"negative window":   `{"host":"h","reply_window_hours":-1}`,
		"port out of range": `{"host":"h","port":70000}`,
	} {
		c, err := ParseCellConfig(bad)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

type cellRig struct {
	srv *smpptest.Server
	db  *database.DB
	gw  *CellGateway
}

func startCell(t *testing.T, mut func(*CellConfig)) *cellRig {
	t.Helper()
	srv, err := smpptest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	srv.SystemID, srv.Password = "meshsat-bridge", "pw"
	host, port, _ := strings.Cut(srv.Addr, ":")
	cfg := DefaultCellConfig()
	cfg.Host = host
	cfg.Port = atoi(port)
	cfg.Password = "pw"
	if mut != nil {
		mut(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	db := newTestDB(t)
	gw := NewCellGateway(cfg, db, "cell_0")
	gw.SetNodeNameResolver(func(id uint32) string {
		if id == 0xabcd {
			return "T-Echo"
		}
		return ""
	})
	if err := gw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gw.Stop() })
	if srv.WaitBound(3*time.Second) == nil {
		t.Fatal("the gateway did not bind")
	}
	waitUntil(t, "gateway bound", func() bool { return gw.Bound() })
	return &cellRig{srv: srv, db: db, gw: gw}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (r *cellRig) inbound(t *testing.T) InboundMessage {
	t.Helper()
	select {
	case m := <-r.gw.Receive():
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message")
	}
	return InboundMessage{}
}

func TestCellStartRefusesWithoutHostOrPassword(t *testing.T) {
	db := newTestDB(t)
	cfg := DefaultCellConfig()
	if err := NewCellGateway(cfg, db, "cell_0").Start(context.Background()); err == nil {
		t.Fatal("no host must refuse to start")
	}
	cfg.Host = "127.0.0.1"
	t.Setenv("CELL_SMPP_PASSWORD", "")
	t.Setenv("MESHSAT_CELL_SMPP_PASSWORD", "")
	if err := NewCellGateway(cfg, db, "cell_0").Start(context.Background()); err == nil {
		t.Fatal("no password anywhere must refuse to start")
	}
	// The environment supplies the password when the config has none.
	t.Setenv("CELL_SMPP_PASSWORD", "envpw")
	g := NewCellGateway(cfg, db, "cell_0")
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.Stop()
	if g.Type() != "cell" {
		t.Fatal("type")
	}
}

func TestCellForwardWhileUnbound(t *testing.T) {
	cfg := DefaultCellConfig()
	cfg.Host, cfg.Port, cfg.Password = "127.0.0.1", 1, "pw" // nothing listens on port 1
	g := NewCellGateway(cfg, newTestDB(t), "cell_0")
	if err := g.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer g.Stop()
	err := g.Forward(context.Background(), &transport.MeshMessage{DecodedText: "hi", Destination: "cell:1001"})
	if !errors.Is(err, transport.ErrNotConnected) {
		t.Fatalf("want ErrNotConnected while the session is down, got %v", err)
	}
	if s := g.Status(); s.Connected || s.Type != "cell" {
		t.Fatalf("status %+v", s)
	}
}

func TestCellInboundTextsReachTheRules(t *testing.T) {
	r := startCell(t, nil)

	// GSM 03.38 septet values, as osmo-msc sends them: 'Y' 'o' then '@' '['.
	st, err := r.srv.Deliver(smpp.SM{SourceTON: smpp.TONNetworkSpecific, SourceNPI: smpp.NPIISDN, SourceAddr: "2002",
		DestAddr: "1000", ESMClass: smpp.ESMModeDatagram, DataCoding: smpp.DCSGSM7,
		ShortMessage: []byte{0x59, 0x6F, 0x20, 0x00, 0x1B, 0x3C}}, 5*time.Second)
	if err != nil || st != smpp.StatusOK {
		t.Fatalf("deliver: %x %v", st, err)
	}
	m := r.inbound(t)
	if m.Text != "Yo @[" || m.Source != "cell" || m.FromAddr != "cell:2002" || m.Plain {
		t.Fatalf("inbound %+v", m)
	}

	// UCS-2, dialled with a plus: the digits decide.
	st, err = r.srv.Deliver(smpp.SM{SourceAddr: "1001", DestAddr: "+1000", DataCoding: smpp.DCSUCS2,
		ShortMessage: smpp.EncodeUCS2("Γειά")}, 5*time.Second)
	if err != nil || st != smpp.StatusOK {
		t.Fatalf("deliver ucs2: %x %v", st, err)
	}
	if m := r.inbound(t); m.Text != "Γειά" || m.FromAddr != "cell:1001" {
		t.Fatalf("ucs2 inbound %+v", m)
	}

	// A text to a number the kit does not serve: refused, the phone sees
	// "number unassigned", nothing reaches the rules.
	st, err = r.srv.Deliver(smpp.SM{SourceAddr: "2002", DestAddr: "5555", DataCoding: smpp.DCSGSM7, ShortMessage: []byte("x")}, 5*time.Second)
	if err != nil || st != smpp.StatusInvalidDstAddr {
		t.Fatalf("other number: %x %v", st, err)
	}
	select {
	case m := <-r.gw.Receive():
		t.Fatalf("a refused text reached the rules: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}

	// The peers table remembers who texted.
	peers, err := r.db.ListCellPeers()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("peers %+v", peers)
	}
	byNum := map[string]database.CellPeer{}
	for _, p := range peers {
		byNum[p.MSISDN] = p
	}
	if byNum["2002"].RxCount != 1 || byNum["2002"].LastDialled != "1000" || byNum["1001"].LastDialled != "+1000" {
		t.Fatalf("peers %+v", peers)
	}
	recent, _ := r.db.RecentCellPeers(time.Now().Add(-time.Hour))
	if len(recent) != 2 {
		t.Fatalf("recent %v", recent)
	}
	cs := r.gw.CellStatus()
	if cs.MessagesIn != 2 || cs.RefusedIn != 1 || cs.Link == nil || cs.Link.State != smpp.StateBound {
		t.Fatalf("status %+v", cs)
	}
}

func TestCellInboundAcceptAnyDestination(t *testing.T) {
	r := startCell(t, func(c *CellConfig) { c.AcceptAnyDestination = true })
	st, err := r.srv.Deliver(smpp.SM{SourceAddr: "2002", DestAddr: "5555", DataCoding: smpp.DCSGSM7, ShortMessage: []byte("x")}, 5*time.Second)
	if err != nil || st != smpp.StatusOK {
		t.Fatalf("%x %v", st, err)
	}
	if m := r.inbound(t); m.Text != "x" {
		t.Fatalf("%+v", m)
	}
}

func TestCellInboundConcatenatedAndReceipt(t *testing.T) {
	r := startCell(t, nil)
	part := func(seq byte, text string) smpp.SM {
		return smpp.SM{SourceAddr: "2002", DestAddr: "1000", ESMClass: smpp.ESMModeDatagram | smpp.ESMUDHI, DataCoding: smpp.DCSGSM7,
			ShortMessage: append(smpp.ConcatUDH(7, 2, seq), []byte(text)...)}
	}
	// Second part first.
	if st, err := r.srv.Deliver(part(2, " world"), 5*time.Second); err != nil || st != smpp.StatusOK {
		t.Fatalf("%x %v", st, err)
	}
	select {
	case m := <-r.gw.Receive():
		t.Fatalf("a lone part was forwarded: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
	if r.gw.CellStatus().PartsPending != 1 {
		t.Fatal("one series must be pending")
	}
	if st, err := r.srv.Deliver(part(1, "hello"), 5*time.Second); err != nil || st != smpp.StatusOK {
		t.Fatalf("%x %v", st, err)
	}
	if m := r.inbound(t); m.Text != "hello world" {
		t.Fatalf("reassembled %+v", m)
	}

	// A delivery receipt is logged and counted, never routed.
	st, err := r.srv.Deliver(smpp.SM{SourceAddr: "1001", DestAddr: "1000", ESMClass: smpp.ESMTypeDeliveryReceipt, DataCoding: smpp.DCS8Bit,
		ShortMessage: []byte("id:00000000 sub:000 dlvrd:000 submit date:YYMMDDhhmm done date:YYMMDDhhmm stat:DELIVRD err:000 text:"),
		TLVs:         []smpp.TLV{smpp.U16TLV(smpp.TagUserMessageReference, 1)}}, 5*time.Second)
	if err != nil || st != smpp.StatusOK {
		t.Fatalf("receipt %x %v", st, err)
	}
	select {
	case m := <-r.gw.Receive():
		t.Fatalf("a receipt was routed as a message: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
	if r.gw.CellStatus().Receipts != 1 || r.gw.CellStatus().MessagesIn != 1 {
		t.Fatalf("%+v", r.gw.CellStatus())
	}
}

func TestCellForward(t *testing.T) {
	r := startCell(t, func(c *CellConfig) { c.DeliveryReceipts = true })
	r.srv.OnSubmit = func(sm smpp.SM) (uint32, time.Duration, string) {
		switch sm.DestAddr {
		case "2002":
			return smpp.StatusInvalidDstAddr, 0, ""
		case "3003":
			return smpp.StatusDeliveryFailure, 0, ""
		case "4004":
			return smpp.StatusSysErr, 0, ""
		}
		return smpp.StatusOK, 20 * time.Millisecond, ""
	}
	ctx := context.Background()

	// A text relayed from the mesh carries its sender; GSM 03.38 on the wire.
	err := r.gw.Forward(ctx, &transport.MeshMessage{From: 0xabcd, DecodedText: "hi [1]", Destination: "cell:1001", MsgRef: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	subs := r.srv.Submits()
	if len(subs) != 1 {
		t.Fatalf("submits %d", len(subs))
	}
	s := subs[0]
	if s.SourceAddr != "1000" || s.DestAddr != "1001" || s.ESMClass != smpp.ESMModeForward || s.DataCoding != smpp.DCSDefault ||
		s.RegisteredDelivery != 1 || smpp.DecodeGSM7(s.ShortMessage) != "MeshSat T-Echo: hi [1]" {
		t.Fatalf("submit %+v text %q", s, smpp.DecodeGSM7(s.ShortMessage))
	}
	if _, ok := s.UserMessageReference(); !ok {
		t.Fatal("user_message_reference missing")
	}

	// Written on the kit (From 0): as typed. Greek: UCS-2.
	if err := r.gw.Forward(ctx, &transport.MeshMessage{DecodedText: "Γειά σου", Destination: "1001"}); err != nil {
		t.Fatal(err)
	}
	s = r.srv.Submits()[1]
	if s.DataCoding != smpp.DCSUCS2 || smpp.DecodeUCS2(s.ShortMessage) != "Γειά σου" {
		t.Fatalf("ucs2 submit %+v", s)
	}

	// Long: two concatenated parts.
	long := strings.Repeat("abcdefghij", 20)
	if err := r.gw.Forward(ctx, &transport.MeshMessage{DecodedText: long, Destination: "cell:1001", RawText: true}); err != nil {
		t.Fatal(err)
	}
	subs = r.srv.Submits()
	if len(subs) != 4 || subs[2].ESMClass&smpp.ESMUDHI == 0 || subs[3].ESMClass&smpp.ESMUDHI == 0 {
		t.Fatalf("long text: %d submits", len(subs))
	}
	u1, b1, _ := smpp.SplitUDH(subs[2].ShortMessage)
	_, b2, _ := smpp.SplitUDH(subs[3].ShortMessage)
	if c, ok := smpp.ParseConcat(u1); !ok || c.Total != 2 || c.Seq != 1 {
		t.Fatalf("concat header %+v %v", c, ok)
	}
	if smpp.DecodeGSM7(b1)+smpp.DecodeGSM7(b2) != long {
		t.Fatal("the parts do not rebuild the text")
	}

	// The phone is off the cell: ErrSubscriberAbsent for 0x0B and 0xFE.
	for _, to := range []string{"cell:2002", "3003"} {
		err := r.gw.Forward(ctx, &transport.MeshMessage{DecodedText: "x", Destination: to})
		if !errors.Is(err, transport.ErrSubscriberAbsent) {
			t.Fatalf("%s: want ErrSubscriberAbsent, got %v", to, err)
		}
	}
	// Any other refusal is an ordinary failure.
	if err := r.gw.Forward(ctx, &transport.MeshMessage{DecodedText: "x", Destination: "4004"}); err == nil || errors.Is(err, transport.ErrSubscriberAbsent) {
		t.Fatalf("0x08: %v", err)
	}
	// No address, bad address.
	if err := r.gw.Forward(ctx, &transport.MeshMessage{DecodedText: "x"}); !errors.Is(err, transport.ErrNoRecipient) {
		t.Fatalf("no destination: %v", err)
	}
	if err := r.gw.Forward(ctx, &transport.MeshMessage{DecodedText: "x", Destination: "PA0ABC-10"}); err == nil {
		t.Fatal("a callsign is not a phone")
	}

	peers, _ := r.db.ListCellPeers()
	if len(peers) != 1 || peers[0].MSISDN != "1001" || peers[0].TxCount != 3 {
		t.Fatalf("peers %+v", peers)
	}
	cs := r.gw.CellStatus()
	if cs.MessagesOut != 3 || cs.PhoneAbsent != 2 || cs.Errors < 1 {
		t.Fatalf("%+v", cs)
	}
}

func TestCellDestinationsAndAlert(t *testing.T) {
	r := startCell(t, func(c *CellConfig) { c.ReplyWindowHours = 1 })

	// Nothing yet: nobody texted, no list.
	if d := r.gw.Destinations(nil); len(d) != 0 {
		t.Fatalf("destinations %v", d)
	}
	if err := r.db.TouchCellPeerRx("2002", "1000"); err != nil {
		t.Fatal(err)
	}
	if d := r.gw.Destinations(nil); len(d) != 1 || d[0] != "cell:2002" {
		t.Fatalf("recent peer: %v", d)
	}
	// The rule's own list wins, deduplicated, bad entries dropped.
	if d := r.gw.Destinations([]string{"1001", "cell:1001", "bogus", " 3003"}); len(d) != 2 || d[0] != "cell:1001" || d[1] != "cell:3003" {
		t.Fatalf("rule list: %v", d)
	}
	// The gateway's list beats the recent peers.
	r.gw.config.Destinations = []string{"4004"}
	if d := r.gw.Destinations(nil); len(d) != 1 || d[0] != "cell:4004" {
		t.Fatalf("gateway list: %v", d)
	}
	// Outside the window nobody is recent.
	r.gw.config.Destinations = nil
	r.gw.config.ReplyWindowHours = 0
	if d := r.gw.Destinations(nil); len(d) != 0 {
		t.Fatalf("window off: %v", d)
	}

	// A row waiting for 1001, and one for 2002, far in the future.
	far := time.Now().Add(time.Hour)
	ids := map[string]int64{}
	for _, to := range []string{"cell:1001", "cell:2002"} {
		id, err := r.db.InsertDelivery(database.MessageDelivery{MsgRef: "m-" + to, Channel: "cell_0", Status: "queued", Priority: 1,
			Payload: []byte("x"), TextPreview: "x", MaxRetries: 3, Destination: to, QoSLevel: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.db.DeferDelivery(id, far, "the phone is not on the cell"); err != nil {
			t.Fatal(err)
		}
		ids[to] = id
	}
	if due, _ := r.db.GetPendingDeliveries("cell_0", 10); len(due) != 0 {
		t.Fatalf("nothing is due yet, got %d", len(due))
	}
	// The cell says 1001 is back: its row is due now, 2002's is not.
	if err := r.srv.Alert("1001", 0); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "nudge", func() bool {
		due, _ := r.db.GetPendingDeliveries("cell_0", 10)
		return len(due) == 1 && due[0].Destination == "cell:1001"
	})
	// 2002 detaches: recorded, nothing nudged.
	if err := r.srv.Alert("2002", 2); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "reachability", func() bool {
		peers, _ := r.db.ListCellPeers()
		for _, p := range peers {
			if p.MSISDN == "2002" && p.Reachable != nil && !*p.Reachable {
				return true
			}
		}
		return false
	})
	if due, _ := r.db.GetPendingDeliveries("cell_0", 10); len(due) != 1 {
		t.Fatalf("a detach must not nudge: %d due", len(due))
	}
	if r.gw.CellStatus().Alerts != 2 {
		t.Fatalf("alerts %d", r.gw.CellStatus().Alerts)
	}
}

// The cell keeps one session per system id: when the gateway reconnects
// after the cell restarts, texts flow again without a restart of the bridge.
func TestCellSurvivesServerRestart(t *testing.T) {
	r := startCell(t, nil)
	if err := r.srv.Unbind(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "rebound", func() bool { return r.srv.Binds() == 2 && r.gw.Bound() })
	if err := r.gw.Forward(context.Background(), &transport.MeshMessage{DecodedText: "x", Destination: "1001"}); err != nil {
		t.Fatal(err)
	}
	if st := r.gw.Status(); !st.Connected || st.State != "bound" {
		t.Fatalf("%+v", st)
	}
}
