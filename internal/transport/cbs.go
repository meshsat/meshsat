package transport

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"go.bug.st/serial"

	"meshsat/internal/smpp"
)

// Cell broadcast as the kit's modem receives it (MESHSAT-1737): the +CBM
// unsolicited result in text mode (+CBM: <sn>,<mid>,<dcs>,<page>,<pages>
// and the data line after it) or in PDU mode (+CBM: <length> and the page
// of 3GPP TS 23.041 9.4.1.2 in hex), the data coding scheme of TS 23.038
// section 5 honoured, the pages of one message put together by serial
// number and message identifier, each message passed on once, and the
// message identifier read into a severity by the TS 23.041 9.4.1.2.2 table
// and the Dutch Regeling NL-Alert.

const (
	cbsPageOctets  = 82 // user data octets of a page (88 less the 6-octet header)
	cbsPageSeptets = 93 // GSM 7-bit characters a page holds
	// cbsAssembleWindow is how long the pages of one message may take to
	// come in; what is still missing then goes on as it is, marked.
	cbsAssembleWindow = 2 * time.Minute
	// cbsRepeatWindow is how long a message's serial number and identifier
	// are remembered so its repetitions (every few seconds for minutes, by
	// design) are not stored again. An Update has a new serial number.
	cbsRepeatWindow = time.Hour
	cbsRepeatMax    = 256
	// cbsHeaderWait is how long a +CBM header waits for its data line.
	cbsHeaderWait = 3 * time.Second
)

// cbsAcceptCommands is the ladder the modem is asked to deliver every cell
// broadcast with, in order, until one answers OK. The bare AT+CSCB=0 the
// Bridge sent until MESHSAT-1737 is "accept the identifiers listed" with
// nothing listed: on the A7670E (A76XX AT manual 9.2.5: mode 0 accepts what
// <mids> names, the default reads +CSCB: 1,(),()) that turned reception
// off. Mode 0 with the whole identifier and coding ranges is the TS 27.005
// form every modem reads the same way (the A76XX example is
// AT+CSCB=0,"15-17,50,86",""); a modem that refuses the ranges gets mode 1
// with nothing listed, which rejects nothing.
var cbsAcceptCommands = []string{
	`AT+CSCB=0,"0-65535","0-255"`,
	`AT+CSCB=1`,
}

// cbsAcceptWith walks the ladder with send and returns the command the
// modem took, "" when none.
func cbsAcceptWith(send func(string) (string, error)) string {
	for _, cmd := range cbsAcceptCommands {
		resp, err := send(cmd)
		if err == nil && !strings.Contains(resp, "ERROR") {
			return cmd
		}
	}
	log.Warn().Msg("cellular: the modem took no AT+CSCB form; cell broadcasts may not be delivered")
	return ""
}

// cbsAcceptOn asks for every cell broadcast on a port the caller holds.
func cbsAcceptOn(sp serial.Port) string {
	return cbsAcceptWith(func(cmd string) (string, error) { return sendAT(sp, cmd, cellATTimeout) })
}

// cbsAcceptExec asks for every cell broadcast through the I/O loop.
func (t *DirectCellTransport) cbsAcceptExec() string {
	return cbsAcceptWith(func(cmd string) (string, error) { return t.execAT(cmd, cellATTimeout) })
}

// cbsSeverity reads a message identifier into a severity: the 3GPP TS
// 23.041 9.4.1.2.2 table for the ETWS (4352-4359) and the EU-Alert / CMAS
// (4370-4399) identifiers, the Regeling NL-Alert for the Dutch channels.
// The amber identifier is 4379 (4392 in additional languages), as
// meshsat-cell's cbs.sh has it.
func cbsSeverity(mid int) string {
	switch {
	case mid == 919 || mid == 3471:
		// NL-Alert on 2G/3G: the Regeling NL-Alert (art. 2 of the 2018
		// text, art. 12 of the 2014 one) has every operator send each
		// NL-Alert message on 919 and 3471 in parallel. Both are real alerts.
		return "alert"
	case mid >= 4352 && mid <= 4354, mid == 4356:
		return "extreme" // ETWS earthquake, tsunami, both, other emergency
	case mid == 4355:
		return "test" // ETWS test
	case mid == 4370 || mid == 4383:
		return "presidential" // EU-Alert level 1 / CMAS presidential (NL-Alert on 4G/5G); 4383 in additional languages
	case mid >= 4371 && mid <= 4374, mid >= 4384 && mid <= 4387:
		return "extreme" // EU-Alert level 2 / CMAS extreme: observed or likely, immediate or expected
	case mid >= 4375 && mid <= 4378, mid >= 4388 && mid <= 4391:
		return "severe" // EU-Alert level 3 / CMAS severe
	case mid == 4379 || mid == 4392:
		return "amber" // child abduction emergency
	case mid == 4380 || mid == 4381 || mid == 4393 || mid == 4394 || mid == 4398 || mid == 4399:
		return "test" // the monthly test, the exercise, the state/local test, each with its additional-languages twin
	case mid == 4382 || mid == 4395:
		return "operator" // operator defined use
	case mid == 4396 || mid == 4397:
		return "info" // public safety alert
	default:
		return "unknown"
	}
}

// cbsCoding is what a cell broadcast data coding scheme says (TS 23.038
// section 5).
type cbsCoding struct {
	charset  string // gsm7, ucs2, 8bit
	language string // ISO 639-1 where the DCS names one
	prefix   bool   // the text starts with a language indication (DCS 0x10, 0x11)
}

var (
	cbsDCSLanguages  = [16]string{"de", "en", "it", "fr", "es", "nl", "sv", "da", "pt", "fi", "no", "el", "tr", "hu", "pl", ""}
	cbsDCSLanguages2 = [5]string{"cs", "he", "ar", "ru", "is"}
)

// decodeCBSDCS reads a DCS octet.
func decodeCBSDCS(dcs byte) cbsCoding {
	switch dcs >> 4 {
	case 0x0:
		return cbsCoding{charset: "gsm7", language: cbsDCSLanguages[dcs&0x0F]}
	case 0x1:
		switch dcs & 0x0F {
		case 0:
			return cbsCoding{charset: "gsm7", prefix: true}
		case 1:
			return cbsCoding{charset: "ucs2", prefix: true}
		}
		return cbsCoding{charset: "gsm7"}
	case 0x2:
		c := cbsCoding{charset: "gsm7"}
		if int(dcs&0x0F) < len(cbsDCSLanguages2) {
			c.language = cbsDCSLanguages2[dcs&0x0F]
		}
		return c
	case 0x4, 0x5, 0x6, 0x7, 0x9: // general data coding; message with a UDH
		switch (dcs >> 2) & 0x03 {
		case 1:
			return cbsCoding{charset: "8bit"}
		case 2:
			return cbsCoding{charset: "ucs2"}
		}
		return cbsCoding{charset: "gsm7"}
	case 0xE:
		return cbsCoding{charset: "8bit"} // defined by the WAP Forum
	case 0xF:
		if dcs&0x04 != 0 {
			return cbsCoding{charset: "8bit"}
		}
		return cbsCoding{charset: "gsm7"}
	}
	return cbsCoding{charset: "gsm7"} // reserved: read as the default alphabet
}

// decodeCBSPage reads one page's user data by its coding: the text and
// the language a prefix carried.
func decodeCBSPage(content []byte, coding cbsCoding) (text, language string) {
	switch coding.charset {
	case "ucs2":
		b := content
		if coding.prefix && len(b) >= 2 {
			// Two GSM 7-bit characters padded to the octet boundary.
			language = strings.ToLower(smpp.DecodeGSM7(smpp.UnpackSeptets(b[:2], 2)))
			b = b[2:]
		}
		text = smpp.DecodeUCS2(b)
	case "8bit":
		r := make([]rune, len(content))
		for i, c := range content {
			r[i] = rune(c)
		}
		text = string(r)
	default:
		text = smpp.DecodeGSM7(smpp.UnpackSeptets(content, cbsPageSeptets))
		if coding.prefix && len(text) >= 3 && text[2] == '\r' {
			language, text = strings.ToLower(text[:2]), text[3:]
		}
	}
	return strings.TrimRight(text, "\r"), language // pages are padded with CR
}

// parseCBSPDU reads the page as it went over the air (TS 23.041 9.4.1.2):
// serial number, message identifier, DCS, page parameter, 82 octets.
func parseCBSPDU(pdu []byte) *CellBroadcastMsg {
	if len(pdu) < 6 {
		return nil
	}
	sn := int(pdu[0])<<8 | int(pdu[1])
	mid := int(pdu[2])<<8 | int(pdu[3])
	page, pages := int(pdu[5]>>4), int(pdu[5]&0x0F)
	if page == 0 || pages == 0 || page > pages {
		page, pages = 1, 1 // 9.4.1.2.4: a zero field reads as 1 of 1
	}
	content := pdu[6:]
	if len(content) > cbsPageOctets {
		content = content[:cbsPageOctets]
	}
	coding := decodeCBSDCS(pdu[4])
	text, lang := decodeCBSPage(content, coding)
	if lang == "" {
		lang = coding.language
	}
	return &CellBroadcastMsg{SerialNumber: sn, MessageID: mid, Channel: mid, Severity: cbsSeverity(mid), Text: text,
		DCS: int(pdu[4]), Charset: coding.charset, Language: lang, Page: page, Pages: pages}
}

// cbsHex reports whether s is an even number of hex digits (a page in
// hex; the SMS reader has its own isHexString).
func cbsHex(s string) bool {
	if s == "" || len(s)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// parseCBM reads a +CBM header and the data after it, in text mode
// (+CBM: <sn>,<mid>,<dcs>,<page>,<pages> and the text the modem decoded
// into its character set, hex for what it could not) or in PDU mode
// (+CBM: <length> and the page in hex).
func parseCBM(header, body string) *CellBroadcastMsg {
	idx := strings.Index(header, "+CBM:")
	if idx == -1 {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(header[idx+5:]), ",")
	body = strings.TrimSpace(body)
	if first, _, ok := strings.Cut(body, "\n"); ok && cbsHex(strings.TrimSpace(first)) {
		body = strings.TrimSpace(first)
	}
	if len(parts) == 1 {
		n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || n <= 0 || !cbsHex(body) {
			return nil
		}
		raw, _ := hex.DecodeString(body)
		if len(raw) > n {
			raw = raw[:n]
		}
		return parseCBSPDU(raw)
	}
	if len(parts) < 3 {
		return nil
	}
	sn, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	mid, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	dcs8, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 8)
	if err != nil {
		dcs8 = 0x0F // not a DCS: the default alphabet, language unspecified
	}
	dcs := int(dcs8)
	page, pages := 1, 1
	if len(parts) >= 5 {
		page, _ = strconv.Atoi(strings.TrimSpace(parts[3]))
		pages, _ = strconv.Atoi(strings.TrimSpace(parts[4]))
		if page == 0 || pages == 0 || page > pages {
			page, pages = 1, 1
		}
	}
	coding := decodeCBSDCS(uint8(dcs8))
	text, lang := body, coding.language
	switch {
	case coding.charset == "ucs2" && cbsHex(text):
		raw, _ := hex.DecodeString(text)
		text, lang = decodeCBSPage(raw, coding)
	case coding.charset == "8bit" && cbsHex(text):
		raw, _ := hex.DecodeString(text)
		text, _ = decodeCBSPage(raw, coding)
	case coding.prefix && len(text) >= 3 && text[2] == '\r':
		lang, text = strings.ToLower(text[:2]), text[3:]
	}
	if lang == "" {
		lang = coding.language
	}
	return &CellBroadcastMsg{SerialNumber: sn, MessageID: mid, Channel: mid, Severity: cbsSeverity(mid),
		Text: strings.TrimRight(text, "\r"), DCS: dcs, Charset: coding.charset, Language: lang, Page: page, Pages: pages}
}

// cbsKey is what the pages of one message share.
type cbsKey struct{ serial, mid int }

type cbsPending struct {
	pages map[int]*CellBroadcastMsg
	total int
	timer *time.Timer
}

// cbsAssembler puts the pages of a message together and passes each
// message on once.
type cbsAssembler struct {
	mu      sync.Mutex
	pending map[cbsKey]*cbsPending
	seen    map[cbsKey]time.Time
	window  time.Duration
	now     func() time.Time
	emit    func(*CellBroadcastMsg)
}

func newCBSAssembler(window time.Duration, emit func(*CellBroadcastMsg)) *cbsAssembler {
	return &cbsAssembler{pending: map[cbsKey]*cbsPending{}, seen: map[cbsKey]time.Time{}, window: window, now: time.Now, emit: emit}
}

// Add takes one page. A one-page message, or the last page of a longer
// one, goes on at once; a message still missing pages when the window
// closes goes on as it is, marked incomplete; a repetition of a message
// already passed on within cbsRepeatWindow is dropped.
func (a *cbsAssembler) Add(page *CellBroadcastMsg) {
	if page == nil {
		return
	}
	key := cbsKey{page.SerialNumber, page.MessageID}
	a.mu.Lock()
	if a.repeatLocked(key) {
		a.mu.Unlock()
		log.Debug().Int("mid", page.MessageID).Int("serial", page.SerialNumber).Msg("cellular: cell broadcast repeated, not stored again")
		return
	}
	if page.Pages <= 1 {
		a.seen[key] = a.now()
		a.mu.Unlock()
		page.Page, page.Pages, page.PagesReceived, page.Complete = 1, 1, 1, true
		a.emit(page)
		return
	}
	p := a.pending[key]
	if p == nil {
		p = &cbsPending{pages: map[int]*CellBroadcastMsg{}, total: page.Pages}
		a.pending[key] = p
		p.timer = time.AfterFunc(a.window, func() { a.flush(key) })
	}
	p.pages[page.Page] = page
	if len(p.pages) < p.total {
		a.mu.Unlock()
		return
	}
	delete(a.pending, key)
	p.timer.Stop()
	a.seen[key] = a.now()
	a.mu.Unlock()
	a.emit(joinCBS(p, true))
}

// repeatLocked reports whether key went on within the repeat window,
// forgetting what is older.
func (a *cbsAssembler) repeatLocked(key cbsKey) bool {
	now := a.now()
	for k, at := range a.seen {
		if now.Sub(at) > cbsRepeatWindow {
			delete(a.seen, k)
		}
	}
	if len(a.seen) >= cbsRepeatMax {
		a.seen = map[cbsKey]time.Time{}
	}
	_, ok := a.seen[key]
	return ok
}

func (a *cbsAssembler) flush(key cbsKey) {
	a.mu.Lock()
	p := a.pending[key]
	delete(a.pending, key)
	if p != nil {
		a.seen[key] = a.now()
	}
	a.mu.Unlock()
	if p == nil {
		return
	}
	log.Warn().Int("mid", key.mid).Int("serial", key.serial).Int("pages", len(p.pages)).Int("of", p.total).
		Msg("cellular: cell broadcast still missing pages when the window closed, passed on as it is")
	a.emit(joinCBS(p, false))
}

// joinCBS is the pages in order; a page that never came is named.
func joinCBS(p *cbsPending, complete bool) *CellBroadcastMsg {
	var out *CellBroadcastMsg
	var b strings.Builder
	for i := 1; i <= p.total; i++ {
		pg := p.pages[i]
		if pg == nil {
			b.WriteString(" [page " + strconv.Itoa(i) + " of " + strconv.Itoa(p.total) + " missing] ")
			continue
		}
		if out == nil {
			c := *pg
			out = &c
		}
		if out.Language == "" {
			out.Language = pg.Language
		}
		b.WriteString(pg.Text)
	}
	out.Text = strings.TrimSpace(b.String())
	out.Page, out.PagesReceived, out.Complete = 1, len(p.pages), complete
	return out
}

// cbsAssembler is made on first use so every constructor shares it.
func (t *DirectCellTransport) cbsPages() *cbsAssembler {
	t.cbsOnce.Do(func() {
		t.cbs = newCBSAssembler(cbsAssembleWindow, t.emitCBS)
	})
	return t.cbs
}

// takeCBS reads a header and its data line and hands the page to the
// assembler. Only the I/O loop calls it.
func (t *DirectCellTransport) takeCBS(header, body string) {
	if body == "OK" || body == "ERROR" {
		body = ""
	}
	page := parseCBM(header, body)
	if page == nil {
		log.Warn().Str("header", header).Int("body_bytes", len(body)).Msg("cellular: +CBM not understood")
		return
	}
	log.Debug().Int("mid", page.MessageID).Int("serial", page.SerialNumber).Int("page", page.Page).Int("of", page.Pages).
		Str("charset", page.Charset).Str("language", page.Language).Msg("cellular: cell broadcast page")
	t.cbsPages().Add(page)
}

// emitCBS passes one whole message on.
func (t *DirectCellTransport) emitCBS(cbs *CellBroadcastMsg) {
	log.Info().Int("mid", cbs.MessageID).Int("serial", cbs.SerialNumber).Str("severity", cbs.Severity).Str("language", cbs.Language).
		Int("pages", cbs.Pages).Bool("complete", cbs.Complete).Str("text", cbs.Text).Msg("cellular: CBS alert received")
	cbsJSON, _ := json.Marshal(cbs)
	t.emitEvent(CellEvent{
		Type:    "cbs_received",
		Message: cbs.Text,
		Data:    cbsJSON,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

// cbsLine is what the I/O loop does with a line when a +CBM header is
// waiting for its data: the line is the data, unless it is plainly
// another result code, in which case the header had none. It reports
// whether the line was taken.
func (t *DirectCellTransport) cbsLine(line string) bool {
	if t.cbsHeader == "" {
		return false
	}
	header := t.cbsHeader
	t.cbsHeader = ""
	if time.Since(t.cbsHeaderAt) > cbsHeaderWait || (strings.HasPrefix(line, "+C") && strings.Contains(line, ":")) {
		t.takeCBS(header, "")
		return false
	}
	t.takeCBS(header, line)
	return true
}
