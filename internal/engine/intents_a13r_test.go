package engine

import (
	"context"
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
)

// A13-R1 acceptance beyond the review's reproducer: a failed store writes
// nothing, in both send modes; a process that dies after the write and
// before the call returns still has its proof counted; a late proof in the
// link mode counts. [MESHSAT-1584]

// msgTx counts the message's own packets (an opportunistic DATA packet to a
// single destination, or a link DATA packet in context none) and can hold
// the sender in Transmit after forwarding one (a process stuck or dying
// after the write).
type msgTx struct {
	*linkTx
	written atomic.Int32
	onLink  atomic.Int32 // of which link packets
	block   chan struct{}
	blocked chan struct{}
	once    sync.Once
}

func isMessagePacket(raw []byte) (msg, link bool) {
	h, err := reticulum.UnmarshalHeader(raw)
	if err != nil || h.PacketType != reticulum.PacketData || h.Context != reticulum.ContextNone {
		return false, false
	}
	return h.DestType == reticulum.DestSingle || h.DestType == reticulum.DestLink, h.DestType == reticulum.DestLink
}

func (tx *msgTx) Transmit(iface string, raw []byte) error {
	msg, link := isMessagePacket(raw)
	if msg {
		tx.written.Add(1)
	}
	if link {
		tx.onLink.Add(1)
	}
	err := tx.linkTx.Transmit(iface, raw)
	if msg && tx.block != nil {
		tx.once.Do(func() { close(tx.blocked) })
		<-tx.block
	}
	return err
}

// isDataProof: a proof of a data packet, not the link request's proof
// (which a link needs to come up).
func isDataProof(raw []byte) bool {
	h, err := reticulum.UnmarshalHeader(raw)
	return err == nil && h.PacketType == reticulum.PacketProof && h.Context != reticulum.ContextLRProof
}

// a13rPair is an LXMF pair whose local node sends through tx, with late
// proofs on and the destination's path known.
func a13rPair(t *testing.T, h *intentHarness, tx *msgTx) *lxmfPair {
	t.Helper()
	p := newLXMFPair(t, []string{"lora_rns"}, []string{"lora_rns"}, "lora_rns")
	tx.linkTx = p.localTX
	fresh, err := rns.New(rns.Config{Identity: p.local.Identity()}, tx)
	if err != nil {
		t.Fatal(err)
	}
	p.local = fresh
	p.remoteTX.peer = fresh
	p.stack = &rnsstack.Stack{Node: fresh, LXMF: lxmf.New(fresh, lxmf.Config{Identity: fresh.Identity()})}
	h.useLXMF(p)
	if err := p.stack.EnableLateProofs(h.db, h.esc.Notify); err != nil {
		t.Fatal(err)
	}
	if err := p.remoteRouter.Announce(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fresh.NextHopInterface(p.remoteRouter.Hash()) == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return p
}

// 300 characters go as one link packet (above the 295 of a single packet).
var linkText = strings.Repeat("l", 300)

// The store before the write fails (a database refusal): nothing of the
// message is written, the attempt is not sent, the peer receives nothing.
func TestIntentA13R_StoreFailureWritesNothing(t *testing.T) {
	for _, mode := range []struct{ name, text string }{{"packet", "store first"}, {"link", linkText}} {
		t.Run(mode.name, func(t *testing.T) {
			h := newIntentHarness(t)
			tx := &msgTx{}
			p := a13rPair(t, h, tx)
			if _, err := h.db.Exec(`CREATE TRIGGER review_fail_proof BEFORE UPDATE OF proof_hash ON delivery_transmissions
				BEGIN SELECT RAISE(ABORT, 'test: the store fails'); END;`); err != nil {
				t.Fatal(err)
			}
			c := h.contact(directory.Address{Kind: directory.KindReticulum, Value: p.remoteRouter.HashHex()})
			id := h.submit(c, mode.text, directory.StrategyPrimaryOnly)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			h.ctx = ctx
			h.work("lxmf_0")
			h.ctx = context.Background()
			time.Sleep(100 * time.Millisecond)
			s := h.snap(id)
			if n := tx.written.Load(); n != 0 || p.received.Load() != 0 {
				t.Fatalf("%d message packets written, %d received; want none", n, p.received.Load())
			}
			if len(s.Transmissions) != 1 || s.Transmissions[0].OutcomeClass != database.ClassNotSent ||
				!strings.Contains(s.Transmissions[0].Detail, "not prepared") {
				t.Fatalf("transmissions %+v; want one, not sent because the store failed before the write", s.Transmissions)
			}
		})
	}
}

// The process dies after the packet is written and before the send
// returns: after the restart (new node, stack and Escalator) the proof
// finds the hash stored before the write and completes the transmission
// once; nothing is sent again.
func TestIntentA13R_CrashAfterTheWrite(t *testing.T) {
	h := newIntentHarness(t)
	tx := &msgTx{block: make(chan struct{}), blocked: make(chan struct{})}
	p := a13rPair(t, h, tx)
	p.remoteTX.mu.Lock()
	p.remoteTX.hold = isDataProof
	p.remoteTX.mu.Unlock()
	c := h.contact(directory.Address{Kind: directory.KindReticulum, Value: p.remoteRouter.HashHex()})
	id := h.submit(c, "the process dies after the write", directory.StrategyPrimaryOnly)
	done := make(chan struct{})
	stuck, stop := context.WithCancel(context.Background())
	defer stop()
	go func() { defer close(done); h.workers["lxmf_0"].deliver(stuck, firstRow(t, h, id)) }()
	select {
	case <-tx.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the message was never written")
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.remoteTX.heldCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.remoteTX.heldCount() == 0 {
		t.Fatal("the destination proved nothing")
	}

	// The restart. The stuck sender stays stuck until the test ends.
	fresh, err := rns.New(rns.Config{Identity: p.local.Identity()}, p.localTX)
	if err != nil {
		t.Fatal(err)
	}
	stack := &rnsstack.Stack{Node: fresh, LXMF: lxmf.New(fresh, lxmf.Config{Identity: fresh.Identity()})}
	if err := stack.EnableLateProofs(h.db, nil); err != nil { // before the node takes a packet
		t.Fatal(err)
	}
	h.restart()
	stack.SetLateProofNotify(h.esc.Notify)
	p.remoteTX.mu.Lock()
	p.remoteTX.peer = fresh
	p.remoteTX.mu.Unlock()
	p.remoteTX.release()
	waitOutcome(t, h, id, database.OutcomeAcceptedDestinationNode)
	if ev := proofEvidence(t, h, id); len(ev) != 1 {
		t.Fatalf("proof evidence %+v; want one", ev)
	}
	if n := tx.written.Load(); n != 1 {
		t.Fatalf("%d message packets written; want 1", n)
	}
	stop()
	close(tx.block)
	<-done
}

// The link mode: the destination's proof of the link packet comes after
// the send stopped waiting (the link still up) and completes the intent.
func TestIntentA13R_LateProofInTheLinkMode(t *testing.T) {
	h := newIntentHarness(t)
	tx := &msgTx{}
	p := a13rPair(t, h, tx)
	p.remoteTX.mu.Lock()
	p.remoteTX.hold = isDataProof
	p.remoteTX.mu.Unlock()
	c := h.contact(directory.Address{Kind: directory.KindReticulum, Value: p.remoteRouter.HashHex()})
	id := h.submit(c, linkText, directory.StrategyPrimaryOnly)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	h.ctx = context.Background()
	s := h.snap(id)
	if len(s.Transmissions) != 1 || s.Transmissions[0].OutcomeClass != database.ClassUnknown || p.remoteTX.heldCount() == 0 {
		t.Fatalf("transmissions %+v, %d proofs held; want one unknown, a proof held", s.Transmissions, p.remoteTX.heldCount())
	}
	var ph string
	_ = h.db.QueryRow(`SELECT proof_hash FROM delivery_transmissions WHERE id = ?`, s.Transmissions[0].ID).Scan(&ph)
	if len(ph) != 64 {
		t.Fatalf("proof hash %q not stored", ph)
	}
	if tx.onLink.Load() != 1 {
		t.Fatalf("%d link packets; want the message as one", tx.onLink.Load())
	}
	p.remoteTX.release()
	waitOutcome(t, h, id, database.OutcomeAcceptedDestinationNode)
}

func firstRow(t *testing.T, h *intentHarness, intentID string) database.MessageDelivery {
	t.Helper()
	s := h.snap(intentID)
	if len(s.Rows) == 0 {
		t.Fatal("no attempt row")
	}
	return s.Rows[0].MessageDelivery
}
