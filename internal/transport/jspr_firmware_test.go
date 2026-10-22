package transport

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedJSPRPort answers JSPR lines from a table, as the modem would.
type scriptedJSPRPort struct {
	mu      sync.Mutex
	written []string
	out     []byte
	answer  map[string]string // request line (without CR) -> response line
}

func (p *scriptedJSPRPort) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := strings.TrimRight(string(b), "\r")
	p.written = append(p.written, line)
	if a, ok := p.answer[line]; ok {
		p.out = append(p.out, a+"\r"...)
	}
	return len(b), nil
}

func (p *scriptedJSPRPort) Read(b []byte) (int, error) {
	for i := 0; i < 20; i++ {
		p.mu.Lock()
		if len(p.out) > 0 {
			n := copy(b, p.out)
			p.out = p.out[n:]
			p.mu.Unlock()
			return n, nil
		}
		p.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return 0, nil
}

func (p *scriptedJSPRPort) SetReadTimeout(time.Duration) error { return nil }
func (p *scriptedJSPRPort) Close() error                       { return nil }

// The firmware comes from GET firmware with the primary slot, written as the
// official library writes it, and reads as vMAJOR.MINOR.PATCH. [MESHSAT-1630]
func TestJSPRGetFirmwareReadsThePrimarySlot(t *testing.T) {
	port := &scriptedJSPRPort{answer: map[string]string{
		`GET firmware {"slot": "primary"}`: `200 firmware {"slot": "primary", "validity": 1, "version": {"major": 5, "minor": 2, "patch": 1, "build_info": "x"}, "hash": "abc"}`,
	}}
	c := newJSPRConn(port)
	c.startReader()
	defer c.stopReader()
	fw, err := c.jsprGetFirmware("primary")
	if err != nil {
		t.Fatal(err)
	}
	if fw.Slot != "primary" || fw.Version.String() != "v5.2.1" || fw.Hash != "abc" {
		t.Fatalf("firmware %+v", fw)
	}
	port.mu.Lock()
	defer port.mu.Unlock()
	if len(port.written) != 1 || port.written[0] != `GET firmware {"slot": "primary"}` {
		t.Fatalf("wrote %q", port.written)
	}
}

// A transport that has not read its firmware says so instead of answering with
// the hardware version. [MESHSAT-1630]
func TestIMTFirmwareVersionIsNotTheHardwareVersion(t *testing.T) {
	tr := &DirectIMTTransport{hwVersion: "1.0"}
	if v, err := tr.GetFirmwareVersion(t.Context()); err == nil || v != "" {
		t.Fatalf("unread firmware: %q %v", v, err)
	}
	tr.fwVersion = "v5.2.1"
	if v, err := tr.GetFirmwareVersion(t.Context()); err != nil || v != "v5.2.1" {
		t.Fatalf("firmware: %q %v", v, err)
	}
}
