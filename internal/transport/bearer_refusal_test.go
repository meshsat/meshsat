package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.bug.st/serial"
)

// What the hop set may take as the bearer's own refusal (R50-V1): only the
// modem's own failure or answer, never a check this side made first, a
// command that never started or a caller that gave up. [MESHSAT-1650]

// The SBD modem failing its load is marked ErrModemFailed as well as
// ErrNotSubmitted; a refusal before the modem is ErrNotSubmitted alone.
func TestSend_ModemFailureIsMarked(t *testing.T) {
	for _, s := range []queuedSend{{"queued data", true}, {"queued text", false}} {
		e := newSBDEmu()
		e.sbdsxLive, e.gssLive, e.loadLost = true, true, true
		tr := emuTransport(t, e)
		o := await(t, s.start(tr), "the send")
		if !errors.Is(o.err, ErrNotSubmitted) || !errors.Is(o.err, ErrModemFailed) {
			t.Fatalf("binary %v: the modem failed the load: %v, want not submitted and the modem's failure", s.binary, o.err)
		}
	}
	tr := emuTransport(t, newSBDEmu())
	for name, send := range map[string]func() error{
		"empty":         func() error { _, err := tr.Send(context.Background(), nil); return err },
		"too large":     func() error { _, err := tr.Send(context.Background(), make([]byte, 341)); return err },
		"text too long": func() error { _, err := tr.SendText(context.Background(), strings.Repeat("a", 121)); return err },
		"line breaks":   func() error { _, err := tr.SendText(context.Background(), "a\nb"); return err },
	} {
		if err := send(); !errors.Is(err, ErrNotSubmitted) || errors.Is(err, ErrModemFailed) {
			t.Errorf("%s: %v, want not submitted and not the modem's failure", name, err)
		}
	}
}

// imt_local_cancellation_is_not_radio_evidence (transport half): a send its
// caller cancels before the handoff comes back not sent with the caller's
// error and no modem status; the modem's own cancel before transit carries
// its status.
func TestIMT_LocalCancellationIsNotRadioEvidence(t *testing.T) {
	f := &fakeIMTHelper{answer: func(cmd map[string]any) []string {
		return []string{moLine(200, "messageOriginateStatus", map[string]any{"final_mo_status": "message_cancelled_pre_transit", "message_id": 3, "request_reference": refOf(cmd)})}
	}}
	tr := newIMTTestTransport(t, f)
	release, err := tr.acquireMO(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	sub, err := tr.SubmitMO(ctx, []byte("x"))
	release()
	if sub.Phase != IMTNotSent || !errors.Is(err, context.Canceled) || sub.ModemAnswered() {
		t.Fatalf("cancelled by its caller: %+v, %v; want not sent, the caller's error, no modem status", sub, err)
	}
	sub, err = tr.SubmitMO(context.Background(), []byte("x"))
	if sub.Phase != IMTNotSent || err != nil || !sub.ModemAnswered() {
		t.Fatalf("the modem's own cancel: %+v, %v; want not sent with its status", sub, err)
	}
	for _, s := range []string{"no_response", "helper_timeout", ""} {
		if (IMTSubmit{Status: s}).ModemAnswered() {
			t.Errorf("%q counted as the modem's own status", s)
		}
	}
}

// sms_local_queue_failure_is_not_radio_evidence (transport half): an SMS
// that never started (behind a busy command, given up) or was refused
// before the modem is not the modem's failure; one whose prompt never came
// is, and the network's refusal is not.
func TestSubmitSMS_LocalQueueFailureIsNotRadioEvidence(t *testing.T) {
	m := &submitModem{afterBody: "\r\n+CMGS: 1\r\n"}
	tr := runSubmitCell(t, m)
	prevT, prevG := smsSubmitTimeout, cmdResponseGrace
	smsSubmitTimeout, cmdResponseGrace = 200*time.Millisecond, 50*time.Millisecond
	defer func() { smsSubmitTimeout, cmdResponseGrace = prevT, prevG }()
	release := make(chan struct{})
	go func() {
		_, _ = tr.execRawFn(func(serial.Port) (string, error) { <-release; return "", nil }, time.Minute)
	}()
	time.Sleep(50 * time.Millisecond)
	res, err := tr.SubmitSMS(context.Background(), "+31612345678", "behind a busy one")
	close(release)
	if res.Phase != SMSNotSent || err == nil || errors.Is(err, ErrModemFailed) {
		t.Fatalf("never started: phase %d, %v; want not sent and not the modem's failure", res.Phase, err)
	}
	if _, err := tr.SubmitSMS(context.Background(), "+31612345678", strings.Repeat("a", 161)); err == nil || errors.Is(err, ErrModemFailed) {
		t.Fatalf("too long: %v; want refused before the modem", err)
	}
	for _, tc := range []struct {
		name  string
		m     *submitModem
		modem bool
	}{
		{"no prompt", &submitModem{failPrompt: true}, true},
		{"the network refused it", &submitModem{afterBody: "\r\n+CMS ERROR: 500\r\n"}, false},
	} {
		tr := runSubmitCell(t, tc.m)
		if _, err := tr.SubmitSMS(context.Background(), "+31612345678", "x"); errors.Is(err, ErrModemFailed) != tc.modem {
			t.Errorf("%s: %v; the modem's failure %v, want %v", tc.name, err, errors.Is(err, ErrModemFailed), tc.modem)
		}
	}
}

// sms_prompt_timeout_is_radio_evidence: a modem that never gives AT+CMGS
// its prompt, with the port up, runs the wait to its deadline: not sent,
// marked as the modem's failure. This is the deadline's branch, not the
// prompt's read failure above (R51-T1).
func TestSubmitSMS_PromptTimeoutIsRadioEvidence(t *testing.T) {
	prev := smsPromptTimeout
	smsPromptTimeout = 200 * time.Millisecond
	defer func() { smsPromptTimeout = prev }()
	m := &submitModem{silentPrompt: true}
	tr := runSubmitCell(t, m)
	start := time.Now()
	res, err := tr.SubmitSMS(context.Background(), "+31612345678", "x")
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the wait took %s; want the prompt's deadline (%s), not a fixed one", took, smsPromptTimeout)
	}
	if res.Phase != SMSNotSent || err == nil || !strings.Contains(err.Error(), "timeout waiting for > prompt") || strings.Contains(err.Error(), "read failed") {
		t.Fatalf("phase %d, %v; want not sent at the prompt's deadline", res.Phase, err)
	}
	if !errors.Is(err, ErrModemFailed) {
		t.Fatalf("%v: the prompt's timeout is not marked as the modem's failure", err)
	}
}
