package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"
)

// SubmitSMS for the delivery intents: how far one SMS got. [MESHSAT-1584]

// submitModem answers AT with OK, AT+CMGS with the prompt (or a read error
// before it), and the body's Ctrl-Z with afterBody, or a read error.
type submitModem struct {
	serial.Port
	mu         sync.Mutex
	written    []byte
	reply      []byte
	inBody     bool
	afterBody  string // "" = the port fails after the body
	failPrompt bool
	// silentPrompt: the modem never answers AT+CMGS, and the port stays up.
	silentPrompt bool
	readErr      bool
	// The text contract: cscs is the character set the modem reports
	// ("" = IRA), cscsRefuse makes it refuse AT+CSCS=, cscsStuck keeps the
	// reported set whatever is written; dcs is AT+CSMP?'s coding.
	cscs       string
	cscsRefuse bool
	cscsStuck  bool
	dcs        int
}

func (m *submitModem) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = append(m.written, b...)
	switch {
	case m.inBody:
		if bytes.IndexByte(b, 0x1A) >= 0 {
			m.inBody = false
			if m.afterBody == "" {
				m.readErr = true
			} else {
				m.reply = append(m.reply, m.afterBody...)
			}
		}
	case bytes.HasPrefix(b, []byte("AT+CMGS=")):
		if m.failPrompt {
			m.readErr = true
		} else if m.silentPrompt {
			// No "> " and no error: the wait runs to its deadline.
		} else {
			m.inBody = true
			m.reply = append(m.reply, "\r\n> "...)
		}
	case bytes.HasPrefix(b, []byte("AT+CSCS?")):
		cs := m.cscs
		if cs == "" {
			cs = "IRA"
		}
		m.reply = append(m.reply, "\r\n+CSCS: \""+cs+"\"\r\n\r\nOK\r\n"...)
	case bytes.HasPrefix(b, []byte("AT+CSCS=")):
		if m.cscsRefuse {
			m.reply = append(m.reply, "\r\nERROR\r\n"...)
			break
		}
		if !m.cscsStuck {
			m.cscs = strings.Trim(strings.TrimSpace(string(b[len("AT+CSCS="):])), `"`)
		}
		m.reply = append(m.reply, "\r\nOK\r\n"...)
	case bytes.HasPrefix(b, []byte("AT+CSMP?")):
		m.reply = append(m.reply, fmt.Sprintf("\r\n+CSMP: 17,167,0,%d\r\n\r\nOK\r\n", m.dcs)...)
	case bytes.HasPrefix(b, []byte("AT")):
		m.reply = append(m.reply, "\r\nOK\r\n"...)
	}
	return len(b), nil
}

func (m *submitModem) Read(b []byte) (int, error) {
	m.mu.Lock()
	if len(m.reply) > 0 {
		n := copy(b, m.reply)
		m.reply = m.reply[n:]
		m.mu.Unlock()
		return n, nil
	}
	fail := m.readErr
	m.mu.Unlock()
	if fail {
		return 0, errors.New("test: port gone")
	}
	time.Sleep(5 * time.Millisecond)
	return 0, nil
}

func (m *submitModem) SetReadTimeout(time.Duration) error { return nil }
func (m *submitModem) Close() error                       { return nil }
func (m *submitModem) wrote() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte{}, m.written...)
}

func runSubmitCell(t *testing.T, m *submitModem) *DirectCellTransport {
	t.Helper()
	settle := smsSendSettle
	smsSendSettle = 0
	tr := NewDirectCellTransport("/dev/null")
	tr.mu.Lock()
	tr.file = m
	tr.running = true
	tr.cmdCh = make(chan atCommand, 16)
	tr.stopCh = make(chan struct{})
	tr.ioDone = make(chan struct{})
	tr.sess = &cellSession{cmdCh: tr.cmdCh, stop: tr.stopCh, done: tr.ioDone, file: m}
	tr.mu.Unlock()
	go tr.ioLoop()
	t.Cleanup(func() {
		close(tr.stopCh)
		select {
		case <-tr.ioDone:
		case <-time.After(3 * time.Second):
			t.Error("the I/O loop did not stop")
		}
		smsSendSettle = settle
	})
	return tr
}

func TestSubmitSMS_Phases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		m     *submitModem
		phase SMSPhase
		ref   int64
	}{
		{"accepted", &submitModem{afterBody: "\r\n+CMGS: 42\r\n\r\nOK\r\n"}, SMSAccepted, 42},
		{"refused", &submitModem{afterBody: "\r\n+CMS ERROR: 500\r\n"}, SMSRefused, 0},
		{"handed_over_no_answer", &submitModem{}, SMSHandedOver, 0},
		{"no_prompt", &submitModem{failPrompt: true}, SMSNotSent, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := runSubmitCell(t, tc.m)
			res, err := tr.SubmitSMS(context.Background(), "+31612345678", "intent text")
			if res.Phase != tc.phase || res.Ref != tc.ref {
				t.Fatalf("phase %d ref %d (err %v); want %d ref %d", res.Phase, res.Ref, err, tc.phase, tc.ref)
			}
			if (tc.phase == SMSAccepted) != (err == nil) {
				t.Fatalf("err %v with phase %d", err, res.Phase)
			}
		})
	}
}

// Never cut: a text over 160 characters is refused before a byte is written.
func TestSubmitSMS_RefusesOverlongText(t *testing.T) {
	m := &submitModem{afterBody: "\r\n+CMGS: 1\r\n"}
	tr := runSubmitCell(t, m)
	res, err := tr.SubmitSMS(context.Background(), "+31612345678", strings.Repeat("a", 161))
	if res.Phase != SMSNotSent || !errors.Is(err, ErrPayloadTooLong) || len(m.wrote()) != 0 {
		t.Fatalf("phase %d err %v, %d bytes written", res.Phase, err, len(m.wrote()))
	}
}

// Queued behind a busy command, the caller gives up: the SMS is not sent and
// never will be; when the loop frees up it writes nothing for it.
func TestSubmitSMS_AbandonedWhileQueuedNeverSends(t *testing.T) {
	m := &submitModem{afterBody: "\r\n+CMGS: 1\r\n"}
	tr := runSubmitCell(t, m)
	prevT, prevG := smsSubmitTimeout, cmdResponseGrace
	smsSubmitTimeout, cmdResponseGrace = 200*time.Millisecond, 50*time.Millisecond
	defer func() { smsSubmitTimeout, cmdResponseGrace = prevT, prevG }()
	release := make(chan struct{})
	go func() {
		_, _ = tr.execRawFn(func(serial.Port) (string, error) { <-release; return "", nil }, time.Minute)
	}()
	time.Sleep(50 * time.Millisecond) // the blocking command is running
	res, err := tr.SubmitSMS(context.Background(), "+31612345678", "behind a busy one")
	if res.Phase != SMSNotSent || err == nil {
		t.Fatalf("phase %d err %v; want not sent", res.Phase, err)
	}
	close(release)
	time.Sleep(200 * time.Millisecond) // the loop runs the queued command now
	if bytes.Contains(m.wrote(), []byte("AT+CMGS")) {
		t.Fatalf("the abandoned SMS was sent after all: %q", m.wrote())
	}
}
