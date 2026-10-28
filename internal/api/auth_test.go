package api

// The API token [MESHSAT-1738]: off by default, and when on, who gets in.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"meshsat/internal/database"
)

const (
	lanAddr  = "192.168.181.50:41000"
	loopAddr = "127.0.0.1:41000"
	loop6    = "[::1]:41000"
	theToken = "kit-token-for-the-tests-0123456789"
)

func newAuthServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db}
	return s, s.Router()
}

// call sends one request to h as if from remote, with the extras applied.
func call(h http.Handler, method, path, remote string, extras ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	for _, f := range extras {
		f(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func cookie(tok string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: authCookieName, Value: tok}) }
}

func origin(o string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Origin", o) }
}

func jsonBody(v interface{}) func(*http.Request) {
	return func(r *http.Request) {
		b, _ := json.Marshal(v)
		r.Body = readCloser(b)
		r.Header.Set("Content-Type", "application/json")
	}
}

func readCloser(b []byte) *nopCloser { return &nopCloser{bytes.NewReader(b)} }

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status %d, want %d (body %s)", what, rec.Code, want, rec.Body.String())
	}
}

// With no token set the middleware changes nothing: a LAN caller is served,
// the CORS header is the `*` it always was, nothing about a token appears.
func TestAuthOffIsTodaysBehaviour(t *testing.T) {
	s, h := newAuthServer(t)
	if s.AuthRequired() {
		t.Fatal("a fresh server must be open")
	}
	rec := call(h, http.MethodGet, "/api/presets", lanAddr, origin("http://anything.example"))
	wantStatus(t, rec, http.StatusOK, "open LAN request")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("ACAO %q, want *", got)
	}
	for _, hdr := range []string{"WWW-Authenticate", "Vary", "Set-Cookie", "Access-Control-Allow-Credentials"} {
		if v := rec.Header().Get(hdr); v != "" {
			t.Fatalf("open API added %s: %q", hdr, v)
		}
	}
	// The same request through the bare handler, without the middleware,
	// answers the same.
	plain := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/presets", nil)
	req.RemoteAddr = lanAddr
	s.handleGetPresets(plain, req)
	if plain.Code != rec.Code || plain.Body.String() != rec.Body.String() {
		t.Fatalf("middleware changed an open response: %d %q vs %d %q", rec.Code, rec.Body.String(), plain.Code, plain.Body.String())
	}
	// Status says so.
	rec = call(h, http.MethodGet, "/api/auth/status", lanAddr)
	wantStatus(t, rec, http.StatusOK, "status")
	var st authStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Required || !st.Authenticated || st.Via != "open" {
		t.Fatalf("open status %+v", st)
	}
}

// With a token from the environment: no token = 401, the right Bearer or
// cookie = 200, a wrong one = 401, the probes stay open.
func TestAuthOnFromEnv(t *testing.T) {
	s, h := newAuthServer(t)
	if err := s.ConfigureAuth(AuthConfig{Token: theToken, TrustLoopback: true}); err != nil {
		t.Fatal(err)
	}
	if !s.AuthRequired() || s.AuthSource() != "env" {
		t.Fatalf("required %v source %q", s.AuthRequired(), s.AuthSource())
	}

	rec := call(h, http.MethodGet, "/api/presets", lanAddr)
	wantStatus(t, rec, http.StatusUnauthorized, "LAN without token")
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Fatalf("WWW-Authenticate %q", got)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "api token required" {
		t.Fatalf("body %s", rec.Body.String())
	}

	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, bearer(theToken)), http.StatusOK, "right Bearer")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, bearer("wrong-token-of-some-length")), http.StatusUnauthorized, "wrong Bearer")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, bearer(theToken+"x")), http.StatusUnauthorized, "Bearer with a byte more")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, bearer(theToken[:len(theToken)-1])), http.StatusUnauthorized, "Bearer with a byte less")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, cookie(theToken)), http.StatusOK, "right cookie")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, cookie("wrong")), http.StatusUnauthorized, "wrong cookie")
	// Basic and a bare token are not Bearer.
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, func(r *http.Request) { r.Header.Set("Authorization", theToken) }), http.StatusUnauthorized, "bare token")

	// Probes and the SPA stay open; /api/auth/status says what is needed.
	wantStatus(t, call(h, http.MethodGet, "/health", lanAddr), http.StatusOK, "/health")
	wantStatus(t, call(h, http.MethodGet, "/metrics", lanAddr), http.StatusOK, "/metrics")
	rec = call(h, http.MethodGet, "/api/auth/status", lanAddr)
	wantStatus(t, rec, http.StatusOK, "status without token")
	var st authStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Required || st.Authenticated || st.Source != "env" || !st.LoopbackTrusted || st.RefusedTotal == 0 {
		t.Fatalf("status %+v", st)
	}
	rec = call(h, http.MethodGet, "/api/auth/status", lanAddr, bearer(theToken))
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Authenticated || st.Via != "bearer" {
		t.Fatalf("status with Bearer %+v", st)
	}

	// The environment owns the token: the API cannot change it.
	wantStatus(t, call(h, http.MethodPut, "/api/auth/token", lanAddr, bearer(theToken)), http.StatusConflict, "rotate an env token")
	wantStatus(t, call(h, http.MethodDelete, "/api/auth/token", lanAddr, bearer(theToken)), http.StatusConflict, "clear an env token")
}

// The kit's own requests get in alone while loopback is trusted, and not
// when it is not; v4 and v6.
func TestAuthLoopback(t *testing.T) {
	s, h := newAuthServer(t)
	_ = s.ConfigureAuth(AuthConfig{Token: theToken, TrustLoopback: true})
	wantStatus(t, call(h, http.MethodGet, "/api/presets", loopAddr), http.StatusOK, "127.0.0.1 trusted")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", loop6), http.StatusOK, "::1 trusted")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr), http.StatusUnauthorized, "LAN while loopback trusted")
	rec := call(h, http.MethodGet, "/api/auth/status", loopAddr)
	var st authStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Via != "loopback" {
		t.Fatalf("via %q", st.Via)
	}

	_ = s.ConfigureAuth(AuthConfig{Token: theToken, TrustLoopback: false})
	wantStatus(t, call(h, http.MethodGet, "/api/presets", loopAddr), http.StatusUnauthorized, "127.0.0.1 untrusted")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", loop6), http.StatusUnauthorized, "::1 untrusted")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", loopAddr, bearer(theToken)), http.StatusOK, "127.0.0.1 with the token")
	// The Bridge's own in-process call still gets in, on the process-local token.
	wantStatus(t, call(h, http.MethodGet, "/api/presets", loopAddr, bearer(s.LocalAPIToken())), http.StatusOK, "self token")
	rec = call(h, http.MethodGet, "/api/auth/status", loopAddr, bearer(s.LocalAPIToken()))
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Via != "self" {
		t.Fatalf("self via %q", st.Via)
	}
	// A pipe address (what a relay tunnel reports) is never loopback.
	if isLoopback("pipe") || isLoopback("") || isLoopback("10.0.0.1:5") {
		t.Fatal("isLoopback too generous")
	}
}

// A request through the Hub relay is authenticated by the tunnel: the same
// LAN request without a token is refused on the plain router and served
// on the relay's.
func TestAuthRelayTunnelIsTheCredential(t *testing.T) {
	s, plain := newAuthServer(t)
	_ = s.ConfigureAuth(AuthConfig{Token: theToken, TrustLoopback: true})
	relay := s.RelayRouter()
	// What a net.Pipe conn reports as its remote address.
	wantStatus(t, call(plain, http.MethodGet, "/api/presets", "pipe"), http.StatusUnauthorized, "plain router, no token")
	wantStatus(t, call(relay, http.MethodGet, "/api/presets", "pipe"), http.StatusOK, "relay router, no token")
	wantStatus(t, call(plain, http.MethodGet, "/api/presets", lanAddr), http.StatusUnauthorized, "plain router from the LAN")
	wantStatus(t, call(relay, http.MethodGet, "/api/presets", lanAddr), http.StatusOK, "relay router from the LAN")
	rec := call(relay, http.MethodGet, "/api/auth/status", "pipe")
	var st authStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Via != "relay" || !st.Authenticated {
		t.Fatalf("relay status %+v", st)
	}
	// With no token set, the mark changes nothing.
	s2, _ := newAuthServer(t)
	wantStatus(t, call(s2.RelayRouter(), http.MethodGet, "/api/presets", lanAddr), http.StatusOK, "relay router, open API")
}

// CORS: `*` while open; with a token only the kit's own origin, and a
// preflight is answered without a token either way.
func TestAuthCORSNarrows(t *testing.T) {
	s, h := newAuthServer(t)
	_ = s.ConfigureAuth(AuthConfig{Token: theToken, TrustLoopback: true})

	own := func(r *http.Request) {
		r.Host = "nllei01tesseract01:6050"
		r.Header.Set("Origin", "http://nllei01tesseract01:6050")
	}
	rec := call(h, http.MethodGet, "/api/presets", lanAddr, bearer(theToken), own)
	wantStatus(t, rec, http.StatusOK, "own origin")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://nllei01tesseract01:6050" {
		t.Fatalf("own origin ACAO %q", got)
	}
	if rec.Header().Get("Vary") != "Origin" || rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("own origin headers %v", rec.Header())
	}

	other := func(r *http.Request) {
		r.Host = "nllei01tesseract01:6050"
		r.Header.Set("Origin", "http://evil.example")
	}
	rec = call(h, http.MethodGet, "/api/presets", lanAddr, bearer(theToken), other)
	wantStatus(t, rec, http.StatusOK, "other origin with the token still answers")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("other origin ACAO %q, want none", got)
	}
	// Same host, different port or scheme: another origin.
	port := func(r *http.Request) { r.Host = "kit:6050"; r.Header.Set("Origin", "http://kit:8080") }
	if got := call(h, http.MethodGet, "/api/presets", lanAddr, bearer(theToken), port).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("other port ACAO %q", got)
	}
	// Never `*` with a token set, even with no Origin.
	if got := call(h, http.MethodGet, "/api/presets", lanAddr, bearer(theToken)).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("no-Origin ACAO %q", got)
	}

	// Preflights are answered without a token.
	rec = call(h, http.MethodOptions, "/api/presets", lanAddr, own)
	wantStatus(t, rec, http.StatusOK, "preflight")
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight headers %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}

	if !sameOrigin("http://Kit:6050", "kit:6050") || sameOrigin("http://kit", "kit:6050") || sameOrigin("kit:6050", "kit:6050") || sameOrigin("http://kit:6050", "") {
		t.Fatal("sameOrigin")
	}
}

// The token endpoints: who may set the first token, that it is returned
// once and kept only as a hash, that it survives a restart, rotation, and
// that clearing opens the API again.
func TestAuthTokenEndpoints(t *testing.T) {
	s, h := newAuthServer(t)

	// Open API: the LAN may not lock the owner out; the kit itself may.
	wantStatus(t, call(h, http.MethodPut, "/api/auth/token", lanAddr), http.StatusForbidden, "first token from the LAN")
	wantStatus(t, call(h, http.MethodDelete, "/api/auth/token", lanAddr), http.StatusForbidden, "clear from the LAN while open")
	wantStatus(t, call(h, http.MethodPut, "/api/auth/token", loopAddr, jsonBody(map[string]string{"token": "short"})), http.StatusBadRequest, "too short")
	wantStatus(t, call(h, http.MethodPut, "/api/auth/token", loopAddr, jsonBody(map[string]string{"token": "has a space in it, really"})), http.StatusBadRequest, "with a space")

	rec := call(h, http.MethodPut, "/api/auth/token", loopAddr)
	wantStatus(t, rec, http.StatusOK, "first token from loopback")
	var reply authTokenReply
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	if len(reply.Token) != 43 || reply.Source != "config" {
		t.Fatalf("reply %+v", reply)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), authCookieName+"="+reply.Token) || !strings.Contains(rec.Header().Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("Set-Cookie %q", rec.Header().Get("Set-Cookie"))
	}
	generated := reply.Token

	// The database holds the hash, never the token.
	stored, _ := s.db.GetSystemConfig(authHashRow)
	want := sha256.Sum256([]byte(generated))
	if stored != hex.EncodeToString(want[:]) || strings.Contains(stored, generated) {
		t.Fatalf("stored %q", stored)
	}

	// It applies at once.
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr), http.StatusUnauthorized, "LAN after the first token")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, bearer(generated)), http.StatusOK, "LAN with the new token")
	rec = call(h, http.MethodGet, "/api/auth/status", lanAddr)
	var st authStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Required || st.Source != "config" {
		t.Fatalf("status %+v", st)
	}

	// A restart reads it back from system_config.
	s2 := &Server{db: s.db}
	if err := s2.ConfigureAuth(AuthConfig{TrustLoopback: true}); err != nil {
		t.Fatal(err)
	}
	h2 := s2.Router()
	wantStatus(t, call(h2, http.MethodGet, "/api/presets", lanAddr), http.StatusUnauthorized, "after restart, no token")
	wantStatus(t, call(h2, http.MethodGet, "/api/presets", lanAddr, bearer(generated)), http.StatusOK, "after restart, the token")
	// The environment outranks the row.
	s3 := &Server{db: s.db}
	_ = s3.ConfigureAuth(AuthConfig{Token: theToken, TrustLoopback: true})
	h3 := s3.Router()
	wantStatus(t, call(h3, http.MethodGet, "/api/presets", lanAddr, bearer(generated)), http.StatusUnauthorized, "env set: the row's token")
	wantStatus(t, call(h3, http.MethodGet, "/api/presets", lanAddr, bearer(theToken)), http.StatusOK, "env set: the env token")

	// Rotation by an authenticated LAN caller, with a token of its own.
	rec = call(h, http.MethodPut, "/api/auth/token", lanAddr, bearer(generated), jsonBody(map[string]string{"token": theToken}))
	wantStatus(t, rec, http.StatusOK, "rotate from the LAN with the token")
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	if reply.Token != theToken {
		t.Fatalf("rotated reply %+v", reply)
	}
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, bearer(generated)), http.StatusUnauthorized, "old token after rotation")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr, bearer(theToken)), http.StatusOK, "new token after rotation")
	// An unauthenticated LAN caller may not rotate.
	wantStatus(t, call(h, http.MethodPut, "/api/auth/token", lanAddr), http.StatusUnauthorized, "rotate without a token")

	// Login and logout.
	rec = call(h, http.MethodPost, "/api/auth/login", lanAddr, jsonBody(map[string]string{"token": theToken}))
	wantStatus(t, rec, http.StatusNoContent, "login")
	sc := rec.Header().Get("Set-Cookie")
	if !strings.Contains(sc, authCookieName+"="+theToken) || !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "Path=/api") || !strings.Contains(sc, "SameSite=Strict") || strings.Contains(sc, "Secure") {
		t.Fatalf("login Set-Cookie %q", sc)
	}
	wantStatus(t, call(h, http.MethodPost, "/api/auth/login", lanAddr, jsonBody(map[string]string{"token": "nope-nope-nope-nope"})), http.StatusUnauthorized, "login with a wrong token")
	wantStatus(t, call(h, http.MethodPost, "/api/auth/login", lanAddr), http.StatusUnauthorized, "login with nothing")
	wantStatus(t, call(h, http.MethodPost, "/api/auth/login", lanAddr, bearer(s.LocalAPIToken())), http.StatusUnauthorized, "login with the self token")
	rec = call(h, http.MethodPost, "/api/auth/logout", lanAddr, cookie(theToken))
	wantStatus(t, rec, http.StatusNoContent, "logout")
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, "Max-Age=0") {
		t.Fatalf("logout Set-Cookie %q", sc)
	}

	// Clearing opens the API again, from the next request on.
	wantStatus(t, call(h, http.MethodDelete, "/api/auth/token", lanAddr), http.StatusUnauthorized, "clear without a token")
	wantStatus(t, call(h, http.MethodDelete, "/api/auth/token", lanAddr, cookie(theToken)), http.StatusNoContent, "clear with the cookie")
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr), http.StatusOK, "open again")
	if stored, _ := s.db.GetSystemConfig(authHashRow); stored != "" {
		t.Fatalf("row after clear %q", stored)
	}
	// Login with no token set sets nothing.
	rec = call(h, http.MethodPost, "/api/auth/login", lanAddr)
	wantStatus(t, rec, http.StatusNoContent, "login while open")
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatal("login while open set a cookie")
	}
}

// tokenMatches compares digests of a fixed length in constant time, so a
// token of any length, a prefix of the right one and the empty string are
// all refused the same way, and nothing matches while no token is set.
func TestAuthTokenMatches(t *testing.T) {
	s := &Server{}
	if s.tokenMatches(theToken) || s.tokenMatches("") {
		t.Fatal("matched with no token set")
	}
	_ = s.ConfigureAuth(AuthConfig{Token: theToken})
	for _, bad := range []string{"", "k", theToken[:10], theToken + "0", strings.ToUpper(theToken), strings.Repeat("x", 4096)} {
		if s.tokenMatches(bad) {
			t.Fatalf("matched %q", bad)
		}
	}
	if !s.tokenMatches(theToken) {
		t.Fatal("the token itself")
	}
	// The process-local token is a token too, and is never the same twice.
	a := s.LocalAPIToken()
	if a == "" || a != s.LocalAPIToken() || !s.tokenMatches(a) {
		t.Fatal("self token")
	}
	if b := (&Server{}).LocalAPIToken(); b == a {
		t.Fatal("two processes' self tokens agree")
	}
	if newToken() == newToken() {
		t.Fatal("newToken repeats")
	}
}

// A stored hash that is not a sha256 leaves the API open and says so, rather
// than locking everyone out on a row nobody can type.
func TestAuthBadStoredHashStaysOpen(t *testing.T) {
	s, h := newAuthServer(t)
	_ = s.db.SetSystemConfig(authHashRow, "not-hex")
	if err := s.ConfigureAuth(AuthConfig{TrustLoopback: true}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, call(h, http.MethodGet, "/api/presets", lanAddr), http.StatusOK, "bad hash: open")
}
