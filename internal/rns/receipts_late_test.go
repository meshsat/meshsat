package rns

import (
	"context"
	"sync"
	"testing"
	"time"

	"meshsat/internal/reticulum"
)

func isProof(raw []byte) bool {
	h, err := reticulum.UnmarshalHeader(raw)
	return err == nil && h.PacketType == reticulum.PacketProof
}

// A-13: a proof that comes after the sender stopped waiting (forgot the
// receipt) or after the receipt timed out goes to OnUnmatchedProof, and
// validates against the hash and key the receipt reported; a receipt still
// held takes its proof and the hook hears nothing. [MESHSAT-1584]
func TestReceipt_LateProofGoesToTheHook(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(a *Node, r *Receipt) // what the sender does before the proof comes
		hook bool
	}{
		{"held", func(*Node, *Receipt) {}, false},
		{"forgotten", func(a *Node, r *Receipt) { a.ForgetReceipt(r.Hash) }, true},
		{"timed_out", func(a *Node, r *Receipt) { a.expireReceipts(time.Now().Add(time.Hour)) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			net := &fakeNet{wires: map[string]*wire{}}
			a, _ := newTestNode(t, net, "a")
			b, btx := newTestNode(t, net, "b")
			connect(net, a, "a", "ab", b, "b", "ba")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.Start(ctx)
			b.Start(ctx)
			bDest := b.AddDestination(&Destination{Name: "test.delivery", ProveAll: true})
			if err := b.Announce(bDest, "", false); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "A learns B", func() bool { return a.Paths().Hops(bDest.Hash) == 1 })

			var mu sync.Mutex
			var unmatched []*Packet
			a.OnUnmatchedProof = func(p *Packet) { mu.Lock(); unmatched = append(unmatched, p); mu.Unlock() }
			btx.mu.Lock()
			btx.hold = isProof
			btx.mu.Unlock()
			r, err := a.SendData(bDest.Hash, []byte("late"), true)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "B proves (held)", func() bool { btx.mu.Lock(); defer btx.mu.Unlock(); return len(btx.held) == 1 })
			tc.end(a, r)
			btx.release()

			if !tc.hook {
				select {
				case res := <-r.Done():
					if !res.Delivered {
						t.Fatal("held receipt not delivered")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("held receipt got no proof")
				}
				time.Sleep(100 * time.Millisecond)
				mu.Lock()
				defer mu.Unlock()
				if len(unmatched) != 0 {
					t.Fatalf("the hook also heard the proof a receipt took")
				}
				return
			}
			waitFor(t, "the hook hears the proof", func() bool { mu.Lock(); defer mu.Unlock(); return len(unmatched) == 1 })
			mu.Lock()
			p := unmatched[0]
			mu.Unlock()
			if err := reticulum.ValidateProof(p.Hdr.Data, r.Hash, r.SigningKey()); err != nil {
				t.Fatalf("the late proof does not validate against the receipt's hash and key: %v", err)
			}
			select {
			case res := <-r.Done():
				if res.Delivered {
					t.Fatal("a forgotten or expired receipt still took the proof")
				}
			default:
			}
		})
	}
}

// The race: the sender forgets the receipt just after the proof validated
// and before it was handed over. The proof goes to the hook, never to a
// receipt nobody waits on (neither would lose it).
func TestReceipt_ForgetBetweenValidationAndHandOver(t *testing.T) {
	net := &fakeNet{wires: map[string]*wire{}}
	a, _ := newTestNode(t, net, "a")
	b, btx := newTestNode(t, net, "b")
	connect(net, a, "a", "ab", b, "b", "ba")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	b.Start(ctx)
	bDest := b.AddDestination(&Destination{Name: "test.delivery", ProveAll: true})
	if err := b.Announce(bDest, "", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A learns B", func() bool { return a.Paths().Hops(bDest.Hash) == 1 })
	hooked := make(chan struct{}, 1)
	a.OnUnmatchedProof = func(*Packet) { hooked <- struct{}{} }
	btx.mu.Lock()
	btx.hold = isProof
	btx.mu.Unlock()
	r, err := a.SendData(bDest.Hash, []byte("race"), true)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B proves (held)", func() bool { btx.mu.Lock(); defer btx.mu.Unlock(); return len(btx.held) == 1 })
	afterProofValidated = func() { a.ForgetReceipt(r.Hash) }
	defer func() { afterProofValidated = nil }()
	btx.release()
	select {
	case <-hooked:
	case <-time.After(5 * time.Second):
		t.Fatal("the proof reached neither the hook nor a waiter: lost")
	}
	select {
	case res := <-r.Done():
		t.Fatalf("a forgotten receipt took the proof: %+v", res)
	default:
	}
}
