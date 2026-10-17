package tetra

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The PDU tesseract sent and parallax received on 17 Oct 2026 21:22.
const (
	liveSentHex = "8202F8014D65736853617420444D4F207465737420322074657373657261637420746F20706172616C6C6178"
	liveRecvHex = liveSentHex + "0D0D37353330323336313030303030313030"
	liveHeader  = "+CTSDSR: 12,100,0,2000,0,496"
)

func TestEncodeTextMatchesTheLiveSend(t *testing.T) {
	pdu, bits := EncodeText(0xF8, "MeshSat DMO test 2 tesseract to parallax")
	if got := strings.ToUpper(hex.EncodeToString(pdu)); got != liveSentHex {
		t.Fatalf("pdu %s, want %s", got, liveSentHex)
	}
	if bits != 352 {
		t.Fatalf("bits %d, want 352", bits)
	}
}

func TestDecodeTheLiveReceive(t *testing.T) {
	r, err := ParseCTSDSR(liveHeader)
	if err != nil {
		t.Fatal(err)
	}
	if r.Service != 12 || r.From != "100" || r.To != "2000" || r.Bits != 496 {
		t.Fatalf("header %+v", r)
	}
	if err := r.DecodePDU(liveRecvHex); err != nil {
		t.Fatal(err)
	}
	if !r.IsText || r.IsReport || r.PID != PIDText || r.Ref != 0xF8 || r.Coding != CodingLatin1 {
		t.Fatalf("decoded %+v", r)
	}
	if r.Text != "MeshSat DMO test 2 tesseract to parallax" {
		t.Fatalf("text %q", r.Text)
	}
	if r.SenderID != "7530236100000100" {
		t.Fatalf("sender id %q", r.SenderID)
	}
}

func TestUTF16RoundTripWithTheRadiosASCIITail(t *testing.T) {
	text := "Γειά σου από το κιτ"
	pdu, _ := EncodeText(7, text)
	if pdu[3] != CodingUTF16BE {
		t.Fatalf("coding 0x%02x, want UTF-16", pdu[3])
	}
	for _, tail := range []string{"", "\r\r7530236100000100", "\r\r100"} {
		r := Received{}
		if err := r.DecodePDU(hex.EncodeToString(append(append([]byte{}, pdu...), tail...))); err != nil {
			t.Fatal(err)
		}
		if r.Text != text {
			t.Fatalf("tail %q: text %q", tail, r.Text)
		}
		if want := strings.TrimPrefix(tail, "\r\r"); r.SenderID != want {
			t.Fatalf("tail %q: sender id %q", tail, r.SenderID)
		}
	}
}

func TestDecodeSimpleTextGreekAndReport(t *testing.T) {
	r := Received{}
	// Simple text (PID 0x02), ISO 8859-7: "Γειά".
	if err := r.DecodePDU("0207C3E5E9DC"); err != nil {
		t.Fatal(err)
	}
	if !r.IsText || r.Text != "Γειά" {
		t.Fatalf("simple greek: %+v", r)
	}
	r = Received{}
	// SDS-REPORT (message type 1): delivery status 0x02 (consumed) for
	// reference 0x31.
	if err := r.DecodePDU("82100231"); err != nil {
		t.Fatal(err)
	}
	if !r.IsReport || r.IsText || r.Ref != 0x31 || r.DeliveryStatus != 0x02 {
		t.Fatalf("report: %+v", r)
	}
	r = Received{}
	// Not a text: another protocol (location, 0x0A) is left to the caller.
	if err := r.DecodePDU("0A0102"); err != nil || r.IsText || r.PID != 0x0A {
		t.Fatalf("binary: %+v %v", r, err)
	}
}

func TestParseAddress(t *testing.T) {
	cases := map[string]string{"2000": "2000", "tetra:2000": "2000", "00002000": "2000", "7530236100002000": "7530236100002000"}
	for in, want := range cases {
		if got, ok := ParseAddress(in); !ok || got != want {
			t.Errorf("ParseAddress(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "tetra:", "0", "16777215", "abc", "12a", "123456789012345678"} {
		if got, ok := ParseAddress(bad); ok {
			t.Errorf("ParseAddress(%q) = %q, want refused", bad, got)
		}
	}
	if SSIOf("7530236100002000") != "2000" || SSIOf("2340239200000100") != "100" {
		t.Fatal("SSIOf")
	}
}

// fakeRadio answers the PEI like the MTP3550 did on 17 Oct 2026.
type fakeRadio struct {
	toSession   *io.PipeWriter
	fromSession *io.PipeReader
	mu          sync.Mutex
	got         []string
	late        string   // sent after the next CMGS's OK
	failCMGS    int      // +CME ERROR for the next CMGS
	afterOK     []string // lines after the next CMGS's OK (a collision)
}

type pipePort struct {
	io.Reader
	io.Writer
	closeFn func() error
}

func (p pipePort) Close() error { return p.closeFn() }

func newFakeRadio(t *testing.T) (*fakeRadio, io.ReadWriteCloser) {
	sr, rw := io.Pipe() // radio -> session
	rr, sw := io.Pipe() // session -> radio
	f := &fakeRadio{toSession: rw, fromSession: rr}
	port := pipePort{Reader: sr, Writer: sw, closeFn: func() error {
		sr.Close()
		sw.Close()
		return nil
	}}
	go f.serve()
	t.Cleanup(func() { rw.Close(); rr.Close() })
	return f, port
}

func (f *fakeRadio) say(lines ...string) {
	for _, l := range lines {
		f.toSession.Write([]byte(l + "\r\n"))
	}
}

func (f *fakeRadio) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.got...)
}

func (f *fakeRadio) serve() {
	br := bufio.NewReader(f.fromSession)
	for {
		cmd, err := br.ReadString('\r')
		if err != nil {
			return
		}
		cmd = strings.TrimSpace(cmd)
		f.mu.Lock()
		f.got = append(f.got, cmd)
		f.mu.Unlock()
		switch {
		case strings.HasPrefix(cmd, "AT+CMGS=") && !strings.HasSuffix(cmd, "?"):
			data, err := br.ReadString('\x1a')
			if err != nil {
				return
			}
			f.mu.Lock()
			f.got = append(f.got, strings.TrimSpace(strings.TrimSuffix(data, "\x1a")))
			late, fail, after := f.late, f.failCMGS, f.afterOK
			f.late, f.failCMGS, f.afterOK = "", 0, nil
			f.mu.Unlock()
			if fail != 0 {
				f.say("+CME ERROR: 3")
				continue
			}
			f.say("+CMGS: 0", "", "OK")
			if late != "" {
				f.say(late)
			}
			f.say(after...)
		case cmd == "ATI":
			f.say("Manufacturer: Motorola Solutions", "Model Type: MTP3550 350-470MHz PTBP952HE", "TEI: 00016B2B2264210",
				"ITSI: 7530236100000100", "GTSI: 7530236100002000", "Radio Ver: R35.000.9426", "", "OK")
		case cmd == "AT+CTOM?":
			f.say("+CTOM: 1", "", "OK")
		case cmd == "AT+CMGS=?":
			f.say("+CMGS: (0-16777214,00000001-10231638316777214,1-255,0-999999999999999999999999),(8-1184)", "", "OK")
		case cmd == "AT+CBC?":
			f.say("+CBC: 1,100", "", "OK")
		case cmd == "AT+CTGS?":
			f.say("+CME ERROR: 3")
		default:
			f.say("OK")
		}
	}
}

func TestSessionCommandsAndIdentity(t *testing.T) {
	_, port := newFakeRadio(t)
	s := NewSession(port, nil, nil)
	defer s.Close()
	ctx := context.Background()
	id, err := s.Identify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id.ITSI != "7530236100000100" || id.GTSI != "7530236100002000" || id.Firmware != "R35.000.9426" || id.TEI != "00016B2B2264210" {
		t.Fatalf("identity %+v", id)
	}
	if m, err := s.Mode(ctx); err != nil || m != 1 {
		t.Fatalf("mode %d %v", m, err)
	}
	if b, err := s.MaxSDSBits(ctx); err != nil || b != 1184 {
		t.Fatalf("max bits %d %v", b, err)
	}
	if p, err := s.Battery(ctx); err != nil || p != 100 {
		t.Fatalf("battery %d %v", p, err)
	}
	_, err = s.Command(ctx, "AT+CTGS?", time.Second)
	var cme *CMEError
	if !errors.As(err, &cme) || cme.Code != 3 {
		t.Fatalf("CTGS? error %v, want +CME ERROR: 3", err)
	}
}

func TestSendSDSWaitsForTheReportAndKeepsTheLineQuiet(t *testing.T) {
	old := reportWait
	reportWait = 500 * time.Millisecond
	defer func() { reportWait = old }()
	f, port := newFakeRadio(t)
	var unsolicited []string
	var mu sync.Mutex
	s := NewSession(port, nil, func(l string) { mu.Lock(); unsolicited = append(unsolicited, l); mu.Unlock() })
	defer s.Close()
	ctx := context.Background()

	pdu, bits := EncodeText(0xB5, "hello")
	f.mu.Lock()
	f.late = "+CMGS: 0,4,181"
	f.mu.Unlock()
	rep, err := s.SendSDS(ctx, "2000", 0xB5, pdu, bits)
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil || rep.Status != 4 || rep.Ref != 181 {
		t.Fatalf("report %+v", rep)
	}
	got := f.commands()
	if len(got) < 3 || got[0] != "AT+CTSDS=12,0,0,0,0" || got[1] != "AT+CMGS=2000,72" || got[2] != strings.ToUpper(hex.EncodeToString(pdu)) {
		t.Fatalf("radio got %q", got)
	}

	// No report this time (the radio skipped it once live): still sent.
	start := time.Now()
	rep, err = s.SendSDS(ctx, "2000", 0xB6, pdu, bits)
	if err != nil || rep != nil {
		t.Fatalf("no report: %+v %v", rep, err)
	}
	if time.Since(start) < reportWait {
		t.Fatal("returned before reportWait without a report")
	}

	// A late +CME ERROR when no command waits is unsolicited, not the
	// next command's answer.
	f.say("+CME ERROR: 35")
	time.Sleep(100 * time.Millisecond)
	if _, err := s.Command(ctx, "AT", time.Second); err != nil {
		t.Fatalf("command after a stray error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(unsolicited) != 1 || unsolicited[0] != "+CME ERROR: 35" {
		t.Fatalf("unsolicited %q", unsolicited)
	}

	f.mu.Lock()
	f.failCMGS = 1
	f.mu.Unlock()
	_, err = s.SendSDS(ctx, "2000", 0xB7, pdu, bits)
	var cme *CMEError
	if !errors.As(err, &cme) || cme.Code != 3 {
		t.Fatalf("refused send: %v", err)
	}
}

func TestIncomingSDSWhileACommandIsInFlight(t *testing.T) {
	f, port := newFakeRadio(t)
	got := make(chan Received, 2)
	s := NewSession(port, func(r Received) { got <- r }, nil)
	defer s.Close()
	// The radio hands over an SDS between a command and its answer.
	go func() {
		time.Sleep(20 * time.Millisecond)
		f.say(liveHeader, liveRecvHex)
	}()
	if _, err := s.Command(context.Background(), "AT+CBC?", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.Text != "MeshSat DMO test 2 tesseract to parallax" || r.From != "100" {
			t.Fatalf("got %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SDS")
	}
}

func TestSessionEndsWhenThePortCloses(t *testing.T) {
	_, port := newFakeRadio(t)
	s := NewSession(port, nil, nil)
	port.Close()
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not end")
	}
	if _, err := s.Command(context.Background(), "AT", time.Second); !errors.Is(err, ErrClosed) {
		t.Fatalf("command on a closed session: %v", err)
	}
}

// The collision of 17 Oct 2026: both kits sent at once; the losing radio
// answered OK, then +CME ERROR: 42 and +CMGS: 0,5,<ref>, and nothing
// arrived. SendSDS must say so.
func TestSendSDSReportsARadioThatDidNotSend(t *testing.T) {
	old := reportWait
	reportWait = 500 * time.Millisecond
	defer func() { reportWait = old }()
	f, port := newFakeRadio(t)
	var unsolicited []string
	var mu sync.Mutex
	s := NewSession(port, nil, func(l string) { mu.Lock(); unsolicited = append(unsolicited, l); mu.Unlock() })
	defer s.Close()
	ctx := context.Background()
	pdu, bits := EncodeText(0x10, "lost in a collision")

	cases := []struct {
		after  []string
		status int
		cme    int
	}{
		{[]string{"+CME ERROR: 42", "+CMGS: 0,5,16"}, 5, 42}, // as seen live
		{[]string{"+CMGS: 0,5,16"}, 5, 0},                    // status alone
		{[]string{"+CME ERROR: 42"}, -1, 42},                 // error, no report
	}
	for _, c := range cases {
		f.mu.Lock()
		f.afterOK = c.after
		f.mu.Unlock()
		_, err := s.SendSDS(ctx, "2000", 0x10, pdu, bits)
		var ns *NotSentError
		if !errors.Is(err, ErrRadioNotSent) || !errors.As(err, &ns) || ns.Status != c.status || ns.CME != c.cme {
			t.Fatalf("after %q: %v (%+v)", c.after, err, ns)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(unsolicited) != 0 {
		t.Fatalf("a late +CME ERROR leaked to OnLine: %q", unsolicited)
	}
	// And a good send right after still goes.
	f.mu.Lock()
	f.afterOK = []string{"+CMGS: 0,4,16"}
	f.mu.Unlock()
	if rep, err := s.SendSDS(ctx, "2000", 0x10, pdu, bits); err != nil || rep == nil || rep.Status != 4 {
		t.Fatalf("good send: %+v %v", rep, err)
	}
}

func TestLinkRoundTripAndTheHashBothEndsAgreeOn(t *testing.T) {
	for _, l := range []Link{{Kind: LinkAck, Ref: 0xF8, Hash: 0xDEADBEEF}, {Kind: LinkHello, Tag: 0x01020304}, {Kind: LinkHelloReply, Tag: 7}} {
		pdu, bits := EncodeLink(l)
		if bits != len(pdu)*8 || pdu[0] != LinkPID {
			t.Fatalf("encode %+v: %x", l, pdu)
		}
		got, err := DecodeLink(pdu)
		if err != nil || got != l {
			t.Fatalf("round trip %+v -> %+v %v", l, got, err)
		}
	}
	if _, err := DecodeLink([]byte{LinkPID, 0x21, 0}); err == nil {
		t.Fatal("a version 2 PDU decoded")
	}
	// The receiver's Body (identity stripped) hashes as the sender's text.
	r, _ := ParseCTSDSR(liveHeader)
	if err := r.DecodePDU(liveRecvHex); err != nil {
		t.Fatal(err)
	}
	if TextHash(r.Body) != TextHash(BodyOf("MeshSat DMO test 2 tesseract to parallax")) {
		t.Fatal("sender and receiver hash differently")
	}
	greek := "Γειά σου"
	pdu, _ := EncodeText(9, greek)
	g := Received{}
	if err := g.DecodePDU(hex.EncodeToString(append(pdu, "\r\r100"...))); err != nil || TextHash(g.Body) != TextHash(BodyOf(greek)) {
		t.Fatalf("UTF-16 hash: %v", err)
	}
}
