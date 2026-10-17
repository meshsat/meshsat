package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// The IMT route review of 17 Oct 2026, its Go cases as supplied (IMT-R1,
// R2, R3); the cancel handoff case uses this package's snapshot hook in
// place of the reviewer's instrumented copy, at the same interleaving, and
// a legacy-to-intent handoff is added. [MESHSAT-1584]

func reviewAck(cmd map[string]any, supplied bool) []string {
	return []string{moLine(200, "messageOriginateStatus", map[string]any{
		"final_mo_status": "mo_ack_received", "message_id": 22,
		"topic_id": 244, "request_reference": refOf(cmd), "segment_supplied": supplied,
	})}
}

func TestReviewIMTHelperAcceptanceControl(t *testing.T) {
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string { return reviewAck(cmd, true) }}
	tr := newIMTTestTransport(t, f)
	sub, err := tr.SubmitMO(context.Background(), []byte("intent"))
	if err != nil || sub.Phase != IMTAccepted || len(f.cmds("send_mo")) != 1 {
		t.Fatalf("control: %+v, %v", sub, err)
	}
}

func TestReviewIMTHelperContradictoryAckIsNotAccepted(t *testing.T) {
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string { return reviewAck(cmd, false) }}
	tr := newIMTTestTransport(t, f)
	sub, err := tr.SubmitMO(context.Background(), []byte("intent"))
	t.Logf("tagged ack with segment_supplied=false: %+v, err=%v", sub, err)
	if sub.Phase == IMTAccepted {
		t.Fatal("an acknowledgement emitted without this payload became this submission's acceptance")
	}
}

func TestReviewIMTAlreadyCancelledDoesNotSubmit(t *testing.T) {
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string { return reviewAck(cmd, true) }}
	tr := newIMTTestTransport(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wrong := 0
	for i := 0; i < 64; i++ {
		sub, err := tr.SubmitMO(ctx, []byte("already cancelled"))
		if !errors.Is(err, context.Canceled) || sub.Phase != IMTNotSent {
			wrong++
		}
	}
	started := len(f.cmds("send_mo"))
	t.Logf("64 already-cancelled calls: send_mo commands=%d, wrong outcomes=%d", started, wrong)
	if started != 0 || wrong != 0 {
		t.Fatal("a cancelled caller started an MO on an idle modem")
	}
}

// The legacy Send follows the same rule.
func TestReviewIMTAlreadyCancelledLegacySendDoesNotSubmit(t *testing.T) {
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string { return reviewAck(cmd, true) }}
	tr := newIMTTestTransport(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 64; i++ {
		if _, err := tr.Send(ctx, []byte("already cancelled")); !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := len(f.cmds("send_mo")); n != 0 {
		t.Fatalf("%d legacy MOs started for a cancelled caller", n)
	}
}

func TestReviewIMTCancelGuardHandoff(t *testing.T) {
	checked := make(chan struct{})
	resumeCancel := make(chan struct{})
	started := make(chan struct{})
	finishMO := make(chan struct{})
	imtCancelSnapshotHook = func() { close(checked); <-resumeCancel }
	defer func() { imtCancelSnapshotHook = nil }()
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string {
		close(started)
		<-finishMO
		return reviewAck(cmd, true)
	}}
	tr := newIMTTestTransport(t, f)
	cancelDone := make(chan bool, 1)
	go func() { cancelDone <- tr.CancelMO() }()
	select {
	case <-checked:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not reach the scheduling hook")
	}
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_, _ = tr.SubmitMO(context.Background(), []byte("intent"))
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("intent did not start")
	}
	tr.mu.Lock()
	protected := tr.moCur != nil && !tr.moCur.cancellable
	tr.mu.Unlock()
	close(resumeCancel)
	var cancelled bool
	select {
	case cancelled = <-cancelDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not return")
	}
	deadline := time.Now().Add(time.Second)
	for len(f.cmds("cancel_mo")) == 0 && cancelled && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	commands := f.cmds("cancel_mo")
	close(finishMO)
	<-sendDone
	t.Logf("intent protected=%v, CancelMO=%v, cancel commands=%v", protected, cancelled, commands)
	if !protected || cancelled || len(commands) != 0 {
		t.Fatal("a cancellation whose guard preceded the intent cancelled the intent's reference")
	}
}

// A cancel decided while a legacy MO (the MT poll) ran, delayed until that
// MO ended and an intent's MO started: it may only name the legacy MO, and
// the helper port refuses it once that MO is no longer with the helper.
func TestReviewIMTCancelLegacyToIntentHandoff(t *testing.T) {
	snapped := make(chan struct{})
	resume := make(chan struct{})
	imtCancelSnapshotHook = func() { close(snapped); <-resume }
	defer func() { imtCancelSnapshotHook = nil }()
	release := map[int]chan struct{}{}
	startedRef := make(chan int, 2)
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string {
		startedRef <- refOf(cmd)
		if ch := release[refOf(cmd)]; ch != nil {
			<-ch
		}
		return reviewAck(cmd, true)
	}}
	tr := newIMTTestTransport(t, f)
	tr.mu.Lock()
	legacyRef := tr.conn.moRef + 1
	if legacyRef > 100 {
		legacyRef = 1
	}
	tr.mu.Unlock()
	finishLegacy := make(chan struct{})
	release[legacyRef] = finishLegacy

	legacyDone := make(chan struct{})
	go func() { defer close(legacyDone); _, _ = tr.Send(context.Background(), []byte("poll")) }()
	if got := <-startedRef; got != legacyRef {
		t.Fatalf("legacy MO ref %d, want %d", got, legacyRef)
	}
	cancelDone := make(chan bool, 1)
	go func() { cancelDone <- tr.CancelMO() }() // snapshot: the legacy MO
	<-snapped
	close(finishLegacy)
	<-legacyDone
	intentDone := make(chan IMTSubmit, 1)
	go func() { s, _ := tr.SubmitMO(context.Background(), []byte("intent")); intentDone <- s }()
	intentRef := <-startedRef
	close(resume)
	cancelled := <-cancelDone
	<-intentDone
	sends := f.cmds("send_mo")
	if len(sends) != 2 || opOf(sends[0]) == "" || opOf(sends[0]) == opOf(sends[1]) {
		t.Fatalf("each MO needs its own op token: %v", sends)
	}
	for _, c := range f.cmds("cancel_mo") {
		if opOf(c) == opOf(sends[1]) {
			t.Fatalf("the intent's MO (ref %d, op %s) was cancelled", intentRef, opOf(c))
		}
	}
	t.Logf("legacy ref %d, intent ref %d, late cancel written: %v, commands %v", legacyRef, intentRef, cancelled, f.cmds("cancel_mo"))
	if cancelled {
		t.Fatal("a cancel for a finished legacy MO was written while the intent's MO ran")
	}
}

// A caller cancelled while it waited for the modem gives the modem back
// and sends nothing, intent or legacy: the permit was handed over first,
// then the cancel, before the caller ran. [review IMT-R3]
func TestIMT_CancelledWhileQueuedDoesNotSubmit(t *testing.T) {
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string { return reviewAck(cmd, true) }}
	tr := newIMTTestTransport(t, f)
	for _, legacy := range []bool{false, true} {
		for i := 0; i < 32; i++ {
			hold, err := tr.acquireMO(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				if legacy {
					_, err := tr.Send(ctx, []byte("queued"))
					done <- err
					return
				}
				_, err := tr.SubmitMO(ctx, []byte("queued"))
				done <- err
			}()
			time.Sleep(5 * time.Millisecond) // parked on the permit
			hold()
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("legacy=%v call %d: %v", legacy, i, err)
			}
		}
	}
	if n := len(f.cmds("send_mo")); n != 0 {
		t.Fatalf("%d MOs started for callers cancelled while queued", n)
	}
}

func opOf(cmd map[string]any) string { v, _ := cmd["op"].(string); return v }

// The cumulative review's counterexample (17 Oct 2026, IMT-R2 residual), as
// supplied: a cancel decided for a legacy MO and delayed across a whole
// cycle of references, until the protected intent's MO carries the same
// reference. It names the legacy MO's op token, which is never reused, so
// it cancels nothing.
func TestCumulativeIMTCancelAfterReferenceWrap(t *testing.T) {
	snapped := make(chan struct{})
	resume := make(chan struct{})
	imtCancelSnapshotHook = func() { close(snapped); <-resume }
	defer func() { imtCancelSnapshotHook = nil }()
	legacyStarted := make(chan int, 1)
	intentStarted := make(chan int, 1)
	finishLegacy := make(chan struct{})
	finishIntent := make(chan struct{})
	var count atomic.Int32
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string {
		n := count.Add(1)
		if n == 1 {
			legacyStarted <- refOf(cmd)
			<-finishLegacy
		}
		if n == 101 {
			intentStarted <- refOf(cmd)
			<-finishIntent
		}
		return reviewAck(cmd, true)
	}}
	tr := newIMTTestTransport(t, f)
	legacyDone := make(chan struct{})
	go func() { defer close(legacyDone); _, _ = tr.Send(context.Background(), []byte("legacy")) }()
	oldRef := <-legacyStarted
	cancelDone := make(chan bool, 1)
	go func() { cancelDone <- tr.CancelMO() }()
	<-snapped
	close(finishLegacy)
	<-legacyDone
	for i := 0; i < 99; i++ {
		s, err := tr.SubmitMO(context.Background(), []byte("intervening"))
		if err != nil || s.Phase != IMTAccepted {
			t.Fatalf("MO %d: %+v %v", i, s, err)
		}
	}
	intentDone := make(chan struct{})
	go func() { defer close(intentDone); _, _ = tr.SubmitMO(context.Background(), []byte("protected")) }()
	newRef := <-intentStarted
	tr.mu.Lock()
	protected := tr.moCur != nil && !tr.moCur.cancellable
	tr.mu.Unlock()
	close(resume)
	cancelled := <-cancelDone
	deadline := time.Now().Add(time.Second)
	for len(f.cmds("cancel_mo")) == 0 && cancelled && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	commands := f.cmds("cancel_mo")
	close(finishIntent)
	<-intentDone
	t.Logf("legacy ref=%d, new intent ref=%d, protected=%v, CancelMO=%v, commands=%v", oldRef, newRef, protected, cancelled, commands)
	if oldRef != newRef || !protected {
		t.Fatal("reference-wrap precondition missing")
	}
	if cancelled || len(commands) != 0 {
		t.Fatal("a late legacy cancel targeted a protected intent after reference reuse")
	}
}

// The same cycle with a cancellable legacy MO at the far end: the late
// cancel still names only the first MO, so the later legacy MO with the
// same reference runs to its own result; a cancel decided for that MO
// itself still reaches it. [review IMT-R2]
func TestIMT_LateCancelSparesALaterLegacyMOWithTheSameReference(t *testing.T) {
	snapped := make(chan struct{})
	resume := make(chan struct{})
	var hookOnce atomic.Bool
	imtCancelSnapshotHook = func() {
		if hookOnce.CompareAndSwap(false, true) {
			close(snapped)
			<-resume
		}
	}
	defer func() { imtCancelSnapshotHook = nil }()
	firstStarted := make(chan int, 1)
	lastStarted := make(chan int, 1)
	finishFirst := make(chan struct{})
	finishLast := make(chan struct{})
	var count atomic.Int32
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string {
		n := count.Add(1)
		if n == 1 {
			firstStarted <- refOf(cmd)
			<-finishFirst
		}
		if n == 101 {
			lastStarted <- refOf(cmd)
			<-finishLast
		}
		return reviewAck(cmd, true)
	}}
	tr := newIMTTestTransport(t, f)
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); _, _ = tr.Send(context.Background(), []byte("first")) }()
	oldRef := <-firstStarted
	cancelDone := make(chan bool, 1)
	go func() { cancelDone <- tr.CancelMO() }()
	<-snapped
	close(finishFirst)
	<-firstDone
	for i := 0; i < 99; i++ {
		if _, err := tr.Send(context.Background(), []byte("intervening")); err != nil {
			t.Fatalf("MO %d: %v", i, err)
		}
	}
	lastDone := make(chan struct{})
	go func() { defer close(lastDone); _, _ = tr.Send(context.Background(), []byte("last")) }()
	newRef := <-lastStarted
	close(resume)
	if <-cancelDone {
		t.Fatal("the late cancel was written while a later MO with the same reference ran")
	}
	if n := len(f.cmds("cancel_mo")); n != 0 {
		t.Fatalf("%d cancel commands for a finished MO", n)
	}
	// Control: a cancel decided now names the MO with the modem.
	if !tr.CancelMO() {
		t.Fatal("a cancel decided for the MO in flight was refused")
	}
	sends := f.cmds("send_mo")
	cancels := f.cmds("cancel_mo")
	close(finishLast)
	<-lastDone
	if oldRef != newRef || len(cancels) != 1 || opOf(cancels[0]) != opOf(sends[len(sends)-1]) {
		t.Fatalf("refs %d/%d, cancels %v", oldRef, newRef, cancels)
	}
}
