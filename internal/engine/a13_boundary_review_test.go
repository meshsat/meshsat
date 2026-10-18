package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/lxmf"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/rnsstack"
	"meshsat/internal/routing"
)

// The A13 review's reproducers (16 Oct 2026, review of d556d73).
// A13-R1 is as supplied. A13-R2's adverse case now starts the stack the
// way production does (rnsstack.Build, which installs the listener before
// the node is handed to the ingress) instead of enabling the listener after
// an injected proof; its control is as supplied. [MESHSAT-1584]

type reviewA13WriteErrorTx struct {
	*linkTx
	failAfterSend bool
	packetHash    [32]byte
}

func (tx *reviewA13WriteErrorTx) Transmit(iface string, raw []byte) error {
	hdr, err := reticulum.UnmarshalHeader(raw)
	isData := err == nil && hdr.PacketType == reticulum.PacketData && hdr.DestType == reticulum.DestSingle
	if isData {
		tx.packetHash = reticulum.PacketHash(raw)
	}
	if err := tx.linkTx.Transmit(iface, raw); err != nil {
		return err
	}
	if isData && tx.failAfterSend {
		return errors.New("review fixture: write failed after bytes reached the peer")
	}
	return nil
}

func TestReviewA13WriteErrorPreservesProofCorrelation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "normal_timeout_control"
		if fail {
			name = "write_error_after_delivery"
		}
		t.Run(name, func(t *testing.T) {
			h := newIntentHarness(t)
			p := newLXMFPair(t, []string{"lora_rns"}, []string{"lora_rns"}, "lora_rns")
			tx := &reviewA13WriteErrorTx{linkTx: p.localTX, failAfterSend: fail}
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
			if fresh.NextHopInterface(p.remoteRouter.Hash()) == "" {
				t.Fatal("no known path")
			}
			p.remoteTX.mu.Lock()
			p.remoteTX.hold = isProofPacket
			p.remoteTX.mu.Unlock()
			contact := h.contact(directory.Address{Kind: directory.KindReticulum, Value: p.remoteRouter.HashHex()})
			id := h.submit(contact, "review ambiguous LXMF write", directory.StrategyPrimaryOnly)
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			h.ctx = ctx
			h.work("lxmf_0")
			h.ctx = context.Background()
			deadline = time.Now().Add(5 * time.Second)
			for p.remoteTX.heldCount() == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if p.remoteTX.heldCount() == 0 || p.received.Load() != 1 {
				t.Fatal("peer did not receive and prove the message")
			}
			p.remoteTX.mu.Lock()
			proof := append([]byte(nil), p.remoteTX.held[0][1]...)
			p.remoteTX.mu.Unlock()
			header, err := reticulum.UnmarshalHeader(proof)
			if err != nil {
				t.Fatal(err)
			}
			if err := reticulum.ValidateProof(header.Data, tx.packetHash, p.remote.Identity().SigningPublicKey()); err != nil {
				t.Fatal(err)
			}
			snapshot := h.snap(id)
			if len(snapshot.Transmissions) != 1 || snapshot.Transmissions[0].OutcomeClass != database.ClassUnknown {
				t.Fatalf("expected one unknown transmission, got %+v", snapshot.Transmissions)
			}
			var hash, key string
			if err := h.db.QueryRow(`SELECT proof_hash, proof_key FROM delivery_transmissions WHERE id = ?`, snapshot.Transmissions[0].ID).Scan(&hash, &key); err != nil {
				t.Fatal(err)
			}
			fresh.Inbound(proof, "lora_rns")
			h.esc.Heartbeat()
			h.drain()
			got := h.snap(id)
			evidence := proofEvidence(t, h, id)
			t.Logf("peer_received=%d proof_hash_len=%d proof_key_len=%d proof_rows=%d outcome=%s transmissions=%d", p.received.Load(), len(hash), len(key), len(evidence), got.Intent.Outcome, len(got.Transmissions))
			if len(hash) != 64 || len(key) != 64 || len(evidence) != 1 || got.Intent.Outcome != database.OutcomeAcceptedDestinationNode {
				t.Fatal("a valid late proof did not complete the unknown transmission")
			}
		})
	}
}

func TestReviewA13RestartIngressBeforeListener(t *testing.T) {
	for _, early := range []bool{false, true} {
		name := "listener_before_ingress_control"
		if early {
			name = "production_bootstrap_first_ingress"
		}
		t.Run(name, func(t *testing.T) {
			h := newIntentHarness(t)
			p := newLXMFPair(t, []string{"lora_rns"}, []string{"lora_rns"}, "lora_rns")
			h.useLXMF(p)
			if err := p.stack.EnableLateProofs(h.db, h.esc.Notify); err != nil {
				t.Fatal(err)
			}
			id := a13Send(t, h, p)
			h.clock.Advance(2 * time.Hour)
			h.esc.Heartbeat()
			h.drain()
			p.remoteTX.mu.Lock()
			proof := append([]byte(nil), p.remoteTX.held[0][1]...)
			p.remoteTX.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var fresh *rns.Node
			if early {
				// The production bootstrap: Build, then hand the node to the
				// ingress, then the first packet in is the proof.
				ident, err := routing.NewIdentity(h.db)
				if err != nil {
					t.Fatal(err)
				}
				st, err := rnsstack.Build(ctx, rnsstack.Config{Identity: ident, Registry: routing.NewInterfaceRegistry(), DB: h.db,
					LXMF: rnsstack.LXMFConfig{Enabled: true}})
				if err != nil {
					t.Fatal(err)
				}
				fresh = st.Node
				p.stack = st
				h.restart()
				h.useLXMF(p)
				st.SetLateProofNotify(h.esc.Notify)
			} else {
				var err error
				fresh, err = rns.New(rns.Config{Identity: p.local.Identity()}, p.localTX)
				if err != nil {
					t.Fatal(err)
				}
				p.local = fresh
				p.stack = &rnsstack.Stack{Node: fresh, LXMF: lxmf.New(fresh, lxmf.Config{Identity: fresh.Identity()})}
				h.restart()
				h.useLXMF(p)
				fresh.Start(ctx)
				if err := p.stack.EnableLateProofs(h.db, h.esc.Notify); err != nil {
					t.Fatal(err)
				}
			}
			processor := &Processor{}
			processor.SetRNSNode(fresh)
			processor.InjectReticulumPacket(append([]byte(nil), proof...), "tcp_0")
			processor.InjectReticulumPacket(append([]byte(nil), proof...), "tcp_0") // a copy: a duplicate, adds nothing
			h.esc.Heartbeat()
			h.drain()
			got := h.snap(id)
			evidence := proofEvidence(t, h, id)
			t.Logf("bootstrap=%v proof_rows=%d state=%s outcome=%s transmissions=%d", early, len(evidence), got.Intent.State, got.Intent.Outcome, len(got.Transmissions))
			if len(evidence) != 1 || got.Intent.Outcome != database.OutcomeAcceptedDestinationNode || len(got.Transmissions) != 1 {
				t.Fatal("a durable proof expectation did not survive the startup order")
			}
		})
	}
}
