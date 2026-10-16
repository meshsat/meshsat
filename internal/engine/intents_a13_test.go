package engine

import (
	"context"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/lxmf"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/rnsstack"
)

func isProofPacket(raw []byte) bool {
	h, err := reticulum.UnmarshalHeader(raw)
	return err == nil && h.PacketType == reticulum.PacketProof
}

// a13Send submits one LXMF intent and runs its send with the destination's
// proofs held back, so the send gives up waiting (its context ends) and
// the transmission is unknown. It returns the intent id.
func a13Send(t *testing.T, h *intentHarness, p *lxmfPair) string {
	t.Helper()
	if err := p.remoteRouter.Announce(); err != nil {
		t.Fatal(err)
	}
	dest := p.remoteRouter.Hash()
	deadline := time.Now().Add(5 * time.Second)
	for p.local.NextHopInterface(dest) == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c := h.contact(directory.Address{Kind: directory.KindReticulum, Value: p.remoteRouter.HashHex()})
	id := h.submit(c, "the proof comes late", directory.StrategyPrimaryOnly)
	p.remoteTX.mu.Lock()
	p.remoteTX.hold = isProofPacket
	p.remoteTX.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	h.ctx = ctx
	h.work("lxmf_0")
	h.ctx = context.Background()
	if p.remoteTX.heldCount() == 0 {
		t.Fatal("the destination proved nothing (no proof held)")
	}
	s := h.snap(id)
	if len(s.Transmissions) != 1 || s.Transmissions[0].OutcomeClass != database.ClassUnknown {
		t.Fatalf("transmissions %+v; want one, unknown (the send gave up waiting)", s.Transmissions)
	}
	var ph, pk string
	if err := h.db.QueryRow(`SELECT proof_hash, proof_key FROM delivery_transmissions WHERE id = ?`, s.Transmissions[0].ID).Scan(&ph, &pk); err != nil || len(ph) != 64 || len(pk) != 64 {
		t.Fatalf("proof hash %q key %q (%v); want both stored as the packet went out", ph, pk, err)
	}
	return id
}

func proofEvidence(t *testing.T, h *intentHarness, id string) []database.DeliveryEvidence {
	t.Helper()
	var out []database.DeliveryEvidence
	for _, e := range h.snap(id).Evidence {
		if e.Event == "proof" {
			out = append(out, e)
		}
	}
	return out
}

func waitOutcome(t *testing.T, h *intentHarness, id, want string) *database.IntentSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.esc.Heartbeat()
		h.drain()
		if s := h.snap(id); s.Intent.Outcome == want {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := h.snap(id)
	t.Fatalf("intent %s: %s/%s, evidence %+v; want %s", id, s.Intent.State, s.Intent.Outcome, s.Evidence, want)
	return nil
}

// A-13, in this process: the proof comes after the send gave up. It is
// recorded as the destination's signed proof on that transmission and the
// intent gets accepted_destination_node; a second copy adds nothing; no
// second transmission. [MESHSAT-1584]
func TestIntentA13_LateProofCountsInThisProcess(t *testing.T) {
	h := newIntentHarness(t)
	p := newLXMFPair(t, []string{"lora_rns"}, []string{"lora_rns"}, "lora_rns")
	h.useLXMF(p)
	if err := p.stack.EnableLateProofs(h.db, h.esc.Notify); err != nil {
		t.Fatal(err)
	}
	id := a13Send(t, h, p)
	held := p.remoteTX.held[0][1]
	p.remoteTX.release()
	s := waitOutcome(t, h, id, database.OutcomeAcceptedDestinationNode)
	ev := proofEvidence(t, h, id)
	if len(ev) != 1 || !ev[0].Matched || ev[0].Trust != "signed" || ev[0].Source != "destination_node" || ev[0].TransmissionID != s.Transmissions[0].ID {
		t.Fatalf("proof evidence %+v; want one, matched, signed, on the transmission", ev)
	}
	p.local.Inbound(append([]byte(nil), held...), "lora_rns") // the same proof again
	time.Sleep(100 * time.Millisecond)
	if n := len(proofEvidence(t, h, id)); n != 1 {
		t.Fatalf("%d proof evidence rows after a second copy; want 1", n)
	}
	if n := len(h.snap(id).Transmissions); n != 1 {
		t.Fatalf("%d transmissions; want 1", n)
	}
}

// A-13, after a restart: the proof comes once the process (node, router,
// stack and Escalator) was started again. The stored hash and key are
// enough: the intent gets accepted_destination_node. The intent had closed
// in between ("may have been sent"); the proof upgrades it (T14).
func TestIntentA13_LateProofCountsAfterARestart(t *testing.T) {
	h := newIntentHarness(t)
	p := newLXMFPair(t, []string{"lora_rns"}, []string{"lora_rns"}, "lora_rns")
	h.useLXMF(p)
	if err := p.stack.EnableLateProofs(h.db, h.esc.Notify); err != nil {
		t.Fatal(err)
	}
	id := a13Send(t, h, p)
	// Past every allowance: the intent closes on what it knows.
	h.clock.Advance(2 * time.Hour)
	h.esc.Heartbeat()
	h.drain()
	if s := h.snap(id); s.Intent.State != database.IntentClosed || s.Intent.Outcome == database.OutcomeAcceptedDestinationNode {
		t.Fatalf("before the proof: %s/%s; want closed, not yet proven", s.Intent.State, s.Intent.Outcome)
	}

	// The restart: a new node and router under the same identity, a new
	// stack that knows only the database, a new Escalator.
	srcID := p.local.Identity()
	fresh, err := rns.New(rns.Config{Identity: srcID}, p.localTX)
	if err != nil {
		t.Fatal(err)
	}
	p.local = fresh
	p.remoteTX.mu.Lock()
	p.remoteTX.peer = fresh
	p.remoteTX.mu.Unlock()
	p.stack = &rnsstack.Stack{Node: fresh, LXMF: lxmf.New(fresh, lxmf.Config{Identity: srcID})}
	h.restart()
	h.useLXMF(p)
	if err := p.stack.EnableLateProofs(h.db, h.esc.Notify); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fresh.Start(ctx)

	p.remoteTX.release()
	waitOutcome(t, h, id, database.OutcomeAcceptedDestinationNode)
	if ev := proofEvidence(t, h, id); len(ev) != 1 || ev[0].Trust != "signed" {
		t.Fatalf("proof evidence %+v", ev)
	}
}

// A-13, negative: a proof with a broken signature for our packet, and
// someone else's proof, record nothing; the real proof still counts.
func TestIntentA13_ForgedOrForeignProofCountsNothing(t *testing.T) {
	h := newIntentHarness(t)
	p := newLXMFPair(t, []string{"lora_rns"}, []string{"lora_rns"}, "lora_rns")
	h.useLXMF(p)
	if err := p.stack.EnableLateProofs(h.db, h.esc.Notify); err != nil {
		t.Fatal(err)
	}
	id := a13Send(t, h, p)
	real := append([]byte(nil), p.remoteTX.held[0][1]...)
	forged := append([]byte(nil), real...)
	forged[len(forged)-1] ^= 0xff // last byte of the signature
	p.local.Inbound(forged, "lora_rns")
	foreign := append([]byte(nil), real...)
	hdr, _ := reticulum.UnmarshalHeader(foreign)
	hdr.DestHash[0] ^= 0xff // another packet's proof address
	p.local.Inbound(hdr.Marshal(), "lora_rns")
	time.Sleep(200 * time.Millisecond)
	h.esc.Heartbeat()
	h.drain()
	if ev := proofEvidence(t, h, id); len(ev) != 0 {
		t.Fatalf("a forged or foreign proof was recorded: %+v", ev)
	}
	if s := h.snap(id); s.Intent.Outcome == database.OutcomeAcceptedDestinationNode {
		t.Fatal("outcome proven by a forged or foreign proof")
	}
	p.remoteTX.release()
	waitOutcome(t, h, id, database.OutcomeAcceptedDestinationNode)
}
