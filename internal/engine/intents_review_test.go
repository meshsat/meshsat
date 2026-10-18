package engine

// The red-team review of stage 1a (16 Oct 2026, commit 72e6eac) reproduced
// four defects with these tests; they are kept here as regressions, as the
// reviewer wrote them, except TestReviewDistinctLXMFIntentsHaveDistinctHashes:
// its rows were built for a plan entry with no wire timestamp, which no
// submission makes any more, so it builds them from two real submissions in
// one millisecond (F-4's acceptance). [MESHSAT-1584]

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

type reviewReceiptMesh struct {
	transport.MeshTransport
	esc   *Escalator
	err   error
	early bool
	id    uint32
}

func (m *reviewReceiptMesh) SendMessageID(context.Context, transport.SendRequest) (uint32, error) {
	if m.early {
		m.receipt()
	}
	return m.id, m.err
}

func (m *reviewReceiptMesh) receipt() {
	m.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: 0xabcd, To: 0xaaaa, RequestID: m.id,
		PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorReason: 0, ErrorName: "NONE"}})
}

// reviewPreparedReceiptMesh is reviewReceiptMesh on the contract the fix
// requires: the send takes the id the engine chose and stored.
type reviewPreparedReceiptMesh struct {
	reviewReceiptMesh
}

func (m *reviewPreparedReceiptMesh) SendMessageWithID(_ context.Context, _ transport.SendRequest, id uint32) error {
	m.id = id
	if m.early {
		m.receipt()
	}
	return m.err
}

// Adapted (fix2 closure review): the self-picking adapter is now refused
// (TestReviewReusedIDReceiptBeforeReturnMustStayAmbiguous), so both
// orderings run on the prepared-id send, with the reviewer's assertions.
func TestReviewMeshCorrelationSurvivesReceiptOrdering(t *testing.T) {
	for _, early := range []bool{true, false} {
		t.Run(fmt.Sprintf("early_receipt_%t", early), func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0), lxmfAddr(1)), "receipt ordering", directory.StrategyOrderedFallback)
			m := &reviewPreparedReceiptMesh{reviewReceiptMesh{esc: h.esc, early: early}}
			if !early {
				m.err = errors.New("serial write returned an error after possible submission")
			}
			h.workers["mesh_0"].mesh = m
			h.work("mesh_0")
			if !early {
				m.receipt()
			}
			h.drain()
			s := h.snap(id)
			var unmatched int
			if err := h.db.Get(&unmatched, `SELECT COUNT(*) FROM delivery_evidence WHERE corr_value = ? AND matched = 0`, fmt.Sprintf("%08x", m.id)); err != nil {
				t.Fatal(err)
			}
			if s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeDeliveredDevice {
				t.Errorf("valid destination ACK did not complete intent: state=%s outcome=%s unmatched=%d corr=%q",
					s.Intent.State, s.Intent.Outcome, unmatched, s.Transmissions[0].CorrValue)
			}
			h.clock.Advance(121 * time.Second)
			h.eval(id)
			h.work("lxmf_0")
			if n := h.lx.n(); n != 0 {
				t.Errorf("fallback sent %d times despite destination ACK", n)
			}
		})
	}
}

func TestReviewClosedEvidenceSurvivesLostNotice(t *testing.T) {
	for _, recovery := range []string{"heartbeat", "restart"} {
		t.Run(recovery, func(t *testing.T) {
			h := newIntentHarness(t)
			id := h.submit(h.contact(meshAddr(0)), "late receipt", directory.StrategyPrimaryOnly)
			h.work("mesh_0")
			h.drain()
			h.clock.Advance(121 * time.Second)
			h.esc.Heartbeat()
			if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
				t.Fatalf("invalid setup: %s/%s", s.Intent.State, s.Intent.Outcome)
			}
			h.esc.MeshRoutingReceipt("mesh_0", transport.MeshMessage{From: 0xabcd, RequestID: h.mesh.lastID(),
				PortNum: int(transport.PortNumRouting), Routing: &transport.RoutingInfo{ErrorName: "NONE"}})
			if s := h.snap(id); len(s.Evidence) != 1 || !s.Evidence[0].Matched || s.Evidence[0].Completes != database.OutcomeDeliveredDevice {
				t.Fatalf("receipt was not persisted and matched: %+v", s.Evidence)
			}
			if recovery == "restart" {
				h.restart()
			} else {
				select {
				case <-h.esc.notify:
				default:
					t.Fatal("missing notice in setup")
				}
			}
			h.esc.Heartbeat()
			if s := h.snap(id); s.Intent.Outcome != database.OutcomeDeliveredDevice {
				t.Fatalf("durable matched ACK was not replayed after %s: outcome=%s evidence=%s", recovery, s.Intent.Outcome, s.Evidence[0].Completes)
			}
		})
	}
}

type reviewPaidTransmitter struct {
	mu   sync.Mutex
	sent []string
	peer *rns.Node
}

func (tx *reviewPaidTransmitter) Transmit(iface string, raw []byte) error {
	tx.mu.Lock()
	tx.sent = append(tx.sent, iface)
	tx.mu.Unlock()
	if tx.peer != nil {
		packet := append([]byte(nil), raw...)
		go tx.peer.Inbound(packet, iface)
	}
	return nil
}

func (*reviewPaidTransmitter) Floodable() []string { return nil }
func (*reviewPaidTransmitter) HWMTU(string) int    { return 500 }
func (*reviewPaidTransmitter) Bitrate(string) int  { return 0 }

func TestReviewFreeIntentCannotStartPaidPathDiscovery(t *testing.T) {
	h := newIntentHarness(t)
	identity, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	tx := &reviewPaidTransmitter{}
	node, err := rns.New(rns.Config{Identity: identity}, tx)
	if err != nil {
		t.Fatal(err)
	}
	destBytes, err := hex.DecodeString(annLXMF)
	if err != nil {
		t.Fatal(err)
	}
	var dest [rns.HashLen]byte
	copy(dest[:], destBytes)
	node.SetPaidPolicy("iridium_0", &rns.PaidPolicy{Dests: [][rns.HashLen]byte{dest}})
	stack := &rnsstack.Stack{Node: node, LXMF: lxmf.New(node, lxmf.Config{Identity: identity})}
	h.d.SetLXMFSender(stack)
	h.workers["lxmf_0"].lxmf = stack
	if stack.CrossesPaidLink(annLXMF) {
		t.Fatal("invalid setup: path should still be unknown")
	}
	id := h.submit(h.contact(lxmfAddr(0)), "free route only", directory.StrategyPrimaryOnly)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	s := h.snap(id)
	if len(tx.sent) != 0 {
		t.Fatalf("free intent emitted %d packets on %v with reserved=%d incurred=%d possible=%d",
			len(tx.sent), tx.sent, s.Intent.ReservedMinor, s.Intent.IncurredMinor, s.Intent.PossibleMinor)
	}
}

func TestReviewFreeIntentCannotUseNewlyDiscoveredPaidPath(t *testing.T) {
	h := newIntentHarness(t)
	source, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	remote, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	localTX, remoteTX := &reviewPaidTransmitter{}, &reviewPaidTransmitter{}
	localNode, err := rns.New(rns.Config{Identity: source}, localTX)
	if err != nil {
		t.Fatal(err)
	}
	remoteNode, err := rns.New(rns.Config{Identity: remote}, remoteTX)
	if err != nil {
		t.Fatal(err)
	}
	localTX.peer, remoteTX.peer = remoteNode, localNode
	var received atomic.Int32
	remoteRouter := lxmf.New(remoteNode, lxmf.Config{Identity: remote, OnMessage: func(m *lxmf.Message) {
		if string(m.Content) == "must stay free" {
			received.Add(1)
		}
	}})
	localRouter := lxmf.New(localNode, lxmf.Config{Identity: source})
	dest := remoteRouter.Hash()
	localNode.SetPaidPolicy("iridium_0", &rns.PaidPolicy{Dests: [][rns.HashLen]byte{dest}})
	remoteNode.SetPaidPolicy("iridium_0", &rns.PaidPolicy{Dests: [][rns.HashLen]byte{dest}})
	if err := localNode.Remember(dest, remote.PublicBytes(), nil); err != nil {
		t.Fatal(err)
	}
	if err := remoteNode.Remember(localRouter.Hash(), source.PublicBytes(), nil); err != nil {
		t.Fatal(err)
	}
	stack := &rnsstack.Stack{Node: localNode, LXMF: localRouter}
	h.d.SetLXMFSender(stack)
	h.workers["lxmf_0"].lxmf = stack
	if stack.CrossesPaidLink(remoteRouter.HashHex()) {
		t.Fatal("invalid setup: path should still be unknown")
	}
	id := h.submit(h.contact(directory.Address{Kind: directory.KindReticulum, Value: remoteRouter.HashHex()}), "must stay free", directory.StrategyPrimaryOnly)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	h.drain()
	s := h.snap(id)
	localTX.mu.Lock()
	sent := append([]string(nil), localTX.sent...)
	localTX.mu.Unlock()
	if len(sent) != 0 {
		t.Fatalf("free intent used newly discovered paid path: packets=%v received=%d outcome=%s reserved=%d incurred=%d possible=%d",
			sent, received.Load(), s.Intent.Outcome, s.Intent.ReservedMinor, s.Intent.IncurredMinor, s.Intent.PossibleMinor)
	}
}

// Adapted (see the file comment): two submissions with the same sender,
// recipient and text in one millisecond get different LXMF hashes, and a
// retry of either keeps its own, after a restart too.
func TestReviewDistinctLXMFIntentsHaveDistinctHashes(t *testing.T) {
	h := newIntentHarness(t) // the fake wall clock stands still: one millisecond
	source, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	dest, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	c := h.contact(lxmfAddr(0))
	ids := []string{h.submit(c, "repeat intentionally", directory.StrategyPrimaryOnly), h.submit(c, "repeat intentionally", directory.StrategyPrimaryOnly)}
	hashOf := func(id string) [32]byte {
		s := h.snap(id)
		var body rnsstack.LXMFBody
		if err := json.Unmarshal(s.Rows[0].Payload, &body); err != nil {
			t.Fatal(err)
		}
		m := &lxmf.Message{Dest: dest.DestHash(lxmf.DeliveryName), Source: source.DestHash(lxmf.DeliveryName),
			Timestamp: body.Timestamp, Title: []byte(body.Title), Content: []byte(body.Content), Fields: lxmf.MapOf()}
		if err := m.Pack(source, 0, nil); err != nil {
			t.Fatal(err)
		}
		return m.Hash
	}
	// The wire timestamps are allocated, not read off the clock: with the
	// wall clock standing still the second is one millisecond after the
	// first, whatever the intents' ids say.
	var wire []int64
	for _, id := range ids {
		in, _ := h.db.GetIntent(id)
		wire = append(wire, in.RoutePlan[0].WireTimestampMs)
	}
	if wire[0] != h.clock.Wall().UnixMilli() || wire[1] != wire[0]+1 {
		t.Fatalf("wire timestamps %v; want %d and one more", wire, h.clock.Wall().UnixMilli())
	}
	first := [][32]byte{hashOf(ids[0]), hashOf(ids[1])}
	if first[0] == first[1] {
		t.Fatalf("distinct intents in one millisecond encode as the same LXMF message: %x", first[0])
	}
	h.restart()
	for i, id := range ids {
		if got := hashOf(id); got != first[i] {
			t.Fatalf("intent %d changed its hash across a restart", i)
		}
		// The next round's row for the same route (an escalation) is
		// built from the frozen plan and keeps the wire timestamp too.
		in, _ := h.db.GetIntent(id)
		row := h.esc.buildRow(*in, in.RoutePlan[0], 2, 2)
		var a, b rnsstack.LXMFBody
		_ = json.Unmarshal(row.Payload, &a)
		_ = json.Unmarshal(h.snap(id).Rows[0].Payload, &b)
		if a.Timestamp != b.Timestamp || a.Timestamp == 0 || !a.FreeOnly {
			t.Fatalf("intent %d: rebuilt row ts %v free_only %v, stored ts %v", i, a.Timestamp, a.FreeOnly, b.Timestamp)
		}
	}
}

// The closure review of the corrective patch (433bcb6) added three more;
// kept as written, except: reviewV62Upgrade falls back to the fixture in
// testdata (made by 72e6eac, see v62-intents.json) and accepts any version
// from 63 on (v64 came with the fix); the reuse test no longer asserts at
// setup that the old id was released, which the fix stopped doing on
// purpose (the radio reuses it anyway, which is the case under test).

func TestReviewUpgradeV62QueuedLXMFStaysFree(t *testing.T) {
	h := reviewV62Upgrade(t)
	var id string
	if err := h.db.Get(&id, `SELECT id FROM delivery_intents WHERE payload = ?`, []byte("v62 queued LXMF")); err != nil {
		t.Fatal(err)
	}
	s := h.snap(id)
	var body rnsstack.LXMFBody
	if err := json.Unmarshal(s.Rows[0].Payload, &body); err != nil {
		t.Fatal(err)
	}
	t.Logf("migrated stored attempt: intent=%s free_only=%t payload=%s", id, body.FreeOnly, s.Rows[0].Payload)
	identity, err := reticulum.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	tx := &reviewPaidTransmitter{}
	node, err := rns.New(rns.Config{Identity: identity}, tx)
	if err != nil {
		t.Fatal(err)
	}
	destBytes, err := hex.DecodeString(annLXMF)
	if err != nil {
		t.Fatal(err)
	}
	var dest [rns.HashLen]byte
	copy(dest[:], destBytes)
	node.SetPaidPolicy("iridium_0", &rns.PaidPolicy{Dests: [][rns.HashLen]byte{dest}})
	stack := &rnsstack.Stack{Node: node, LXMF: lxmf.New(node, lxmf.Config{Identity: identity})}
	h.d.SetLXMFSender(stack)
	h.workers["lxmf_0"].lxmf = stack
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	s = h.snap(id)
	if len(tx.sent) != 0 {
		t.Fatalf("v62 queued intent emitted paid packets after real v63 migration with flag off: packets=%v reserved=%d incurred=%d possible=%d",
			tx.sent, s.Intent.ReservedMinor, s.Intent.IncurredMinor, s.Intent.PossibleMinor)
	}
}

func TestReviewUpgradeV62ReplaysClosedEvidence(t *testing.T) {
	h := reviewV62Upgrade(t)
	var id string
	if err := h.db.Get(&id, `SELECT id FROM delivery_intents WHERE payload = ?`, []byte("v62 closed with unapplied ACK")); err != nil {
		t.Fatal(err)
	}
	h.esc.Heartbeat()
	s := h.snap(id)
	var dirty int
	if err := h.db.Get(&dirty, `SELECT dirty FROM delivery_intents WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if len(s.Evidence) != 1 || !s.Evidence[0].Matched || s.Evidence[0].Completes != database.OutcomeDeliveredDevice {
		t.Fatalf("fixture evidence was not preserved: %+v", s.Evidence)
	}
	if s.Intent.Outcome != database.OutcomeDeliveredDevice {
		t.Fatalf("v62 matched ACK remains unapplied after v63 startup and heartbeat: outcome=%s evidence=%s dirty=%d",
			s.Intent.Outcome, s.Evidence[0].Completes, dirty)
	}
}

// Adapted (fix2): both sends are prepared-id sends, and the reuse is
// applied to their stored ids, as a radio that reused one would leave it.
func TestReviewRetainedOldACKCannotCompleteNewIntent(t *testing.T) {
	h := newIntentHarness(t)
	c := h.contact(meshAddr(0))
	old := h.submit(c, "old message with delayed ACK", directory.StrategyPrimaryOnly)
	h.work("mesh_0")
	h.reuseMeshID(old, "0badf00d")
	h.drain()
	h.clock.Advance(121 * time.Second)
	h.esc.Heartbeat()
	if _, err := h.db.Exec(`UPDATE delivery_transmissions SET handoff_at = datetime('now', '-2 hours') WHERE intent_id = ?`, old); err != nil {
		t.Fatal(err)
	}
	fresh := h.submit(c, "new message without any recipient ACK", directory.StrategyPrimaryOnly)
	h.work("mesh_0")
	h.reuseMeshID(fresh, "0badf00d")
	h.drain()
	h.ack(0x0badf00d, annMesh, 0)
	s := h.snap(fresh)
	if s.Intent.Outcome == database.OutcomeDeliveredDevice {
		t.Fatalf("the delayed ACK for retained old intent %s completed new intent %s: outcome=%s matched_evidence=%+v",
			old, fresh, s.Intent.Outcome, s.Evidence)
	}
}

// countingFallbackMesh is the review's self-picking adapter with a count
// of its sends.
type countingFallbackMesh struct {
	*reviewReceiptMesh
	calls int
}

func (m *countingFallbackMesh) SendMessageID(ctx context.Context, req transport.SendRequest) (uint32, error) {
	m.calls++
	return m.reviewReceiptMesh.SendMessageID(ctx, req)
}

// The fix2 closure review's case, in the form it asks for once the
// fallback is gone: a radio that picks its own id and could answer before
// telling it is refused before it is called, and the older intent that
// holds the id it would reuse is not changed.
func TestReviewReusedIDReceiptBeforeReturnMustStayAmbiguous(t *testing.T) {
	h := newIntentHarness(t)
	contact := h.contact(meshAddr(0))
	old := h.submit(contact, "older unconfirmed transmission", directory.StrategyPrimaryOnly)
	h.work("mesh_0") // the kits' radio: prepared id
	h.reuseMeshID(old, "0badf00d")
	h.drain()
	h.clock.Advance(121 * time.Second)
	h.esc.Heartbeat()
	before := h.snap(old)
	if before.Intent.Outcome != database.OutcomeAcceptedUnconfirmed {
		t.Fatalf("old fixture: %s", before.Intent.Outcome)
	}
	if _, err := h.db.Exec(`UPDATE delivery_transmissions SET handoff_at = datetime('now', '-2 hours') WHERE intent_id = ?`, old); err != nil {
		t.Fatal(err)
	}
	mesh := &countingFallbackMesh{reviewReceiptMesh: &reviewReceiptMesh{esc: h.esc, id: 0x0badf00d, early: true}}
	h.workers["mesh_0"].mesh = mesh
	fresh := h.submit(contact, "new transmission whose ACK overtakes return", directory.StrategyPrimaryOnly)
	h.work("mesh_0")
	h.drain()
	h.esc.Heartbeat()
	after, newer := h.snap(old), h.snap(fresh)
	var evidence int
	if err := h.db.Get(&evidence, `SELECT COUNT(*) FROM delivery_evidence WHERE corr_value = '0badf00d'`); err != nil {
		t.Fatal(err)
	}
	if mesh.calls != 0 || evidence != 0 {
		t.Fatalf("the self-picking adapter was called %d times, %d answers stored; want refused before any call", mesh.calls, evidence)
	}
	if after.Intent.Outcome != before.Intent.Outcome || after.Intent.Version != before.Intent.Version {
		t.Fatalf("the older intent changed: %s v%d -> %s v%d", before.Intent.Outcome, before.Intent.Version, after.Intent.Outcome, after.Intent.Version)
	}
	r := rowsByRound(newer)[1]
	if newer.Intent.Outcome == database.OutcomeDeliveredDevice || r.Status != database.AttemptNotSent || r.Reason != "skipped_unsupported" || len(newer.Transmissions) != 0 {
		t.Fatalf("the new intent: %s, row %s/%s, %d transmissions; want not sent before any transmission", newer.Intent.Outcome, r.Status, r.Reason, len(newer.Transmissions))
	}
}
