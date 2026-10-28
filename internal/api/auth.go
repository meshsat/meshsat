package api

// The API token [MESHSAT-1738]. The Bridge's HTTP API is bound on every
// interface and, until this, answered anyone who could reach the port: a
// design for a sealed kit, a blocker on any open network. This file adds
// one token, OFF BY DEFAULT: with no token set the middleware is a pass
// through and every response is byte for byte what it was.
//
// With a token set, every request under /api needs it, as
// `Authorization: Bearer <token>` or as the cookie POST /api/auth/login
// sets for the browser (the SPA's event stream is an EventSource, which
// cannot carry a header). Four things never need it:
//
//   - /health and /metrics, which are not under /api at all;
//   - GET /api/auth/status and POST /api/auth/login, which are how a client
//     finds out and gets in;
//   - a request through the Hub relay (RelayRouter): the Hub verified the
//     phone's certificate before it opened the tunnel, so the tunnel is
//     the credential;
//   - a request from the kit itself (loopback) while TrustLoopback is on,
//     the default: the kiosk, the host services and the operator's shell
//     keep working without a change. Anyone with a shell on the kit is
//     already next to the database and the private keys, so the loopback
//     trust gives away nothing a token would protect. Turn it off
//     (MESHSAT_API_TRUST_LOOPBACK=false) on a host where other people
//     have accounts; the Bridge's own in-process calls still get in on a
//     process-local token.
//
// The token is never stored: system_config keeps its SHA-256
// (api_token_sha256), the environment keeps the token itself when the
// operator chose MESHSAT_API_TOKEN, and nothing writes it to a log. A
// presented token is hashed and compared in constant time, so the
// comparison takes the same time whatever the first wrong byte is.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

const (
	// authHashRow is the system_config row that keeps the token's
	// SHA-256 as hex; never the token.
	authHashRow = "api_token_sha256"
	// authCookieName is the cookie POST /api/auth/login sets. HttpOnly, so
	// a script on the page cannot read it; the SPA keeps its own copy.
	authCookieName = "meshsat_api_token"
	// minTokenChars is the shortest token PUT /api/auth/token accepts when
	// the caller brings one; a generated token is 43 characters.
	minTokenChars = 16
)

// AuthConfig is what main.go gives ConfigureAuth from the environment.
type AuthConfig struct {
	// Token is MESHSAT_API_TOKEN. Empty = read system_config; empty there
	// too = no token, the API is open as before.
	Token string
	// TrustLoopback lets a request from 127.0.0.1 or ::1 in without a token.
	TrustLoopback bool
}

// apiAuth is the server's token state. Its zero value is "no token": a
// Server a test builds as &Server{} is open.
type apiAuth struct {
	mu            sync.RWMutex
	hash          []byte // sha256 of the token; nil = off
	source        string // "env" or "config" while set
	trustLoopback bool
	self          string // the process-local token, made on first use
	refused       atomic.Int64
}

// authVia says how a request got in: what GET /api/auth/status reports
// and what the token handlers decide on.
type authVia string

const (
	viaOpen     authVia = "open"     // no token set
	viaBearer   authVia = "bearer"   // Authorization: Bearer
	viaCookie   authVia = "cookie"   // the login cookie
	viaLoopback authVia = "loopback" // from the kit itself, trusted
	viaRelay    authVia = "relay"    // through the Hub relay
	viaSelf     authVia = "self"     // the Bridge's own in-process call
	viaNone     authVia = ""         // not authenticated
)

type ctxKey int

const (
	relayCtxKey ctxKey = iota
	viaCtxKey
)

// RelayAuthenticated marks every request h serves as having come through
// the Hub relay. Only main.go wraps the relay's handler in it, and only
// the relay's TLS listener feeds that handler, so the mark cannot be
// forged from a socket.
func RelayAuthenticated(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), relayCtxKey, true)))
	})
}

// RelayRouter is the router the Hub relay serves: the same routes, with
// every request authenticated by the tunnel it arrived through.
func (s *Server) RelayRouter() http.Handler {
	return RelayAuthenticated(s.Router())
}

func isRelayRequest(r *http.Request) bool {
	v, _ := r.Context().Value(relayCtxKey).(bool)
	return v
}

// ConfigureAuth installs the token: the environment's when given, else
// the hash system_config keeps, else none. Called once at start; the
// token handlers change the state afterwards without a restart.
func (s *Server) ConfigureAuth(cfg AuthConfig) error {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	s.auth.trustLoopback = cfg.TrustLoopback
	if cfg.Token != "" {
		h := sha256.Sum256([]byte(cfg.Token))
		s.auth.hash = h[:]
		s.auth.source = "env"
		return nil
	}
	s.auth.hash, s.auth.source = nil, ""
	if s.db == nil {
		return nil
	}
	stored, err := s.db.GetSystemConfig(authHashRow)
	if err != nil {
		return err
	}
	if stored == "" {
		return nil
	}
	h, err := hex.DecodeString(stored)
	if err != nil || len(h) != sha256.Size {
		log.Warn().Msg("api token: the stored hash is not a sha256; the API stays open until it is set again")
		return nil
	}
	s.auth.hash = h
	s.auth.source = "config"
	return nil
}

// AuthRequired is whether a token is set.
func (s *Server) AuthRequired() bool {
	s.auth.mu.RLock()
	defer s.auth.mu.RUnlock()
	return s.auth.hash != nil
}

// AuthSource is where the token came from: "env", "config" or "".
func (s *Server) AuthSource() string {
	s.auth.mu.RLock()
	defer s.auth.mu.RUnlock()
	return s.auth.source
}

// TrustsLoopback is whether a request from the kit itself gets in alone.
func (s *Server) TrustsLoopback() bool {
	s.auth.mu.RLock()
	defer s.auth.mu.RUnlock()
	return s.auth.trustLoopback
}

// LocalAPIToken is the process-local token the Bridge's own HTTP calls to
// itself present (hubreporter's Hub-command shim), so they get in even
// with the loopback trust off. It exists only in this process's memory.
func (s *Server) LocalAPIToken() string {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	if s.auth.self == "" {
		s.auth.self = newToken()
	}
	return s.auth.self
}

// newToken is 32 random bytes as base64url: 43 characters.
func newToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("api token: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// tokenMatches compares a presented token with the one set, in constant
// time: both sides are SHA-256 digests, so the length is fixed and the
// comparison never stops at the first wrong byte. The empty string never
// matches, whatever is set.
func (s *Server) tokenMatches(presented string) bool {
	if presented == "" {
		return false
	}
	s.auth.mu.RLock()
	hash, self := s.auth.hash, s.auth.self
	s.auth.mu.RUnlock()
	if hash == nil {
		return false
	}
	got := sha256.Sum256([]byte(presented))
	if subtle.ConstantTimeCompare(got[:], hash) == 1 {
		return true
	}
	if self != "" {
		want := sha256.Sum256([]byte(self))
		return subtle.ConstantTimeCompare(got[:], want[:]) == 1
	}
	return false
}

// isSelfToken is whether presented is the process-local token (for the
// status report; tokenMatches has already accepted it).
func (s *Server) isSelfToken(presented string) bool {
	s.auth.mu.RLock()
	self := s.auth.self
	s.auth.mu.RUnlock()
	if self == "" || presented == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(presented)), sha256.Sum256([]byte(self))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func cookieToken(r *http.Request) string {
	c, err := r.Cookie(authCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// isLoopback is whether the request came from the kit itself. A relay
// tunnel's address is "pipe", which never parses, so the Hub relay is
// never mistaken for loopback; it has its own mark.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authenticate says how the request gets in, or viaNone. It never reads
// the body and never logs the credential.
func (s *Server) authenticate(r *http.Request) authVia {
	if !s.AuthRequired() {
		return viaOpen
	}
	if isRelayRequest(r) {
		return viaRelay
	}
	if t := bearerToken(r); t != "" && s.tokenMatches(t) {
		if s.isSelfToken(t) {
			return viaSelf
		}
		return viaBearer
	}
	if t := cookieToken(r); t != "" && s.tokenMatches(t) {
		return viaCookie
	}
	if s.TrustsLoopback() && isLoopback(r.RemoteAddr) {
		return viaLoopback
	}
	return viaNone
}

// authExempt is the two routes under /api a client reaches without a
// token: the one that says a token is needed and the one that takes it.
func authExempt(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/auth/status":
		return r.Method == http.MethodGet
	case "/api/auth/login":
		return r.Method == http.MethodPost
	}
	return false
}

// authMiddleware guards everything under /api. With no token set it is a
// pass through: the request reaches the handler untouched and the
// response carries nothing extra. The CORS middleware runs before it on
// the root router, so a preflight is answered as before.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.AuthRequired() {
			next.ServeHTTP(w, r)
			return
		}
		via := s.authenticate(r)
		if via == viaNone && !authExempt(r) {
			s.auth.refused.Add(1)
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			log.Debug().Str("remote", host).Str("method", r.Method).Str("path", r.URL.Path).Msg("api: refused, no valid token")
			w.Header().Set("WWW-Authenticate", `Bearer realm="meshsat"`)
			writeError(w, http.StatusUnauthorized, "api token required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viaCtxKey, via)))
	})
}

// requestVia is how the request in hand got in (set by authMiddleware).
func requestVia(r *http.Request) authVia {
	v, _ := r.Context().Value(viaCtxKey).(authVia)
	return v
}

// corsMiddleware answers cross-origin requests. Open API: `*`, as it has
// always been. With a token set, only the kit's own origin (the host the
// browser reached the kit by) is allowed, and a page on any other origin
// gets no Access-Control-Allow-Origin at all: a browser then refuses to
// hand it the response, token or not. Preflights are answered either way,
// before the token check, since a browser sends them without credentials.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.AuthRequired() {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin := r.Header.Get("Origin"); origin != "" && sameOrigin(origin, r.Host) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-HAL-Key")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin is whether an Origin header names the host the request came
// to (the kit's own origin, whatever name or address the browser used).
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// authStatus is GET /api/auth/status.
type authStatus struct {
	Required        bool   `json:"required"`                // a token is set
	Source          string `json:"source,omitempty"`        // env or config
	LoopbackTrusted bool   `json:"loopback_trusted"`        // the kit's own requests get in alone
	Authenticated   bool   `json:"authenticated"`           // this request would reach a protected route
	Via             string `json:"via,omitempty"`           // how: bearer, cookie, loopback, relay, self, open
	RefusedTotal    int64  `json:"refused_total,omitempty"` // requests refused since start
}

// handleAuthStatus reports whether the API needs a token and whether the
// request in hand has one. Reachable without a token: it tells a client
// no more than a 401 would.
// @Summary API token status
// @Description Whether a token is required (and where it came from: env or config), whether loopback callers are trusted, and whether this request is authenticated and how. Open to any caller; it reveals nothing a 401 would not. [MESHSAT-1738]
// @Tags auth
// @Produce json
// @Success 200 {object} authStatus
// @Router /api/auth/status [get]
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	via := requestVia(r)
	if via == viaNone && !s.AuthRequired() {
		via = viaOpen
	}
	writeJSON(w, http.StatusOK, authStatus{
		Required:        s.AuthRequired(),
		Source:          s.AuthSource(),
		LoopbackTrusted: s.TrustsLoopback(),
		Authenticated:   via != viaNone,
		Via:             string(via),
		RefusedTotal:    s.auth.refused.Load(),
	})
}

type authLoginBody struct {
	Token string `json:"token"`
}

// setAuthCookie gives the browser the token as an HttpOnly cookie on the
// API path, so its EventSource (which cannot send a header) gets in too.
// Secure only over TLS: on a kit's plain-HTTP LAN a Secure cookie would
// never be sent back.
func setAuthCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: authCookieName, Value: token, Path: "/api", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 86400 * 30,
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: "", Path: "/api", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// handleAuthLogin takes the token (in the body or as a Bearer header) and
// answers with the cookie. Reachable without a token, by design: it is the
// door. A wrong token is a 401 like any other.
// @Summary Log in with the API token
// @Description Takes the token in the JSON body or as Authorization: Bearer and sets the HttpOnly cookie the SPA's event stream needs. With no token set it answers 204 and sets nothing. [MESHSAT-1738]
// @Tags auth
// @Accept json
// @Param body body authLoginBody false "The token"
// @Success 204 "logged in"
// @Failure 401 {object} map[string]string
// @Router /api/auth/login [post]
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.AuthRequired() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	token := bearerToken(r)
	if token == "" && r.Body != nil {
		var body authLoginBody
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
		token = strings.TrimSpace(body.Token)
	}
	if !s.tokenMatches(token) || s.isSelfToken(token) {
		s.auth.refused.Add(1)
		w.Header().Set("WWW-Authenticate", `Bearer realm="meshsat"`)
		writeError(w, http.StatusUnauthorized, "api token refused")
		return
	}
	setAuthCookie(w, r, token)
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthLogout clears the cookie. The token itself stays valid.
// @Summary Log out
// @Description Clears the login cookie; the token stays as it is. [MESHSAT-1738]
// @Tags auth
// @Success 204 "logged out"
// @Router /api/auth/logout [post]
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	clearAuthCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type authTokenBody struct {
	// Token is the token to set; empty = the Bridge makes one.
	Token string `json:"token"`
}

type authTokenReply struct {
	// Token is returned once, here, and nowhere else: the Bridge keeps
	// only its hash.
	Token  string `json:"token"`
	Source string `json:"source"`
}

// mayChangeToken is who may set, rotate or clear the token: with a token
// set, any caller the middleware let in (it has the token, or is the kit
// itself, or came through the Hub); with the API still open, only the kit
// itself or the Hub relay, so that a stranger on the LAN cannot lock the
// owner out of an open kit.
func (s *Server) mayChangeToken(r *http.Request) bool {
	if !s.AuthRequired() {
		return isLoopback(r.RemoteAddr) || isRelayRequest(r)
	}
	return requestVia(r) != viaNone
}

// handleSetAuthToken sets or rotates the token in system_config and
// applies it at once; the new token is returned in this response only.
// @Summary Set or rotate the API token
// @Description Stores the token's SHA-256 in system_config and requires it from the next request on. Body token empty = a random 43-character token is made. Returned ONCE in the response; the Bridge never keeps or shows the token itself. Refused with 409 while MESHSAT_API_TOKEN sets the token (change the environment instead), and with 403 from the LAN while the API is still open (set the first token from the kit itself, through the Hub relay, or in the environment). Written to the audit log as api_token_set. [MESHSAT-1738]
// @Tags auth
// @Accept json
// @Produce json
// @Param body body authTokenBody false "The token to set; empty = generate"
// @Success 200 {object} authTokenReply
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/auth/token [put]
func (s *Server) handleSetAuthToken(w http.ResponseWriter, r *http.Request) {
	if !s.mayChangeToken(r) {
		writeError(w, http.StatusForbidden, "set the first token from the kit itself, through the Hub relay, or with MESHSAT_API_TOKEN")
		return
	}
	if s.AuthSource() == "env" {
		writeError(w, http.StatusConflict, "the token is set by MESHSAT_API_TOKEN; change it there")
		return
	}
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	var body authTokenBody
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil && err.Error() != "EOF" {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		token = newToken()
	} else if len(token) < minTokenChars {
		writeError(w, http.StatusBadRequest, "a token has at least 16 characters")
		return
	} else if strings.ContainsAny(token, " \t\r\n\"") {
		writeError(w, http.StatusBadRequest, "a token has no spaces or quotes")
		return
	}
	h := sha256.Sum256([]byte(token))
	if err := s.db.SetSystemConfig(authHashRow, hex.EncodeToString(h[:])); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auth.mu.Lock()
	s.auth.hash, s.auth.source = h[:], "config"
	s.auth.mu.Unlock()
	if s.signing != nil {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		detail, _ := json.Marshal(map[string]string{"via": string(requestVia(r)), "remote": host})
		s.signing.AuditEvent("api_token_set", nil, nil, nil, nil, string(detail))
	}
	log.Info().Msg("api token set; every request under /api needs it from now on")
	// The caller that set it is logged in at once if it is a browser.
	setAuthCookie(w, r, token)
	writeJSON(w, http.StatusOK, authTokenReply{Token: token, Source: "config"})
}

// handleClearAuthToken removes the token: the API is open again.
// @Summary Clear the API token
// @Description Deletes the stored token; the API is open again from the next request on. Refused with 409 while MESHSAT_API_TOKEN sets the token. Written to the audit log as api_token_cleared. [MESHSAT-1738]
// @Tags auth
// @Success 204 "cleared"
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /api/auth/token [delete]
func (s *Server) handleClearAuthToken(w http.ResponseWriter, r *http.Request) {
	if !s.mayChangeToken(r) {
		writeError(w, http.StatusForbidden, "not allowed from here")
		return
	}
	if s.AuthSource() == "env" {
		writeError(w, http.StatusConflict, "the token is set by MESHSAT_API_TOKEN; remove it there")
		return
	}
	if s.db != nil {
		if err := s.db.SetSystemConfig(authHashRow, ""); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.auth.mu.Lock()
	s.auth.hash, s.auth.source = nil, ""
	s.auth.mu.Unlock()
	if s.signing != nil {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		detail, _ := json.Marshal(map[string]string{"via": string(requestVia(r)), "remote": host})
		s.signing.AuditEvent("api_token_cleared", nil, nil, nil, nil, string(detail))
	}
	log.Warn().Msg("api token cleared; the API is open again")
	clearAuthCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
