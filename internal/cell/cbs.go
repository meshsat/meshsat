package cell

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/rs/zerolog/log"

	"meshsat/internal/smpp"
)

// Cell broadcast on the kit's own 2G cell: the 2G cell leg of the warning
// relay (MESHSAT-1734) and the operator's test sends. A client for
// osmo-cbc's ECBE REST interface on the loopback, an allow-list of
// channels enforced here in code exactly as meshsat-cell's core/cbs.sh
// enforces it, the pages cut from the text the way TS 23.041 lays them
// out, and a TX-log row written before every request reaches osmo-cbc,
// since a broadcast is an emission of the licensed carrier like any other.
//
// 3GPP TS 23.041 reserves 4352-4359 (ETWS), 4370-4399 (EU-Alert / CMAS,
// NL-Alert on 4G) and 4400-6399 for public warnings: a phone shows those as
// official government alerts without any setting. The designated TEST
// identifiers among them (ETWS test 4355, the EU-Alert / CMAS monthly test
// 4380 and exercise 4381) are shown only by a phone with "Test alerts" on,
// marked as a test. Ordinary channels (1-4351) are read only by phones
// that subscribed to them. Everything from 4352 up that is not a test
// identifier is refused unless the owner put it in MESHSAT_CELL_CBS_REAL_IDS
// after recording the RDI clearance for it (docs/cell-broadcast.md).

// Channels and identifiers (TS 23.041 9.4.1.2.2).
const (
	OrdinaryChannel    = 50   // the ordinary channel the kits use; a phone must subscribe to it
	OrdinaryChannelMax = 4351 // 1-4351: no phone shows these unasked
	AlertIDMax         = 6399 // 4352-6399: public warning identifiers

	ChannelETWSTest        = 4355
	ChannelPresidential    = 4370 // EU-Alert level 1 / CMAS presidential; NL-Alert on 4G/5G
	ChannelExtremeFirst    = 4371 // 4371-4374: extreme, observed/likely x immediate/expected
	ChannelSevereFirst     = 4375 // 4375-4378: severe, the same four
	ChannelAmber           = 4379
	ChannelEUAlertTest     = 4380 // the monthly test
	ChannelEUAlertExercise = 4381

	// VTYCBC is osmo-cbc's VTY (libosmovty's default for it); its REST
	// interface has no GET, so what is on air is read there.
	VTYCBC = 4264
	// ECBEBase is osmo-cbc's REST interface as the Bridge renders it.
	ECBEBase = "http://127.0.0.1:12345/api/ecbe/v1"

	cbsMaxPages    = 15
	cbsPageSeptets = 93 // GSM 7-bit characters a page holds
	cbsPageUCS2    = 41 // UCS-2 characters a page holds (no language prefix)
	cbsPageOctets  = 82

	// RepetitionUnit is osmo-cbc's repetition period unit.
	RepetitionUnit          = 1.883
	DefaultRepetitionPeriod = 5  // about 9.4 s, as cbs.sh
	DefaultBroadcasts       = 30 // about 5 minutes, as cbs.sh
	MaxRepetitionPeriod     = 4095
	MaxBroadcasts           = 65535
)

// TestChannels are the designated test identifiers.
var TestChannels = []int{ChannelETWSTest, ChannelEUAlertTest, ChannelEUAlertExercise}

// ErrChannelRefused is a channel outside the allow-list.
var ErrChannelRefused = errors.New("cell: channel refused")

// ErrRadioDown is a broadcast asked for while the radio is not keyed up:
// osmo-cbc would hold it and send it later, unlogged.
var ErrRadioDown = errors.New("cell: the radio is not keyed up; a broadcast would go out unlogged when it is")

// Allow is the allow-list of channels.
type Allow struct {
	real map[int]bool
}

// NewAllow builds the allow-list: ordinary channels and the test
// identifiers always; the real identifiers given on top.
func NewAllow(real []int) Allow {
	a := Allow{real: map[int]bool{}}
	for _, id := range real {
		a.real[id] = true
	}
	return a
}

// ParseRealIDs reads MESHSAT_CELL_CBS_REAL_IDS: identifiers and ranges,
// "4370,4371-4378". Only public warning identifiers (4352-6399) may be
// named, and never a test identifier (those are allowed anyway).
func ParseRealIDs(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if a, b, ok := strings.Cut(part, "-"); ok {
			lo, hi = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		from, err1 := strconv.Atoi(lo)
		to, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || from > to {
			return nil, fmt.Errorf("cell: MESHSAT_CELL_CBS_REAL_IDS: %q is not an identifier or a range", part)
		}
		if from <= OrdinaryChannelMax || to > AlertIDMax {
			return nil, fmt.Errorf("cell: MESHSAT_CELL_CBS_REAL_IDS: %q is outside the public warning identifiers %d-%d", part, OrdinaryChannelMax+1, AlertIDMax)
		}
		for id := from; id <= to; id++ {
			out = append(out, id)
		}
	}
	return out, nil
}

// IsTestChannel reports whether ch is a designated test identifier.
func IsTestChannel(ch int) bool {
	for _, t := range TestChannels {
		if ch == t {
			return true
		}
	}
	return false
}

// Check refuses what cbs.sh refuses: anything from 4352 up that is not a
// test identifier, unless the owner cleared it; and anything below 1.
func (a Allow) Check(ch int) error {
	switch {
	case ch >= 1 && ch <= OrdinaryChannelMax:
		return nil
	case IsTestChannel(ch):
		return nil
	case a.real[ch]:
		return nil
	case ch > OrdinaryChannelMax && ch <= AlertIDMax:
		return fmt.Errorf("%w: %d is a real public-warning identifier (a phone shows it as a government alert); allowed: ordinary 1-%d, the test identifiers %v, and MESHSAT_CELL_CBS_REAL_IDS %v",
			ErrChannelRefused, ch, OrdinaryChannelMax, TestChannels, a.Real())
	}
	return fmt.Errorf("%w: %d is not a cell broadcast channel (1-%d)", ErrChannelRefused, ch, AlertIDMax)
}

// Real lists the cleared real identifiers.
func (a Allow) Real() []int {
	out := make([]int, 0, len(a.real))
	for id := range a.real {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// Serial is the serial number of TS 23.041 9.4.1.2.1: geographical scope
// (2 bits), message code (10 bits), update number (4 bits). The same
// message code with a new update number is a changed message; a phone
// treats a new message code as a new message.
type Serial struct {
	GeoScope int `json:"geo_scope"` // 0 cell wide immediate, 1 PLMN wide, 2 location area wide, 3 cell wide
	MsgCode  int `json:"msg_code"`
	UpdateNr int `json:"update_nr"`
}

// Uint16 is the serial number as it goes on the air.
func (s Serial) Uint16() int {
	return (s.GeoScope&0x03)<<14 | (s.MsgCode&0x3FF)<<4 | (s.UpdateNr & 0x0F)
}

var geoScopeNames = []string{"cell_wide_immediate", "plmn_wide", "lac_sac_tac_wide", "cell_wide"}

// GeoScopeName is osmo-cbc's name for the scope.
func (s Serial) GeoScopeName() string {
	if s.GeoScope < 0 || s.GeoScope >= len(geoScopeNames) {
		return geoScopeNames[1]
	}
	return geoScopeNames[s.GeoScope]
}

// MsgCodeOf derives a message code (1-1023) from what a broadcast is about,
// so two warnings on one channel never share one by chance more than one
// in a thousand.
func MsgCodeOf(about string) int {
	h := fnv.New32a()
	h.Write([]byte(about))
	return int(h.Sum32()%1023) + 1
}

// dcsLanguages is the DCS 0x0X table of TS 23.038 section 5.
var dcsLanguages = map[string]byte{"de": 0x00, "en": 0x01, "it": 0x02, "fr": 0x03, "es": 0x04, "nl": 0x05, "sv": 0x06, "da": 0x07,
	"pt": 0x08, "fi": 0x09, "no": 0x0A, "el": 0x0B, "tr": 0x0C, "hu": 0x0D, "pl": 0x0E}

// DCSFor is the data coding scheme of a GSM 7-bit page in the language
// given (0x0F, language unspecified, when the table has none) or of a
// UCS-2 page (0x48, general data coding, no class).
func DCSFor(language string, ucs2 bool) byte {
	if ucs2 {
		return 0x48
	}
	if d, ok := dcsLanguages[strings.ToLower(language)]; ok {
		return d
	}
	return 0x0F
}

// Pages cuts text into pages as TS 23.041 9.3.19 lays them out: GSM 7-bit
// when every character is in GSM 03.38, 93 characters a page, padded with
// CR; otherwise UCS-2, 41 characters a page. At most 15 pages.
func Pages(text, language string) (pages [][]byte, dcs byte, charset string, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, 0, "", errors.New("cell: nothing to broadcast")
	}
	if sept, err := smpp.EncodeGSM7(text); err == nil {
		for len(sept) > 0 {
			n := min(cbsPageSeptets, len(sept))
			if n < len(sept) && sept[n-1] == 0x1B {
				n-- // an escape pair stays on one page
			}
			page := append([]byte(nil), sept[:n]...)
			sept = sept[n:]
			for len(page) < cbsPageSeptets {
				page = append(page, 0x0D)
			}
			pages = append(pages, smpp.PackSeptets(page))
		}
		dcs, charset = DCSFor(language, false), "gsm"
	} else {
		units := utf16.Encode([]rune(text))
		for len(units) > 0 {
			n := min(cbsPageUCS2, len(units))
			if n < len(units) && units[n-1] >= 0xD800 && units[n-1] < 0xDC00 {
				n-- // a surrogate pair stays on one page
			}
			page := make([]byte, 0, cbsPageOctets)
			for _, u := range units[:n] {
				page = binary.BigEndian.AppendUint16(page, u)
			}
			units = units[n:]
			for len(page) < cbsPageOctets {
				page = append(page, 0x00, 0x0D)
			}
			pages = append(pages, page)
		}
		dcs, charset = DCSFor(language, true), "ucs2"
	}
	if len(pages) > cbsMaxPages {
		return nil, 0, "", fmt.Errorf("cell: the text takes %d pages, a broadcast holds at most %d", len(pages), cbsMaxPages)
	}
	return pages, dcs, charset, nil
}

// BroadcastRequest is one message to put on the air.
type BroadcastRequest struct {
	Channel  int    // the message identifier
	Text     string // the words; cut into pages here
	Language string // ISO 639-1, into the DCS of a GSM 7-bit page
	// Ref is what the broadcast is about (a warning's id); "" for an
	// operator's test. A later request with the same Ref replaces it.
	Ref string
	// Replaces is the Ref this one updates: that broadcast comes off the
	// air and this one continues its update number.
	Replaces string
	Purpose  string // the TX-log row's purpose
	Test     bool   // a test run (the row says so)
	Period   int    // repetition period in units of 1.883 s; 0 = DefaultRepetitionPeriod
	Count    int    // broadcasts; 0 = DefaultBroadcasts
	MsgCode  int    // 0 = derived from Ref or the text
}

// Broadcast is one message on the air as this Bridge put it there.
type Broadcast struct {
	Channel  int       `json:"channel"`
	Severity string    `json:"severity"` // what a phone makes of the channel
	Serial   Serial    `json:"serial"`
	SerialNr int       `json:"serial_nr"` // the 16-bit value
	Pages    int       `json:"pages"`
	Charset  string    `json:"charset"`
	DCS      int       `json:"dcs"`
	Language string    `json:"language,omitempty"`
	Text     string    `json:"text"`
	Ref      string    `json:"ref,omitempty"`
	Purpose  string    `json:"purpose"`
	Test     bool      `json:"test"`
	Period   int       `json:"period"`
	Count    int       `json:"broadcasts"`
	At       time.Time `json:"at"`
	Form     string    `json:"form"` // the REST body osmo-cbc took: encoded (dcs + pages) or decoded (text)
	Until    time.Time `json:"until"`
}

// ChannelSeverity is what a phone makes of a channel, in the words the
// kit's receive side uses.
func ChannelSeverity(ch int) string {
	switch {
	case ch == 919 || ch == 3471:
		return "alert"
	case ch >= 4352 && ch <= 4354, ch == 4356:
		return "extreme"
	case ch == ChannelETWSTest, ch == ChannelEUAlertTest, ch == ChannelEUAlertExercise:
		return "test"
	case ch == ChannelPresidential:
		return "presidential"
	case ch >= ChannelExtremeFirst && ch < ChannelSevereFirst:
		return "extreme"
	case ch >= ChannelSevereFirst && ch < ChannelAmber:
		return "severe"
	case ch == ChannelAmber:
		return "amber"
	case ch >= 1 && ch <= OrdinaryChannelMax:
		return "ordinary"
	}
	return "unknown"
}

// CBC drives osmo-cbc.
type CBC struct {
	base    string
	http    *http.Client
	txlog   TXLog
	site    func() (arfcn int, operator, licence string)
	radioUp func() bool
	allow   Allow
	now     func() time.Time

	// op serialises Broadcast and Delete end to end (the row, the delete,
	// the post and the book); mu guards the book alone.
	op    sync.Mutex
	mu    sync.Mutex
	onAir map[int]*Broadcast // by channel: osmo-cbc holds one message per identifier
}

// NewCBC makes the client. site answers the carrier and the operator and
// licence the TX log cites; radioUp whether the radio is keyed up. The
// HTTP timeout is short: osmo-cbc is on the loopback, and the warning
// relay waits for the answer.
func NewCBC(txlog TXLog, allow Allow, site func() (int, string, string), radioUp func() bool) *CBC {
	return &CBC{base: ECBEBase, http: &http.Client{Timeout: 3 * time.Second}, txlog: txlog, site: site, radioUp: radioUp,
		allow: allow, now: time.Now, onAir: map[int]*Broadcast{}}
}

// SetBase points the client at another ECBE (tests).
func (c *CBC) SetBase(base string) { c.base = strings.TrimRight(base, "/") }

// Allow is the allow-list in force.
func (c *CBC) Allow() Allow { return c.allow }

// Live reports whether the radio is keyed up: a broadcast can go now.
func (c *CBC) Live() bool { return c.radioUp != nil && c.radioUp() }

// Broadcast puts one message on the air: the allow-list, the radio check,
// the pages, the TX-log row, then osmo-cbc. A message already on the
// channel, or the one Replaces names, comes off first.
func (c *CBC) Broadcast(ctx context.Context, req BroadcastRequest) (*Broadcast, error) {
	c.op.Lock()
	defer c.op.Unlock()
	purpose := strings.TrimSpace(req.Purpose)
	if len(purpose) < 3 {
		return nil, errors.New("cell: a purpose for the TX log is required")
	}
	if req.MsgCode < 0 || req.MsgCode > 1023 {
		return nil, fmt.Errorf("cell: message code %d is not 10 bits (0 = derived, else 1-1023)", req.MsgCode)
	}
	if req.Language != "" {
		if _, ok := dcsLanguages[strings.ToLower(req.Language)]; !ok {
			req.Language = "" // not in the DCS table: unspecified
		}
	}
	if req.Test {
		purpose = "test run: " + purpose
	}
	if err := c.allow.Check(req.Channel); err != nil {
		c.logTX("cbs-refused", fmt.Sprintf("cell broadcast channel %d", req.Channel), purpose+" ("+err.Error()+")")
		return nil, err
	}
	if !c.Live() {
		c.logTX("cbs-refused", fmt.Sprintf("cell broadcast channel %d", req.Channel), purpose+" (the radio is not keyed up)")
		return nil, ErrRadioDown
	}
	pages, dcs, charset, err := Pages(req.Text, req.Language)
	if err != nil {
		return nil, err
	}
	period, count := req.Period, req.Count
	if period <= 0 {
		period = DefaultRepetitionPeriod
	}
	if count <= 0 {
		count = DefaultBroadcasts
	}
	if period > MaxRepetitionPeriod || count > MaxBroadcasts {
		return nil, fmt.Errorf("cell: repetition period at most %d, broadcasts at most %d", MaxRepetitionPeriod, MaxBroadcasts)
	}
	about := req.Ref
	if about == "" {
		about = req.Text
	}
	serial := Serial{GeoScope: 1, MsgCode: req.MsgCode} // PLMN wide, as cbs.sh
	if serial.MsgCode <= 0 {
		serial.MsgCode = MsgCodeOf(about)
	}

	// What comes off the air first: the channel's current message, and the
	// one this replaces wherever it is (its update number continues).
	c.mu.Lock()
	var off []int
	if cur := c.onAir[req.Channel]; cur != nil {
		off = append(off, req.Channel)
	}
	if req.Replaces != "" {
		for ch, b := range c.onAir {
			if b.Ref == req.Replaces {
				serial.MsgCode, serial.UpdateNr = b.Serial.MsgCode, (b.Serial.UpdateNr+1)&0x0F
				if ch != req.Channel {
					off = append(off, ch)
				}
			}
		}
	}
	c.mu.Unlock()

	b := &Broadcast{Channel: req.Channel, Severity: ChannelSeverity(req.Channel), Serial: serial, SerialNr: serial.Uint16(),
		Pages: len(pages), Charset: charset, DCS: int(dcs), Language: strings.ToLower(req.Language), Text: strings.TrimSpace(req.Text),
		Ref: req.Ref, Purpose: purpose, Test: req.Test, Period: period, Count: count, At: c.now().UTC()}
	b.Until = b.At.Add(time.Duration(float64(period)*float64(count)*RepetitionUnit*float64(time.Second)) + time.Second)
	setting := fmt.Sprintf("cell broadcast channel %d (%s), serial 0x%04X (%s, code %d, update %d), %d page(s) %s dcs 0x%02X, %d x every %.1f s",
		b.Channel, b.Severity, b.SerialNr, serial.GeoScopeName(), serial.MsgCode, serial.UpdateNr, b.Pages, charset, dcs, count, float64(period)*RepetitionUnit)
	// Logged before osmo-cbc hears of it, so no emission goes unrecorded.
	if err := c.logTX("cbs", setting, purpose); err != nil {
		return nil, fmt.Errorf("cell: the TX log could not be written, nothing is broadcast: %w", err)
	}
	for _, ch := range off {
		if err := c.delete(ctx, ch); err != nil {
			log.Warn().Err(err).Int("channel", ch).Msg("cell: the message to replace did not come off the air")
		}
		c.mu.Lock()
		delete(c.onAir, ch)
		c.mu.Unlock()
	}
	form, err := c.post(ctx, b, pages, dcs)
	if err != nil {
		c.logTX("cbs-failed", setting, purpose+" ("+err.Error()+")")
		return nil, err
	}
	b.Form = form
	c.mu.Lock()
	c.onAir[req.Channel] = b
	c.mu.Unlock()
	log.Info().Int("channel", b.Channel).Str("severity", b.Severity).Int("serial", b.SerialNr).Int("pages", b.Pages).Str("charset", charset).
		Int("count", count).Int("period", period).Str("form", form).Bool("test", req.Test).Str("purpose", purpose).Msg("cell: broadcast on the air")
	return b, nil
}

// post sends the message: the encoded form (our DCS and pages, exact)
// first; an osmo-cbc that refuses that body with 400 gets the decoded form
// cbs.sh uses (its own paging); 409 means the identifier is held, which
// a delete clears once.
func (c *CBC) post(ctx context.Context, b *Broadcast, pages [][]byte, dcs byte) (string, error) {
	hexPages := make([]string, len(pages))
	for i, p := range pages {
		hexPages[i] = hex.EncodeToString(p)
	}
	encoded := map[string]any{"payload_encoded": map[string]any{"dcs": int(dcs), "pages": hexPages}}
	charset := b.Charset
	if charset == "" {
		charset = "gsm"
	}
	language := b.Language
	if language == "" {
		language = "en"
	}
	decoded := map[string]any{"payload_decoded": map[string]any{"character_set": charset, "language": language, "data_utf8": b.Text}}
	body := func(payload map[string]any) map[string]any {
		return map[string]any{
			"cbe_name": "meshsat-bridge", "category": "normal",
			"repetition_period": b.Period, "num_of_bcast": b.Count,
			"scope": map[string]any{"scope_plmn": map[string]any{}},
			"smscb_message": map[string]any{
				"serial_nr":  map[string]any{"serial_nr_decoded": map[string]any{"geo_scope": b.Serial.GeoScopeName(), "msg_code": b.Serial.MsgCode, "update_nr": b.Serial.UpdateNr}},
				"message_id": b.Channel,
				"payload":    payload,
			},
		}
	}
	status, answer, err := c.call(ctx, http.MethodPost, "/message", body(encoded))
	if err != nil {
		return "", err
	}
	if status == http.StatusConflict {
		if err := c.delete(ctx, b.Channel); err == nil {
			status, answer, err = c.call(ctx, http.MethodPost, "/message", body(encoded))
			if err != nil {
				return "", err
			}
		}
	}
	form := "encoded"
	if status == http.StatusBadRequest {
		form = "decoded"
		status, answer, err = c.call(ctx, http.MethodPost, "/message", body(decoded))
		if err != nil {
			return "", err
		}
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return "", fmt.Errorf("cell: osmo-cbc answered HTTP %d to the broadcast: %s", status, strings.TrimSpace(answer))
	}
	return form, nil
}

func (c *CBC) call(ctx context.Context, method, path string, body any) (int, string, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("cell: osmo-cbc on %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(answer), nil
}

// delete takes one identifier off osmo-cbc; one it does not hold is fine.
func (c *CBC) delete(ctx context.Context, channel int) error {
	status, answer, err := c.call(ctx, http.MethodDelete, "/message/"+strconv.Itoa(channel), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent && status != http.StatusNotFound {
		return fmt.Errorf("cell: osmo-cbc answered HTTP %d to the delete of %d: %s", status, channel, strings.TrimSpace(answer))
	}
	return nil
}

// Delete takes the channel's message off the air, with a closing row.
func (c *CBC) Delete(ctx context.Context, channel int, purpose string) error {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	b := c.onAir[channel]
	delete(c.onAir, channel)
	c.mu.Unlock()
	if err := c.delete(ctx, channel); err != nil {
		return err
	}
	setting := fmt.Sprintf("cell broadcast channel %d stopped", channel)
	if b != nil {
		setting = fmt.Sprintf("cell broadcast channel %d stopped (serial 0x%04X, %d page(s), on since %s)", channel, b.SerialNr, b.Pages, b.At.Format(time.RFC3339))
	}
	c.logTX("cbs-stop", setting, purpose)
	log.Info().Int("channel", channel).Str("purpose", purpose).Msg("cell: broadcast off the air")
	return nil
}

// DeleteRef takes every broadcast about ref off the air (a Cancel) and
// answers how many there were.
func (c *CBC) DeleteRef(ctx context.Context, ref, purpose string) (int, error) {
	if ref == "" {
		return 0, nil
	}
	c.mu.Lock()
	var chans []int
	for ch, b := range c.onAir {
		if b.Ref == ref {
			chans = append(chans, ch)
		}
	}
	c.mu.Unlock()
	var first error
	for _, ch := range chans {
		if err := c.Delete(ctx, ch, purpose); err != nil && first == nil {
			first = err
		}
	}
	return len(chans), first
}

// ClearAll takes everything this Bridge put on the air off it (the radio
// keys down: what osmo-cbc held would go out again, unlogged, when it keys
// up).
func (c *CBC) ClearAll(ctx context.Context, purpose string) int {
	c.mu.Lock()
	chans := make([]int, 0, len(c.onAir))
	for ch := range c.onAir {
		chans = append(chans, ch)
	}
	c.mu.Unlock()
	for _, ch := range chans {
		if err := c.Delete(ctx, ch, purpose); err != nil {
			log.Warn().Err(err).Int("channel", ch).Msg("cell: broadcast not cleared")
		}
	}
	return len(chans)
}

// OnAir lists what this Bridge put on the air and has not taken off,
// channel order; what osmo-cbc finished (count reached) is dropped.
func (c *CBC) OnAir() []Broadcast {
	now := c.now()
	c.mu.Lock()
	out := make([]Broadcast, 0, len(c.onAir))
	for ch, b := range c.onAir {
		if now.After(b.Until) {
			delete(c.onAir, ch)
			continue
		}
		out = append(out, *b)
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Channel < out[j].Channel })
	return out
}

// VTYMessages is what osmo-cbc itself says it holds ("show messages cbs"
// on its VTY), nil when it does not answer.
func (c *CBC) VTYMessages() []string {
	lines, err := vtyFn(VTYCBC, "show messages cbs")
	if err != nil {
		return nil
	}
	return lines
}

func (c *CBC) logTX(event, setting, purpose string) error {
	if c.txlog == nil {
		return nil
	}
	arfcn, operator, licence := 0, "", ""
	if c.site != nil {
		arfcn, operator, licence = c.site()
	}
	if licence == "" {
		licence = "not set"
	}
	freq := 0.0
	if arfcn > 0 {
		freq = FreqMHz(arfcn)
	}
	return c.txlog.AppendTX(TXRow{At: c.now().UTC(), Event: event, ARFCN: arfcn, FreqMHz: freq, Setting: setting,
		Output: "<< 200 mW ERP (the carrier's)", Purpose: purpose, Operator: operator, Licence: licence})
}
