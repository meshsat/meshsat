package demo

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"meshsat/internal/database"
	"meshsat/internal/post"
)

// ReadOnlyError is the answer to anything but a GET: the demo changes
// nothing and sends nothing. demo.js shows it as a short notice.
const ReadOnlyError = "Read-only demo: nothing was sent or saved."

// HeaderDemo marks the demo's own refusals, so the page can tell them from
// a real error.
const HeaderDemo = "X-MeshSat-Demo"

// streams are the event streams the web app opens. A 204 closes an
// EventSource for good, so nothing reconnects and nothing stays open.
var streams = map[string]bool{"events": true, "spectrum/stream": true, "tak/events": true, "devices/usb/events": true}

// segment is what one path segment may be: the API's ids and names. Any
// other byte answers 404 before a lookup.
var segment = regexp.MustCompile(`^[A-Za-z0-9._:!-]{1,64}$`)

// API answers /api/* from the fixtures, and the Community Post's operator
// reads from the running post, masked.
type API struct {
	Fixtures *Fixtures
	Post     *post.Service // nil = the post reads come from the fixtures too
	DB       *database.DB
	Now      func() time.Time

	labelOnce sync.Once
	labelKey  []byte

	cacheMu sync.Mutex
	cache   map[string]cached
}

// cached is one shifted fixture, good for the second it was made in.
type cached struct {
	sec  int64
	body []byte
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	writeJSON(w, status, b)
}

// ServeHTTP answers one /api request. The body is never read.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.Header().Set(HeaderDemo, "1")
		writeErr(w, http.StatusMethodNotAllowed, ReadOnlyError)
		return
	}
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	if p == "" || len(p) > 200 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if !segment.MatchString(s) || s == "." || s == ".." {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	if streams[p] {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if strings.HasPrefix(p, "post/") && a.Post != nil {
		a.servePost(w, segs[1:])
		return
	}
	now := a.now()
	if body, ok := a.lookupCached(segs, now); ok {
		writeJSON(w, http.StatusOK, body)
		return
	}
	writeErr(w, http.StatusNotFound, "not found")
}

// lookupCached is lookup, made at most once a second per path: shifting a
// fixture is a decode and an encode, and a public listener must not do that
// for every request a visitor (or anyone) sends.
func (a *API) lookupCached(segs []string, now time.Time) ([]byte, bool) {
	key, sec := strings.Join(segs, "/"), now.Unix()
	a.cacheMu.Lock()
	if c, ok := a.cache[key]; ok && c.sec == sec {
		a.cacheMu.Unlock()
		return c.body, true
	}
	a.cacheMu.Unlock()
	body, ok := a.lookup(segs, now)
	if !ok {
		return nil, false
	}
	a.cacheMu.Lock()
	if a.cache == nil || len(a.cache) > 1000 {
		a.cache = map[string]cached{}
	}
	a.cache[key] = cached{sec: sec, body: body}
	a.cacheMu.Unlock()
	return body, true
}

// lookup finds the fixture for a path: the exact one ("a_b_c"), else the
// path with its id segments as "{id}" (one fixture answers every id).
func (a *API) lookup(segs []string, now time.Time) ([]byte, bool) {
	if b, ok := a.Fixtures.At(strings.Join(segs, "_"), now); ok {
		return b, true
	}
	for i := len(segs) - 1; i >= 1; i-- {
		try := append([]string{}, segs...)
		try[i] = "{id}"
		if b, ok := a.Fixtures.At(strings.Join(try, "_"), now); ok {
			return b, true
		}
	}
	return nil, false
}

// servePost answers the post's operator reads from the running post. A
// visitor's words never leave the post through here: every post and board
// line is masked (owner ruling 12 Nov 2026). A post's own page (by its
// code) is not served at all; the pickup page on the portal is the only
// place a post is read whole, by whoever holds its code.
func (a *API) servePost(w http.ResponseWriter, rest []string) {
	switch {
	case len(rest) == 1 && rest[0] == "config":
		cfg, st, state := a.Post.Config(), a.Post.Settings(), a.Post.State()
		b, _ := json.Marshal(map[string]any{
			"enabled": true, "portal_url": cfg.PortalURL, "bearer": "satellite (demo, nothing leaves)",
			"max_chars": cfg.MaxTextChars, "max_bytes": cfg.MaxBytes, "board_days": 0,
			"auto_approve": false, "posts_per_hour": st.PostsPerHour, "posts_per_day": st.PostsPerDay,
			"operator": st.Operator, "purge_days": 0, "closed": state.Closed,
			"print": false, "printer": post.PrinterState{},
		})
		writeJSON(w, http.StatusOK, b)
	case len(rest) == 1 && rest[0] == "queue":
		pending, err1 := a.Post.Queue(100)
		recent, err2 := a.Post.Recent(50)
		counts, err3 := a.DB.CountPostMessages()
		if err1 != nil || err2 != nil || err3 != nil {
			writeErr(w, http.StatusInternalServerError, "the post could not be read")
			return
		}
		b, _ := json.Marshal(map[string]any{"pending": a.MaskPosts(pending), "recent": a.MaskPosts(recent), "counts": counts})
		writeJSON(w, http.StatusOK, b)
	case len(rest) == 1 && rest[0] == "board":
		lines, err := a.Post.BoardLines()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "the board could not be read")
			return
		}
		b, _ := json.Marshal(MaskBoard(lines))
		writeJSON(w, http.StatusOK, b)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// MaskedPost is a post as the demo's public volunteer screen shows it.
type MaskedPost struct {
	Code      string    `json:"code"`
	Recipient string    `json:"recipient"`
	Text      string    `json:"text"`
	Name      string    `json:"name"`
	Class     string    `json:"class"`
	Status    string    `json:"status"`
	Lang      string    `json:"lang"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MaskPosts keeps what a visitor needs to see their post go through (its
// time, its length, its status) and nothing anyone could read it by: the
// recipient as its country code and last two digits, no name, the text as
// its length, and instead of the code a label that holds no letter of it,
// so it opens nobody's pickup page and narrows no guess.
func (a *API) MaskPosts(in []database.PostMessage) []MaskedPost {
	out := make([]MaskedPost, 0, len(in))
	for _, m := range in {
		out = append(out, MaskedPost{
			Code:      a.label(m.Code),
			Recipient: MaskRecipient(m.Recipient),
			Text:      MaskedText(m.Text),
			Class:     m.Class,
			Status:    m.Status,
			Lang:      m.Lang,
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		})
	}
	return out
}

// label stands for a code on the public screen: four hex digits of an
// HMAC under a key made at start, so it is stable for a post while the
// process runs and says nothing about the code.
func (a *API) label(code string) string {
	a.labelOnce.Do(func() {
		a.labelKey = make([]byte, 32)
		if _, err := rand.Read(a.labelKey); err != nil {
			a.labelKey = nil
		}
	})
	mac := hmac.New(sha256.New, a.labelKey)
	mac.Write([]byte(code))
	return "•••" + hex.EncodeToString(mac.Sum(nil))[:4]
}

// MaskRecipient keeps the least a visitor needs to recognise their own
// post: a number's country code (its first three characters) and last two
// digits, an address's first letter and top-level domain.
func MaskRecipient(v string) string {
	if i := strings.LastIndex(v, "@"); i > 0 {
		tld := ""
		if j := strings.LastIndex(v[i:], "."); j > 0 {
			tld = v[i+j:]
		}
		r, _ := utf8.DecodeRuneInString(v)
		return string(r) + "•••@•••" + tld
	}
	if len(v) < 6 {
		return "•••"
	}
	return v[:3] + "•••" + v[len(v)-2:]
}

// MaskedText is a visitor's text as its length only.
func MaskedText(text string) string {
	n := utf8.RuneCountInString(text)
	if n == 1 {
		return "(a visitor's message, 1 character)"
	}
	return "(a visitor's message, " + itoa(n) + " characters)"
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// MaskedLine is a board line as the public volunteer screen shows it.
type MaskedLine struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	Place         string    `json:"place"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	HasRemoveCode bool      `json:"has_remove_code"`
}

// MaskBoard keeps a line's existence and times, never its words: the name
// as its initials, the status and place gone.
func MaskBoard(in []database.PostBoardEntry) []MaskedLine {
	out := make([]MaskedLine, 0, len(in))
	for _, e := range in {
		out = append(out, MaskedLine{ID: e.ID, Name: Initials(e.Name), Status: "(a visitor's line)",
			CreatedAt: e.CreatedAt, ExpiresAt: e.ExpiresAt, HasRemoveCode: e.HasRemoveCode})
	}
	return out
}

// Initials is a name as its initials ("Maria Papadaki" -> "M. P."), at
// most three.
func Initials(name string) string {
	var parts []string
	for _, w := range strings.Fields(name) {
		r, _ := utf8.DecodeRuneInString(w)
		parts = append(parts, strings.ToUpper(string(r))+".")
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return "•"
	}
	return strings.Join(parts, " ")
}
