package gateway

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// mockKISSTNC is a TCP server that simulates a Direwolf KISS TNC.
type mockKISSTNC struct {
	listener net.Listener
	received [][]byte // raw AX.25 payloads received (KISS-decoded)
	mu       sync.Mutex
	wg       sync.WaitGroup
	sendCh   chan []byte // frames to send to the client
}

func newMockKISSTNC(t *testing.T) *mockKISSTNC {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock kiss: listen: %v", err)
	}
	s := &mockKISSTNC{
		listener: l,
		sendCh:   make(chan []byte, 10),
	}
	s.wg.Add(1)
	go s.accept()
	return s
}

func (s *mockKISSTNC) accept() {
	defer s.wg.Done()
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	s.wg.Add(2)
	go s.readFromClient(conn)
	go s.writeToClient(conn)
}

func (s *mockKISSTNC) readFromClient(conn net.Conn) {
	defer s.wg.Done()
	buf := make([]byte, 4096)
	var frame []byte
	inFrame := false

	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			if b == kissFEND {
				if inFrame && len(frame) > 0 {
					decoded, err := KISSDecode(frame)
					if err == nil {
						s.mu.Lock()
						s.received = append(s.received, decoded)
						s.mu.Unlock()
					}
					frame = nil
				}
				inFrame = true
				continue
			}
			if inFrame {
				frame = append(frame, b)
			}
		}
	}
}

func (s *mockKISSTNC) writeToClient(conn net.Conn) {
	defer s.wg.Done()
	for data := range s.sendCh {
		kissFrame := KISSEncode(data)
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		conn.Write(kissFrame)
	}
}

func (s *mockKISSTNC) addr() string {
	return s.listener.Addr().String()
}

func (s *mockKISSTNC) close() {
	close(s.sendCh)
	s.listener.Close()
	s.wg.Wait()
}

func (s *mockKISSTNC) frames() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.received))
	for i, f := range s.received {
		c := make([]byte, len(f))
		copy(c, f)
		out[i] = c
	}
	return out
}

// sendAPRSPosition sends a mock APRS position frame to connected client.
func (s *mockKISSTNC) sendAPRSPosition(src AX25Address, lat, lon float64, comment string) {
	dst := AX25Address{Call: "APRS", SSID: 0}
	info := EncodeAPRSPosition(lat, lon, '/', '-', comment)
	frame := EncodeAX25Frame(dst, src, nil, info)
	s.sendCh <- frame
}

func TestAPRSIntegration_ForwardMessage(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:         host,
		KISSPort:         port,
		Callsign:         "TEST",
		SSID:             10,
		FrequencyMHz:     144.800,
		ExternalDirewolf: true, // Tests drive mockKISSTNC directly — no supervisor.
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	// Forward a mesh message to APRS
	msg := &transport.MeshMessage{
		From:        0xAABBCCDD,
		PortNum:     1,
		DecodedText: "Hello from mesh via APRS",
	}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("forward: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	frames := tnc.frames()
	if len(frames) == 0 {
		t.Fatal("expected at least 1 KISS frame sent to TNC, got 0")
	}

	// Decode the AX.25 frame
	ax25, err := DecodeAX25Frame(frames[0])
	if err != nil {
		t.Fatalf("decode AX.25: %v", err)
	}
	if ax25.Src.Call != "TEST" {
		t.Errorf("src callsign: got %q, want TEST", ax25.Src.Call)
	}
	if ax25.Src.SSID != 10 {
		t.Errorf("src SSID: got %d, want 10", ax25.Src.SSID)
	}
	if !strings.Contains(string(ax25.Info), "Hello from mesh via APRS") {
		t.Errorf("info field should contain message: %q", string(ax25.Info))
	}

	status := gw.Status()
	if !status.Connected {
		t.Error("expected connected=true")
	}
	if status.MessagesOut < 1 {
		t.Errorf("expected msgsOut >= 1, got %d", status.MessagesOut)
	}
}

func TestAPRSIntegration_ReceivePosition(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:         host,
		KISSPort:         port,
		Callsign:         "TEST",
		SSID:             10,
		FrequencyMHz:     144.800,
		ExternalDirewolf: true, // Tests drive mockKISSTNC directly — no supervisor.
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	// Give the gateway a moment to start reading
	time.Sleep(100 * time.Millisecond)

	// Send an APRS position from the mock TNC
	tnc.sendAPRSPosition(
		AX25Address{Call: "PA3XYZ", SSID: 7},
		52.3676, 4.9041,
		"Mobile station",
	)

	// Read from receive channel
	select {
	case msg := <-gw.Receive():
		if msg.Source != "aprs" {
			t.Errorf("source: got %q, want aprs", msg.Source)
		}
		if !strings.Contains(msg.Text, "PA3XYZ") {
			t.Errorf("text should contain callsign: %q", msg.Text)
		}
		if !strings.Contains(msg.Text, "52.") {
			t.Errorf("text should contain latitude: %q", msg.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for APRS inbound message")
	}

	status := gw.Status()
	if status.MessagesIn < 1 {
		t.Errorf("expected msgsIn >= 1, got %d", status.MessagesIn)
	}
}

func TestAPRSIntegration_ConnectionRefused(t *testing.T) {
	cfg := APRSConfig{
		KISSHost:         "127.0.0.1",
		KISSPort:         19998,
		Callsign:         "TEST",
		SSID:             10,
		FrequencyMHz:     144.800,
		ExternalDirewolf: true, // Tests drive mockKISSTNC directly — no supervisor.
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx := context.Background()

	err := gw.Start(ctx)
	if err == nil {
		gw.Stop()
		t.Fatal("expected connection error, got nil")
	}
}

func TestAPRSIntegration_GatewayType(t *testing.T) {
	cfg := DefaultAPRSConfig()
	cfg.Callsign = "TEST"
	gw := NewAPRSGateway(cfg, nil)
	if gw.Type() != "aprs" {
		t.Errorf("type: got %q, want aprs", gw.Type())
	}
}

// TestAPRSIntegration_EncryptedForwardWireFormat verifies that when the
// DeliveryWorker hands off a message with Encrypted=true + a
// codec-version-byte-prefixed payload, the APRS gateway emits a
// KISS/AX.25 frame whose info field is exactly "{E1}" + the ciphertext
// (no version byte, no `[MeshSat ...]` attribution wrapper, no APRS
// position prefix) and whose digipeater path has been cut.
func TestAPRSIntegration_EncryptedForwardWireFormat(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:         host,
		KISSPort:         port,
		Callsign:         "TEST",
		SSID:             10,
		FrequencyMHz:     868.0, // ISM, not amateur — avoids the band warning
		ExternalDirewolf: true,
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	// Simulate what the DeliveryWorker hands us: the base64 ciphertext
	// with the protocol version byte prepended.
	cipher := "dGVzdENpcGhlcg==" // fake base64 — the gateway doesn't decrypt
	versioned := string([]byte{0x01}) + cipher
	msg := &transport.MeshMessage{
		From:        0x12345678,
		PortNum:     1,
		DecodedText: versioned,
		Encrypted:   true,
	}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("forward: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	frames := tnc.frames()
	if len(frames) == 0 {
		t.Fatal("expected at least 1 KISS frame, got 0")
	}
	ax25, err := DecodeAX25Frame(frames[0])
	if err != nil {
		t.Fatalf("decode AX.25: %v", err)
	}

	wantInfo := "{E1}" + cipher
	if string(ax25.Info) != wantInfo {
		t.Errorf("info field: got %q, want %q", string(ax25.Info), wantInfo)
	}
	if len(ax25.Path) != 0 {
		t.Errorf("digipeater path must be empty on encrypted frames, got %d hops", len(ax25.Path))
	}
	if !strings.HasPrefix(string(ax25.Info), "{E1}") {
		t.Errorf("info must start with {E1}, got %q", string(ax25.Info[:4]))
	}
	// Ensure the binary version byte was stripped — if it remained it
	// would appear immediately after the "{E1}" prefix.
	if len(ax25.Info) > 4 && ax25.Info[4] == 0x01 {
		t.Errorf("version byte 0x01 was not stripped before {E1} wrap")
	}
}

// TestAPRSIntegration_EncryptedReceiveBypassesParser verifies that a
// frame whose info field starts with "{E1}" is emitted straight to
// Receive() with Text = raw base64 (no APRS parser formatting) and
// FromAddr = the AX.25 source callsign. This is the complement of the
// egress test; together they prove a two-kit loopback.
func TestAPRSIntegration_EncryptedReceiveBypassesParser(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:         host,
		KISSPort:         port,
		Callsign:         "TEST",
		SSID:             10,
		FrequencyMHz:     868.0,
		ExternalDirewolf: true,
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	time.Sleep(100 * time.Millisecond)

	// Craft an encrypted-wrapped frame and push it from the mock TNC.
	cipher := "QUFBQUFBQUFBQUE=" // ≥ 12 bytes when decoded — plausible nonce+payload
	src := AX25Address{Call: "PA3ENC", SSID: 7}
	dst := AX25Address{Call: "APMSHT", SSID: 0}
	info := []byte("{E1}" + cipher)
	frame := EncodeAX25Frame(dst, src, nil, info)
	tnc.sendCh <- frame

	select {
	case msg := <-gw.Receive():
		if msg.Source != "aprs" {
			t.Errorf("source: got %q, want aprs", msg.Source)
		}
		if msg.Text != cipher {
			t.Errorf("text: got %q, want %q (no {E1} prefix, no APRS formatting)", msg.Text, cipher)
		}
		if msg.FromAddr != "PA3ENC-7" {
			t.Errorf("from_addr: got %q, want PA3ENC-7", msg.FromAddr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for encrypted inbound message")
	}
}

// TestAPRSIntegration_MalformedEncryptedFrameDoesNotCrash sends an
// `{E1}`-prefixed frame whose payload is empty (malformed). The
// gateway must not panic and the readWorker must stay alive so the
// next frame still gets processed.
func TestAPRSIntegration_MalformedEncryptedFrameDoesNotCrash(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:         host,
		KISSPort:         port,
		Callsign:         "TEST",
		SSID:             10,
		FrequencyMHz:     868.0,
		ExternalDirewolf: true,
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	time.Sleep(100 * time.Millisecond)

	// Malformed: just the prefix, no ciphertext.
	src := AX25Address{Call: "BADENC", SSID: 0}
	dst := AX25Address{Call: "APMSHT", SSID: 0}
	tnc.sendCh <- EncodeAX25Frame(dst, src, nil, []byte("{E1}"))

	// The empty-payload frame is still a valid InboundMessage (Text == "");
	// downstream ingress transforms will fail but we don't crash here.
	select {
	case msg := <-gw.Receive():
		if msg.Text != "" {
			t.Errorf("text: got %q, want empty (malformed ciphertext)", msg.Text)
		}
	case <-time.After(3 * time.Second):
		// Also acceptable — some frame framing may drop it silently.
	}

	// Then send a well-formed plaintext frame and confirm the gateway
	// is still alive and processing. Callsign kept ≤ 6 chars since
	// AX.25 truncates anything longer.
	tnc.sendAPRSPosition(
		AX25Address{Call: "PA3GUD", SSID: 1},
		52.0, 4.5,
		"still alive",
	)
	select {
	case msg := <-gw.Receive():
		if !strings.Contains(msg.Text, "PA3GUD") {
			t.Errorf("expected follow-up plaintext frame to parse: %q", msg.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readWorker stopped after malformed encrypted frame")
	}
}

// TestAPRSIntegration_PlaintextUnchangedWhenEncryptedFalse confirms the
// existing plaintext-APRS wire format (WIDE1-1,WIDE2-1 digipeater path,
// `[MeshSat !xxx] ...` attribution) still applies when msg.Encrypted is
// left unset. This is the regression guard that encryption doesn't
// leak into messages it was never asked to touch (HeMB raw sends, SOS,
// hub-initiated traffic, etc.).
func TestAPRSIntegration_PlaintextUnchangedWhenEncryptedFalse(t *testing.T) {
	tnc := newMockKISSTNC(t)
	defer tnc.close()

	host, port := splitHostPort(t, tnc.addr())
	cfg := APRSConfig{
		KISSHost:         host,
		KISSPort:         port,
		Callsign:         "TEST",
		SSID:             10,
		FrequencyMHz:     868.0,
		ExternalDirewolf: true,
	}

	gw := NewAPRSGateway(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer gw.Stop()

	msg := &transport.MeshMessage{
		From:        0xDEADBEEF,
		PortNum:     1,
		DecodedText: "plain text ping",
		Encrypted:   false,
	}
	if err := gw.Forward(ctx, msg); err != nil {
		t.Fatalf("forward: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	frames := tnc.frames()
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	ax25, err := DecodeAX25Frame(frames[0])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(ax25.Path) != 2 {
		t.Errorf("plaintext path should keep WIDE1/WIDE2, got %d hops", len(ax25.Path))
	}
	if !strings.Contains(string(ax25.Info), "MeshSat") ||
		!strings.Contains(string(ax25.Info), "plain text ping") {
		t.Errorf("plaintext info should carry attribution + message: %q", string(ax25.Info))
	}
	if strings.HasPrefix(string(ax25.Info), "{E1}") {
		t.Errorf("plaintext frame must NOT carry {E1} prefix: %q", string(ax25.Info))
	}
}

func TestIsLikelyAmateurBand(t *testing.T) {
	amateur := []float64{1.8, 3.75, 7.1, 14.2, 50.5, 144.0, 144.8, 147.9, 148.0, 432.0, 1296.0}
	// Frequencies that must NOT trip the soft warning. Deliberately
	// avoids the well-known ambiguous bands (433.92 MHz and 915 MHz
	// overlap amateur + ISM depending on ITU region) — since the
	// warning is advisory, we accept false-positives there and only
	// require the far-from-amateur cases to stay quiet.
	notAmateur := []float64{0.5, 3.0, 27.0, 100.0, 137.0, 152.0, 500.0, 868.0, 2400.0}

	for _, f := range amateur {
		if !IsLikelyAmateurBand(f) {
			t.Errorf("%.3f MHz should be flagged as amateur", f)
		}
	}
	for _, f := range notAmateur {
		if IsLikelyAmateurBand(f) {
			t.Errorf("%.3f MHz should NOT be flagged as amateur", f)
		}
	}
}
