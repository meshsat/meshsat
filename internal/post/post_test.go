package post

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/transport"
)

// ---- fakes ----

type queued struct {
	key, iface, text string
	opts             engine.DirectSendOptions
}

// fakeQueue is the dispatcher: it records what was queued and writes a
// delivery row the service's Tick can follow.
type fakeQueue struct {
	mu   sync.Mutex
	db   *database.DB
	rows []queued
	keys map[string]int64
	err  error
}

func (f *fakeQueue) QueueDirectSendKeyed(source, key, fp, iface, text string, opts engine.DirectSendOptions) (int64, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, "", false, f.err
	}
	if id, ok := f.keys[key]; ok {
		return id, "ref-" + key, true, nil
	}
	id, err := f.db.InsertDelivery(database.MessageDelivery{Channel: iface, Status: "queued", MsgRef: "ref-" + key,
		TextPreview: text, Class: opts.Class, Precedence: opts.Precedence, MaxRetries: 3})
	if err != nil {
		return 0, "", false, err
	}
	f.keys[key] = id
	f.rows = append(f.rows, queued{key: key, iface: iface, text: text, opts: opts})
	return id, "ref-" + key, false, nil
}

func (f *fakeQueue) last() queued {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rows) == 0 {
		return queued{}
	}
	return f.rows[len(f.rows)-1]
}

type env struct {
	t     *testing.T
	db    *database.DB
	q     *fakeQueue
	svc   *Service
	srv   *httptest.Server
	clock time.Time
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, db: db, q: &fakeQueue{db: db, keys: map[string]int64{}}, clock: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.PortalURL == "" {
		cfg.PortalURL = "http://10.44.0.1/"
	}
	e.svc = New(cfg, Deps{DB: db, Queue: e.q, Bearer: func() (string, bool) { return "iridium_0", true },
		Now: func() time.Time { return e.clock }})
	e.srv = httptest.NewServer(e.svc.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

// client is one phone: it keeps cookies and never follows redirects.
func (e *env) client() *http.Client {
	jar := &cookieJar{m: map[string]*http.Cookie{}}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type cookieJar struct {
	mu sync.Mutex
	m  map[string]*http.Cookie
}

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		j.m[c.Name] = c
	}
}

func (j *cookieJar) Cookies(_ *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.m))
	for _, c := range j.m {
		out = append(out, c)
	}
	return out
}

func (e *env) get(c *http.Client, path string, headers ...string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res, b.String()
}

func (e *env) post(c *http.Client, path string, form url.Values) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res, b.String()
}

var codeInPage = regexp.MustCompile(`<div class="code">([A-Z2-9]{6})</div>`)

func (e *env) write(c *http.Client, to, name, text string, emergency bool) (string, string) {
	e.t.Helper()
	form := url.Values{"to": {to}, "name": {name}, "text": {text}}
	if emergency {
		form.Set("emergency", "1")
	}
	res, body := e.post(c, "/send?lang=en", form)
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("send: %d", res.StatusCode)
	}
	m := codeInPage.FindStringSubmatch(body)
	if m == nil {
		return "", body
	}
	return m[1], body
}

// ---- codes, text, wire ----

func TestCodesAreSixCharactersOfTheAlphabetAndNormalise(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c := NewCode()
		if !ValidCode(c) {
			t.Fatalf("bad code %q", c)
		}
		if strings.ContainsAny(c, "ILOU01") {
			t.Fatalf("ambiguous character in %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 199 {
		t.Fatalf("codes repeat: %d distinct of 200", len(seen))
	}
	for in, want := range map[string]string{" k7qw2x ": "K7QW2X", "#K7QW2X": "K7QW2X", "K7Q W2X": "K7QW2X", "K7Q-W2X": "K7QW2X",
		"K7QW2": "", "K7QW2XX": "", "K7QW0X": "", "K7QWIX": ""} {
		if got := NormaliseCode(in); got != want {
			t.Errorf("NormaliseCode(%q) = %q, want %q", in, got, want)
		}
	}
	// Every six-character word of the alphabet is a candidate ("THANKS" is
	// one); the caller checks each against the codes it knows.
	if got := CodesIn("ok, ref #k7qw2x and also ABCDEF thanks, k7qw2x again"); len(got) != 3 || got[0] != "K7QW2X" || got[1] != "ABCDEF" || got[2] != "THANKS" {
		t.Errorf("CodesIn = %v", got)
	}
}

func TestRecipientsNumbersAndAddresses(t *testing.T) {
	cases := map[string]string{
		"+31 6 12345678":        "sms:+31612345678",
		"0031612345678":         "sms:+31612345678",
		"+30 (69) 123-4567":     "sms:+30691234567",
		"Ops@Example.ORG":       "email:ops@example.org",
		"0612345678":            "", // no country code
		"+1":                    "",
		"+31 6 1234 5678 extra": "",
		"not an address":        "",
		"a@b":                   "",
		"":                      "",
	}
	for in, want := range cases {
		r, err := ParseRecipient(in)
		got := ""
		if err == nil {
			got = r.Kind + ":" + r.Value
		}
		if got != want {
			t.Errorf("ParseRecipient(%q) = %q (%v), want %q", in, got, err, want)
		}
	}
}

func TestTextIsTidiedAndLinksRefused(t *testing.T) {
	if got, err := CleanText("  water\n at the\tnorth   gate ", 150, 240); err != nil || got != "water at the north gate" {
		t.Fatalf("clean = %q, %v", got, err)
	}
	for in, want := range map[string]error{
		"see http://example.org/x":   ErrLink,
		"go to www.example.org":      ErrLink,
		"mail me at evil.com/login":  ErrLink,
		"the site example.nl please": ErrLink,
		"ok. kom naar de school":     nil,
		"bel 06 12345678 a.u.b.":     nil,
		"":                           ErrEmpty,
		"   ":                        ErrEmpty,
		"bad\x00char":                ErrControl,
		strings.Repeat("x", 151):     ErrTooLong,
	} {
		if _, err := CleanText(in, 150, 240); !errors.Is(err, want) {
			t.Errorf("CleanText(%q) = %v, want %v", in, err, want)
		}
	}
	// The byte budget holds where the character count does not.
	if _, err := CleanText(strings.Repeat("ω", 130), 150, 240); !errors.Is(err, ErrTooLong) {
		t.Errorf("130 Greek letters are 260 bytes; got %v", err)
	}
	if _, err := CleanName("Maria: x"); !errors.Is(err, ErrName) {
		t.Error("a colon in a name must be refused")
	}
}

func TestComposeIsTheLanesForm(t *testing.T) {
	r, _ := ParseRecipient("+31 6 12345678")
	if got := Compose(r, "Maria", "water at the north gate", "K7QW2X", false); got != "@+31612345678 Maria: water at the north gate [#K7QW2X]" {
		t.Fatalf("compose = %q", got)
	}
	if got := Compose(r, "", "trapped, second floor", "K7QW2X", true); got != "@+31612345678 SOS trapped, second floor [#K7QW2X]" {
		t.Fatalf("emergency compose = %q", got)
	}
	e, _ := ParseRecipient("ops@example.org")
	if got := Compose(e, "Nikos", "need fuel", "ABCDEF", false); got != "@ops@example.org Nikos: need fuel [#ABCDEF]" {
		t.Fatalf("email compose = %q", got)
	}
}

func TestReplyParserReadsTheHubsForm(t *testing.T) {
	known := func(c string) bool { return c == "K7QW2X" }
	r, ok := ParseReply("[from +31612345678] #K7QW2X on my way", known)
	if !ok || r.From != "+31612345678" || r.Code != "K7QW2X" || r.Text != "on my way" {
		t.Fatalf("hub form: %+v %v", r, ok)
	}
	r, ok = ParseReply("[from ops@example.org] fuel is coming, ref k7qw2x", known)
	if !ok || r.Code != "K7QW2X" || r.Text != "fuel is coming, ref k7qw2x" {
		t.Fatalf("quoted code: %+v %v", r, ok)
	}
	r, ok = ParseReply("[from +31612345678] THANKS a lot", known)
	if !ok || r.Code != "" || r.Text != "THANKS a lot" {
		t.Fatalf("a six-letter word is not a code unless known: %+v", r)
	}
	for _, in := range []string{"on my way", "[MeshSat Hub] not sent to +31612345678: busy", "AgEDBAU=", "[from ] x", "from +316] x", "[from +31 6] x"} {
		if r, ok := ParseReply(in, known); ok {
			t.Errorf("%q must not parse as a reply: %+v", in, r)
		}
	}
	if r, ok := ParseReply("[from +316] ", known); !ok || r.Text != "" || r.Code != "" {
		t.Errorf("an empty reply still names its sender: %+v %v", r, ok)
	}
	if addr, reason, ok := NoticeFromHub("[MeshSat Hub] not sent to +31612345678: this device has sent its limit for this hour"); !ok ||
		addr != "+31612345678" || reason != "this device has sent its limit for this hour" {
		t.Fatalf("notice: %q %q %v", addr, reason, ok)
	}
}

// ---- the portal ----

func TestCaptiveDetectionBeforeAndAfterThePortalOpens(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.client()
	res, _ := e.get(c, "/generate_204")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "http://10.44.0.1/" {
		t.Fatalf("generate_204 before: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	res, _ = e.get(c, "/hotspot-detect.html")
	if res.StatusCode != http.StatusFound {
		t.Fatalf("hotspot-detect before: %d", res.StatusCode)
	}
	res, body := e.get(c, "/.well-known/captive-portal")
	if res.Header.Get("Content-Type") != "application/captive+json" || !strings.Contains(body, `"captive":true`) ||
		!strings.Contains(body, `"user-portal-url":"http://10.44.0.1/"`) || !strings.Contains(body, `"can-extend-session":false`) {
		t.Fatalf("capport before: %s %s", res.Header.Get("Content-Type"), body)
	}
	res, _ = e.get(c, "/anything/else?x=1")
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(res.Header.Get("Location"), "http://10.44.0.1/?lang=") {
		t.Fatalf("catch-all: %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	// The phone opens the portal: from then on its probes are answered.
	if res, _ := e.get(c, "/"); res.StatusCode != http.StatusOK {
		t.Fatalf("portal: %d", res.StatusCode)
	}
	if res, _ := e.get(c, "/generate_204"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("generate_204 after: %d", res.StatusCode)
	}
	if res, body := e.get(c, "/hotspot-detect.html"); res.StatusCode != http.StatusOK || !strings.Contains(body, "<TITLE>Success</TITLE>") {
		t.Fatalf("hotspot-detect after: %d %q", res.StatusCode, body)
	}
	if _, body := e.get(c, "/ncsi.txt"); body != "Microsoft NCSI" {
		t.Fatalf("ncsi after: %q", body)
	}
	if _, body := e.get(c, "/connecttest.txt"); body != "Microsoft Connect Test" {
		t.Fatalf("connecttest after: %q", body)
	}
	if res, _ := e.get(c, "/canonical.html"); res.StatusCode != http.StatusOK {
		t.Fatalf("canonical after: %d", res.StatusCode)
	}
	if _, body := e.get(c, "/.well-known/captive-portal"); !strings.Contains(body, `"captive":false`) {
		t.Fatalf("capport after: %s", body)
	}
	// The sign-in mark survives the service's memory (a restart).
	e.svc.signedIn = map[string]time.Time{}
	if res, _ := e.get(c, "/generate_204"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("generate_204 after a restart: %d", res.StatusCode)
	}
}

func TestPagesSpeakThreeLanguagesAndCarryNoScript(t *testing.T) {
	e := newEnv(t, Config{Operator: "Gemeente Leiden"})
	c := e.client()
	_, body := e.get(c, "/", "Accept-Language", "el-GR,el;q=0.9,en;q=0.8")
	if !strings.Contains(body, `<html lang="el">`) || !strings.Contains(body, "Κοινοτικό Ταχυδρομείο") {
		t.Fatalf("Greek by Accept-Language: %s", body[:200])
	}
	_, body = e.get(c, "/?lang=nl")
	if !strings.Contains(body, `<html lang="nl">`) || !strings.Contains(body, "Berichtenpost") {
		t.Fatal("Dutch by ?lang")
	}
	// The choice sticks in the cookie for the next page without ?lang.
	_, body = e.get(c, "/about")
	if !strings.Contains(body, `<html lang="nl">`) || !strings.Contains(body, "Gemeente Leiden") {
		t.Fatal("the language cookie or the operator's name is missing on the about page")
	}
	_, body = e.get(c, "/?lang=en")
	if !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, "Community Post") {
		t.Fatal("English by ?lang")
	}
	for _, path := range []string{"/", "/pickup", "/board", "/about", "/done"} {
		res, body := e.get(c, path+"?lang=en")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
		if strings.Contains(strings.ToLower(body), "<script") || strings.Contains(body, "http://") || strings.Contains(body, "https://") {
			t.Fatalf("%s carries a script or an outside reference", path)
		}
		if res.Header.Get("Cache-Control") != "no-store" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("%s: headers %v", path, res.Header)
		}
	}
	if _, body := e.get(c, "/?lang=xx"); !strings.Contains(body, `<html lang="en">`) {
		t.Fatal("an unknown language falls back to English")
	}
}

func TestWriteApproveSendAndPickUpTheReply(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.client()
	e.get(c, "/?lang=en") // the cookie
	code, body := e.write(c, "+31 6 12345678", "Maria", "water at the north gate", false)
	if code == "" {
		t.Fatalf("no code on the confirmation page: %s", body)
	}
	if !strings.Contains(body, "Take a screenshot") || !strings.Contains(body, "An operator at this post checks") {
		t.Fatal("the confirmation must say to screenshot the code and that the operator checks it")
	}
	if e.q.last().text != "" {
		t.Fatal("nothing leaves before the operator approves")
	}
	m, _, _ := e.svc.Status(code)
	if m.Status != database.PostStatusPending || m.Recipient != "+31612345678" || m.Name != "Maria" {
		t.Fatalf("post = %+v", m)
	}
	// The pickup page knows the phone's own posts by its cookie.
	if _, body := e.get(c, "/pickup?lang=en"); !strings.Contains(body, code) {
		t.Fatal("the phone's own post is not listed on the pickup page")
	}

	// The operator approves: one plain text in the lane's form, keyed by
	// the code so a second approve queues nothing twice.
	if err := e.svc.Approve(code); err != nil {
		t.Fatal(err)
	}
	q := e.q.last()
	want := "@+31612345678 Maria: water at the north gate [#" + code + "]"
	if q.text != want || q.iface != "iridium_0" || q.opts.Class != database.DeliveryClassPlain || q.opts.Precedence != "Priority" || q.opts.Critical {
		t.Fatalf("queued %+v, want %q plain Priority on iridium_0", q, want)
	}
	if err := e.svc.Approve(code); err != nil || len(e.q.rows) != 1 {
		t.Fatalf("approve twice: %v, %d rows", err, len(e.q.rows))
	}
	m, _, _ = e.svc.Status(code)
	if m.Status != database.PostStatusQueued || m.DeliveryID == 0 {
		t.Fatalf("after approve: %+v", m)
	}
	// The delivery goes out; Tick follows it.
	if _, err := e.db.Exec(`UPDATE message_deliveries SET status = 'sent' WHERE id = ?`, m.DeliveryID); err != nil {
		t.Fatal(err)
	}
	e.svc.Tick()
	m, _, _ = e.svc.Status(code)
	if m.Status != database.PostStatusSent {
		t.Fatalf("after Tick: %s", m.Status)
	}
	if _, body := e.get(c, "/pickup?code="+code+"&lang=en"); !strings.Contains(body, "sent, no reply yet") {
		t.Fatal("the pickup page must show the status")
	}

	// The reply comes back on the satellite link in the Hub's form.
	e.svc.HandleInbound("iridium_0", "rock7", "[from +31612345678] #"+code+" on my way, 20 min")
	_, body = e.get(c, "/pickup?code="+strings.ToLower(code)+"&lang=en")
	if !strings.Contains(body, "on my way, 20 min") || !strings.Contains(body, "reply received") {
		t.Fatalf("the reply is not on the pickup page: %s", body)
	}
	// A second reply with no code goes to the latest open post to that address.
	e.svc.HandleInbound("iridium_imt_0", "cloudloop", "[from +31612345678] still there?")
	_, replies, _ := e.svc.Status(code)
	if len(replies) != 2 || replies[1].Text != "still there?" {
		t.Fatalf("replies = %+v", replies)
	}
	// Unknown codes, other links and non-replies are dropped without a trace.
	e.svc.HandleInbound("iridium_0", "rock7", "[from +31612345678] #ZZZZZZ nobody")
	e.svc.HandleInbound("cellular_0", "+31612345678", "[from +31612345678] #"+code+" not over satellite")
	e.svc.HandleInbound("iridium_0", "rock7", "plain words from the Hub")
	if _, replies, _ := e.svc.Status(code); len(replies) != 2 {
		t.Fatalf("replies after junk = %+v", replies)
	}
	// The Hub's notice fails the post.
	e.svc.HandleInbound("iridium_0", "rock7", "[MeshSat Hub] not sent to +31612345678: this account has no SMS provider (Settings, Integrations)")
	m, replies, _ = e.svc.Status(code)
	if m.Status != database.PostStatusFailed || len(replies) != 3 || !strings.HasPrefix(replies[2].Text, "not sent: ") {
		t.Fatalf("after the notice: %s %+v", m.Status, replies)
	}
	// A code the kit never gave out is said so on the pickup page.
	if _, body := e.get(c, "/pickup?code=ZZZZZZ&lang=en"); !strings.Contains(body, "not known at this post") {
		t.Fatal("unknown code")
	}
}

func TestEmergencyAutoApproveAndRefuse(t *testing.T) {
	e := newEnv(t, Config{AutoApprove: true})
	c := e.client()
	code, _ := e.write(c, "ops@example.org", "", "trapped, second floor", true)
	if code == "" {
		t.Fatal("no code")
	}
	q := e.q.last()
	if q.text != "@ops@example.org SOS trapped, second floor [#"+code+"]" || !q.opts.Critical || q.opts.Precedence != "Flash" {
		t.Fatalf("emergency: %+v", q)
	}
	m, _, _ := e.svc.Status(code)
	if m.Status != database.PostStatusQueued || m.Class != database.PostClassEmergency {
		t.Fatalf("post = %+v", m)
	}
	// The queue is back on; a post waits, and a refused one never leaves.
	if err := e.svc.SetSettings(Settings{AutoApprove: false, PostsPerHour: 3, PostsPerDay: 10}); err != nil {
		t.Fatal(err)
	}
	code2, _ := e.write(c, "+31612345678", "", "second", false)
	pending, _ := e.svc.Queue(10)
	if len(pending) != 1 || pending[0].Code != code2 {
		t.Fatalf("queue = %+v", pending)
	}
	if err := e.svc.Refuse(code2); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Approve(code2); err == nil {
		t.Fatal("a refused post must not be approvable")
	}
	if len(e.q.rows) != 1 {
		t.Fatal("a refused post left the kit")
	}
	if _, body := e.get(c, "/pickup?code="+code2+"&lang=en"); !strings.Contains(body, "refused by the operator") {
		t.Fatal("the pickup page must say it was refused")
	}
}

func TestFormRefusalsAndQuotas(t *testing.T) {
	e := newEnv(t, Config{PostsPerHour: 2, PostsPerDay: 3})
	c := e.client()
	e.get(c, "/?lang=en")
	cases := []struct{ to, text, want string }{
		{"0612345678", "hi", "country code"},
		{"+31612345678", "", "Write a message first"},
		{"+31612345678", "see http://x.y/z", "Links cannot be sent"},
		{"+31612345678", strings.Repeat("a", 151), "too long"},
	}
	for _, tc := range cases {
		_, body := e.post(c, "/send?lang=en", url.Values{"to": {tc.to}, "text": {tc.text}})
		if !strings.Contains(body, tc.want) || codeInPage.MatchString(body) {
			t.Errorf("to=%q text=%q: want %q in the page", tc.to, tc.text, tc.want)
		}
	}
	if len(e.q.rows) != 0 {
		t.Fatal("a refused form queued something")
	}
	// Two an hour for this phone, then the third is refused; the clock
	// moving to the next hour frees one more, the day's ceiling then holds.
	if code, _ := e.write(c, "+31612345678", "", "one", false); code == "" {
		t.Fatal("first")
	}
	if code, _ := e.write(c, "+31612345678", "", "two", false); code == "" {
		t.Fatal("second")
	}
	if code, body := e.write(c, "+31612345678", "", "three", false); code != "" || !strings.Contains(body, "sent its share") {
		t.Fatal("the third post in the hour must be refused")
	}
	e.clock = e.clock.Add(time.Hour)
	if code, _ := e.write(c, "+31612345678", "", "three", false); code == "" {
		t.Fatal("the next hour frees a slot")
	}
	if code, _ := e.write(c, "+31612345678", "", "four", false); code != "" {
		t.Fatal("the day's ceiling must hold")
	}
	// Another phone has its own counters.
	if code, _ := e.write(e.client(), "+31612345678", "", "other phone", false); code == "" {
		t.Fatal("another phone is not held by the first one's counters")
	}
	// A ceiling of 0 is none.
	if err := e.svc.SetSettings(Settings{PostsPerHour: 0, PostsPerDay: 0}); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.write(c, "+31612345678", "", "five", false); code == "" {
		t.Fatal("no ceiling")
	}
}

func TestSafeAndWellBoard(t *testing.T) {
	e := newEnv(t, Config{BoardRetention: 48 * time.Hour})
	c := e.client()
	_, body := e.post(c, "/board?lang=en", url.Values{"name": {"Maria Papadaki"}, "status": {"safe"}, "place": {"the school"}})
	if !strings.Contains(body, "You are on the board") {
		t.Fatalf("add: %s", body)
	}
	_, body = e.post(c, "/board?lang=en", url.Values{"name": {"Nikos"}, "status": {"looking for Maria"}})
	if !strings.Contains(body, "You are on the board") {
		t.Fatal("second add")
	}
	_, body = e.get(c, "/board?lang=en&q=papadaki")
	if !strings.Contains(body, "Maria Papadaki") || !strings.Contains(body, "the school") || strings.Contains(body, "Nikos") {
		t.Fatalf("search: %s", body)
	}
	if _, body := e.get(c, "/board?lang=en&q=nobody"); !strings.Contains(body, "Nobody by that name") {
		t.Fatal("empty search")
	}
	if _, body := e.post(c, "/board?lang=en", url.Values{"name": {"x"}, "status": {"see www.example.org"}}); !strings.Contains(body, "Links cannot") {
		t.Fatal("a link on the board must be refused")
	}
	// Lines expire with the retention.
	e.clock = e.clock.Add(49 * time.Hour)
	if _, body := e.get(c, "/board?lang=en&q=maria"); !strings.Contains(body, "Nobody by that name") {
		t.Fatal("an expired line is still shown")
	}
}

func TestApproveNeedsABearerAndTheFormRefusesABadMethod(t *testing.T) {
	e := newEnv(t, Config{})
	e.svc.deps.Bearer = func() (string, bool) { return "iridium_0", false }
	c := e.client()
	code, _ := e.write(c, "+31612345678", "", "hello", false)
	if err := e.svc.Approve(code); err == nil || !strings.Contains(err.Error(), "no satellite link") {
		t.Fatalf("approve without a bearer: %v", err)
	}
	if res, _ := e.get(c, "/send"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /send: %d", res.StatusCode)
	}
	if err := e.svc.Approve("ZZZZZZ"); !errors.Is(err, ErrUnknownCode) {
		t.Fatalf("unknown code: %v", err)
	}
}

// ---- slips, closing and purging ----

// fakePrinter is the receipt printer: it records the slips it was handed.
type fakePrinter struct {
	mu    sync.Mutex
	slips []Slip
}

func (f *fakePrinter) PrintSlip(s Slip) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slips = append(f.slips, s)
}

func (f *fakePrinter) all() []Slip {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Slip(nil), f.slips...)
}

func TestMaskRecipientAndExcerpt(t *testing.T) {
	for in, want := range map[string]string{"+31612345678": "+3161****78", "+306912345678": "+3069****78", "+1234567": "+12****67",
		"+123": "****", "maria@example.org": "m***@example.org", "ops@example.org": "o***@example.org"} {
		if got := MaskRecipient(in); got != want {
			t.Errorf("MaskRecipient(%q) = %q, want %q", in, got, want)
		}
	}
	if got := excerpt("short", 100); got != "short" {
		t.Errorf("excerpt short = %q", got)
	}
	long := strings.Repeat("ω", 120)
	if got := excerpt(long, 100); got != strings.Repeat("ω", 100)+"..." {
		t.Errorf("excerpt long = %q", got)
	}
}

func TestSlipsPrintOnApproveAndOnReply(t *testing.T) {
	e := newEnv(t, Config{Print: true, Operator: "Gemeente Leiden"})
	pr := &fakePrinter{}
	e.svc.deps.Printer = pr
	c := e.client()
	code, _ := e.write(c, "+31 6 12345678", "Maria", "water at the north gate", false)
	if code == "" {
		t.Fatal("no code")
	}
	if len(pr.all()) != 0 {
		t.Fatal("a post that is only written prints nothing")
	}
	if err := e.svc.Approve(code); err != nil {
		t.Fatal(err)
	}
	slips := pr.all()
	if len(slips) != 1 {
		t.Fatalf("after approve: %d slips", len(slips))
	}
	s := slips[0]
	if s.Kind != SlipPost || s.Code != code || s.To != "+3161****78" || s.URL != "http://10.44.0.1/" ||
		s.Pickup != "http://10.44.0.1/pickup?code="+code || s.At != "2026-10-28T12:00:00Z" || s.Emergency || s.Text != "" || s.Operator != "Gemeente Leiden" {
		t.Fatalf("post slip = %+v", s)
	}
	// A second approve queues nothing twice and prints nothing twice.
	if err := e.svc.Approve(code); err != nil {
		t.Fatal(err)
	}
	if len(pr.all()) != 1 {
		t.Fatal("approving twice printed twice")
	}
	// The reply prints its first 100 characters.
	long := strings.Repeat("abcdefghij", 12)
	e.svc.HandleInbound("iridium_0", "rock7", "[from +31612345678] #"+code+" "+long)
	slips = pr.all()
	if len(slips) != 2 || slips[1].Kind != SlipReply || slips[1].Code != code || slips[1].To != "+3161****78" ||
		slips[1].Text != strings.Repeat("abcdefghij", 10)+"..." {
		t.Fatalf("reply slip = %+v", slips[len(slips)-1])
	}
	// The Hub's "not sent" notice is a reply too, and prints.
	e.svc.HandleInbound("iridium_0", "rock7", "[MeshSat Hub] not sent to +31612345678: no SMS provider")
	if slips = pr.all(); len(slips) != 3 || slips[2].Text != "not sent: no SMS provider" {
		t.Fatalf("notice slip = %+v", slips)
	}
	// An emergency is marked on the slip; without a printer nothing is handed out.
	code2, _ := e.write(c, "ops@example.org", "", "trapped", true)
	if err := e.svc.Approve(code2); err != nil {
		t.Fatal(err)
	}
	if slips = pr.all(); len(slips) != 4 || !slips[3].Emergency || slips[3].To != "o***@example.org" {
		t.Fatalf("emergency slip = %+v", slips[len(slips)-1])
	}
	e.svc.deps.Printer = nil
	e.svc.HandleInbound("iridium_0", "rock7", "[from ops@example.org] #"+code2+" coming")
	if len(pr.all()) != 4 {
		t.Fatal("printed with no printer")
	}
}

func TestEventPrinterEmitsAPostSlipEvent(t *testing.T) {
	var got []transport.MeshEvent
	p := EventPrinter{Emit: func(ev transport.MeshEvent) { got = append(got, ev) }}
	p.PrintSlip(Slip{Kind: SlipReply, Code: "K7QW2X", To: "+3161****78", At: "2026-10-28T12:00:00Z", URL: "http://10.44.0.1/", Text: "ok"})
	if len(got) != 1 || got[0].Type != SlipEventType || got[0].Time != "2026-10-28T12:00:00Z" || !strings.Contains(got[0].Message, "K7QW2X") {
		t.Fatalf("event = %+v", got)
	}
	var back Slip
	if err := json.Unmarshal(got[0].Data, &back); err != nil || back.Kind != SlipReply || back.Code != "K7QW2X" || back.Text != "ok" {
		t.Fatalf("data = %s (%v)", got[0].Data, err)
	}
	EventPrinter{}.PrintSlip(Slip{}) // no emitter: nothing happens
}

func TestCloseThenPurgeAfterTheDays(t *testing.T) {
	e := newEnv(t, Config{PurgeAfter: 30 * 24 * time.Hour})
	c := e.client()
	e.get(c, "/?lang=en")
	code, _ := e.write(c, "+31612345678", "Maria", "water", false)
	if err := e.svc.Approve(code); err != nil {
		t.Fatal(err)
	}
	e.svc.HandleInbound("iridium_0", "rock7", "[from +31612345678] #"+code+" on my way")
	if _, body := e.post(c, "/board?lang=en", url.Values{"name": {"Maria"}, "status": {"safe"}}); !strings.Contains(body, "You are on the board") {
		t.Fatal("board add")
	}
	if st := e.svc.State(); st.Closed || !st.PurgeAt.IsZero() {
		t.Fatalf("open state = %+v", st)
	}

	// The operator closes the post.
	st, err := e.svc.Close()
	if err != nil || !st.Closed || !st.ClosedAt.Equal(e.clock) || !st.PurgeAt.Equal(e.clock.Add(30*24*time.Hour)) {
		t.Fatalf("close = %+v %v", st, err)
	}
	if st2, _ := e.svc.Close(); !st2.ClosedAt.Equal(st.ClosedAt) {
		t.Fatal("closing twice moved the purge")
	}
	// The portal takes no new post and no board line; a reply is still picked up.
	if _, body := e.get(c, "/?lang=en"); !strings.Contains(body, "This post is closed") || !strings.Contains(body, "2026-11-27") || strings.Contains(body, "<form") {
		t.Fatalf("write page while closed: %s", body)
	}
	if code2, body := e.write(c, "+31612345678", "", "late", false); code2 != "" || !strings.Contains(body, "This post is closed") {
		t.Fatal("a post was taken while closed")
	}
	if _, body := e.post(c, "/board?lang=en", url.Values{"name": {"Nikos"}, "status": {"safe"}}); strings.Contains(body, "You are on the board") || !strings.Contains(body, "This post is closed") {
		t.Fatal("a board line was taken while closed")
	}
	if _, body := e.get(c, "/board?lang=en&q=maria"); !strings.Contains(body, "Maria") {
		t.Fatal("the board is not searchable while closed")
	}
	if _, body := e.get(c, "/pickup?code="+code+"&lang=en"); !strings.Contains(body, "on my way") {
		t.Fatal("a reply is not picked up while closed")
	}
	if _, body := e.get(c, "/about?lang=en"); !strings.Contains(body, "Deleted 30 days after the operator closes") || !strings.Contains(body, "a line stays 30 days") {
		t.Fatalf("the notice does not say what happens: %s", body)
	}

	// The sweep waits for the day, then purges once.
	e.clock = e.clock.Add(29 * 24 * time.Hour)
	e.svc.Tick()
	if m, _, _ := e.svc.Status(code); m == nil {
		t.Fatal("purged a day early")
	}
	e.clock = e.clock.Add(24 * time.Hour)
	e.svc.Tick()
	if m, replies, _ := e.svc.Status(code); m != nil || len(replies) != 0 {
		t.Fatalf("not purged: %+v %+v", m, replies)
	}
	if entries, _ := e.svc.SearchBoard(""); len(entries) != 0 {
		t.Fatalf("the board survived the purge: %+v", entries)
	}
	if sess, _ := e.db.GetPostSession("ip:127.0.0.1"); sess != nil {
		t.Fatal("the phones' counters survived the purge")
	}
	if res, _ := e.get(c, "/generate_204"); res.StatusCode != http.StatusFound {
		t.Fatal("a purged phone is still signed in")
	}
	if st := e.svc.State(); !st.Closed || st.PurgedAt.IsZero() || st.PurgedAt.Before(st.ClosedAt) {
		t.Fatalf("state after the sweep = %+v", st)
	}
	purged := e.svc.State().PurgedAt
	e.clock = e.clock.Add(time.Hour)
	e.svc.Tick()
	if !e.svc.State().PurgedAt.Equal(purged) {
		t.Fatal("the sweep purged twice for one closing")
	}
	if _, body := e.get(c, "/pickup?code="+code+"&lang=en"); !strings.Contains(body, "not known at this post") {
		t.Fatal("a purged code is still known")
	}

	// Reopened, the post takes posts again; closed again, it waits again.
	if st, err := e.svc.Open(); err != nil || st.Closed || !st.PurgedAt.IsZero() {
		t.Fatalf("open = %+v %v", st, err)
	}
	code3, _ := e.write(c, "+31612345678", "", "again", false)
	if code3 == "" {
		t.Fatal("no post after reopening")
	}
	if _, err := e.svc.Close(); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(24 * time.Hour)
	e.svc.Tick()
	if m, _, _ := e.svc.Status(code3); m == nil {
		t.Fatal("the second closing purged at once")
	}
	// The operator purges now; the post stays closed.
	n, err := e.svc.Purge()
	if err != nil || n.Messages != 1 {
		t.Fatalf("purge = %+v %v", n, err)
	}
	if m, _, _ := e.svc.Status(code3); m != nil || !e.svc.State().Closed {
		t.Fatal("purge now")
	}
}

func TestPurgeNowOnAnOpenPostKeepsItOpen(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.client()
	code, _ := e.write(c, "+31612345678", "", "one", false)
	e.post(c, "/board?lang=en", url.Values{"name": {"Maria"}, "status": {"safe"}})
	n, err := e.svc.Purge()
	if err != nil || n.Messages != 1 || n.Board != 1 || n.Sessions == 0 {
		t.Fatalf("purge = %+v %v", n, err)
	}
	if m, _, _ := e.svc.Status(code); m != nil || e.svc.State().Closed {
		t.Fatal("after the purge")
	}
	if code, _ := e.write(c, "+31612345678", "", "two", false); code == "" {
		t.Fatal("an open post takes posts after a purge")
	}
}

func TestPrinterReportAgesOut(t *testing.T) {
	e := newEnv(t, Config{Print: true})
	if st := e.svc.PrinterState(); !st.Print || st.Heard || st.Report != nil {
		t.Fatalf("no report yet: %+v", st)
	}
	e.svc.ReportPrinter(PrinterReport{Service: "meshsat-receipt-printer", Printer: "5A:4A:A1:18:4A:B9", Count: 42})
	if st := e.svc.PrinterState(); !st.Heard || st.Report == nil || st.Report.Count != 42 || st.AgeSeconds != 0 {
		t.Fatalf("fresh report: %+v", st)
	}
	e.clock = e.clock.Add(PrinterStaleAfter + time.Second)
	if st := e.svc.PrinterState(); st.Heard || st.AgeSeconds < int(PrinterStaleAfter.Seconds()) {
		t.Fatalf("stale report: %+v", st)
	}
}

// The public warnings in force at this kit stand at the top of every page
// (MESHSAT-1734): the event and severity, until when, the words, the label
// the mesh carries, TEST for a test; a cancelled, replaced or expired one is
// not shown; the three languages have their own words around it.
func TestWarningsInForceStandAtTheTopOfEveryPage(t *testing.T) {
	e := newEnv(t, Config{})
	c := e.client()
	if _, body := e.get(c, "/?lang=en"); strings.Contains(body, `class="warn"`) {
		t.Fatal("a banner with no warning")
	}
	sent := e.clock.Add(-2 * time.Hour)
	rows := []*database.Warning{
		{ID: "f161d743dd14e974", MsgType: "Alert", Source: "meteoalarm-nl", Sent: sent, Expires: e.clock.Add(22 * time.Hour), Severity: "Moderate",
			Event: "Wind", Language: "nl", Text: "Kans op windstoten tot 90 km/u", Via: "hub", ReceivedAt: sent},
		{ID: "0101010101010101", MsgType: "Update", Ref: "0000000000000001", Test: true, Source: "test", Sent: sent.Add(time.Minute), Severity: "Extreme",
			Event: "Flood", Text: "Oefening, geen echte waarschuwing", Via: "iridium_0", ReceivedAt: sent},
		{ID: "0202020202020202", MsgType: "Alert", Source: "dwd", Sent: sent, Expires: e.clock.Add(-time.Minute), Severity: "Severe", Event: "Rain",
			Text: "voorbij", Via: "hub", ReceivedAt: sent},
		{ID: "0303030303030303", MsgType: "Alert", Source: "dwd", Sent: sent, Expires: e.clock.Add(time.Hour), Severity: "Minor", Event: "Fog",
			Text: "ingetrokken", Via: "hub", ReceivedAt: sent, Cancelled: true},
	}
	for _, w := range rows {
		if err := e.db.InsertWarning(w); err != nil {
			t.Fatal(err)
		}
	}
	label := "Relay of KNMI via MeteoAlarm warning, issued 28 Oct 10:00 UTC via MeshSat, not an official NL-Alert"
	for _, path := range []string{"/", "/pickup", "/board", "/about", "/done"} {
		res, body := e.get(c, path+"?lang=en")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
		for _, want := range []string{`class="warn" role="alert"`, "Public warning", "<strong>Wind Moderate</strong>", "until 10:00 UTC",
			"Kans op windstoten tot 90 km/u", label, "TEST", "Update", "<strong>Flood Extreme</strong>", "Oefening, geen echte waarschuwing",
			"Relay of MeshSat test warning"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s lacks %q:\n%s", path, want, body)
			}
		}
		for _, no := range []string{"voorbij", "ingetrokken", "Rain Severe", "Fog Minor"} {
			if strings.Contains(body, no) {
				t.Fatalf("%s shows a warning no longer in force (%q)", path, no)
			}
		}
		// Newest sent first: the test above the wind warning; and the
		// banner right under the navigation, above the page's own content.
		if strings.Index(body, "Flood Extreme") > strings.Index(body, "Wind Moderate") {
			t.Fatalf("%s: the order is not newest first", path)
		}
		if nav := strings.Index(body, "</nav>"); nav < 0 || strings.Index(body, `class="warn"`) < nav {
			t.Fatalf("%s: the banner is not right under the navigation", path)
		}
		if strings.Contains(strings.ToLower(body), "<script") || strings.Contains(body, "http://") || strings.Contains(body, "https://") {
			t.Fatalf("%s carries a script or an outside reference", path)
		}
	}
	// The three languages.
	if _, body := e.get(c, "/?lang=nl"); !strings.Contains(body, "Publieke waarschuwing") || !strings.Contains(body, "tot 10:00 UTC") || !strings.Contains(body, label) {
		t.Fatal("Dutch banner")
	}
	if _, body := e.get(c, "/?lang=el"); !strings.Contains(body, "Δημόσια προειδοποίηση") || !strings.Contains(body, "ΔΟΚΙΜΗ") || !strings.Contains(body, "έως 10:00 UTC") {
		t.Fatal("Greek banner")
	}
	// A closed post still shows it.
	if _, err := e.svc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, body := e.get(c, "/?lang=en"); !strings.Contains(body, "This post is closed") || !strings.Contains(body, "Kans op windstoten") {
		t.Fatal("the closed page lost the banner")
	}
	// Time passes: the wind warning expires and the test one, which names
	// no expiry, ages out after a day; the banner goes.
	e.clock = e.clock.Add(23 * time.Hour)
	if _, body := e.get(c, "/?lang=en"); strings.Contains(body, `class="warn"`) {
		t.Fatal("an expired warning is still on the banner")
	}
}
