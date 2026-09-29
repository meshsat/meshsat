package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/selfpos"
	"meshsat/internal/transport"
)

// Every APRS-IS test talks to fakeAPRSIS on 127.0.0.1. None may ever reach a
// real APRS-IS server: each gateway's aprs_is_server is the fake's address
// before it starts. [MESHSAT-1421]

const (
	logrespVerified   = "# logresp N0CALL-10 verified, server T2TEST"
	logrespUnverified = "# logresp N0CALL-10 unverified, server T2TEST"
	fakeISBanner      = "# aprsc 2.1.14-gd8b8bc1 (fake APRS-IS on loopback)"
)

// fakeAPRSIS is an APRS-IS server on loopback: it writes the banner, records
// the login line, answers with logresp, then records every line the client
// sends and writes what the test gives it.
type fakeAPRSIS struct {
	ln      net.Listener
	quit    chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	logresp string
	delay   []time.Duration // banner delay of the n-th connection
	accepts []time.Time
	logins  []string
	lines   []string
	cur     net.Conn // the logged-in connection
	all     []net.Conn
}

func newFakeAPRSIS(t *testing.T, logresp string) *fakeAPRSIS {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake APRS-IS: listen: %v", err)
	}
	f := &fakeAPRSIS{ln: ln, quit: make(chan struct{}), logresp: logresp}
	f.wg.Add(1)
	go f.accept()
	t.Cleanup(f.close)
	return f
}

func (f *fakeAPRSIS) addr() string { return f.ln.Addr().String() }

func (f *fakeAPRSIS) accept() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		n := len(f.accepts)
		f.accepts = append(f.accepts, time.Now())
		f.all = append(f.all, c)
		var d time.Duration
		if n < len(f.delay) {
			d = f.delay[n]
		}
		f.mu.Unlock()
		f.wg.Add(1)
		go f.serve(c, d)
	}
}

func (f *fakeAPRSIS) serve(c net.Conn, bannerDelay time.Duration) {
	defer f.wg.Done()
	defer c.Close()
	if bannerDelay > 0 {
		select {
		case <-f.quit:
			return
		case <-time.After(bannerDelay):
		}
	}
	if _, err := fmt.Fprintf(c, "%s\r\n", fakeISBanner); err != nil {
		return
	}
	r := bufio.NewReader(c)
	login, err := r.ReadString('\n')
	if err != nil {
		return
	}
	// The connection is the current one before the client can read the
	// reply, so a test that saw the login complete can always reach it.
	f.mu.Lock()
	f.logins = append(f.logins, strings.TrimRight(login, "\r\n"))
	resp := f.logresp
	f.cur = c
	f.mu.Unlock()
	if _, err := fmt.Fprintf(c, "%s\r\n", resp); err != nil {
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		f.mu.Lock()
		f.lines = append(f.lines, strings.TrimRight(line, "\r\n"))
		f.mu.Unlock()
	}
}

func (f *fakeAPRSIS) close() {
	close(f.quit)
	f.ln.Close()
	f.mu.Lock()
	for _, c := range f.all {
		c.Close()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

// send writes one line to the logged-in client.
func (f *fakeAPRSIS) send(t *testing.T, line string) {
	t.Helper()
	f.mu.Lock()
	c := f.cur
	f.mu.Unlock()
	if c == nil {
		t.Fatal("fake APRS-IS: no client logged in")
	}
	if _, err := fmt.Fprintf(c, "%s\r\n", line); err != nil {
		t.Fatalf("fake APRS-IS: send: %v", err)
	}
}

// dropClient closes the logged-in connection, as a server going away.
func (f *fakeAPRSIS) dropClient() {
	f.mu.Lock()
	c := f.cur
	f.cur = nil
	f.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func (f *fakeAPRSIS) snapshot() (logins, lines []string, accepts []time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logins...), append([]string(nil), f.lines...), append([]time.Time(nil), f.accepts...)
}

func (f *fakeAPRSIS) waitLogins(t *testing.T, n int, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		logins, _, _ := f.snapshot()
		if len(logins) >= n {
			return logins
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake APRS-IS: %d login(s) after %s, want %d", len(logins), within, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fakeAPRSIS) waitLine(t *testing.T, want string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		_, lines, _ := f.snapshot()
		for _, l := range lines {
			if l == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake APRS-IS never received %q; got %q", want, lines)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// setAPRSISKnobs shortens the APRS-IS timing for one test. Gateways started
// in the test are stopped by their own cleanups, which run first.
func setAPRSISKnobs(t *testing.T, backoff time.Duration) {
	t.Helper()
	dial, read, write, bmin, bmax, check := aprsISDialTimeout, aprsISReadTimeout, aprsISWriteTimeout, aprsISBackoffMin, aprsISBackoffMax, positionBeaconCheck
	aprsISDialTimeout, aprsISReadTimeout, aprsISWriteTimeout = 2*time.Second, 5*time.Second, 2*time.Second
	aprsISBackoffMin, aprsISBackoffMax = backoff, 4*backoff
	t.Cleanup(func() {
		aprsISDialTimeout, aprsISReadTimeout, aprsISWriteTimeout = dial, read, write
		aprsISBackoffMin, aprsISBackoffMax, positionBeaconCheck = bmin, bmax, check
	})
}

// newISGateway builds a mode-is gateway for N0CALL-10 on the fake, validated
// as the manager would. It is not started.
func newISGateway(t *testing.T, f *fakeAPRSIS, mutate func(*APRSConfig)) *APRSGateway {
	t.Helper()
	cfg := DefaultAPRSConfig()
	cfg.Mode = APRSModeIS
	cfg.Callsign = "N0CALL"
	cfg.SSID = 10
	cfg.APRSISServer = f.addr()
	cfg.APRSISPass = "13023"
	if mutate != nil {
		mutate(&cfg)
	}
	if !strings.HasPrefix(cfg.APRSISServer, "127.0.0.1:") {
		t.Fatalf("test gateway pointed at %q: only the loopback fake is allowed", cfg.APRSISServer)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return NewAPRSGateway(cfg, nil)
}

func startGateway(t *testing.T, g *APRSGateway) {
	t.Helper()
	if err := g.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop() })
}

func waitISState(t *testing.T, g *APRSGateway, want string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		s := g.is.snapshot()
		if s.state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("APRS-IS state %q (last_error %q) after %s, want %q", s.state, s.lastErr, within, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// aprsISPasscode is Android's AprsIsPasscode.calculate, here only to derive
// the passcode the login vectors use.
func aprsISPasscode(callsign string) string {
	base := strings.ToUpper(strings.SplitN(callsign, "-", 2)[0])
	if base == "" {
		return "-1"
	}
	h := 0x73E2
	for i := 0; i < len(base); i += 2 {
		h ^= int(base[i]) << 8
		if i+1 < len(base) {
			h ^= int(base[i+1])
		}
	}
	return fmt.Sprint(h & 0x7FFF)
}

// The AprsIsPasscodeTest vectors, so the 13023 of the login vector is
// traceable to Android's algorithm.
func TestAPRSISPasscodeVectors(t *testing.T) {
	for call, want := range map[string]string{"N0CALL": "13023", "PA3XYZ": "18849", "PA3ABC": "21153", "W3ADO": "10901", "N0CAL": "12947", "": "-1", "-5": "-1"} {
		if got := aprsISPasscode(call); got != want {
			t.Errorf("passcode(%q) = %s, want %s", call, got, want)
		}
	}
	if aprsISPasscode("PA3XYZ-10") != aprsISPasscode("PA3XYZ") || aprsISPasscode("pa3xyz") != aprsISPasscode("PA3XYZ") {
		t.Error("SSID or case changed the passcode")
	}
}

// The 12 AprsIsClientTest vectors, plus a source no AX.25 address can hold.
func TestParseTNC2Line(t *testing.T) {
	near := func(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

	p, err := ParseTNC2Line("PA3XYZ-10>APMSHT,WIDE1-1:!5222.06N/00454.25E-MeshSat Gateway")
	if err != nil || p.Source != "PA3XYZ-10" || p.Dest != "APMSHT" || p.Path != "WIDE1-1" || p.DataType != '!' ||
		!near(p.Lat, 52.3676, 0.001) || !near(p.Lon, 4.9041, 0.001) || !strings.Contains(p.Comment, "MeshSat") {
		t.Errorf("1 position without timestamp: %+v %v", p, err)
	}
	p, err = ParseTNC2Line("N0CALL>APRS,TCPIP*:/092345z4903.50N/07201.75W-PHG2360")
	if err != nil || p.Source != "N0CALL" || p.DataType != '/' || !near(p.Lat, 49.0583, 0.01) || !near(p.Lon, -72.0291, 0.01) {
		t.Errorf("2 position with timestamp: %+v %v", p, err)
	}
	p, err = ParseTNC2Line("PA3ABC-5>APRS,TCPIP*::PA3XYZ-10:Hello from APRS-IS{42")
	if err != nil || p.Source != "PA3ABC-5" || p.DataType != ':' || p.MsgTo != "PA3XYZ-10" || p.Message != "Hello from APRS-IS" || p.MsgID != "42" {
		t.Errorf("3 message with id: %+v %v", p, err)
	}
	p, err = ParseTNC2Line("W3ADO-1>APRS::PA3XYZ   :Weather alert")
	if err != nil || p.MsgTo != "PA3XYZ" || p.Message != "Weather alert" || p.MsgID != "" {
		t.Errorf("4 message without id: %+v %v", p, err)
	}
	p, err = ParseTNC2Line("PA3XYZ-10>APRS,TCPIP*::PA3ABC-5 :ack42")
	if err != nil || p.MsgTo != "PA3ABC-5" || p.Message != "ack42" {
		t.Errorf("5 ack: %+v %v", p, err)
	}
	p, err = ParseTNC2Line("VK2ABC>APRS:!3352.00S/15112.00W-Test")
	if err != nil || !(p.Lat < 0) || !(p.Lon < 0) {
		t.Errorf("6 south west: %+v %v", p, err)
	}
	p, err = ParseTNC2Line("N0CALL>APRS,WIDE1-1,WIDE2-1,qAR,RELAY:!0000.00N/00000.00E-test")
	if err != nil || p.Path != "WIDE1-1,WIDE2-1,qAR,RELAY" {
		t.Errorf("7 multi-hop path: %+v %v", p, err)
	}
	for i, bad := range []string{"malformed line", "SRC>DST", ">DST:data"} {
		if p, err := ParseTNC2Line(bad); err == nil || p != nil {
			t.Errorf("%d %q: got %+v, want an error", 8+i, bad, p)
		}
	}
	p, err = ParseTNC2Line("N0CALL>APRS:>Status text here")
	if err != nil || p.DataType != '>' || p.Raw != ">Status text here" {
		t.Errorf("11 unknown data type: %+v %v", p, err)
	}
	p, err = ParseTNC2Line("TEST>APRS:!4903.50N/07201.75W-test")
	if err != nil || !near(p.Lat, 49.0583, 0.0005) || !near(p.Lon, -72.0291, 0.0005) {
		t.Errorf("12 precision: %+v %v", p, err)
	}
	// The source stays as the server wrote it.
	p, err = ParseTNC2Line("OE3XYZ-WX>APRS,TCPIP*,qAC,T2TEST:!4903.50N/07201.75W_weather")
	if err != nil || p.Source != "OE3XYZ-WX" || p.Path != "TCPIP*,qAC,T2TEST" {
		t.Errorf("verbatim source: %+v %v", p, err)
	}
}

func TestAPRSISLogin(t *testing.T) {
	setAPRSISKnobs(t, 50*time.Millisecond)
	pass := aprsISPasscode("N0CALL")
	amsterdam := func(c *APRSConfig) {
		c.APRSISPass = pass
		c.APRSISFilterLat, c.APRSISFilterLon, c.APRSISFilterKm = 52.3676, 4.9041, 100
	}

	t.Run("the configured centre", func(t *testing.T) {
		f := newFakeAPRSIS(t, logrespVerified)
		g := newISGateway(t, f, amsterdam)
		startGateway(t, g)
		if got, want := f.waitLogins(t, 1, 5*time.Second)[0], "user N0CALL-10 pass 13023 vers MeshSat 1.0 filter r/52.4/4.9/100"; got != want {
			t.Fatalf("login\n got %q\nwant %q", got, want)
		}
		waitISState(t, g, APRSStateConnected, 5*time.Second)
		if _, verified := g.is.current(); !verified {
			t.Fatal("verified logresp read as not verified")
		}
	})

	t.Run("no centre gives no filter", func(t *testing.T) {
		f := newFakeAPRSIS(t, logrespVerified)
		g := newISGateway(t, f, func(c *APRSConfig) { c.APRSISPass = pass })
		startGateway(t, g)
		if got, want := f.waitLogins(t, 1, 5*time.Second)[0], "user N0CALL-10 pass 13023 vers MeshSat 1.0"; got != want {
			t.Fatalf("login\n got %q\nwant %q", got, want)
		}
	})

	t.Run("the own position is the centre when the config has none", func(t *testing.T) {
		f := newFakeAPRSIS(t, logrespVerified)
		g := newISGateway(t, f, func(c *APRSConfig) { c.APRSISPass = pass })
		g.SetSelfPosition(func() (selfpos.Fix, bool) {
			return selfpos.Fix{Latitude: 52.3676, Longitude: 4.9041}, true
		})
		startGateway(t, g)
		if got, want := f.waitLogins(t, 1, 5*time.Second)[0], "user N0CALL-10 pass 13023 vers MeshSat 1.0 filter r/52.4/4.9/100"; got != want {
			t.Fatalf("login\n got %q\nwant %q", got, want)
		}
	})

	t.Run("unverified", func(t *testing.T) {
		f := newFakeAPRSIS(t, logrespUnverified)
		g := newISGateway(t, f, amsterdam)
		startGateway(t, g)
		waitISState(t, g, APRSStateConnected, 5*time.Second)
		if _, verified := g.is.current(); verified {
			t.Fatal("unverified logresp read as verified")
		}
	})

	t.Run("no passcode is receive only", func(t *testing.T) {
		f := newFakeAPRSIS(t, logrespUnverified)
		g := newISGateway(t, f, func(c *APRSConfig) {
			amsterdam(c)
			c.APRSISPass = ""
		})
		startGateway(t, g)
		if got, want := f.waitLogins(t, 1, 5*time.Second)[0], "user N0CALL-10 pass -1 vers MeshSat 1.0 filter r/52.4/4.9/100"; got != want {
			t.Fatalf("login\n got %q\nwant %q", got, want)
		}
	})
}

func TestAPRSISVerifiedReading(t *testing.T) {
	for resp, want := range map[string]bool{
		logrespVerified:   true,
		logrespUnverified: false,
		"# LOGRESP N0CALL-10 VERIFIED, server T2TEST": true,
		"# logresp N0CALL-10 UnVerified, server X":    false,
		"":                   false,
		"# aprsc 2.1.14-gd8": false,
	} {
		if got := aprsISVerified(resp); got != want {
			t.Errorf("aprsISVerified(%q) = %v, want %v", resp, got, want)
		}
	}
}

// The server closes: the state reads disconnected, then connecting, and the
// client logs in again by itself.
func TestAPRSISReconnect(t *testing.T) {
	setAPRSISKnobs(t, 300*time.Millisecond)
	f := newFakeAPRSIS(t, logrespVerified)
	f.delay = []time.Duration{0, 400 * time.Millisecond} // the second banner waits, so connecting shows
	g := newISGateway(t, f, nil)
	startGateway(t, g)
	f.waitLogins(t, 1, 5*time.Second)
	waitISState(t, g, APRSStateConnected, 5*time.Second)

	f.dropClient()
	waitISState(t, g, APRSStateDisconnected, 3*time.Second)
	if st := g.GetAPRSStatus(); !strings.Contains(fmt.Sprint(st["last_error"]), "closed the connection") || st["connected"] != false {
		t.Fatalf("after the server closed: last_error %q connected %v", st["last_error"], st["connected"])
	}
	waitISState(t, g, APRSStateConnecting, 3*time.Second)
	waitISState(t, g, APRSStateConnected, 5*time.Second)
	logins := f.waitLogins(t, 2, 5*time.Second)
	if logins[1] != logins[0] {
		t.Fatalf("second login %q differs from the first %q", logins[1], logins[0])
	}
	if st := g.GetAPRSStatus(); st["last_error"] != "" || st["connected"] != true {
		t.Fatalf("after the reconnect: last_error %q connected %v", st["last_error"], st["connected"])
	}
}

// A server that fails every session gets the wait doubled each time, up to
// the cap.
func TestAPRSISReconnectBackoffDoubles(t *testing.T) {
	setAPRSISKnobs(t, 40*time.Millisecond) // 40, 80, 160, then 160
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var accepts []time.Time
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepts = append(accepts, time.Now())
			mu.Unlock()
			c.Close() // no banner: the session fails before the login
		}
	}()
	t.Cleanup(func() { ln.Close() })
	cfg := DefaultAPRSConfig()
	cfg.Mode, cfg.Callsign, cfg.APRSISServer = APRSModeIS, "N0CALL", ln.Addr().String()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	g := NewAPRSGateway(cfg, nil)
	startGateway(t, g)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(accepts)
		mu.Unlock()
		if n >= 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connects in 10 s, want 6", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = g.Stop()
	mu.Lock()
	defer mu.Unlock()
	gap := func(i int) time.Duration { return accepts[i+1].Sub(accepts[i]) }
	for i, least := range []time.Duration{40, 80, 160, 160} {
		if gap(i) < least*time.Millisecond {
			t.Errorf("wait %d: %s, want at least %dms", i+1, gap(i), least)
		}
	}
	if gap(4) > 2*time.Second {
		t.Errorf("wait 5: %s, the cap is 160ms", gap(4))
	}
	if st := g.is.snapshot(); st.state != APRSStateDisconnected {
		t.Errorf("after Stop: state %q", st.state)
	}
}

// Keepalives are ignored; a message to this station reaches the inbound
// channel through the same path as a frame from the TNC and is acked; a
// third-party position stays in the heard list.
func TestAPRSISInboundFiltering(t *testing.T) {
	setAPRSISKnobs(t, 50*time.Millisecond)
	f := newFakeAPRSIS(t, logrespVerified)
	g := newISGateway(t, f, nil)
	startGateway(t, g)
	f.waitLogins(t, 1, 5*time.Second)
	waitISState(t, g, APRSStateConnected, 5*time.Second)

	f.send(t, "# aprsc 2.1.14-gd8b8bc1 29 Sep 2026 06:00:00 GMT T2TEST 127.0.0.1:14580")
	f.send(t, "PA3ABC-5>APRS,TCPIP*::N0CALL-10:hi{7")
	select {
	case msg := <-g.Receive():
		if msg.Text != "[APRS:PA3ABC-5→N0CALL-10] hi" || msg.FromAddr != "PA3ABC-5" || msg.Source != "aprs" {
			t.Fatalf("inbound message %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the message to N0CALL-10 never reached the inbound channel")
	}
	f.waitLine(t, "N0CALL-10>APMSHT,TCPIP*::PA3ABC-5 :ack7", 5*time.Second)

	// Third-party traffic and an ack for someone: the heard list only.
	f.send(t, "PD0PYL-5>APRS,TCPIP*,qAC,T2TEST:!5222.06N/00454.25E-passing by")
	f.send(t, "OE3XYZ-WX>APRS,TCPIP*,qAC,T2TEST:!4903.50N/07201.75W_weather")
	f.send(t, "PA3ABC-5>APRS,TCPIP*::N0CALL-10:ack3")
	deadline := time.Now().Add(5 * time.Second)
	for g.is.rx.Load() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("lines decoded: %d, want 4", g.is.rx.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case msg := <-g.Receive():
		t.Fatalf("delivered as a message: %q", msg.Text)
	case <-time.After(300 * time.Millisecond):
	}
	if n := g.thirdPartyDropped.Load(); n != 2 {
		t.Fatalf("third_party_dropped %d, want 2", n)
	}
	heard := map[string]bool{}
	for _, h := range g.tracker.GetHeardStations() {
		heard[h.Callsign] = true
	}
	if len(heard) != 3 || !heard["PA3ABC-5"] || !heard["PD0PYL-5"] || !heard["OE3XYZ-WX"] {
		t.Fatalf("heard list %v, want PA3ABC-5, PD0PYL-5 and OE3XYZ-WX (no keepalive)", heard)
	}
	if n := g.badFrames.Load(); n != 0 {
		t.Fatalf("bad_frames %d: a keepalive was taken for a packet", n)
	}
	if n := g.acksSent.Load(); n != 1 {
		t.Fatalf("acks_sent %d, want 1", n)
	}
}

func TestAPRSISForward(t *testing.T) {
	setAPRSISKnobs(t, 50*time.Millisecond)
	ctx := context.Background()
	text := func(s string) *transport.MeshMessage { return &transport.MeshMessage{PortNum: 1, DecodedText: s} }

	t.Run("verified", func(t *testing.T) {
		f := newFakeAPRSIS(t, logrespVerified)
		g := newISGateway(t, f, nil)
		startGateway(t, g)
		f.waitLogins(t, 1, 5*time.Second)
		waitISState(t, g, APRSStateConnected, 5*time.Second)

		err := g.Forward(ctx, &transport.MeshMessage{PortNum: 1, DecodedText: "c2VjcmV0", Encrypted: true})
		if err == nil || err.Error() != "aprs-is: encrypted traffic is never sent to APRS-IS" || !errors.Is(err, transport.ErrNoAck) {
			t.Fatalf("encrypted: %v", err)
		}
		for _, c := range []struct {
			msg  *transport.MeshMessage
			line string
		}{
			{text("@PA3ABC hello"), "N0CALL-10>APMSHT,TCPIP*::PA3ABC   :hello"},
			{text("@pa3abc-7 lower case call"), "N0CALL-10>APMSHT,TCPIP*::PA3ABC-7 :lower case call"},
			{text("hello all"), "N0CALL-10>APMSHT,TCPIP*::BLN1     :hello all"},
			{&transport.MeshMessage{PortNum: 1, DecodedText: "reply", Destination: "pa3xyz-7"}, "N0CALL-10>APMSHT,TCPIP*::PA3XYZ-7 :reply"},
			{text(strings.Repeat("x", 100)), "N0CALL-10>APMSHT,TCPIP*::BLN1     :" + strings.Repeat("x", 67)},
			// A line break in a text never starts a second APRS-IS line.
			{text("one\r\nEVIL>APRS:!0000.00N/00000.00E-x"), "N0CALL-10>APMSHT,TCPIP*::BLN1     :one  EVIL>APRS:!0000.00N/00000.00E-x"},
		} {
			if err := g.Forward(ctx, c.msg); err != nil {
				t.Fatalf("forward %q: %v", c.msg.DecodedText, err)
			}
			f.waitLine(t, c.line, 5*time.Second)
		}
		_, lines, _ := f.snapshot()
		for _, l := range lines {
			if !strings.HasPrefix(l, "N0CALL-10>APMSHT,TCPIP*:") {
				t.Fatalf("a line not from this station: %q", l)
			}
		}
		if err := g.Forward(ctx, &transport.MeshMessage{RawPayload: []byte{1, 2, 3}}); !errors.Is(err, transport.ErrNoAck) {
			t.Fatalf("binary payload: %v", err)
		}
		if err := g.Forward(ctx, &transport.MeshMessage{DecodedText: "x", Destination: "NOT A CALL"}); !errors.Is(err, transport.ErrNoAck) {
			t.Fatalf("bad destination: %v", err)
		}
		if n := g.Status().MessagesOut; n != 6 {
			t.Fatalf("messages_out %d, want 6", n)
		}
	})

	t.Run("receive only", func(t *testing.T) {
		f := newFakeAPRSIS(t, logrespUnverified)
		g := newISGateway(t, f, func(c *APRSConfig) { c.APRSISPass = "-1" })
		startGateway(t, g)
		f.waitLogins(t, 1, 5*time.Second)
		waitISState(t, g, APRSStateConnected, 5*time.Second)

		err := g.Forward(ctx, text("hello"))
		if err == nil || err.Error() != "aprs-is: receive only (passcode -1 or not verified)" || !errors.Is(err, transport.ErrNoAck) {
			t.Fatalf("unverified forward: %v", err)
		}
		if err := g.Forward(ctx, &transport.MeshMessage{DecodedText: "x", Encrypted: true}); err != errAPRSISEncrypted {
			t.Fatalf("encrypted while unverified: %v", err)
		}
		// Receive still works, and a message asking for an ack gets none.
		f.send(t, "PA3ABC-5>APRS,TCPIP*::N0CALL-10:hi{7")
		select {
		case <-g.Receive():
		case <-time.After(5 * time.Second):
			t.Fatal("receive only must still receive")
		}
		time.Sleep(200 * time.Millisecond)
		if _, lines, _ := f.snapshot(); len(lines) != 0 {
			t.Fatalf("receive only sent %q", lines)
		}
		if n := g.Status().MessagesOut; n != 0 {
			t.Fatalf("messages_out %d, want 0", n)
		}
	})

	t.Run("not connected", func(t *testing.T) {
		f := newFakeAPRSIS(t, logrespVerified)
		g := newISGateway(t, f, nil) // never started
		if err := g.Forward(ctx, text("hello")); !errors.Is(err, transport.ErrNotConnected) {
			t.Fatalf("forward before the login: %v", err)
		}
		if err := g.Enqueue(text("hello")); !errors.Is(err, transport.ErrNotConnected) {
			t.Fatalf("enqueue before the login: %v", err)
		}
	})
}

// Mode is never touches the RF paths: no Direwolf supervisor, nothing on
// the KISS link, and no receive health for the watchdog to act on.
func TestAPRSISHasNoRadio(t *testing.T) {
	cfg := DefaultAPRSConfig()
	cfg.Mode, cfg.Callsign = APRSModeIS, "N0CALL"
	cfg.KISSDevice = "/dev/ttyUSB9" // ignored in mode is
	g := NewAPRSGateway(cfg, nil)
	if g.supervisor != nil || g.SerialTNC() || cfg.SerialTNC() {
		t.Fatal("mode is built a Direwolf supervisor or a serial TNC")
	}
	if err := g.KISSSendFrame([]byte{1}); !errors.Is(err, transport.ErrNotConnected) {
		t.Fatalf("KISSSendFrame: %v", err)
	}
	if err := g.ReopenTNC(context.Background()); err == nil {
		t.Fatal("ReopenTNC in mode is must fail")
	}
	if _, ok := g.ReceiveHealth(); ok {
		t.Fatal("mode is reported receive health")
	}
	g.SetReceiveState(ReceiveStateDeaf)
	if _, has := g.GetAPRSStatus()["receive_state"]; has {
		t.Fatal("mode is reported a receive state")
	}
}
