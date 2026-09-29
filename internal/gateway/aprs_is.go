package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// APRS-IS Direct: mode "is" of the aprs gateway. [MESHSAT-1421]
//
// The Bridge logs in to an APRS-IS server over TCP the way MeshSat Android's
// AprsIsClient does (Android is the reference, v2.19.4): read the banner, send
// "user CALL-SSID pass PASSCODE vers MeshSat 1.0[ filter r/LAT/LON/KM]", read
// the server's "# logresp", then read TNC-2 text lines. There is no radio, no
// Direwolf and no KISS link in this mode. Unlike Android the session comes
// back by itself: 5 s after a loss, doubling to 5 min.
//
// What it sends ("CALL-SSID>APMSHT,TCPIP*:<info>"): a directed message or a
// BLN1 bulletin for a text, acks for directed messages to this station, and
// the position beacon (aprs_position_beacon.go). Nothing is sent before the
// server verified the passcode (passcode -1 is receive only), encrypted
// traffic is never sent, and a position at 0,0 never goes out.

// APRS link states, both modes. [MESHSAT-1421]
const (
	APRSStateDisconnected = "disconnected"
	APRSStateConnecting   = "connecting"
	APRSStateConnected    = "connected"
	APRSStateError        = "error"
)

// aprsISVersion is the software name and version the login announces,
// Android's.
const aprsISVersion = "MeshSat 1.0"

// APRS-IS timing. Package vars so tests can shorten them.
var (
	// Android's CONNECT_TIMEOUT_MS.
	aprsISDialTimeout = 15 * time.Second
	// Android's soTimeout. Servers send a "#" keepalive about every 20 s,
	// so 90 s without a line is a dead connection.
	aprsISReadTimeout = 90 * time.Second
	// How long one line may take to go out.
	aprsISWriteTimeout = 10 * time.Second
	// The wait before a reconnect: the first, doubling up to the last.
	aprsISBackoffMin = 5 * time.Second
	aprsISBackoffMax = 5 * time.Minute
)

// aprsISMaxLine bounds one line from the server. An APRS-IS packet is at
// most 512 bytes; a far longer line is not APRS-IS and ends the session.
const aprsISMaxLine = 4096

// aprsISMaxText is the longest message text APRS carries.
const aprsISMaxText = 67

// aprsDirectedRE is Android's "@CALL words" form of a directed message
// (GatewayService.kt:2524).
var aprsDirectedRE = regexp.MustCompile(`^@([A-Za-z0-9-]{1,9})\s+(.+)$`)

// aprsAddresseeRE is an addressee the gateway writes into a message.
var aprsAddresseeRE = regexp.MustCompile(`^[A-Z0-9-]{1,9}$`)

// aprsISRefusal is something APRS-IS Direct will not carry. It matches
// transport.ErrNoAck, so the delivery worker hands the message to the next
// member of the rule's failover group rather than retrying a link that will
// refuse it again. The text is the whole error, as the status shows it.
type aprsISRefusal string

func (e aprsISRefusal) Error() string        { return string(e) }
func (e aprsISRefusal) Is(target error) bool { return target == transport.ErrNoAck }

const (
	errAPRSISEncrypted   aprsISRefusal = "aprs-is: encrypted traffic is never sent to APRS-IS"
	errAPRSISReceiveOnly aprsISRefusal = "aprs-is: receive only (passcode -1 or not verified)"
	errAPRSISNoText      aprsISRefusal = "aprs-is: APRS-IS carries text only, and this message has none"
	errAPRSISControl     aprsISRefusal = "aprs-is: a line with control characters is never sent"
)

// aprsISLink is the APRS-IS session of a gateway in mode is: the connection
// while logged in, and what the status reports about it.
type aprsISLink struct {
	mu          sync.Mutex
	conn        net.Conn // nil unless logged in
	state       string
	verified    bool
	banner      string
	lastErr     string
	connectedAt time.Time

	wmu sync.Mutex // one line at a time on the connection

	rx atomic.Int64 // lines decoded as packets
	tx atomic.Int64 // lines sent: messages, acks, beacons
}

type aprsISSnapshot struct {
	state       string
	verified    bool
	banner      string
	lastErr     string
	connectedAt time.Time
}

func (l *aprsISLink) snapshot() aprsISSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return aprsISSnapshot{state: l.state, verified: l.verified, banner: l.banner, lastErr: l.lastErr, connectedAt: l.connectedAt}
}

func (l *aprsISLink) connecting() {
	l.mu.Lock()
	l.state = APRSStateConnecting
	l.mu.Unlock()
}

func (l *aprsISLink) loggedIn(conn net.Conn, banner string, verified bool) {
	l.mu.Lock()
	l.conn, l.state, l.banner, l.verified = conn, APRSStateConnected, banner, verified
	l.lastErr, l.connectedAt = "", time.Now()
	l.mu.Unlock()
}

func (l *aprsISLink) ended(state string, err error) {
	l.mu.Lock()
	l.conn, l.state, l.verified, l.connectedAt = nil, state, false, time.Time{}
	if err != nil {
		l.lastErr = err.Error()
	}
	l.mu.Unlock()
}

// current returns the logged-in connection (nil when there is none) and
// whether the server verified the passcode.
func (l *aprsISLink) current() (net.Conn, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != APRSStateConnected {
		return nil, false
	}
	return l.conn, l.verified
}

// startIS starts APRS-IS Direct. It returns at once: the session connects in
// the background, so a PUT of the gateway never waits for the internet.
func (g *APRSGateway) startIS() error {
	bgCtx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.startTime = time.Now()
	g.lifeMu.Lock()
	g.done = make(chan struct{})
	g.lifeMu.Unlock()
	g.is.connecting()
	g.running.Store(true)

	g.wg.Add(1)
	go g.runAPRSIS(bgCtx)
	if g.config.PositionBeacon {
		g.wg.Add(1)
		go g.positionBeaconWorker(bgCtx)
	}
	log.Info().Str("server", g.config.APRSISServer).Str("callsign", g.aprsISCall()).
		Bool("position_beacon", g.config.PositionBeacon).
		Msg("aprs gateway started: APRS-IS Direct, no radio")
	return nil
}

// runAPRSIS keeps the gateway logged in until ctx ends. After a failure or
// the server closing it waits 5 s, doubling to 5 min, and connects again; a
// session that got logged in starts the wait over.
func (g *APRSGateway) runAPRSIS(ctx context.Context) {
	defer g.wg.Done()
	wait := aprsISBackoffMin
	for {
		g.is.connecting()
		loggedIn, err := g.aprsISSession(ctx)
		if ctx.Err() != nil {
			return
		}
		// The server closing is Disconnected, anything else Error, as
		// Android's client reads it.
		state := APRSStateError
		if errors.Is(err, io.EOF) {
			state = APRSStateDisconnected
		} else {
			g.errors.Add(1)
		}
		g.is.ended(state, err)
		if loggedIn {
			wait = aprsISBackoffMin
		}
		log.Warn().Err(err).Str("server", g.config.APRSISServer).Dur("retry_in", wait).Msg("aprs-is: connection lost")
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > aprsISBackoffMax {
			wait = aprsISBackoffMax
		}
	}
}

// aprsISSession runs one connection: dial, banner, login, the server's
// "# logresp", then every line until the connection ends. loggedIn reports
// whether it got as far as the login response.
func (g *APRSGateway) aprsISSession(ctx context.Context) (loggedIn bool, err error) {
	server := g.config.APRSISServer
	if server == "" {
		server = DefaultAPRSISServer
	}
	pass := g.config.APRSISPass
	if pass == "" {
		pass = "-1"
	}
	// The filter centre is looked up before the dial, so a slow position
	// source never holds the login back once the server has greeted us.
	lat, lon := g.aprsISFilterCentre()
	login := aprsISLoginLine(g.aprsISCall(), pass, lat, lon, g.config.APRSISFilterKm)

	dialer := net.Dialer{Timeout: aprsISDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", server)
	if err != nil {
		return false, fmt.Errorf("aprs-is: dial %s: %w", server, err)
	}
	defer conn.Close()
	// Stop closes the connection, which ends a read in progress.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 1024), aprsISMaxLine)
	next := func(what string) (string, error) {
		if sc.Scan() {
			return sc.Text(), nil
		}
		if err := sc.Err(); err != nil {
			return "", fmt.Errorf("aprs-is: %s: %w", what, err)
		}
		return "", fmt.Errorf("aprs-is: %s: the server closed the connection: %w", what, io.EOF)
	}

	_ = conn.SetReadDeadline(time.Now().Add(aprsISReadTimeout))
	banner, err := next("no server banner")
	if err != nil {
		return false, err
	}

	_ = conn.SetWriteDeadline(time.Now().Add(aprsISWriteTimeout))
	if _, err := io.WriteString(conn, login+"\r\n"); err != nil {
		return false, fmt.Errorf("aprs-is: send login: %w", err)
	}

	// The reply is "# logresp CALL verified, server NAME", or unverified. A
	// keepalive that lands before it is skipped; the wait for it is one read
	// timeout in all, so a server that never answers is left.
	_ = conn.SetReadDeadline(time.Now().Add(aprsISReadTimeout))
	var resp string
	for {
		line, err := next("no login response")
		if err != nil {
			return false, err
		}
		if line == "" || strings.HasPrefix(line, "#") && !hasPrefixFold(line, "# logresp") {
			continue
		}
		resp = line
		break
	}
	verified := aprsISVerified(resp)
	g.is.loggedIn(conn, banner, verified)
	log.Info().Str("server", server).Str("banner", banner).Str("logresp", resp).Bool("verified", verified).
		Bool("filter", lat != 0 || lon != 0).Msg("aprs-is: logged in")
	if !verified {
		log.Warn().Msg("aprs-is: the server did not verify the passcode: receive only, nothing will be sent")
	}

	for {
		_ = conn.SetReadDeadline(time.Now().Add(aprsISReadTimeout))
		line, err := next("read")
		if err != nil {
			return true, err
		}
		g.handleAPRSISLine(line)
	}
}

// aprsISLoginLine is Android's login line exactly. The filter clause goes in
// only when there is a centre. Go writes %.1f with a point whatever the
// locale; Android's follows the phone's locale and can send "r/52,4/4,9".
func aprsISLoginLine(call, pass string, lat, lon float64, km int) string {
	line := fmt.Sprintf("user %s pass %s vers %s", call, pass, aprsISVersion)
	if lat != 0 || lon != 0 {
		line += fmt.Sprintf(" filter r/%.1f/%.1f/%d", lat, lon, km)
	}
	return line
}

// aprsISVerified reads the login response as Android does: verified when it
// says "verified" and not "unverified", in any case.
func aprsISVerified(resp string) bool {
	l := strings.ToLower(resp)
	return strings.Contains(l, "verified") && !strings.Contains(l, "unverified")
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// aprsISCall is the station's login and source callsign, CALL-SSID in upper
// case (the bare call for SSID 0).
func (g *APRSGateway) aprsISCall() string {
	return FormatCallsign(AX25Address{Call: strings.ToUpper(strings.TrimSpace(g.config.Callsign)), SSID: g.config.SSID})
}

// aprsISFilterCentre is the centre of the login's range filter: the
// configured point, or when both are 0 the device's own position at connect
// time, or 0,0 (no filter clause) with neither. [MESHSAT-1421]
func (g *APRSGateway) aprsISFilterCentre() (lat, lon float64) {
	if g.config.APRSISFilterLat != 0 || g.config.APRSISFilterLon != 0 {
		return g.config.APRSISFilterLat, g.config.APRSISFilterLon
	}
	if fix, ok := g.selfPosition(); ok {
		return fix.Latitude, fix.Longitude
	}
	return 0, 0
}

// handleAPRSISLine takes one line from the server. Server comments and
// keepalives ("#") are skipped; a packet goes through the same receive path
// as a frame from the TNC. A directed message to this station that asks for
// an ack gets one, as on Android, once the server verified the passcode.
func (g *APRSGateway) handleAPRSISLine(line string) {
	if line == "" || line[0] == '#' {
		return
	}
	pkt, err := ParseTNC2Line(line)
	if err != nil {
		g.badFrames.Add(1)
		log.Debug().Err(err).Str("line", CapPacketText(line)).Msg("aprs-is: line not decoded")
		return
	}
	g.is.rx.Add(1)
	g.lastFrameAt.Store(time.Now().UnixNano())
	if pkt.DataType == ':' && pkt.MsgID != "" && strings.EqualFold(strings.TrimSpace(pkt.MsgTo), g.aprsISCall()) {
		if _, _, isReply := aprsAckReply(pkt); !isReply {
			g.aprsISAck(pkt.Source, pkt.MsgID)
		}
	}
	g.handleParsedPacket(pkt, pkt.Source)
}

// aprsISAck acknowledges message id from to. Both come off the network, so
// both must be clean tokens before they go into a line of ours.
func (g *APRSGateway) aprsISAck(to, id string) {
	if !aprsISTokenOK(to, 9) || !aprsISAckIDOK(id) {
		return
	}
	if _, verified := g.is.current(); !verified {
		log.Debug().Str("to", to).Str("id", id).Msg("aprs-is: ack not sent, receive only")
		return
	}
	if !g.ackLedger.allow(strings.ToUpper(to)+"|"+id, time.Now()) {
		return
	}
	if err := g.aprsISTransmit(string(EncodeAPRSMessage(to, "ack"+id, ""))); err != nil {
		log.Debug().Err(err).Str("to", to).Str("id", id).Msg("aprs-is: ack not sent")
		return
	}
	g.acksSent.Add(1)
}

// aprsISTokenOK reports whether s is 1 to maxLen printable characters
// without spaces.
func aprsISTokenOK(s string, maxLen int) bool {
	if s == "" || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// aprsISAckIDOK accepts an APRS message id, five alphanumerics at most, or
// the reply-ack form "MM}AA".
func aprsISAckIDOK(id string) bool {
	if id == "" || len(id) > 11 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '}') {
			return false
		}
	}
	return true
}

// forwardIS sends a text to APRS-IS, synchronously, so the delivery worker
// learns the outcome: refused (encrypted, receive only, nothing to send), not
// connected (deferred and tried again), or sent.
func (g *APRSGateway) forwardIS(msg *transport.MeshMessage) error {
	if msg.Encrypted {
		return errAPRSISEncrypted
	}
	info, err := aprsISMessageInfo(msg)
	if err != nil {
		return err
	}
	if err := g.aprsISTransmit(info); err != nil {
		return err
	}
	g.msgsOut.Add(1)
	return nil
}

// aprsISMessageInfo is the info field a text goes out as, as Android sends
// it (GatewayService.kt:2513-2583): to the delivery's destination, or to CALL
// for a text "@CALL words", a directed message; anything else a BLN1
// bulletin. The text is cut to 67 characters. Control characters become
// spaces first, so a text can never start a second line of its own.
func aprsISMessageInfo(msg *transport.MeshMessage) (string, error) {
	text := aprsISCleanText(msg.DecodedText)
	if strings.TrimSpace(text) == "" {
		return "", errAPRSISNoText
	}
	if dest := strings.ToUpper(strings.TrimSpace(msg.Destination)); dest != "" {
		if !aprsAddresseeRE.MatchString(dest) {
			return "", aprsISRefusal(fmt.Sprintf("aprs-is: destination %q is not a callsign", msg.Destination))
		}
		return string(EncodeAPRSMessage(dest, aprsCutRunes(text, aprsISMaxText), "")), nil
	}
	if m := aprsDirectedRE.FindStringSubmatch(text); m != nil {
		return string(EncodeAPRSMessage(strings.ToUpper(m[1]), aprsCutRunes(m[2], aprsISMaxText), "")), nil
	}
	return string(EncodeAPRSMessage("BLN1", aprsCutRunes(text, aprsISMaxText), "")), nil
}

// aprsISCleanText replaces every control character with a space.
func aprsISCleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// aprsCutRunes keeps the first n characters of s.
func aprsCutRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// aprsISTransmit sends one packet, "CALL-SSID>APMSHT,TCPIP*:<info>", once
// the server has verified the passcode. Unverified, the server would drop the
// packet; the gateway refuses it itself so the sender knows.
func (g *APRSGateway) aprsISTransmit(info string) error {
	line := g.aprsISCall() + ">APMSHT,TCPIP*:" + info
	for i := 0; i < len(line); i++ {
		if line[i] < 0x20 || line[i] == 0x7f {
			return errAPRSISControl
		}
	}
	conn, verified := g.is.current()
	if conn == nil {
		return fmt.Errorf("aprs-is: %w", transport.ErrNotConnected)
	}
	if !verified {
		return errAPRSISReceiveOnly
	}
	g.is.wmu.Lock()
	defer g.is.wmu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(aprsISWriteTimeout))
	if _, err := io.WriteString(conn, line+"\r\n"); err != nil {
		g.errors.Add(1)
		_ = conn.Close() // the reader ends the session and reconnects
		return fmt.Errorf("aprs-is: send: %v: %w", err, transport.ErrNotConnected)
	}
	g.is.tx.Add(1)
	g.tracker.RecordTX()
	g.lastActive.Store(time.Now().Unix())
	log.Debug().Str("line", CapPacketText(line)).Msg("aprs-is: sent")
	return nil
}

// aprsISStatus is GetAPRSStatus in mode is. The RF fields of mode kiss
// (frequency, KISS link, Direwolf, receive health) do not exist here.
func (g *APRSGateway) aprsISStatus() map[string]interface{} {
	s := g.is.snapshot()
	connected := s.state == APRSStateConnected
	uptime := ""
	if connected && !s.connectedAt.IsZero() {
		uptime = time.Since(s.connectedAt).Round(time.Second).String()
	}
	lastBeacon := ""
	if ts := g.lastPositionBeaconAt.Load(); ts > 0 {
		lastBeacon = time.Unix(0, ts).UTC().Format(time.RFC3339)
	}
	return map[string]interface{}{
		"connected":           connected,
		"mode":                APRSModeIS,
		"state":               s.state,
		"last_error":          s.lastErr,
		"callsign":            g.aprsISCall(),
		"uptime":              uptime,
		"rx":                  g.is.rx.Load(),
		"tx":                  g.is.tx.Load(),
		"errors":              g.errors.Load(),
		"bad_frames":          g.badFrames.Load(),
		"third_party_dropped": g.thirdPartyDropped.Load(),
		"acks_sent":           g.acksSent.Load(),
		"acks_received":       g.acksReceived.Load(),
		"heard_count":         len(g.tracker.GetHeardStations()),
		"packet_types":        g.tracker.GetPacketTypeBreakdown(),
		"direwolf_bundled":    false,
		"aprs_is_server":      g.config.APRSISServer,
		"aprs_is_verified":    s.verified,
		"aprs_is_banner":      s.banner,
		"position_beacons":    g.positionBeacons.Load(),
		"last_beacon_at":      lastBeacon,
	}
}

// aprsISGatewayStatus is Status in mode is: connected is the APRS-IS state.
func (g *APRSGateway) aprsISGatewayStatus() GatewayStatus {
	s := g.is.snapshot()
	connected := s.state == APRSStateConnected
	bundled := false
	verified := s.verified
	beacons := g.positionBeacons.Load()
	st := GatewayStatus{
		Type:            "aprs",
		Connected:       connected,
		MessagesIn:      g.msgsIn.Load(),
		MessagesOut:     g.msgsOut.Load(),
		Errors:          g.errors.Load(),
		DirewolfBundled: &bundled,
		Mode:            APRSModeIS,
		State:           s.state,
		LastError:       s.lastErr,
		APRSISServer:    g.config.APRSISServer,
		APRSISVerified:  &verified,
		APRSISBanner:    s.banner,
		PositionBeacons: &beacons,
	}
	if ts := g.lastActive.Load(); ts > 0 {
		st.LastActivity = time.Unix(ts, 0)
	}
	if connected && !s.connectedAt.IsZero() {
		st.ConnectionUptime = time.Since(s.connectedAt).Truncate(time.Second).String()
	}
	if ts := g.lastPositionBeaconAt.Load(); ts > 0 {
		at := time.Unix(0, ts).UTC()
		st.LastBeaconAt = &at
	}
	if bad := g.badFrames.Load(); bad > 0 {
		st.BadFrames = &bad
	}
	return st
}
