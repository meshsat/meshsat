package transport

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"go.bug.st/serial"
)

type reviewSMSPort struct {
	serial.Port
	failAfter   bool
	failBefore  bool
	bodyEntered chan struct{}
	bodyRelease chan struct{}
}

func (p *reviewSMSPort) Write(b []byte) (int, error) {
	if bytes.Equal(b, []byte("review body")) && p.bodyEntered != nil {
		close(p.bodyEntered)
		<-p.bodyRelease
	}
	if bytes.Equal(b, []byte{0x1a}) && p.failBefore {
		return 0, errors.New("review: failed before Ctrl-Z")
	}
	n, err := p.Port.Write(b)
	if bytes.Equal(b, []byte{0x1a}) && p.failAfter {
		return n, errors.New("review: failed after Ctrl-Z reached modem")
	}
	return n, err
}

func reviewRunSMSPort(t *testing.T, p serial.Port) *DirectCellTransport {
	t.Helper()
	settle := smsSendSettle
	smsSendSettle = 0
	tr := NewDirectCellTransport("/dev/null")
	tr.file = p
	tr.running = true
	tr.cmdCh = make(chan atCommand, 16)
	tr.stopCh = make(chan struct{})
	tr.ioDone = make(chan struct{})
	tr.sess = &cellSession{cmdCh: tr.cmdCh, stop: tr.stopCh, done: tr.ioDone, file: p}
	go tr.ioLoop()
	t.Cleanup(func() {
		close(tr.stopCh)
		select {
		case <-tr.ioDone:
		case <-time.After(3 * time.Second):
			t.Error("I/O loop did not stop")
		}
		smsSendSettle = settle
	})
	return tr
}

func TestReviewB9SMSWriteBoundary(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after bool
		want          SMSPhase
		wire          bool
	}{
		{"accepted_control", false, false, SMSAccepted, true},
		{"prewrite_failure_control", true, false, SMSNotSent, false},
		{"completed_write_then_error", false, true, SMSHandedOver, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &submitModem{afterBody: "\r\n+CMGS: 42\r\n\r\nOK\r\n"}
			tr := reviewRunSMSPort(t, &reviewSMSPort{Port: m, failBefore: tc.before, failAfter: tc.after})
			got, err := tr.SubmitSMS(context.Background(), "+15550000000", "review body")
			wire := bytes.Contains(m.wrote(), []byte{0x1a})
			t.Logf("phase=%d err=%v ctrl_z_reached_modem=%v", got.Phase, err, wire)
			if wire != tc.wire {
				t.Fatalf("wire=%v; want %v", wire, tc.wire)
			}
			if got.Phase != tc.want {
				t.Fatalf("phase=%d; want %d", got.Phase, tc.want)
			}
		})
	}
}

func TestReviewB9SMSStartedCommandOutlivesCaller(t *testing.T) {
	m := &submitModem{afterBody: "\r\n+CMGS: 42\r\n\r\nOK\r\n"}
	p := &reviewSMSPort{Port: m, bodyEntered: make(chan struct{}), bodyRelease: make(chan struct{})}
	tr := reviewRunSMSPort(t, p)
	prevT, prevG := smsSubmitTimeout, cmdResponseGrace
	smsSubmitTimeout, cmdResponseGrace = time.Second, 50*time.Millisecond
	t.Cleanup(func() { smsSubmitTimeout, cmdResponseGrace = prevT, prevG })
	firstStarted, firstRelease, firstDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = tr.execRawFn(func(serial.Port) (string, error) {
			close(firstStarted)
			<-firstRelease
			return "", nil
		}, 10*time.Second)
		close(firstDone)
	}()
	<-firstStarted
	type result struct {
		submit SMSSubmit
		err    error
	}
	returned := make(chan result, 1)
	accepted := make(chan struct{}, 1)
	tr.SetSentHook(func(string, string) { accepted <- struct{}{} })
	go func() {
		s, e := tr.SubmitSMS(context.Background(), "+15550000000", "review body")
		returned <- result{s, e}
	}()
	deadline := time.Now().Add(time.Second)
	for len(tr.cmdCh) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	close(firstRelease)
	<-firstDone
	select {
	case <-p.bodyEntered:
	case <-time.After(2 * time.Second):
		close(p.bodyRelease)
		t.Fatal("SMS never reached body write")
	}
	var got result
	select {
	case got = <-returned:
	case <-time.After(3 * time.Second):
		close(p.bodyRelease)
		t.Fatal("caller did not time out")
	}
	before := bytes.Contains(m.wrote(), []byte{0x1a})
	close(p.bodyRelease)
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not complete after caller returned")
	}
	after := bytes.Contains(m.wrote(), []byte{0x1a})
	t.Logf("returned_phase=%d err=%v ctrl_z_before_return=%v ctrl_z_after_return=%v", got.submit.Phase, got.err, before, after)
	if !after || before {
		t.Fatal("invalid boundary setup")
	}
	if got.submit.Phase == SMSNotSent {
		t.Fatal("returned definitive not-sent while the active command later sent the SMS")
	}
}

func TestReviewB9SMSAcceptanceRequiresCMGS(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		accepted       bool
	}{
		{"cmgs_control", "\r\n+CMGS: 42\r\n\r\nOK\r\n", true},
		{"bare_ok", "\r\nOK\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &submitModem{afterBody: tc.response}
			tr := reviewRunSMSPort(t, m)
			got, err := tr.SubmitSMS(context.Background(), "+15550000000", "review body")
			t.Logf("phase=%d ref=%d err=%v has_cmgs=%v", got.Phase, got.Ref, err, tc.accepted)
			if (got.Phase == SMSAccepted) != tc.accepted {
				t.Fatal("carrier acceptance was asserted without a +CMGS reference")
			}
		})
	}
}
