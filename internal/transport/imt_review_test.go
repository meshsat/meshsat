package transport

import (
	"context"
	"errors"
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
	for _, c := range f.cmds("cancel_mo") {
		if refOf(c) == intentRef {
			t.Fatalf("the intent's MO (ref %d) was cancelled", intentRef)
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
