package cell

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/smpp"
)

// MESHSAT-1734: the cell broadcast client against a fake osmo-cbc, the
// allow-list, the pages, the TX-log rows.

type fakeCBC struct {
	mu        sync.Mutex
	posts     []map[string]any
	deletes   []string
	answer    int // the status a POST gets; 0 = 201
	refuseEnc bool
	srv       *httptest.Server
}

func newFakeCBC(t *testing.T) *fakeCBC {
	t.Helper()
	f := &fakeCBC{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/ecbe/v1/message":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				w.WriteHeader(400)
				return
			}
			f.posts = append(f.posts, body)
			if f.answer != 0 {
				w.WriteHeader(f.answer)
				return
			}
			payload := body["smscb_message"].(map[string]any)["payload"].(map[string]any)
			if _, enc := payload["payload_encoded"]; enc && f.refuseEnc {
				w.WriteHeader(400)
				io.WriteString(w, "no encoded payload here")
				return
			}
			w.WriteHeader(201)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/ecbe/v1/message/"):
			f.deletes = append(f.deletes, strings.TrimPrefix(r.URL.Path, "/api/ecbe/v1/message/"))
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCBC) lastPost() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts[len(f.posts)-1]
}

func cbcRig(t *testing.T, real []int) (*CBC, *fakeCBC, *memTX, *bool) {
	t.Helper()
	f := newFakeCBC(t)
	tx := &memTX{}
	up := true
	c := NewCBC(tx, NewAllow(real), func() (int, string, string) { return 873, "kit-test", "TEST-LICENCE" }, func() bool { return up })
	c.SetBase(f.srv.URL + "/api/ecbe/v1")
	return c, f, tx, &up
}

func smscb(post map[string]any) map[string]any { return post["smscb_message"].(map[string]any) }

// The allow-list is cbs.sh's: ordinary 1-4351 and the test identifiers;
// everything from 4352 up refused unless cleared, and nothing below 1.
func TestAllowList(t *testing.T) {
	a := NewAllow(nil)
	for _, ch := range []int{1, 50, 919, 3471, 4351, 4355, 4380, 4381} {
		if err := a.Check(ch); err != nil {
			t.Errorf("%d refused: %v", ch, err)
		}
	}
	for _, ch := range []int{0, -1, 4352, 4354, 4356, 4370, 4371, 4379, 4382, 4399, 4400, 6399, 6400, 65535} {
		if err := a.Check(ch); !errors.Is(err, ErrChannelRefused) {
			t.Errorf("%d allowed: %v", ch, err)
		}
	}
	if err := a.Check(4370); err == nil || !strings.Contains(err.Error(), "government alert") {
		t.Fatalf("the refusal must say why: %v", err)
	}
	ids, err := ParseRealIDs(" 4370, 4375-4378 ")
	if err != nil || len(ids) != 5 || ids[0] != 4370 || ids[4] != 4378 {
		t.Fatalf("%v %v", ids, err)
	}
	cleared := NewAllow(ids)
	if cleared.Check(4370) != nil || cleared.Check(4376) != nil || !errors.Is(cleared.Check(4371), ErrChannelRefused) {
		t.Fatal("cleared identifiers")
	}
	if got := cleared.Real(); len(got) != 5 || got[1] != 4375 {
		t.Fatalf("real %v", got)
	}
	for _, bad := range []string{"50", "4370-4300", "x", "4352-6400", "4351"} {
		if _, err := ParseRealIDs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if ids, err := ParseRealIDs(""); err != nil || len(ids) != 0 {
		t.Fatal("empty")
	}
}

// Pages: 93 GSM 7-bit characters a page, CR padded; UCS-2 for what GSM
// 03.38 cannot carry, 41 a page; 15 pages at most.
func TestPages(t *testing.T) {
	text := strings.Repeat("Relay of DWD warning via MeshSat, not an official NL-Alert. ", 4) // 240 characters
	pages, dcs, charset, err := Pages(text, "nl")
	if err != nil || len(pages) != 3 || dcs != 0x05 || charset != "gsm" {
		t.Fatalf("%d pages dcs %#x %s %v", len(pages), dcs, charset, err)
	}
	for i, p := range pages {
		if len(p) != cbsPageOctets {
			t.Fatalf("page %d is %d octets", i, len(p))
		}
	}
	back := smpp.DecodeGSM7(smpp.UnpackSeptets(pages[0], cbsPageSeptets))
	if back != text[:93] {
		t.Fatalf("page 1 %q", back)
	}
	if mid := smpp.DecodeGSM7(smpp.UnpackSeptets(pages[1], cbsPageSeptets)); mid != text[93:186] {
		t.Fatalf("page 2 %q", mid)
	}
	if last := strings.TrimRight(smpp.DecodeGSM7(smpp.UnpackSeptets(pages[2], cbsPageSeptets)), "\r"); last != strings.TrimSpace(text[186:]) {
		t.Fatalf("page 3 %q", last)
	}
	// An escape pair (the euro sign) is not cut in two.
	euro := strings.Repeat("x", 92) + "€y"
	pages, _, _, _ = Pages(euro, "en")
	if len(pages) != 2 || !strings.HasPrefix(smpp.DecodeGSM7(smpp.UnpackSeptets(pages[1], cbsPageSeptets)), "€y") {
		t.Fatalf("escape pair: %d pages", len(pages))
	}
	greek := "Δοκιμή MeshSat: μήνυμα δύο σελίδων στο κανάλι 4380. Δεν είναι επίσημη ειδοποίηση."
	pages, dcs, charset, err = Pages(greek, "el")
	if err != nil || len(pages) != 2 || dcs != 0x48 || charset != "ucs2" || len(pages[1]) != cbsPageOctets {
		t.Fatalf("ucs2: %d pages dcs %#x %s %v", len(pages), dcs, charset, err)
	}
	if got := smpp.DecodeUCS2(pages[0]); !strings.HasPrefix(got, "Δοκιμή MeshSat") || len([]rune(got)) != 41 {
		t.Fatalf("ucs2 page 1 %q", got)
	}
	if _, _, _, err := Pages(strings.Repeat("a", 93*15+1), "en"); err == nil {
		t.Fatal("16 pages accepted")
	}
	if _, _, _, err := Pages("  ", "en"); err == nil {
		t.Fatal("empty accepted")
	}
	if DCSFor("", false) != 0x0F || DCSFor("EN", false) != 0x01 || DCSFor("nl", true) != 0x48 {
		t.Fatal("DCS table")
	}
	s := Serial{GeoScope: 1, MsgCode: 161, UpdateNr: 2}
	if s.Uint16() != 0x4A12 || s.GeoScopeName() != "plmn_wide" {
		t.Fatalf("serial %#x %s", s.Uint16(), s.GeoScopeName())
	}
	if c := MsgCodeOf("abc"); c < 1 || c > 1023 || c != MsgCodeOf("abc") || c == MsgCodeOf("abd") {
		t.Fatal("message code")
	}
}

// A broadcast: the row first, then osmo-cbc's body with our serial number,
// DCS and pages; the refusals are rows too; a replace deletes first and
// continues the update number; a cancel deletes; the radio down refuses.
func TestBroadcastThroughOsmoCBC(t *testing.T) {
	c, f, tx, up := cbcRig(t, nil)
	ctx := context.Background()
	b, err := c.Broadcast(ctx, BroadcastRequest{Channel: ChannelEUAlertTest, Text: "TEST WARNING Wind Moderate\nRelay via MeshSat, not an official NL-Alert",
		Language: "nl", Ref: "warn-1", Purpose: "T1 on 4380", Test: true})
	if err != nil {
		t.Fatal(err)
	}
	if b.Channel != 4380 || b.Severity != "test" || b.Pages != 1 || b.Charset != "gsm" || b.DCS != 0x05 || b.Serial.UpdateNr != 0 ||
		b.Serial.GeoScope != 1 || b.Period != DefaultRepetitionPeriod || b.Count != DefaultBroadcasts || b.Form != "encoded" || !b.Test {
		t.Fatalf("%+v", b)
	}
	post := f.lastPost()
	if post["cbe_name"] != "meshsat-bridge" || post["repetition_period"].(float64) != 5 || post["num_of_bcast"].(float64) != 30 {
		t.Fatalf("post %v", post)
	}
	m := smscb(post)
	if m["message_id"].(float64) != 4380 {
		t.Fatalf("message id %v", m["message_id"])
	}
	sn := m["serial_nr"].(map[string]any)["serial_nr_decoded"].(map[string]any)
	if sn["geo_scope"] != "plmn_wide" || int(sn["msg_code"].(float64)) != b.Serial.MsgCode || sn["update_nr"].(float64) != 0 {
		t.Fatalf("serial %v", sn)
	}
	enc := m["payload"].(map[string]any)["payload_encoded"].(map[string]any)
	pages := enc["pages"].([]any)
	if enc["dcs"].(float64) != 5 || len(pages) != 1 {
		t.Fatalf("payload %v", enc)
	}
	raw, _ := hex.DecodeString(pages[0].(string))
	if len(raw) != 82 || !strings.HasPrefix(smpp.DecodeGSM7(smpp.UnpackSeptets(raw, 93)), "TEST WARNING Wind Moderate\n") {
		t.Fatalf("page %x", raw)
	}
	rows := tx.snapshot()
	if len(rows) != 1 || rows[0].Event != "cbs" || rows[0].ARFCN != 873 || rows[0].FreqMHz != 1877.4 || rows[0].Licence != "TEST-LICENCE" ||
		rows[0].Operator != "kit-test" || !strings.HasPrefix(rows[0].Purpose, "test run: T1 on 4380") ||
		!strings.Contains(rows[0].Setting, "channel 4380 (test)") || !strings.Contains(rows[0].Setting, "1 page(s) gsm dcs 0x05") ||
		!strings.Contains(rows[0].Setting, "30 x every 9.4 s") || !strings.Contains(rows[0].Setting, "update 0") {
		t.Fatalf("rows %+v", rows)
	}
	if on := c.OnAir(); len(on) != 1 || on[0].Channel != 4380 || on[0].Ref != "warn-1" {
		t.Fatalf("on air %+v", on)
	}

	// A real identifier: refused, a row says so, nothing posted.
	if _, err := c.Broadcast(ctx, BroadcastRequest{Channel: ChannelPresidential, Text: "x", Purpose: "must not go"}); !errors.Is(err, ErrChannelRefused) {
		t.Fatalf("4370: %v", err)
	}
	rows = tx.snapshot()
	if len(rows) != 2 || rows[1].Event != "cbs-refused" || !strings.Contains(rows[1].Purpose, "government alert") || len(f.posts) != 1 {
		t.Fatalf("refusal %+v posts %d", rows[1], len(f.posts))
	}

	// An Update of warn-1 on the same channel: the old one deleted first,
	// the update number 1, the same message code.
	b2, err := c.Broadcast(ctx, BroadcastRequest{Channel: ChannelEUAlertTest, Text: "TEST UPDATE", Ref: "warn-2", Replaces: "warn-1", Purpose: "T7 update", Test: true})
	if err != nil {
		t.Fatal(err)
	}
	if b2.Serial.UpdateNr != 1 || b2.Serial.MsgCode != b.Serial.MsgCode || len(f.deletes) != 1 || f.deletes[0] != "4380" || len(f.posts) != 2 {
		t.Fatalf("update %+v deletes %v posts %d", b2.Serial, f.deletes, len(f.posts))
	}
	if on := c.OnAir(); len(on) != 1 || on[0].Ref != "warn-2" {
		t.Fatalf("on air after update %+v", on)
	}
	// A Cancel of warn-2 takes it off, with a row.
	n, err := c.DeleteRef(ctx, "warn-2", "T5 cancel")
	if err != nil || n != 1 || len(f.deletes) != 2 || len(c.OnAir()) != 0 {
		t.Fatalf("cancel %d %v deletes %v", n, err, f.deletes)
	}
	if rows = tx.snapshot(); rows[len(rows)-1].Event != "cbs-stop" || rows[len(rows)-1].Purpose != "T5 cancel" {
		t.Fatalf("stop row %+v", rows[len(rows)-1])
	}
	if n, _ := c.DeleteRef(ctx, "never", "x"); n != 0 {
		t.Fatal("a ref never on air")
	}

	// The radio down: refused, nothing posted, a row.
	*up = false
	if _, err := c.Broadcast(ctx, BroadcastRequest{Channel: OrdinaryChannel, Text: "x", Purpose: "radio down"}); !errors.Is(err, ErrRadioDown) {
		t.Fatalf("radio down: %v", err)
	}
	if rows = tx.snapshot(); rows[len(rows)-1].Event != "cbs-refused" || len(f.posts) != 2 {
		t.Fatal("radio down must not post")
	}
	*up = true

	// A purpose is required; a bad period refused.
	if _, err := c.Broadcast(ctx, BroadcastRequest{Channel: 50, Text: "x"}); err == nil {
		t.Fatal("no purpose")
	}
	if _, err := c.Broadcast(ctx, BroadcastRequest{Channel: 50, Text: "x", Purpose: "bad", Period: 5000}); err == nil {
		t.Fatal("period 5000")
	}
}

// osmo-cbc without the encoded payload form: the decoded one is sent; a
// 409 is cleared by a delete; a 500 fails with its text and a row.
func TestBroadcastFallbacks(t *testing.T) {
	c, f, tx, _ := cbcRig(t, nil)
	ctx := context.Background()
	f.refuseEnc = true
	b, err := c.Broadcast(ctx, BroadcastRequest{Channel: 50, Text: "Προσοχή", Language: "el", Purpose: "decoded form"})
	if err != nil || b.Form != "decoded" || len(f.posts) != 2 {
		t.Fatalf("%v %+v posts %d", err, b, len(f.posts))
	}
	dec := smscb(f.lastPost())["payload"].(map[string]any)["payload_decoded"].(map[string]any)
	if dec["character_set"] != "ucs2" || dec["language"] != "el" || dec["data_utf8"] != "Προσοχή" {
		t.Fatalf("decoded %v", dec)
	}
	f.refuseEnc = false
	f.answer = 409
	c.mu.Lock()
	delete(c.onAir, 50) // osmo-cbc holds one we do not know of
	c.mu.Unlock()
	go func() { time.Sleep(20 * time.Millisecond); f.mu.Lock(); f.answer = 0; f.mu.Unlock() }()
	// The first POST gets 409, the delete runs, the second POST may still
	// get 409 (answer flips after 20 ms) and then the call fails cleanly.
	_, err = c.Broadcast(ctx, BroadcastRequest{Channel: 50, Text: "again", Purpose: "conflict"})
	if err != nil && !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("conflict: %v", err)
	}
	if len(f.deletes) == 0 || f.deletes[len(f.deletes)-1] != "50" {
		t.Fatalf("no delete on 409: %v", f.deletes)
	}
	f.mu.Lock()
	f.answer = 500
	f.mu.Unlock()
	if _, err := c.Broadcast(ctx, BroadcastRequest{Channel: 50, Text: "fail", Purpose: "server error"}); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("500: %v", err)
	}
	rows := tx.snapshot()
	if rows[len(rows)-1].Event != "cbs-failed" {
		t.Fatalf("failed row %+v", rows[len(rows)-1])
	}
	// osmo-cbc not there at all: an error, no panic.
	c.SetBase("http://127.0.0.1:1/api/ecbe/v1")
	if _, err := c.Broadcast(ctx, BroadcastRequest{Channel: 50, Text: "x", Purpose: "no cbc"}); err == nil {
		t.Fatal("no osmo-cbc")
	}
}

// ClearAll on a radio stop takes everything off; OnAir forgets what ran
// its course.
func TestClearAllAndExpiry(t *testing.T) {
	c, f, _, _ := cbcRig(t, []int{4371})
	ctx := context.Background()
	if _, err := c.Broadcast(ctx, BroadcastRequest{Channel: 4371, Text: "cleared real id", Purpose: "T6 real identifier", Count: 2, Period: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Broadcast(ctx, BroadcastRequest{Channel: 50, Text: "ordinary", Purpose: "T2 ordinary channel"}); err != nil {
		t.Fatal(err)
	}
	if on := c.OnAir(); len(on) != 2 || on[0].Channel != 50 || on[1].Channel != 4371 || on[1].Severity != "extreme" {
		t.Fatalf("%+v", on)
	}
	c.now = func() time.Time { return time.Now().Add(10 * time.Second) }
	if on := c.OnAir(); len(on) != 1 || on[0].Channel != 50 {
		t.Fatalf("after 4371's two broadcasts: %+v", on)
	}
	if n := c.ClearAll(ctx, "the radio keyed down"); n != 1 || len(f.deletes) != 1 || len(c.OnAir()) != 0 {
		t.Fatalf("clear %d deletes %v", n, f.deletes)
	}
	if ChannelSeverity(50) != "ordinary" || ChannelSeverity(4380) != "test" || ChannelSeverity(4375) != "severe" || ChannelSeverity(919) != "alert" || ChannelSeverity(7000) != "unknown" {
		t.Fatal("channel severity")
	}
}

func (m *memTX) snapshot() []TXRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]TXRow(nil), m.rows...)
}
