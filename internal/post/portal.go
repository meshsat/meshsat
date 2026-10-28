package post

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/warnings"
)

// The portal: plain HTML, no script, no outside asset, forms that POST. It
// lives on its own listener (Config.Listen) and never on the Bridge's API
// port, which is unauthenticated and bound on every interface (MESHSAT-1738).
//
// Captive-portal detection: a phone that joined the open WiFi probes a known
// URL; until the phone has opened the portal the probe is answered with a
// redirect to it (Android shows "sign in to network", iOS opens its captive
// network assistant, Windows its sign-in window), afterwards with the
// answer the phone expects, so it stops nagging and keeps the WiFi. RFC 8908
// gives the same in JSON for phones that ask (DHCP option 114 names it). The
// detection is keyed by the phone's address: its probes carry no cookie.

const (
	sessionCookie  = "msp"
	languageCookie = "msp_lang"
	maxFormBytes   = 8 << 10
)

// Handler is the portal's HTTP handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		mux.ServeHTTP(w, r)
	})
}

// Detection URLs the phones probe, and what a signed-in phone expects back.
var detection = map[string]struct {
	body        string
	contentType string
	noContent   bool
}{
	"/generate_204":              {noContent: true},                                                                                        // Android, Chrome
	"/gen_204":                   {noContent: true},                                                                                        // Android
	"/hotspot-detect.html":       {body: "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>", contentType: "text/html"}, // iOS, macOS
	"/library/test/success.html": {body: "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>", contentType: "text/html"}, // iOS, macOS
	"/ncsi.txt":                  {body: "Microsoft NCSI", contentType: "text/plain"},                                                      // Windows
	"/connecttest.txt":           {body: "Microsoft Connect Test", contentType: "text/plain"},                                              // Windows
	"/canonical.html":            {body: "<!doctype html><html><body><p>Success</p></body></html>", contentType: "text/html"},              // Ubuntu
	"/success.txt":               {body: "success\n", contentType: "text/plain"},                                                           // Firefox
	"/check_network_status.txt":  {body: "NetworkManager is online\n", contentType: "text/plain"},                                          // NetworkManager
	"/nm-check.txt":              {body: "NetworkManager is online\n", contentType: "text/plain"},                                          // NetworkManager
}

func (s *Service) route(w http.ResponseWriter, r *http.Request) {
	path := strings.ToLower(r.URL.Path)
	if d, ok := detection[path]; ok {
		s.detect(w, r, d.body, d.contentType, d.noContent)
		return
	}
	switch path {
	case "/.well-known/captive-portal":
		s.capport(w, r)
	case "/":
		s.write(w, r)
	case "/send":
		s.send(w, r)
	case "/pickup":
		s.pickup(w, r)
	case "/board":
		s.board(w, r)
	case "/about":
		s.about(w, r)
	case "/done":
		s.done(w, r)
	default:
		// Every name resolves to this post; every other page is the portal.
		http.Redirect(w, r, s.cfg.PortalURL+"?lang="+s.lang(w, r, false), http.StatusFound)
	}
}

// ipKey is the phone's address, the key the captive detection and the
// quota fallback use.
func ipKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// clientKey is the phone's session: its cookie when it sends one, else its
// address. The cookie is set on the first page; a phone whose captive
// browser keeps none (iOS) works on the address alone.
func (s *Service) clientKey(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil && len(c.Value) == 32 {
		if _, err := hex.DecodeString(c.Value); err == nil {
			return "c:" + c.Value
		}
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ipKey(r)
	}
	v := hex.EncodeToString(b[:])
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: v, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode, HttpOnly: true})
	// This request still counts under the address: the cookie only exists
	// from the next one on, and the first post must not be free of a key.
	return ipKey(r)
}

// lang chooses the page's language: ?lang, the cookie, Accept-Language.
// With set, a ?lang choice is kept in the cookie.
func (s *Service) lang(w http.ResponseWriter, r *http.Request, set bool) string {
	if l := strings.ToLower(r.URL.Query().Get("lang")); KnownLanguage(l) {
		if set {
			http.SetCookie(w, &http.Cookie{Name: languageCookie, Value: l, Path: "/", MaxAge: 86400 * 30, SameSite: http.SameSiteLaxMode})
		}
		return l
	}
	if c, err := r.Cookie(languageCookie); err == nil && KnownLanguage(c.Value) {
		return c.Value
	}
	return PickLanguage(r.Header.Get("Accept-Language"))
}

func (s *Service) detect(w http.ResponseWriter, r *http.Request, body, contentType string, noContent bool) {
	if !s.SignedIn(ipKey(r)) {
		http.Redirect(w, r, s.cfg.PortalURL, http.StatusFound)
		return
	}
	if noContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

// capport is RFC 8908: the captive portal API a phone reads from DHCP
// option 114.
func (s *Service) capport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/captive+json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"captive":            !s.SignedIn(ipKey(r)),
		"user-portal-url":    s.cfg.PortalURL,
		"can-extend-session": false,
	})
}

// page is what every template sees.
type page struct {
	Lang      string
	Q         string // "?lang=xx", carried on every link and form
	Langs     []langLink
	S         map[string]string
	Path      string
	Title     string
	Err       string
	Form      map[string]string
	Post      *database.PostMessage
	Replies   []database.PostReply
	Mine      []database.PostMessage
	Board     []database.PostBoardEntry
	Query     string
	Added     bool
	Operator  string
	MaxChars  int
	BoardDays int
	Status    string
	Emergency bool
	Closed    bool   // the operator closed the post
	PurgeDate string // the day everything is deleted, while closed
	PurgeDays int
	// Warnings are the public warnings in force at this kit (the Hub's
	// last-mile relay, MESHSAT-1734), shown at the top of every page.
	Warnings []warningBanner
}

type langLink struct{ Code, Name, Href string }

// warningBanner is one relayed warning as the banner shows it: the event
// and severity as the digest names them (English, as on the mesh), the
// words, until when, and the label line the mesh carries. A test is marked;
// a cancelled, replaced or expired warning is not shown at all.
type warningBanner struct {
	Test     bool
	Update   bool
	Event    string
	Severity string
	Until    string // "HH:MM UTC", "" when the warning names no expiry
	Text     string
	Label    string
}

// bannerWarnings reads the warnings in force through the warnings package.
func (s *Service) bannerWarnings() []warningBanner {
	list, err := warnings.InForce(s.deps.DB, s.now())
	if err != nil {
		log.Warn().Err(err).Msg("post: warnings in force unreadable, no banner")
		return nil
	}
	var out []warningBanner
	for _, w := range list {
		b := warningBanner{Test: w.Test, Update: w.MsgType == "Update", Event: w.Event, Severity: w.Severity, Text: w.Text,
			Label: warnings.LabelFor(w.Source, w.Sent)}
		if !w.Expires.IsZero() {
			b.Until = w.Expires.UTC().Format("15:04") + " UTC"
		}
		out = append(out, b)
	}
	return out
}

func (s *Service) newPage(w http.ResponseWriter, r *http.Request, path string) *page {
	lang := s.lang(w, r, true)
	st := s.Settings()
	p := &page{Lang: lang, Q: "?lang=" + lang, S: merged(lang), Path: path, Form: map[string]string{},
		Operator: st.Operator, MaxChars: s.cfg.MaxTextChars, BoardDays: int(s.cfg.BoardRetention.Hours() / 24),
		PurgeDays: int(s.cfg.PurgeAfter.Hours() / 24)}
	if p.Operator == "" {
		p.Operator = "(not set)"
	}
	if state := s.State(); state.Closed {
		p.Closed, p.PurgeDate = true, state.PurgeAt.UTC().Format("2006-01-02")
	}
	p.Warnings = s.bannerWarnings()
	for _, l := range Languages {
		p.Langs = append(p.Langs, langLink{Code: l, Name: T(l, "lang_name"), Href: path + "?lang=" + l})
	}
	p.Title = p.S["title"]
	return p
}

func merged(lang string) map[string]string {
	out := map[string]string{}
	for k, v := range strs[DefaultLanguage] {
		out[k] = v
	}
	for k, v := range strs[lang] {
		out[k] = v
	}
	return out
}

func (s *Service) render(w http.ResponseWriter, name string, p *page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, p); err != nil {
		log.Error().Err(err).Str("template", name).Msg("post: render failed")
	}
}

func (s *Service) write(w http.ResponseWriter, r *http.Request) {
	s.SignIn(ipKey(r))
	s.clientKey(w, r)
	p := s.newPage(w, r, "/")
	if p.Closed {
		s.render(w, "closed", p)
		return
	}
	s.render(w, "write", p)
}

func (s *Service) send(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/"+"?lang="+s.lang(w, r, false), http.StatusSeeOther)
		return
	}
	s.SignIn(ipKey(r))
	key := s.clientKey(w, r)
	p := s.newPage(w, r, "/")
	if p.Closed {
		s.render(w, "closed", p)
		return
	}
	if err := r.ParseForm(); err != nil {
		p.Err = p.S["err_other"]
		s.render(w, "write", p)
		return
	}
	in := Submission{Recipient: r.PostForm.Get("to"), Text: r.PostForm.Get("text"), Name: r.PostForm.Get("name"),
		Emergency: r.PostForm.Get("emergency") != ""}
	p.Form = map[string]string{"to": in.Recipient, "text": in.Text, "name": in.Name}
	p.Emergency = in.Emergency
	m, err := s.Create(key, p.Lang, in)
	if err != nil {
		if errors.Is(err, ErrClosed) {
			s.render(w, "closed", s.newPage(w, r, "/"))
			return
		}
		p.Err = errorText(p.S, err)
		if p.Err == p.S["err_other"] {
			log.Error().Err(err).Msg("post: create failed")
		}
		s.render(w, "write", p)
		return
	}
	p.Post = m
	p.Status = p.S["status_"+statusKey(m.Status)]
	s.render(w, "sent", p)
}

func (s *Service) pickup(w http.ResponseWriter, r *http.Request) {
	s.SignIn(ipKey(r))
	key := s.clientKey(w, r)
	p := s.newPage(w, r, "/pickup")
	code := r.URL.Query().Get("code")
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err == nil {
			code = r.PostForm.Get("code")
		}
	}
	if strings.TrimSpace(code) != "" {
		p.Form["code"] = code
		m, replies, err := s.Status(code)
		if err != nil {
			log.Error().Err(err).Msg("post: pickup failed")
			p.Err = p.S["err_other"]
		} else if m == nil {
			p.Err = p.S["unknown_code"]
		} else {
			p.Post, p.Replies = m, replies
			p.Status = p.S["status_"+statusKey(m.Status)]
		}
	}
	if strings.HasPrefix(key, "c:") {
		if mine, err := s.deps.DB.ListPostMessages(nil, 200); err == nil {
			for _, m := range mine {
				if m.Session == key {
					p.Mine = append(p.Mine, m)
				}
			}
		}
	}
	s.render(w, "pickup", p)
}

func (s *Service) board(w http.ResponseWriter, r *http.Request) {
	s.SignIn(ipKey(r))
	s.clientKey(w, r)
	p := s.newPage(w, r, "/board")
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			p.Err = p.S["err_other"]
		} else {
			p.Form = map[string]string{"name": r.PostForm.Get("name"), "status": r.PostForm.Get("status"), "place": r.PostForm.Get("place")}
			if _, err := s.AddBoard(p.Form["name"], p.Form["status"], p.Form["place"]); errors.Is(err, ErrClosed) {
				p.Err = fmt.Sprintf(p.S["closed_text"], p.PurgeDate)
			} else if err != nil {
				p.Err = errorText(p.S, err)
				if p.Err == p.S["err_other"] {
					log.Error().Err(err).Msg("post: board add failed")
				}
			} else {
				p.Added = true
				p.Form = map[string]string{}
			}
		}
	}
	p.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if p.Query != "" || p.Added {
		if entries, err := s.SearchBoard(p.Query); err == nil {
			p.Board = entries
		}
	}
	s.render(w, "board", p)
}

func (s *Service) about(w http.ResponseWriter, r *http.Request) {
	s.SignIn(ipKey(r))
	p := s.newPage(w, r, "/about")
	s.render(w, "about", p)
}

func (s *Service) done(w http.ResponseWriter, r *http.Request) {
	s.SignIn(ipKey(r))
	p := s.newPage(w, r, "/done")
	s.render(w, "done", p)
}

func statusKey(status string) string {
	switch status {
	case database.PostStatusPending:
		return "pending"
	case database.PostStatusQueued:
		return "queued"
	case database.PostStatusSent:
		return "sent"
	case database.PostStatusReplied:
		return "replied"
	case database.PostStatusRefused:
		return "refused"
	case database.PostStatusFailed:
		return "failed"
	}
	return status
}

func errorText(s map[string]string, err error) string {
	switch {
	case errors.Is(err, ErrRecipient):
		return s["err_recipient"]
	case errors.Is(err, ErrEmpty):
		return s["err_empty"]
	case errors.Is(err, ErrTooLong):
		return s["err_too_long"]
	case errors.Is(err, ErrLink):
		return s["err_link"]
	case errors.Is(err, ErrControl):
		return s["err_control"]
	case errors.Is(err, ErrName):
		return s["err_name"]
	case errors.Is(err, ErrQuota):
		return s["err_quota"]
	case errors.Is(err, ErrClosed):
		return s["closed_title"]
	}
	return s["err_other"]
}

var templates = template.Must(template.New("post").Funcs(template.FuncMap{
	"when":   func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" },
	"printf": fmt.Sprintf,
}).Parse(portalTemplates))

// portalTemplates are the pages. One stylesheet inline, nothing fetched,
// nothing scripted; tap targets sized for a phone held in one hand.
const portalTemplates = `
{{define "head"}}<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body{margin:0;background:#101418;color:#e6e9ec;font:17px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:34rem;margin:0 auto;padding:1rem 1rem 3rem}
h1{font-size:1.35rem;margin:.6rem 0 .3rem}
h2{font-size:1.1rem;margin:1.2rem 0 .4rem}
p{margin:.5rem 0}
nav{display:flex;gap:.4rem;flex-wrap:wrap;margin:.4rem 0 1rem}
nav a{flex:1 1 auto;text-align:center;padding:.7rem .6rem;border-radius:.5rem;background:#1c232b;color:#e6e9ec;text-decoration:none;font-weight:600}
nav a.on{background:#2a6f6a}
.langs{display:flex;gap:.6rem;justify-content:flex-end;font-size:.9rem}
.langs a{color:#9fb3c8;text-decoration:none;padding:.3rem .4rem}
.langs a.on{color:#fff;text-decoration:underline}
label{display:block;margin:.9rem 0 .25rem;font-weight:600}
input,textarea{width:100%;box-sizing:border-box;padding:.75rem;border-radius:.5rem;border:1px solid #3a4652;background:#0b0f13;color:#fff;font:inherit}
textarea{min-height:7rem}
.hint{font-size:.9rem;color:#9fb3c8}
button{width:100%;padding:1rem;margin-top:1rem;border:0;border-radius:.6rem;background:#2a6f6a;color:#fff;font:inherit;font-weight:700;font-size:1.05rem}
.err{background:#5a1f1f;border:1px solid #a33;padding:.7rem .9rem;border-radius:.5rem;margin:.8rem 0}
.ok{background:#1f4a2e;border:1px solid #3a8a55;padding:.7rem .9rem;border-radius:.5rem;margin:.8rem 0}
.code{font:700 2.6rem/1.2 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;letter-spacing:.18em;text-align:center;background:#fff;color:#000;padding:1rem;border-radius:.6rem;margin:1rem 0}
.card{background:#1c232b;border-radius:.5rem;padding:.8rem .9rem;margin:.6rem 0}
.card .meta{font-size:.85rem;color:#9fb3c8}
.warn{background:#5a3a14;border:1px solid #d08a2a;border-radius:.5rem;padding:.7rem .9rem;margin:.4rem 0 .8rem}
.warn .meta{font-size:.85rem;color:#f3dcb4}
.warn .tag{font-weight:700;letter-spacing:.06em}
.chk{display:flex;gap:.6rem;align-items:flex-start;margin:1rem 0 .2rem}
.chk input{width:1.4rem;height:1.4rem;margin:.1rem 0 0}
.chk label{margin:0}
.foot{margin-top:2rem;font-size:.8rem;color:#6c7f92;text-align:center}
a{color:#8fd3c9}
</style></head><body><main>
<div class="langs">{{range .Langs}}<a href="{{.Href}}"{{if eq .Code $.Lang}} class="on"{{end}}>{{.Name}}</a>{{end}}</div>
<h1>{{.S.title}}</h1>
<nav>
<a href="/{{.Q}}"{{if eq .Path "/"}} class="on"{{end}}>{{.S.nav_write}}</a>
<a href="/pickup{{.Q}}"{{if eq .Path "/pickup"}} class="on"{{end}}>{{.S.nav_pickup}}</a>
<a href="/board{{.Q}}"{{if eq .Path "/board"}} class="on"{{end}}>{{.S.nav_board}}</a>
<a href="/about{{.Q}}"{{if eq .Path "/about"}} class="on"{{end}}>{{.S.nav_about}}</a>
</nav>
{{range .Warnings}}<div class="warn" role="alert"><div class="meta"><span class="tag">{{$.S.warn_title}}</span>{{if .Test}} &middot; <span class="tag">{{$.S.warn_test}}</span>{{end}}{{if .Update}} &middot; {{$.S.warn_update}}{{end}}</div><div><strong>{{.Event}} {{.Severity}}</strong>{{if .Until}} &middot; {{$.S.warn_until}} {{.Until}}{{end}}</div>{{if .Text}}<div>{{.Text}}</div>{{end}}<div class="meta">{{.Label}}</div></div>
{{end}}{{if .Err}}<div class="err">{{.Err}}</div>{{end}}
{{end}}

{{define "foot"}}<p class="foot">{{.S.powered}} &middot; <a href="/done{{.Q}}">{{.S.done}}</a></p></main></body></html>{{end}}

{{define "write"}}{{template "head" .}}
<p>{{.S.write_intro}}</p>
<form method="post" action="/send{{.Q}}">
<label for="to">{{.S.recipient}}</label>
<input id="to" name="to" value="{{index .Form "to"}}" placeholder="{{.S.recipient_hint}}" autocomplete="off" inputmode="email" required>
<label for="name">{{.S.name}}</label>
<input id="name" name="name" value="{{index .Form "name"}}" maxlength="40" autocomplete="name">
<label for="text">{{.S.message}}</label>
<textarea id="text" name="text" maxlength="{{.MaxChars}}" required>{{index .Form "text"}}</textarea>
<p class="hint">{{printf .S.chars_left .MaxChars}}</p>
<div class="chk"><input type="checkbox" id="emergency" name="emergency" value="1"{{if .Emergency}} checked{{end}}><label for="emergency">{{.S.emergency}}</label></div>
<p class="hint">{{.S.emergency_hint}}</p>
<button type="submit">{{.S.send}}</button>
</form>
{{template "foot" .}}{{end}}

{{define "sent"}}{{template "head" .}}
<h2>{{.S.sent_title}}</h2>
<div class="code">{{.Post.Code}}</div>
<p><strong>{{.S.sent_intro}}</strong></p>
<p>{{if eq .Post.Status "pending_approval"}}{{.S.sent_pending}}{{else}}{{.S.sent_queued}}{{end}}</p>
<div class="card"><div class="meta">{{.S.sent_to}}: {{.Post.Recipient}} &middot; {{.S.status}}: {{.Status}}</div>{{if .Post.Name}}<div>{{.Post.Name}}:</div>{{end}}<div>{{.Post.Text}}</div></div>
<p><a href="/pickup?code={{.Post.Code}}&amp;lang={{.Lang}}">{{.S.nav_pickup}}: {{.Post.Code}}</a></p>
{{template "foot" .}}{{end}}

{{define "pickup"}}{{template "head" .}}
<p>{{.S.pickup_intro}}</p>
<form method="post" action="/pickup{{.Q}}">
<label for="code">{{.S.code}}</label>
<input id="code" name="code" value="{{index .Form "code"}}" maxlength="8" autocomplete="off" autocapitalize="characters" style="font-size:1.4rem;letter-spacing:.15em;text-transform:uppercase">
<button type="submit">{{.S.lookup}}</button>
</form>
{{if .Post}}
<div class="code">{{.Post.Code}}</div>
<div class="card"><div class="meta">{{.S.sent_to}}: {{.Post.Recipient}} &middot; {{when .Post.CreatedAt}}</div>{{if .Post.Name}}<div>{{.Post.Name}}:</div>{{end}}<div>{{.Post.Text}}</div><div class="meta">{{.S.status}}: {{.Status}}</div></div>
<h2>{{.S.replies}}</h2>
{{if .Replies}}{{range .Replies}}<div class="card"><div class="meta">{{.From}} &middot; {{when .ReceivedAt}}</div><div>{{.Text}}</div></div>{{end}}{{else}}<p>{{$.S.no_replies}}</p>{{end}}
{{end}}
{{if .Mine}}<h2>{{.S.your_posts}}</h2>{{range .Mine}}<div class="card"><a href="/pickup?code={{.Code}}&amp;lang={{$.Lang}}"><strong>{{.Code}}</strong></a> &middot; <span class="meta">{{.Recipient}} &middot; {{when .CreatedAt}}</span></div>{{end}}{{end}}
{{template "foot" .}}{{end}}

{{define "board"}}{{template "head" .}}
<p>{{printf .S.board_intro .BoardDays}}</p>
{{if .Added}}<div class="ok">{{.S.board_added}}</div>{{end}}
<form method="get" action="/board">
<input type="hidden" name="lang" value="{{.Lang}}">
<label for="q">{{.S.board_search}}</label>
<input id="q" name="q" value="{{.Query}}" maxlength="60" autocomplete="off">
<button type="submit">{{.S.board_find}}</button>
</form>
{{if .Query}}{{if .Board}}{{range .Board}}<div class="card"><strong>{{.Name}}</strong> &middot; {{.Status}}{{if .Place}} &middot; {{.Place}}{{end}}<div class="meta">{{when .CreatedAt}}</div></div>{{end}}{{else}}<p>{{.S.board_empty}}</p>{{end}}{{end}}
{{if .Closed}}<h2>{{.S.closed_title}}</h2><p>{{printf .S.closed_text .PurgeDate}}</p>{{else}}
<h2>{{.S.board_add}}</h2>
<form method="post" action="/board{{.Q}}">
<label for="bname">{{.S.board_name}}</label>
<input id="bname" name="name" value="{{index .Form "name"}}" maxlength="60" required>
<label for="bstatus">{{.S.board_status}}</label>
<input id="bstatus" name="status" value="{{index .Form "status"}}" maxlength="60" required>
<label for="bplace">{{.S.board_place}}</label>
<input id="bplace" name="place" value="{{index .Form "place"}}" maxlength="60">
<button type="submit">{{.S.board_add}}</button>
</form>
{{end}}
{{template "foot" .}}{{end}}

{{define "about"}}{{template "head" .}}
<p><strong>{{.S.about_operator}}:</strong> {{.Operator}}</p>
<p>{{.S.about_how}}</p>
<p>{{.S.about_cost}}</p>
<h2>{{.S.data_title}}</h2>
<p>{{printf .S.data_notice .PurgeDays .BoardDays}}</p>
{{if .Closed}}<h2>{{.S.closed_title}}</h2><p>{{printf .S.closed_text .PurgeDate}}</p>{{end}}
{{template "foot" .}}{{end}}

{{define "closed"}}{{template "head" .}}
<h2>{{.S.closed_title}}</h2>
<p>{{printf .S.closed_text .PurgeDate}}</p>
<p><a href="/pickup{{.Q}}">{{.S.nav_pickup}}</a></p>
{{template "foot" .}}{{end}}

{{define "done"}}{{template "head" .}}
<h2>{{.S.done_title}}</h2>
<p>{{.S.done_text}}</p>
{{template "foot" .}}{{end}}
`
