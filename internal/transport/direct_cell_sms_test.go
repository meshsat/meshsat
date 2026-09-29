package transport

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"
)

// scriptedModem is a serial port with a modem in SMS text mode behind it: it
// answers AT commands with OK, AT+CMGS with the "> " prompt, and a message
// body ended by Ctrl-Z with +CMGS. It keeps every byte written to it.
type scriptedModem struct {
	serial.Port
	mu      sync.Mutex
	written []byte
	reply   []byte
	inBody  bool
}

func (m *scriptedModem) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = append(m.written, b...)
	switch {
	case m.inBody:
		if bytes.IndexByte(b, 0x1A) >= 0 {
			m.inBody = false
			m.reply = append(m.reply, "\r\n+CMGS: 7\r\n\r\nOK\r\n"...)
		}
	case bytes.HasPrefix(b, []byte("AT+CMGS=")):
		m.inBody = true
		m.reply = append(m.reply, "\r\n> "...)
	case bytes.HasPrefix(b, []byte("AT")):
		m.reply = append(m.reply, "\r\nOK\r\n"...)
	}
	return len(b), nil
}

func (m *scriptedModem) Read(b []byte) (int, error) {
	m.mu.Lock()
	if len(m.reply) > 0 {
		n := copy(b, m.reply)
		m.reply = m.reply[n:]
		m.mu.Unlock()
		return n, nil
	}
	m.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	return 0, nil
}

func (m *scriptedModem) SetReadTimeout(time.Duration) error { return nil }
func (m *scriptedModem) Close() error                       { return nil }

func (m *scriptedModem) bytesWritten() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte{}, m.written...)
}

// runScriptedCell starts a DirectCellTransport's I/O loop on a scripted modem,
// with no settle pause after a sent SMS; the loop stops with the test.
func runScriptedCell(t *testing.T) (*DirectCellTransport, *scriptedModem) {
	t.Helper()
	settle := smsSendSettle
	smsSendSettle = 0
	m := &scriptedModem{}
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
		case <-time.After(2 * time.Second):
			t.Error("the I/O loop did not stop")
		}
		smsSendSettle = settle
	})
	return tr, m
}

func sentCount(tr *DirectCellTransport) int64 {
	tr.stateMu.RLock()
	defer tr.stateMu.RUnlock()
	return tr.smsSent
}

// A text reaches the modem byte for byte after the AT+CMGS prompt, ended by
// exactly one Ctrl-Z.
func TestDirectCellSendSMS_BodyThenOneCtrlZ(t *testing.T) {
	tr, m := runScriptedCell(t)
	const text = "SOS: Anna needs help. Position 52.12345, 4.12345 (+/-12 m). Sent by MeshSat at 12:34 UTC."
	if err := tr.SendSMS(context.Background(), "+31612345678", text); err != nil {
		t.Fatalf("send: %v", err)
	}
	w := m.bytesWritten()
	cmd := []byte("AT+CMGS=\"+31612345678\"\r")
	at := bytes.Index(w, cmd)
	if at < 0 {
		t.Fatalf("no AT+CMGS in %q", w)
	}
	if body := w[at+len(cmd):]; string(body) != text+"\x1a" {
		t.Fatalf("after the prompt the modem got %q, want the text and one Ctrl-Z", body)
	}
	if n := bytes.Count(w, []byte{0x1A}); n != 1 {
		t.Fatalf("%d Ctrl-Z written, want 1: %q", n, w)
	}
	if n := sentCount(tr); n != 1 {
		t.Fatalf("sent counter %d, want 1", n)
	}
}

// Ctrl-Z in a text would end the SMS early and run the rest as AT commands;
// ESC would drop it while the modem answers OK. A quote or a line break in
// the number would end AT+CMGS itself. The transport refuses all of them
// before it writes a byte, whatever path the SMS came by, and the send fails.
func TestDirectCellSendSMS_RefusesCtrlZAndEsc(t *testing.T) {
	tr, m := runScriptedCell(t)
	for _, c := range []struct{ name, to, text string }{
		{"Ctrl-Z in the text", "+31612345678", "help\x1aAT+CFUN=0\r"},
		{"Ctrl-Z at the end", "+31612345678", "help\x1a"},
		{"ESC in the text", "+31612345678", "help\x1b is on the way"},
		{"Ctrl-Z past 160 bytes", "+31612345678", strings.Repeat("a", 170) + "\x1a"},
		{"quote in the number", "+31612345678\"\r\x1aAT+CFUN=0", "help"},
		{"line break in the number", "+31612345678\rAT+CFUN=0", "help"},
	} {
		err := tr.SendSMS(context.Background(), c.to, c.text)
		if !errors.Is(err, ErrSMSUnsafe) {
			t.Fatalf("%s: err %v, want ErrSMSUnsafe", c.name, err)
		}
		if w := m.bytesWritten(); len(w) != 0 {
			t.Fatalf("%s: %q reached the modem", c.name, w)
		}
	}
	if n := sentCount(tr); n != 0 {
		t.Fatalf("sent counter %d after refused sends", n)
	}

	// The same modem takes a clean text right after: the refusals were the
	// transport's, not a harness that writes nothing.
	if err := tr.SendSMS(context.Background(), "+31612345678", "help is on the way"); err != nil {
		t.Fatalf("clean send: %v", err)
	}
	if !bytes.HasSuffix(m.bytesWritten(), []byte("help is on the way\x1a")) {
		t.Fatalf("clean send: modem got %q", m.bytesWritten())
	}
}

// The kits' AT transport declares that it takes SMS in text mode, where only
// the GSM basic set gets through; ModemManager encodes any text and does not.
func TestSMSTextMode_ATTransportOnly(t *testing.T) {
	var cell CellTransport = NewDirectCellTransport("/dev/null")
	if m, ok := cell.(SMSTextModeModem); !ok || !m.SMSTextMode() {
		t.Fatal("the AT transport does not declare text mode")
	}
	cell = NewMMCellTransport()
	if _, ok := cell.(SMSTextModeModem); ok {
		t.Fatal("ModemManager declares text mode")
	}
}
