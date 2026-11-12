package demo

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/post"
)

func TestShiftMovesEveryTimeAndLeavesTheRestAlone(t *testing.T) {
	doc := []byte(`{"created_at":"2026-11-01T09:50:00Z","sent":"2026-11-01T09:40:00.5Z","zero":"0001-01-01T00:00:00Z",
		"rx_time":1793526600,"last_heard":1793526000,"count":1793526600,"name":"2026-11-01T09:50:00Z is a date",
		"list":[{"timestamp":1793526600,"snr":4.25}],"max_wait":1793526600000}`)
	out, err := Shift(doc, 11*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"created_at": "2026-11-12T09:50:00Z", "sent": "2026-11-12T09:40:00.5Z", "zero": "0001-01-01T00:00:00Z",
		"rx_time": float64(1793526600 + 11*86400), "last_heard": float64(1793526000 + 11*86400),
		"count": float64(1793526600), "name": "2026-11-01T09:50:00Z is a date", "max_wait": float64(1793526600000),
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s: %v, want %v", k, v[k], w)
		}
	}
	item := v["list"].([]any)[0].(map[string]any)
	if item["timestamp"] != float64(1793526600+11*86400) || item["snr"] != 4.25 {
		t.Errorf("list item: %v", item)
	}
}

func TestPassesAlwaysLieAheadAndAgreeBetweenRequests(t *testing.T) {
	for _, now := range []time.Time{time.Date(2026, 11, 12, 18, 3, 0, 0, time.UTC), time.Date(2027, 4, 30, 23, 59, 0, 0, time.UTC)} {
		a, b := Passes(now), Passes(now.Add(7*time.Second))
		if len(a.Passes) < 30 {
			t.Fatalf("%s: %d passes in a day", now, len(a.Passes))
		}
		if a.Passes[0].LOS <= now.Unix() {
			t.Fatalf("%s: the first pass is over", now)
		}
		if a.Passes[1].AOS != b.Passes[1].AOS || a.Passes[1].Satellite != b.Passes[1].Satellite {
			t.Fatal("two requests seconds apart must agree")
		}
		for _, p := range a.Passes {
			if p.LOS <= p.AOS || p.PeakElevDeg < 12 || p.PeakElevDeg > 78 || p.IsActive != (now.Unix() >= p.AOS) {
				t.Fatalf("bad pass %+v", p)
			}
		}
	}
}

func TestEveryFixtureLoadsAndShifts(t *testing.T) {
	f, err := LoadFixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Keys()) < 100 {
		t.Fatalf("%d fixtures", len(f.Keys()))
	}
	now := time.Now()
	for _, k := range f.Keys() {
		b, ok := f.At(k, now)
		if !ok || !json.Valid(b) {
			t.Errorf("%s: not served as JSON", k)
		}
	}
}

type env struct {
	t      *testing.T
	db     *database.DB
	svc    *post.Service
	sim    *Simulator
	clock  time.Time
	bridge *httptest.Server
	portal *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, db: db, clock: time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)}
	now := func() time.Time { return e.clock }
	e.svc = post.New(post.Config{Listen: ":0", PortalURL: "https://app.safertogether.eu/", Operator: "MeshSat (public demo)",
		Bearer: DemoInterface, PostsPerHour: 5, PostsPerDay: 20, BoardRetention: time.Hour, PurgeAfter: time.Hour, Demo: true,
		Ceiling: post.Ceiling{Posts: 60, Board: 30, Window: 10 * time.Minute, MaxRows: 500}},
		post.Deps{DB: db, Queue: &Queue{DB: db}, Bearer: func() (string, bool) { return DemoInterface, true }, Now: now})
	e.sim = &Simulator{Post: e.svc, DB: db, Now: now, ApproveAfter: 20 * time.Second, SendAfter: time.Minute,
		ReplyAfter: time.Minute, Retain: time.Hour}
	fx, err := LoadFixtures()
	if err != nil {
		t.Fatal(err)
	}
	spa, err := NewSPA(fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html><html><head><title>MeshSat</title></head><body class=\"x\"><div id=\"app\"></div><script>\n    if ('serviceWorker' in navigator) { navigator.serviceWorker.register('/service-worker.js') }\n  </script></body></html>")},
		"assets/index.js":   {Data: []byte("console.log(1)")},
		"logo.png":          {Data: []byte("png")},
		"service-worker.js": {Data: []byte("self")},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.bridge = httptest.NewServer(BridgeHandler(&API{Fixtures: fx, Post: e.svc, DB: db, Now: now}, spa))
	t.Cleanup(e.bridge.Close)
	e.portal = httptest.NewServer(PortalHandler(e.svc.Handler(), ParseNets([]string{"127.0.0.0/8"})))
	t.Cleanup(e.portal.Close)
	return e
}

func (e *env) do(srv *httptest.Server, method, path string, body io.Reader, headers ...string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, body)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

// The demo's API reads, refuses and stays shut: every write is a 405 with
// the demo's header and a JSON reason, streams are closed with 204, odd
// paths are a JSON 404, and a fixture answers for any id on its path.
func TestAPIReadsRefusesAndStaysShut(t *testing.T) {
	e := newEnv(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		res, body := e.do(e.bridge, m, "/api/messages/send", strings.NewReader(`{"text":"x"}`))
		if res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get(HeaderDemo) != "1" || !strings.Contains(body, ReadOnlyError) {
			t.Errorf("%s: %d %q %q", m, res.StatusCode, res.Header.Get(HeaderDemo), body)
		}
	}
	for _, p := range []string{"/api/events", "/api/spectrum/stream"} {
		if res, _ := e.do(e.bridge, http.MethodGet, p, nil); res.StatusCode != http.StatusNoContent {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	for _, p := range []string{"/api/nope", "/api/..%2f..%2fetc", "/api/a/" + strings.Repeat("x", 300), "/api/post/ABCDEF", "/api/oob/peers/1/key"} {
		res, body := e.do(e.bridge, http.MethodGet, p, nil)
		if res.StatusCode != http.StatusNotFound || !strings.Contains(body, `"error"`) {
			t.Errorf("%s: %d %q", p, res.StatusCode, body)
		}
	}
	res, body := e.do(e.bridge, http.MethodGet, "/api/status", nil)
	if res.StatusCode != http.StatusOK || !json.Valid([]byte(body)) || res.Header.Get("Content-Security-Policy") != SPACSP {
		t.Fatalf("status: %d %q", res.StatusCode, body)
	}
	res, body = e.do(e.bridge, http.MethodGet, "/api/iridium/passes?hours=24", nil)
	var passes PassesAnswer
	if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &passes) != nil || len(passes.Passes) == 0 || passes.Passes[0].LOS <= e.clock.Unix() {
		t.Fatalf("passes: %d %q", res.StatusCode, body)
	}
	for _, p := range []string{"/health", "/healthz"} {
		if res, body := e.do(e.bridge, http.MethodGet, p, nil); res.StatusCode != http.StatusOK || !strings.Contains(body, "healthy") {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	if res, body := e.do(e.portal, http.MethodGet, "/robots.txt", nil); res.StatusCode != http.StatusOK || !strings.Contains(body, "Disallow: /") ||
		res.Header.Get("X-Robots-Tag") == "" {
		t.Errorf("robots on the portal: %d %q", res.StatusCode, body)
	}
}

// The web app is served as built, with the demo's banner and its two files
// put in, the service worker's registration taken out, and a history route
// answered with the app; a missing asset is a 404, never the app.
func TestWebAppIsServedWithTheBannerAndNoWorker(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/", "/map", "/settings?tab=iridium"} {
		res, body := e.do(e.bridge, http.MethodGet, p, nil)
		if res.StatusCode != http.StatusOK || !strings.Contains(body, `id="meshsat-demo-banner"`) ||
			!strings.Contains(body, AssetPrefix+"demo.js") || strings.Contains(body, "serviceWorker") ||
			res.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: %d %q", p, res.StatusCode, body)
		}
		if strings.Index(body, `id="meshsat-demo-banner"`) > strings.Index(body, `id="app"`) {
			t.Errorf("%s: the banner must come before the app", p)
		}
	}
	if res, body := e.do(e.bridge, http.MethodGet, "/assets/index.js", nil); res.StatusCode != http.StatusOK || body != "console.log(1)" {
		t.Errorf("asset: %d %q", res.StatusCode, body)
	}
	for _, p := range []string{"/assets/missing.js", "/service-worker.js", AssetPrefix + "nope.js"} {
		if res, _ := e.do(e.bridge, http.MethodGet, p, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, res.StatusCode)
		}
	}
	for _, p := range []string{AssetPrefix + "demo.js", AssetPrefix + "demo.css"} {
		if res, body := e.do(e.bridge, http.MethodGet, p, nil); res.StatusCode != http.StatusOK || len(body) < 100 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	if res, _ := e.do(e.bridge, http.MethodPost, "/", nil); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d", res.StatusCode)
	}
}

var codeRe = regexp.MustCompile(`<div class="code">([A-Z2-9]{6})</div>`)

// A visitor writes on the portal; the demo volunteer approves it, the demo
// sky sends it, an automatic reply in the visitor's language comes back
// under the code; the public volunteer screen shows the post masked all
// along; an hour later it is gone. [MESHSAT-1845]
func TestAVisitorsPostGoesThroughMaskedAndIsGoneAfterAnHour(t *testing.T) {
	e := newEnv(t)
	form := url.Values{"to": {"+447700900123"}, "name": {"Maria Testpersoon"}, "text": {"Wir sind sicher an der Schule. Oma ist bei uns."}}
	res, body := e.do(e.portal, http.MethodPost, "/send?lang=de", strings.NewReader(form.Encode()),
		"Content-Type", "application/x-www-form-urlencoded", "X-Forwarded-For", "203.0.113.7")
	m := codeRe.FindStringSubmatch(body)
	if res.StatusCode != http.StatusOK || m == nil || !strings.Contains(body, post.T("de", "demo_notice")) {
		t.Fatalf("send: %d %q", res.StatusCode, body)
	}
	code := m[1]
	secrets := []string{code, "+447700900123", "Maria", "Testpersoon", "Schule", "Oma"}
	public := func(stage string) {
		t.Helper()
		for _, p := range []string{"/api/post/queue", "/api/post/board", "/api/post/config"} {
			_, b := e.do(e.bridge, http.MethodGet, p, nil)
			for _, s := range secrets {
				if strings.Contains(b, s) {
					t.Errorf("%s: %s shows %q", stage, p, s)
				}
			}
		}
	}
	status := func() string {
		_, b := e.do(e.portal, http.MethodGet, "/pickup?lang=de&code="+code, nil)
		return b
	}
	public("written")
	if !strings.Contains(status(), post.T("de", "status_pending")) {
		t.Fatal("a new post waits for the volunteer")
	}
	_, q := e.do(e.bridge, http.MethodGet, "/api/post/queue", nil)
	if !strings.Contains(q, code[:3]+"•••") || !strings.Contains(q, "+4477****23") || !strings.Contains(q, "47 characters") {
		t.Fatalf("masked queue: %s", q)
	}

	e.clock = e.clock.Add(25 * time.Second)
	e.sim.Step()
	if !strings.Contains(status(), post.T("de", "status_queued")) {
		t.Fatal("after the volunteer's 20 s the post waits for the satellite")
	}
	public("approved")
	e.clock = e.clock.Add(61 * time.Second)
	e.sim.Step()
	e.clock = e.clock.Add(61 * time.Second)
	e.sim.Step()
	page := status()
	if !strings.Contains(page, post.T("de", "demo_reply")) || !strings.Contains(page, post.T("de", "status_replied")) {
		t.Fatalf("the automatic reply must be on the pickup page in German: %s", page)
	}
	public("replied")
	var deliveries int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries WHERE text_preview LIKE '%Schule%' OR payload LIKE '%Schule%'`).Scan(&deliveries)
	if deliveries != 0 {
		t.Fatal("a delivery row keeps the visitor's words")
	}

	e.clock = e.clock.Add(time.Hour)
	e.sim.Step()
	if !strings.Contains(status(), post.T("de", "unknown_code")) {
		t.Fatal("an hour later the post is gone")
	}
	if n, _ := e.db.CountPostRows(); n != 0 {
		t.Fatalf("%d rows left after an hour", n)
	}
}

func TestBoardLinesAreMaskedOnTheVolunteerScreen(t *testing.T) {
	e := newEnv(t)
	form := url.Values{"name": {"Nikos Exampleidis"}, "status": {"safe at the harbour"}, "place": {"Pier 3"}}
	if _, body := e.do(e.portal, http.MethodPost, "/board?lang=en", strings.NewReader(form.Encode()),
		"Content-Type", "application/x-www-form-urlencoded"); !strings.Contains(body, post.T("en", "board_added")) {
		t.Fatalf("board add: %s", body)
	}
	_, b := e.do(e.bridge, http.MethodGet, "/api/post/board", nil)
	for _, s := range []string{"Nikos", "Exampleidis", "harbour", "Pier"} {
		if strings.Contains(b, s) {
			t.Errorf("the board shows %q: %s", s, b)
		}
	}
	if !strings.Contains(b, `"N. E."`) {
		t.Errorf("the line's initials are missing: %s", b)
	}
}

// The portal keys its per-phone ceilings on the visitor's address: read
// from X-Forwarded-For only when the ingress sent it, the rightmost
// untrusted hop, an IPv6 address by its /64.
func TestClientIPTrustsOnlyTheIngress(t *testing.T) {
	trusted := ParseNets([]string{"10.2.0.0/16"})
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct{ remote, xff, want string }{
		{"10.2.3.4:5000", "203.0.113.7", "203.0.113.7"},
		{"10.2.3.4:5000", "1.2.3.4, 203.0.113.7", "203.0.113.7"},  // a forged left entry is ignored
		{"10.2.3.4:5000", "203.0.113.7, 10.2.9.9", "203.0.113.7"}, // an inner proxy hop is skipped
		{"198.51.100.9:5000", "203.0.113.7", "198.51.100.9"},      // not from the ingress: the header is not read
		{"10.2.3.4:5000", "2001:db8:1:2:aaaa::1", "2001:db8:1:2::"},
		{"10.2.3.4:5000", "", "10.2.3.4"},
		{"10.2.3.4:5000", "not-an-ip", "10.2.3.4"},
	}
	for _, c := range cases {
		if got := ClientIP(req(c.remote, c.xff), trusted); got != c.want {
			t.Errorf("%s / %q: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
	var seen string
	h := WithClientIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.RemoteAddr }), trusted)
	h.ServeHTTP(httptest.NewRecorder(), req("10.2.3.4:5000", "203.0.113.7"))
	if host, _, _ := net.SplitHostPort(seen); host != "203.0.113.7" {
		t.Fatalf("the portal saw %q", seen)
	}
}

func TestMaskers(t *testing.T) {
	if MaskCode("K7QW2X") != "K7Q•••" || MaskCode("AB") != "•••" {
		t.Error("MaskCode")
	}
	if Initials("maria  van der berg") != "M. V. D." || Initials("  ") != "•" || Initials("Ελένη Παπά") != "Ε. Π." {
		t.Error("Initials")
	}
	if MaskedText("héllo") != "(a visitor's message, 5 characters)" || MaskedText("x") != "(a visitor's message, 1 character)" {
		t.Error("MaskedText")
	}
}
