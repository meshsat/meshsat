package transport

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"meshsat/internal/smpp"
)

// MESHSAT-1737: cell broadcast pages, data coding schemes, the AT+CSCB
// form the modem takes, and the severity table.

// cbsPDU builds one page as 3GPP TS 23.041 9.4.1.2 lays it out: serial
// number, message identifier, DCS, page parameter, 82 octets of user data
// padded with CR.
func cbsPDU(t *testing.T, serial, mid int, dcs byte, page, pages int, text string) []byte {
	t.Helper()
	var content []byte
	switch decodeCBSDCS(dcs).charset {
	case "ucs2":
		content = smpp.EncodeUCS2(text)
		for len(content) < cbsPageOctets {
			content = append(content, 0x00, 0x0D)
		}
	default:
		sept, err := smpp.EncodeGSM7(text)
		if err != nil {
			t.Fatal(err)
		}
		for len(sept) < cbsPageSeptets {
			sept = append(sept, 0x0D)
		}
		content = smpp.PackSeptets(sept)
	}
	if len(content) != cbsPageOctets {
		t.Fatalf("page is %d octets, want %d", len(content), cbsPageOctets)
	}
	pdu := []byte{byte(serial >> 8), byte(serial), byte(mid >> 8), byte(mid), dcs, byte(page<<4 | pages)}
	return append(pdu, content...)
}

// The septet packer against the classic vector, so the fixtures built
// with it are trustworthy.
func TestSeptetPacking(t *testing.T) {
	sept, _ := smpp.EncodeGSM7("hellohello")
	packed := smpp.PackSeptets(sept)
	if got := strings.ToUpper(hex.EncodeToString(packed)); got != "E8329BFD4697D9EC37" {
		t.Fatalf("packed %s", got)
	}
	if back := smpp.DecodeGSM7(smpp.UnpackSeptets(packed, 10)); back != "hellohello" {
		t.Fatalf("unpacked %q", back)
	}
}

// A GSM 7-bit page in PDU mode: the DCS names Dutch, 919 is NL-Alert.
func TestCBSPDUGSM7Page(t *testing.T) {
	text := "NL-Alert 28-10-2026 12:00 Testbericht: dit is een test van NL-Alert. Zie www.nl-alert.nl"
	pdu := cbsPDU(t, 0x4A10, 919, 0x05, 1, 1, text)
	m := parseCBM("+CBM: 88", hex.EncodeToString(pdu))
	if m == nil {
		t.Fatal("not parsed")
	}
	if m.SerialNumber != 0x4A10 || m.MessageID != 919 || m.Channel != 919 || m.Severity != "alert" || m.DCS != 5 ||
		m.Charset != "gsm7" || m.Language != "nl" || m.Page != 1 || m.Pages != 1 {
		t.Fatalf("%+v", m)
	}
	if m.Text != text {
		t.Fatalf("text %q", m.Text)
	}
	// The same page with a language prefix (DCS 0x10): "nl\r" ahead of the words.
	pdu = cbsPDU(t, 0x4A10, 919, 0x10, 1, 1, "nl\r"+text[:80])
	m = parseCBM("+CBM: 88", hex.EncodeToString(pdu))
	if m == nil || m.Language != "nl" || m.Text != text[:80] {
		t.Fatalf("prefix: %+v", m)
	}
}

// A UCS-2 message of two pages in PDU mode, the pages delivered last
// first: one message with the whole text.
func TestCBSPDUUCS2TwoPages(t *testing.T) {
	text := "Δοκιμή MeshSat: μήνυμα δύο σελίδων στο κανάλι 4380. Δεν είναι επίσημη ειδοποίηση."
	units := []rune(text)
	if len(units) <= 41 || len(units) > 82 {
		t.Fatalf("the fixture must take two pages, has %d characters", len(units))
	}
	p1 := cbsPDU(t, 0x4A21, 4380, 0x48, 1, 2, string(units[:41]))
	p2 := cbsPDU(t, 0x4A21, 4380, 0x48, 2, 2, string(units[41:]))
	var got []*CellBroadcastMsg
	a := newCBSAssembler(time.Minute, func(m *CellBroadcastMsg) { got = append(got, m) })
	a.Add(parseCBM("+CBM: 88", hex.EncodeToString(p2)))
	if len(got) != 0 {
		t.Fatal("a message went on with a page missing")
	}
	a.Add(parseCBM("+CBM: 88", hex.EncodeToString(p1)))
	if len(got) != 1 {
		t.Fatalf("%d messages", len(got))
	}
	m := got[0]
	if m.Text != text || !m.Complete || m.Pages != 2 || m.PagesReceived != 2 || m.Charset != "ucs2" || m.Severity != "test" || m.DCS != 0x48 {
		t.Fatalf("%+v", m)
	}
	// The repetitions the cell sends for minutes are not stored again.
	a.Add(parseCBM("+CBM: 88", hex.EncodeToString(p1)))
	a.Add(parseCBM("+CBM: 88", hex.EncodeToString(p2)))
	if len(got) != 1 {
		t.Fatal("a repetition went on again")
	}
	// An Update has a new serial number (update number 2) and goes on.
	u1 := cbsPDU(t, 0x4A22, 4380, 0x48, 1, 2, string(units[:41]))
	u2 := cbsPDU(t, 0x4A22, 4380, 0x48, 2, 2, "ΕΝΗΜΕΡΩΣΗ")
	a.Add(parseCBM("+CBM: 88", hex.EncodeToString(u1)))
	a.Add(parseCBM("+CBM: 88", hex.EncodeToString(u2)))
	if len(got) != 2 || !strings.HasSuffix(got[1].Text, "ΕΝΗΜΕΡΩΣΗ") {
		t.Fatalf("update: %+v", got)
	}
}

// Text mode: the header's five fields, the modem's decoded line, a UCS-2
// line the modem hands over in hex, the DCS languages.
func TestParseCBMTextMode(t *testing.T) {
	m := parseCBM("+CBM: 18960,3471,5,1,1", "NL-Alert testbericht\r\n")
	if m == nil || m.Severity != "alert" || m.Language != "nl" || m.Text != "NL-Alert testbericht" || m.Page != 1 || m.Pages != 1 {
		t.Fatalf("%+v", m)
	}
	ucs := hex.EncodeToString(smpp.EncodeUCS2("Προσοχή: δοκιμή"))
	m = parseCBM("+CBM: 18977,4371,72,1,1", strings.ToUpper(ucs))
	if m == nil || m.Charset != "ucs2" || m.Text != "Προσοχή: δοκιμή" || m.Severity != "extreme" || m.Language != "" {
		t.Fatalf("ucs2 hex: %+v", m)
	}
	m = parseCBM("+CBM: 1,4379,16,2,3", "en\rAMBER Alert page two")
	if m == nil || m.Language != "en" || m.Text != "AMBER Alert page two" || m.Page != 2 || m.Pages != 3 || m.Severity != "amber" {
		t.Fatalf("prefix: %+v", m)
	}
	// A page parameter of 0 reads as 1 of 1; three fields are enough.
	m = parseCBM("+CBM: 7,50,1,0,0", "hello")
	if m == nil || m.Page != 1 || m.Pages != 1 || m.Severity != "unknown" {
		t.Fatalf("zero pages: %+v", m)
	}
	if parseCBM("+CBM: 7", "zz") != nil || parseCBM("+CBM: 7,8", "x") != nil || parseCBM("+CMTI: 1", "x") != nil {
		t.Fatal("garbage parsed")
	}
	for dcs, want := range map[byte]cbsCoding{
		0x00: {charset: "gsm7", language: "de"}, 0x01: {charset: "gsm7", language: "en"}, 0x0B: {charset: "gsm7", language: "el"},
		0x0F: {charset: "gsm7"}, 0x10: {charset: "gsm7", prefix: true}, 0x11: {charset: "ucs2", prefix: true},
		0x20: {charset: "gsm7", language: "cs"}, 0x44: {charset: "8bit"}, 0x48: {charset: "ucs2"}, 0x58: {charset: "ucs2"},
		0x98: {charset: "ucs2"}, 0xE0: {charset: "8bit"}, 0xF0: {charset: "gsm7"}, 0xF4: {charset: "8bit"}, 0x30: {charset: "gsm7"},
	} {
		if got := decodeCBSDCS(dcs); got != want {
			t.Errorf("DCS %#x: %+v, want %+v", dcs, got, want)
		}
	}
}

// A message whose second page never comes goes on when the window closes,
// marked, with the gap named.
func TestCBSAssemblerWindow(t *testing.T) {
	got := make(chan *CellBroadcastMsg, 2)
	a := newCBSAssembler(30*time.Millisecond, func(m *CellBroadcastMsg) { got <- m })
	a.Add(parseCBM("+CBM: 5,4375,1,1,2", "SEVERE first page"))
	select {
	case m := <-got:
		t.Fatalf("went on early: %+v", m)
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case m := <-got:
		if m.Complete || m.PagesReceived != 1 || m.Pages != 2 || !strings.Contains(m.Text, "[page 2 of 2 missing]") || !strings.HasPrefix(m.Text, "SEVERE first page") {
			t.Fatalf("%+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("never went on")
	}
	// Its late page is a repetition now, not a new message.
	a.Add(parseCBM("+CBM: 5,4375,1,2,2", "late"))
	select {
	case m := <-got:
		t.Fatalf("the late page went on alone: %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
}

// The I/O loop: a +CBM header's data is the next line, in the idle reader
// and inside an AT response alike; another result code right after the
// header means the header had no data.
func TestCBSHeaderThenDataLine(t *testing.T) {
	tr := NewDirectCellTransport("x")
	ch := make(chan CellEvent, 8)
	tr.eventSubs[0] = ch
	tr.handleURC("+CBM: 18960,919,5,1,1")
	tr.handleURC("NL-Alert: test")
	select {
	case ev := <-ch:
		var m CellBroadcastMsg
		if ev.Type != "cbs_received" || json.Unmarshal(ev.Data, &m) != nil || m.Text != "NL-Alert: test" || m.Severity != "alert" || !m.Complete {
			t.Fatalf("%+v", ev)
		}
	default:
		t.Fatal("no event")
	}
	tr.handleURC("+CBM: 18961,919,5,1,1")
	tr.handleURC("+CREG: 1,\"0001\",\"1A2B\",0")
	select {
	case ev := <-ch:
		var m CellBroadcastMsg
		_ = json.Unmarshal(ev.Data, &m)
		if m.Text != "" || m.SerialNumber != 18961 {
			t.Fatalf("header with no data: %+v", m)
		}
	default:
		t.Fatal("a header followed by another result code was lost")
	}
	// Inside an AT response: the loop executeCommand runs over the lines.
	resp := "+CSQ: 18,99\r\n+CBM: 18962,4380,1,1,1\r\nTEST monthly\r\nOK\r\n"
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if tr.cbsHeader != "" || strings.HasPrefix(line, "+CMTI:") || strings.HasPrefix(line, "+CBM:") {
			tr.handleURC(line)
		}
	}
	select {
	case ev := <-ch:
		var m CellBroadcastMsg
		_ = json.Unmarshal(ev.Data, &m)
		if m.Text != "TEST monthly" || m.Severity != "test" {
			t.Fatalf("in a response: %+v", m)
		}
	default:
		t.Fatal("a +CBM inside an AT response was lost")
	}
	if tr.cbsHeader != "" {
		t.Fatal("a header is left waiting")
	}
}

// The modem is asked for every cell broadcast with the explicit TS 27.005
// form first; a modem that refuses the ranges gets mode 1 with nothing
// listed. The bare AT+CSCB=0 is never sent: on the A7670E it accepts
// nothing.
func TestCBSAcceptLadder(t *testing.T) {
	var sent []string
	took := cbsAcceptWith(func(cmd string) (string, error) { sent = append(sent, cmd); return "\r\nOK\r\n", nil })
	if took != `AT+CSCB=0,"0-65535","0-255"` || len(sent) != 1 {
		t.Fatalf("%q %v", took, sent)
	}
	sent = nil
	took = cbsAcceptWith(func(cmd string) (string, error) {
		sent = append(sent, cmd)
		if len(sent) == 1 {
			return "\r\n+CMS ERROR: 303\r\n", nil
		}
		return "\r\nOK\r\n", nil
	})
	if took != "AT+CSCB=1" || len(sent) != 2 || sent[0] != `AT+CSCB=0,"0-65535","0-255"` {
		t.Fatalf("%q %v", took, sent)
	}
	for _, c := range cbsAcceptCommands {
		if c == "AT+CSCB=0" {
			t.Fatal("the bare form is back")
		}
	}
}
