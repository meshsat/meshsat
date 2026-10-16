package engine

// Acceptance for the corrections after the stage 1a red-team review
// (F-1 to F-4) beyond the reviewer's own regressions. [MESHSAT-1584]

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/lxmf"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/rnsstack"
	"meshsat/internal/transport"
)

// linkTx joins two RNS nodes: every interface in carries reaches the peer;
// free is what the transport floods on (the free interfaces).
type linkTx struct {
	mu      sync.Mutex
	sent    map[string]int
	peer    *rns.Node
	free    []string
	carries map[string]bool
}

func newLinkTx(free []string, carries ...string) *linkTx {
	t := &linkTx{sent: map[string]int{}, free: free, carries: map[string]bool{}}
	for _, c := range carries {
		t.carries[c] = true
	}
	return t
}

func (t *linkTx) Transmit(iface string, raw []byte) error {
	t.mu.Lock()
	t.sent[iface]++
	peer, carry := t.peer, t.carries[iface]
	t.mu.Unlock()
	if peer != nil && carry {
		p := append([]byte(nil), raw...)
		go peer.Inbound(p, iface)
	}
	return nil
}

func (t *linkTx) Floodable() []string { return t.free }
func (*linkTx) HWMTU(string) int      { return 500 }
func (*linkTx) Bitrate(string) int    { return 0 }
func (t *linkTx) count(iface string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sent[iface]
}

type lxmfPair struct {
	localTX, remoteTX *linkTx
	local, remote     *rns.Node
	stack             *rnsstack.Stack
	remoteRouter      *lxmf.Router
	received          atomic.Int32
}

func newLXMFPair(t *testing.T, localFree, remoteFree []string, carries ...string) *lxmfPair {
	t.Helper()
	p := &lxmfPair{localTX: newLinkTx(localFree, carries...), remoteTX: newLinkTx(remoteFree, carries...)}
	src, _ := reticulum.GenerateIdentity()
	dst, _ := reticulum.GenerateIdentity()
	var err error
	if p.local, err = rns.New(rns.Config{Identity: src}, p.localTX); err != nil {
		t.Fatal(err)
	}
	if p.remote, err = rns.New(rns.Config{Identity: dst}, p.remoteTX); err != nil {
		t.Fatal(err)
	}
	p.localTX.peer, p.remoteTX.peer = p.remote, p.local
	p.remoteRouter = lxmf.New(p.remote, lxmf.Config{Identity: dst, OnMessage: func(*lxmf.Message) { p.received.Add(1) }})
	p.stack = &rnsstack.Stack{Node: p.local, LXMF: lxmf.New(p.local, lxmf.Config{Identity: src})}
	return p
}

func (h *intentHarness) useLXMF(p *lxmfPair) {
	h.d.SetLXMFSender(p.stack)
	h.workers["lxmf_0"].lxmf = p.stack
}

// F-1: under the constraint a free path still delivers, end to end over
// real LXMF and RNS (discovery, proof); and (F-4) two intentional messages
// with the same text in one millisecond both reach the receiver.
func TestIntentFix_FreePathDeliversAndTwinsBothArrive(t *testing.T) {
	h := newIntentHarness(t)
	p := newLXMFPair(t, []string{"lora_rns"}, []string{"lora_rns"}, "lora_rns", "iridium_0")
	h.useLXMF(p)
	c := h.contact(directory.Address{Kind: directory.KindReticulum, Value: p.remoteRouter.HashHex()})
	a := h.submit(c, "twice on purpose", directory.StrategyPrimaryOnly)
	b := h.submit(c, "twice on purpose", directory.StrategyPrimaryOnly)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	h.drain()
	for _, id := range []string{a, b} {
		if s := h.snap(id); s.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
			t.Fatalf("intent %s: %s/%s (%+v)", id, s.Intent.State, s.Intent.Outcome, s.Transmissions)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.received.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := p.received.Load(); n != 2 {
		t.Fatalf("the receiver got %d messages; want both", n)
	}
	if ha, hb := h.snap(a).Transmissions[0].CorrValue, h.snap(b).Transmissions[0].CorrValue; ha == hb || ha == "" {
		t.Fatalf("hashes %q %q; want two", ha, hb)
	}
	if n := p.localTX.count("iridium_0"); n != 0 {
		t.Fatalf("%d packets on the paid link", n)
	}
}

// F-1: the path moves onto a paid link after admission, where the worker's
// own check cannot see it (no paid policy on this node): the router sends
// nothing on the paid link, and the attempt ends not sent. Without the
// constraint the same send does go out on it (positive control).
func TestIntentFix_PathOnAPaidLinkSendsNothing(t *testing.T) {
	h := newIntentHarness(t)
	p := newLXMFPair(t, []string{"lora_rns"}, nil, "iridium_0")
	h.useLXMF(p)
	dest := p.remoteRouter.Hash()
	p.remote.SetPaidPolicy("iridium_0", &rns.PaidPolicy{Dests: [][rns.HashLen]byte{dest}})
	c := h.contact(directory.Address{Kind: directory.KindReticulum, Value: p.remoteRouter.HashHex()})
	id := h.submit(c, "stay free", directory.StrategyPrimaryOnly) // admitted: no path known yet
	if err := p.remoteRouter.Announce(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.local.NextHopInterface(dest) == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := p.local.NextHopInterface(dest); got != "iridium_0" {
		t.Fatalf("setup: path on %q; want iridium_0", got)
	}
	before := p.localTX.count("iridium_0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	s := h.snap(id)
	r := rowsByRound(s)[1]
	if n := p.localTX.count("iridium_0") - before; n != 0 {
		t.Fatalf("%d packets left on the paid link", n)
	}
	if len(s.Transmissions) != 1 || s.Transmissions[0].OutcomeClass != database.ClassNotSent ||
		!strings.Contains(s.Transmissions[0].Detail, "paid link") || r.Status != "retry" || s.Intent.State != database.IntentActive {
		t.Fatalf("transmission %+v, row %s; want not sent (paid link), the row waiting for a free path", s.Transmissions, r.Status)
	}
	if s.Intent.ReservedMinor != 0 || s.Intent.IncurredMinor != 0 || s.Intent.PossibleMinor != 0 {
		t.Fatalf("aggregates %d/%d/%d", s.Intent.ReservedMinor, s.Intent.IncurredMinor, s.Intent.PossibleMinor)
	}
	// Positive control: the legacy send to the same destination uses it.
	body, _ := json.Marshal(rnsstack.LXMFBody{Content: "legacy"})
	_, _ = p.stack.Deliver(ctx, database.MessageDelivery{Destination: p.remoteRouter.HashHex(), Payload: body})
	if p.localTX.count("iridium_0") == before {
		t.Fatal("positive control: the unconstrained send did not use the paid link, so the test proves nothing")
	}
}

// preparedReceiptMesh answers inside the send, before it returns.
type preparedReceiptMesh struct {
	transport.MeshTransport
	esc   *Escalator
	calls int
}

func (m *preparedReceiptMesh) SendMessageWithID(_ context.Context, _ transport.SendRequest, id uint32) error {
	m.calls++
	m.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: 0xabcd, To: 0xaaaa, RequestID: id,
		PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorName: "NONE"}})
	return nil
}

// F-2 with the kits' radio (prepared ids): the id is stored at the
// handoff, so an ACK inside the send matches at once; and after a stop
// between the send and finalization the restart keeps the id, the ACK
// after the restart completes the intent, no fallback is sent.
func TestIntentFix_PreparedPacketID(t *testing.T) {
	t.Run("ACK before the send returns", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "prepared", directory.StrategyOrderedFallback)
		m := &preparedReceiptMesh{esc: h.esc}
		h.workers["mesh_0"].mesh = m
		h.work("mesh_0")
		h.drain()
		s := h.snap(id)
		if s.Intent.Outcome != database.OutcomeDeliveredDevice || len(s.Evidence) != 1 || !s.Evidence[0].Matched {
			t.Fatalf("%s/%s, evidence %+v", s.Intent.State, s.Intent.Outcome, s.Evidence)
		}
	})
	t.Run("stop after the handoff", func(t *testing.T) {
		h := newIntentHarness(t)
		id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "prepared", directory.StrategyOrderedFallback)
		row := rowsByRound(h.snap(id))[1]
		_, _ = h.db.ClaimDeliveryForSending(row.ID)
		txID, _ := h.db.AuthorizeTransmission(row.ID, 1, "", 0, 0)
		pkt, _ := h.workers["mesh_0"].reserveMeshID()
		hexID := strconv.FormatUint(uint64(pkt), 16)
		for len(hexID) < 8 {
			hexID = "0" + hexID
		}
		if err := h.db.MarkTransmissionHandedOffWith(txID, database.Correlation{Kind: meshCorrKind, Value: hexID, Scope: "mesh_0", Peer: annMesh}); err != nil {
			t.Fatal(err)
		}
		h.restart() // the radio had the packet; finalization never ran
		s := h.snap(id)
		if s.Transmissions[0].OutcomeClass != database.ClassUnknown || s.Transmissions[0].CorrValue != hexID {
			t.Fatalf("recovered: %+v; want unknown with the id kept", s.Transmissions[0])
		}
		h.ack(pkt, annMesh, 0)
		h.work("lxmf_0")
		s = h.snap(id)
		if s.Intent.Outcome != database.OutcomeDeliveredDevice || h.lx.n() != 0 {
			t.Fatalf("%s/%s, %d fallback sends", s.Intent.State, s.Intent.Outcome, h.lx.n())
		}
	})
}

// F-3 with the flag off: an intent closed before the restart still takes
// in its late ACK on the next heartbeat, with the notice lost; nothing is
// sent again.
func TestIntentFix_ClosedIntentTakesItsReceiptWithTheFlagOff(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(0)), "late", directory.StrategyPrimaryOnly)
	h.work("mesh_0")
	h.drain()
	pkt := h.mesh.lastID()
	h.clock.Advance(121 * time.Second)
	h.esc.Heartbeat()
	if kept, err := h.db.HasIntents(); err != nil || !kept {
		t.Fatalf("HasIntents with one closed intent: %v %v; main.go would not start the Escalator", kept, err)
	}
	h.start(false)
	attempts, rows := h.mesh.attempts, len(h.snap(id).Rows)
	h.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: 0xabcd, RequestID: pkt,
		PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorName: "NONE"}})
	for len(h.esc.notify) > 0 {
		<-h.esc.notify // lost
	}
	h.esc.Heartbeat()
	s := h.snap(id)
	if s.Intent.Outcome != database.OutcomeDeliveredDevice || s.Intent.LastTransition != "T14" || s.Intent.State != database.IntentClosed {
		t.Fatalf("%s/%s by %s; want closed, delivered_device by T14", s.Intent.State, s.Intent.Outcome, s.Intent.LastTransition)
	}
	if h.mesh.attempts != attempts || len(s.Rows) != rows {
		t.Fatal("a closed intent sent again")
	}
}

// A-5: the acceptance is stamped at finalization from the Escalator's
// clock, so a restart before the Escalator looked does not move the
// receipt allowance later: with heartbeats every 10 s and one restart it
// ends 110 to 120 s after the acceptance.
func TestIntentFix_AcceptanceStampSurvivesARestart(t *testing.T) {
	h := newIntentHarness(t)
	id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "stamp", directory.StrategyOrderedFallback)
	for i := 0; i < 3; i++ {
		h.clock.Advance(10 * time.Second)
		h.esc.Heartbeat()
	}
	h.work("mesh_0") // accepted at 30 s
	if r := rowsByRound(h.snap(id))[1]; r.AcceptedElapsedMs == nil || *r.AcceptedElapsedMs != 30000 {
		t.Fatalf("stamp %v; want 30000 ms at finalization", r.AcceptedElapsedMs)
	}
	h.clock.mu.Lock()
	h.clock.trusted = false // and no downtime counted
	h.clock.mu.Unlock()
	h.restart()
	fired := -1
	for real := 30; real <= 200 && fired < 0; real += 10 {
		if h.snap(id).Intent.ActiveRound == 2 {
			fired = real - 30
			break
		}
		h.clock.Advance(10 * time.Second)
		h.esc.Heartbeat()
	}
	if fired < 110 || fired > 120 {
		t.Fatalf("the receipt allowance ended %d s after the acceptance; want 110 to 120", fired)
	}
}

// A-14: a packet id is matched only inside its window: an id used again
// after the window names the newer transmission alone; CorrelationInUse
// tells a sender which ids are still live.
func TestIntentFix_ReusedPacketIDAfterTheWindow(t *testing.T) {
	h := newIntentHarness(t)
	h.mesh.fixedID = 0x0badf00d
	h.workers["mesh_0"].mesh = pickOwnIDMesh{m: h.mesh}
	c := h.contact(meshAddr(0))
	old := h.submit(c, "old", directory.StrategyPrimaryOnly)
	h.work("mesh_0")
	if live, _ := h.db.CorrelationInUse(meshCorrKind, "0badf00d", "mesh_0", meshCorrWindowS); !live {
		t.Fatal("a fresh id is not live")
	}
	if _, err := h.db.Exec(`UPDATE delivery_transmissions SET handoff_at = datetime('now', '-2 hours') WHERE intent_id = ?`, old); err != nil {
		t.Fatal(err)
	}
	if live, _ := h.db.CorrelationInUse(meshCorrKind, "0badf00d", "mesh_0", meshCorrWindowS); live {
		t.Fatal("an id two hours old is still live")
	}
	fresh := h.submit(c, "new", directory.StrategyPrimaryOnly)
	h.work("mesh_0")
	h.ack(0x0badf00d, annMesh, 0)
	if s := h.snap(fresh); s.Intent.Outcome != database.OutcomeDeliveredDevice {
		t.Fatalf("the newer transmission: %s/%s", s.Intent.State, s.Intent.Outcome)
	}
	if s := h.snap(old); s.Intent.Outcome == database.OutcomeDeliveredDevice || len(s.Evidence) != 0 {
		t.Fatalf("the expired transmission took the ACK: %s, %d evidence", s.Intent.Outcome, len(s.Evidence))
	}
}

// F-1 at the send itself: with the path on a paid interface (as after a
// path change between the router's check and the send), a constrained
// packet and a constrained link request leave nothing on it; the same
// calls without the constraint do (positive control).
func TestIntentFix_RNSRefusesAPaidPathAtTheSend(t *testing.T) {
	p := newLXMFPair(t, []string{"lora_rns"}, nil, "iridium_0")
	dest := p.remoteRouter.Hash()
	p.remote.SetPaidPolicy("iridium_0", &rns.PaidPolicy{Dests: [][rns.HashLen]byte{dest}})
	if err := p.remoteRouter.Announce(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.local.NextHopInterface(dest) != "iridium_0" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.local.NextHopInterface(dest) != "iridium_0" {
		t.Fatal("setup: no path on iridium_0")
	}
	free := rns.Constraint{FreeOnly: true}
	before := p.localTX.count("iridium_0")
	if _, err := p.local.SendDataWith(dest, []byte("x"), true, free); !errors.Is(err, rns.ErrPaidRefused) {
		t.Fatalf("constrained packet: %v; want ErrPaidRefused", err)
	}
	if _, err := p.local.Links().InitiateWith(dest, free); !errors.Is(err, rns.ErrPaidRefused) {
		t.Fatalf("constrained link request: %v; want ErrPaidRefused", err)
	}
	if n := p.localTX.count("iridium_0") - before; n != 0 {
		t.Fatalf("%d packets left on the paid interface", n)
	}
	if _, err := p.local.SendDataWith(dest, []byte("x"), false, rns.Constraint{}); err != nil || p.localTX.count("iridium_0") == before {
		t.Fatalf("positive control: unconstrained send %v, %d packets", err, p.localTX.count("iridium_0")-before)
	}
}

// F-1 on links: an existing link on a paid interface is not used by a
// constrained send, and a constrained packet or resource on it is refused
// before anything is encrypted or sent.
func TestIntentFix_RNSRefusesAPaidLink(t *testing.T) {
	p := newLXMFPair(t, []string{"lora_rns"}, nil)
	free := rns.Constraint{FreeOnly: true}
	paid, ok := &rns.Link{Iface: "iridium_0"}, &rns.Link{Iface: "lora_rns"}
	if p.local.Links().Allows(paid, free) || !p.local.Links().Allows(ok, free) || !p.local.Links().Allows(paid, rns.Constraint{}) {
		t.Fatal("Allows does not follow the interface")
	}
	if _, err := p.local.Links().SendWith(paid, []byte("x"), free); !errors.Is(err, rns.ErrPaidRefused) {
		t.Fatalf("packet on a paid link: %v", err)
	}
	if _, err := p.local.Links().SendResourceWith(paid, []byte("x"), free); !errors.Is(err, rns.ErrPaidRefused) {
		t.Fatalf("resource on a paid link: %v", err)
	}
	if n := p.localTX.count("iridium_0"); n != 0 {
		t.Fatalf("%d packets on the paid link", n)
	}
}
