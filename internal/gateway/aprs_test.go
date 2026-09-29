package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
)

// --- KISS encode/decode tests ---

func TestKISSEncode(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03}
	frame := KISSEncode(payload)

	// Should start and end with FEND
	if frame[0] != kissFEND {
		t.Errorf("first byte: got 0x%02x, want 0x%02x", frame[0], kissFEND)
	}
	if frame[len(frame)-1] != kissFEND {
		t.Errorf("last byte: got 0x%02x, want 0x%02x", frame[len(frame)-1], kissFEND)
	}
	// Second byte should be command (0x00)
	if frame[1] != kissData {
		t.Errorf("command byte: got 0x%02x, want 0x00", frame[1])
	}
}

func TestKISSEncodeEscape(t *testing.T) {
	// Payload containing FEND and FESC characters
	payload := []byte{0x01, kissFEND, 0x02, kissFESC, 0x03}
	frame := KISSEncode(payload)

	// Should be: FEND + 0x00 + 0x01 + FESC TFEND + 0x02 + FESC TFESC + 0x03 + FEND
	expected := []byte{kissFEND, 0x00, 0x01, kissFESC, kissTFEND, 0x02, kissFESC, kissTFESC, 0x03, kissFEND}
	if len(frame) != len(expected) {
		t.Fatalf("frame length: got %d, want %d", len(frame), len(expected))
	}
	for i, b := range frame {
		if b != expected[i] {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x", i, b, expected[i])
		}
	}
}

func TestKISSDecode(t *testing.T) {
	// Command byte + unescaped data
	frame := []byte{0x00, 0x01, 0x02, 0x03}
	payload, err := KISSDecode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload) != 3 {
		t.Fatalf("payload length: got %d, want 3", len(payload))
	}
	if payload[0] != 0x01 || payload[1] != 0x02 || payload[2] != 0x03 {
		t.Errorf("payload: got %v", payload)
	}
}

func TestKISSDecodeEscape(t *testing.T) {
	// Command byte + escaped FEND and FESC
	frame := []byte{0x00, 0x01, kissFESC, kissTFEND, 0x02, kissFESC, kissTFESC, 0x03}
	payload, err := KISSDecode(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	expected := []byte{0x01, kissFEND, 0x02, kissFESC, 0x03}
	if len(payload) != len(expected) {
		t.Fatalf("payload length: got %d, want %d", len(payload), len(expected))
	}
	for i, b := range payload {
		if b != expected[i] {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x", i, b, expected[i])
		}
	}
}

func TestKISSEncodeDecodeRoundtrip(t *testing.T) {
	original := []byte{0x00, kissFEND, kissFESC, 0xFF, 0x42}
	encoded := KISSEncode(original)

	// Strip outer FENDs and decode
	inner := encoded[1 : len(encoded)-1]
	decoded, err := KISSDecode(inner)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("length: got %d, want %d", len(decoded), len(original))
	}
	for i, b := range decoded {
		if b != original[i] {
			t.Errorf("byte %d: got 0x%02x, want 0x%02x", i, b, original[i])
		}
	}
}

// --- AX.25 encode/decode tests ---

func TestAX25EncodeDecodeRoundtrip(t *testing.T) {
	src := AX25Address{Call: "PA3XYZ", SSID: 10}
	dst := AX25Address{Call: "APRS", SSID: 0}
	path := []AX25Address{{Call: "WIDE1", SSID: 1}}
	info := []byte("!5222.08N/00454.24E-MeshSat")

	encoded := EncodeAX25Frame(dst, src, path, info)
	decoded, err := DecodeAX25Frame(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.Src.Call != "PA3XYZ" {
		t.Errorf("src call: got %q", decoded.Src.Call)
	}
	if decoded.Src.SSID != 10 {
		t.Errorf("src ssid: got %d", decoded.Src.SSID)
	}
	if decoded.Dst.Call != "APRS" {
		t.Errorf("dst call: got %q", decoded.Dst.Call)
	}
	if len(decoded.Path) != 1 {
		t.Fatalf("path len: got %d", len(decoded.Path))
	}
	if decoded.Path[0].Call != "WIDE1" || decoded.Path[0].SSID != 1 {
		t.Errorf("path[0]: got %v", decoded.Path[0])
	}
	if string(decoded.Info) != "!5222.08N/00454.24E-MeshSat" {
		t.Errorf("info: got %q", string(decoded.Info))
	}
}

func TestFormatCallsign(t *testing.T) {
	tests := []struct {
		addr     AX25Address
		expected string
	}{
		{AX25Address{Call: "PA3XYZ", SSID: 0}, "PA3XYZ"},
		{AX25Address{Call: "PA3XYZ", SSID: 10}, "PA3XYZ-10"},
		{AX25Address{Call: "WIDE1", SSID: 1}, "WIDE1-1"},
	}
	for _, tt := range tests {
		got := FormatCallsign(tt.addr)
		if got != tt.expected {
			t.Errorf("FormatCallsign(%v): got %q, want %q", tt.addr, got, tt.expected)
		}
	}
}

// --- APRS position encode/decode tests ---

func TestEncodeAPRSPosition(t *testing.T) {
	pos := EncodeAPRSPosition(52.3676, 4.9041, '/', '-', "MeshSat Bridge")
	s := string(pos)

	if s[0] != '!' {
		t.Errorf("first byte: got %q, want '!'", string(s[0]))
	}
	if !strings.Contains(s, "N") {
		t.Errorf("missing N hemisphere: %s", s)
	}
	if !strings.Contains(s, "E") {
		t.Errorf("missing E hemisphere: %s", s)
	}
	if !strings.Contains(s, "MeshSat Bridge") {
		t.Errorf("missing comment: %s", s)
	}
}

func TestParseAPRSPosition(t *testing.T) {
	// Create a frame with a position
	info := []byte("!5222.06N/00454.25E-Test station")
	frame := &AX25Frame{
		Src:  AX25Address{Call: "PA3XYZ", SSID: 10},
		Dst:  AX25Address{Call: "APRS", SSID: 0},
		Info: info,
	}

	pkt, err := ParseAPRSPacket(frame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pkt.DataType != '!' {
		t.Errorf("data type: got %q", string(pkt.DataType))
	}

	// 52°22.06'N = 52 + 22.06/60 = 52.36767
	expectedLat := 52.0 + 22.06/60.0
	if math.Abs(pkt.Lat-expectedLat) > 0.001 {
		t.Errorf("lat: got %f, want ~%f", pkt.Lat, expectedLat)
	}

	// 004°54.25'E = 4 + 54.25/60 = 4.90417
	expectedLon := 4.0 + 54.25/60.0
	if math.Abs(pkt.Lon-expectedLon) > 0.001 {
		t.Errorf("lon: got %f, want ~%f", pkt.Lon, expectedLon)
	}

	if pkt.Comment != "Test station" {
		t.Errorf("comment: got %q", pkt.Comment)
	}
}

func TestParseAPRSMessage(t *testing.T) {
	info := []byte(":PA3ABC   :Hello from MeshSat{42")
	frame := &AX25Frame{
		Src:  AX25Address{Call: "PA3XYZ", SSID: 10},
		Dst:  AX25Address{Call: "APRS", SSID: 0},
		Info: info,
	}

	pkt, err := ParseAPRSPacket(frame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pkt.DataType != ':' {
		t.Errorf("data type: got %q", string(pkt.DataType))
	}
	if pkt.MsgTo != "PA3ABC" {
		t.Errorf("msg_to: got %q", pkt.MsgTo)
	}
	if pkt.Message != "Hello from MeshSat" {
		t.Errorf("message: got %q", pkt.Message)
	}
	if pkt.MsgID != "42" {
		t.Errorf("msg_id: got %q", pkt.MsgID)
	}
}

func TestEncodeAPRSMessage(t *testing.T) {
	msg := EncodeAPRSMessage("PA3ABC", "Hello", "123")
	s := string(msg)

	if !strings.HasPrefix(s, ":PA3ABC   :Hello{123") {
		t.Errorf("encoded message: got %q", s)
	}
}

// --- APRS config tests ---

func TestAPRSConfigValidate(t *testing.T) {
	// Missing callsign
	cfg := DefaultAPRSConfig()
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing callsign")
	}

	// Valid config
	cfg.Callsign = "PA3XYZ"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// APRS-IS without passcode
	cfg.APRSISEnable = true
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for APRS-IS without passcode")
	}

	// APRS-IS with passcode
	cfg.APRSISPass = "12345"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAPRSConfigParse(t *testing.T) {
	j := `{"callsign":"PA3XYZ","ssid":10,"kiss_host":"192.168.1.100","kiss_port":8001,"frequency_mhz":144.800}`
	cfg, err := ParseAPRSConfig(j)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Callsign != "PA3XYZ" {
		t.Errorf("callsign: got %q", cfg.Callsign)
	}
	if cfg.SSID != 10 {
		t.Errorf("ssid: got %d", cfg.SSID)
	}
	if cfg.KISSHost != "192.168.1.100" {
		t.Errorf("kiss_host: got %q", cfg.KISSHost)
	}
	if cfg.FrequencyMHz != 144.800 {
		t.Errorf("frequency: got %f", cfg.FrequencyMHz)
	}
}

func TestAPRSConfigRedacted(t *testing.T) {
	cfg := APRSConfig{
		Callsign:   "PA3XYZ",
		APRSISPass: "secret123",
	}
	redacted := cfg.Redacted()
	if redacted.APRSISPass != "****" {
		t.Errorf("passcode not redacted: %q", redacted.APRSISPass)
	}
	if redacted.Callsign != "PA3XYZ" {
		t.Errorf("callsign changed: %q", redacted.Callsign)
	}
}

// newTestDB opens a fresh database: its migrations seed mesh_0 and
// iridium_imt_0 only, the phone's link rows.
func newTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// The mode field, its defaults, the unknown-mode refusal, the APRS-IS
// checks, and the kiss rules as they were. [MESHSAT-1421]
func TestAPRSConfigValidateMode(t *testing.T) {
	def := DefaultAPRSConfig()
	if def.Mode != APRSModeKISS || def.APRSISServer != "rotate.aprs2.net:14580" || def.APRSISPass != "" ||
		def.APRSISFilterKm != 100 || def.APRSISFilterLat != 0 || def.APRSISFilterLon != 0 ||
		def.PositionBeacon || def.PositionBeaconMin != 10 || def.BeaconSecs != 0 {
		t.Fatalf("defaults %+v", def)
	}

	// Mode is: the passcode becomes -1 in the running config only, so the
	// saved JSON, which GET re-parses and masks, keeps reading "".
	cfg, err := ParseAPRSConfig(`{"callsign":"N0CALL","mode":"is"}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Redacted().APRSISPass != "" {
		t.Fatalf("GET would mask a passcode never set: %q", cfg.Redacted().APRSISPass)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("mode is with defaults: %v", err)
	}
	if cfg.APRSISPass != "-1" || cfg.APRSISServer != DefaultAPRSISServer || !cfg.IsMode() {
		t.Fatalf("mode is defaults: passcode %q server %q", cfg.APRSISPass, cfg.APRSISServer)
	}
	// The kiss rule on aprs_is_enabled does not apply in mode is.
	cfg, _ = ParseAPRSConfig(`{"callsign":"N0CALL","mode":"is","aprs_is_enabled":true}`)
	if err := cfg.Validate(); err != nil || cfg.APRSISPass != "-1" {
		t.Fatalf("mode is with aprs_is_enabled: %v, passcode %q", err, cfg.APRSISPass)
	}

	// An unknown mode; an empty mode is kiss.
	cfg, _ = ParseAPRSConfig(`{"callsign":"N0CALL","mode":"ax25"}`)
	if err := cfg.Validate(); err == nil || err.Error() != "mode must be kiss or is" {
		t.Fatalf("unknown mode: %v", err)
	}
	empty := APRSConfig{Callsign: "N0CALL"}
	if err := empty.Validate(); err != nil || empty.Mode != APRSModeKISS || empty.IsMode() {
		t.Fatalf("empty mode: %v, mode %q", err, empty.Mode)
	}

	// The kiss rules are unchanged, and the APRS-IS checks do not apply.
	k := DefaultAPRSConfig()
	if err := k.Validate(); err == nil || err.Error() != "callsign is required for APRS" {
		t.Fatalf("kiss without callsign: %v", err)
	}
	k.Callsign, k.SSID = "PA3XYZ", 16
	if err := k.Validate(); err == nil || err.Error() != "ssid must be 0-15" {
		t.Fatalf("kiss ssid 16: %v", err)
	}
	k.SSID, k.APRSISEnable = 10, true
	if err := k.Validate(); err == nil || err.Error() != "aprs_is_passcode is required when APRS-IS is enabled" {
		t.Fatalf("kiss aprs_is_enabled without passcode: %v", err)
	}
	k.APRSISPass = "12345"
	k.APRSISFilterKm, k.APRSISFilterLat, k.PositionBeaconMin, k.APRSISServer = -1, 91, -1, "not a server"
	if err := k.Validate(); err != nil {
		t.Fatalf("kiss must ignore the APRS-IS fields: %v", err)
	}
	if k.Mode != APRSModeKISS || k.APRSISServer != "not a server" || k.APRSISPass != "12345" {
		t.Fatalf("kiss touched the APRS-IS fields: %+v", k)
	}

	// Mode is checks what goes into the login line.
	for _, c := range []struct{ json, want string }{
		{`{"mode":"is"}`, "callsign is required for APRS"},
		{`{"mode":"is","callsign":"N0CALL","ssid":16}`, "ssid must be 0-15"},
		{`{"mode":"is","callsign":"PA3-XY"}`, "callsign must be 1-6 letters and digits"},
		{`{"mode":"is","callsign":"N0CALL7"}`, "callsign must be 1-6 letters and digits"},
		{`{"mode":"is","callsign":"N0 CAL"}`, "callsign must be 1-6 letters and digits"},
		{`{"mode":"is","callsign":"N0CALL","aprs_is_passcode":"12 34"}`, "aprs_is_passcode must be one word"},
		{`{"mode":"is","callsign":"N0CALL","aprs_is_passcode":"1\r\n#filter r/0/0/1"}`, "aprs_is_passcode must be one word"},
		{`{"mode":"is","callsign":"N0CALL","aprs_is_server":"host:0"}`, "aprs_is_server must be host:port"},
		{`{"mode":"is","callsign":"N0CALL","aprs_is_server":"a b:14580"}`, "aprs_is_server must be host:port"},
		{`{"mode":"is","callsign":"N0CALL","aprs_is_server":"::1"}`, "aprs_is_server must be host:port"},
		{`{"mode":"is","callsign":"N0CALL","aprs_is_filter_km":-1}`, "aprs_is_filter_km must be 0 or more"},
		{`{"mode":"is","callsign":"N0CALL","aprs_is_filter_lat":91}`, "aprs_is_filter_lat must be -90 to 90"},
		{`{"mode":"is","callsign":"N0CALL","aprs_is_filter_lon":-181}`, "aprs_is_filter_lon must be -180 to 180"},
		{`{"mode":"is","callsign":"N0CALL","position_beacon_min":1441}`, "position_beacon_min must be 0-1440"},
		{`{"mode":"is","callsign":"N0CALL","position_beacon_min":-1}`, "position_beacon_min must be 0-1440"},
	} {
		cfg, err := ParseAPRSConfig(c.json)
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.json, err, c.want)
		}
	}

	// Normalised: a host without a port gets 14580, an IPv6 literal its
	// brackets; 0 km and 0 min are allowed.
	for in, want := range map[string]string{" euro.aprs2.net ": "euro.aprs2.net:14580", "[::1]": "[::1]:14580", "[::1]:10152": "[::1]:10152", "127.0.0.1:14580": "127.0.0.1:14580"} {
		cfg := APRSConfig{Mode: APRSModeIS, Callsign: "n0call", APRSISServer: in}
		if err := cfg.Validate(); err != nil || cfg.APRSISServer != want {
			t.Errorf("server %q: %v, got %q want %q", in, err, cfg.APRSISServer, want)
		}
	}
	cfg = &APRSConfig{Mode: APRSModeIS, Callsign: "N0CALL", APRSISFilterKm: 0, PositionBeaconMin: 0}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("0 km, 0 min: %v", err)
	}
	// Any one-word passcode is the server's to judge, as Android's field
	// allows ("12-345" logs in unverified there too).
	cfg = &APRSConfig{Mode: APRSModeIS, Callsign: "N0CALL", APRSISPass: " 12-345 "}
	if err := cfg.Validate(); err != nil || cfg.APRSISPass != "12-345" {
		t.Fatalf("passcode 12-345: %v, kept %q", err, cfg.APRSISPass)
	}

	// Through the manager, the words a PUT answers with.
	m := NewManager(newTestDB(t), nil)
	err = m.Configure(context.Background(), "aprs", false, `{"callsign":"N0CALL","mode":"ax25"}`)
	if err == nil || err.Error() != "invalid config: mode must be kiss or is" {
		t.Fatalf("PUT with an unknown mode: %v", err)
	}
}

// mode, state and last_error in both modes, and the APRS-IS fields of mode
// is, on both status paths and on the gateway list. [MESHSAT-1421]
func TestAPRSStatusState(t *testing.T) {
	t.Run("kiss", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		accepted := make(chan net.Conn, 1)
		go func() {
			if c, err := ln.Accept(); err == nil {
				accepted <- c
			}
		}()
		host, port := splitHostPort(t, ln.Addr().String())
		g := NewAPRSGateway(APRSConfig{KISSHost: host, KISSPort: port, Callsign: "TEST", SSID: 10, FrequencyMHz: 144.800, ExternalDirewolf: true}, nil)
		if st := g.GetAPRSStatus(); st["mode"] != APRSModeKISS || st["state"] != APRSStateDisconnected {
			t.Fatalf("before start: mode %v state %v", st["mode"], st["state"])
		}
		if err := g.Start(context.Background()); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer g.Stop()
		var tnc net.Conn
		select {
		case tnc = <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatal("the gateway never dialled the TNC")
		}
		st := g.GetAPRSStatus()
		if st["state"] != APRSStateConnected || st["connected"] != true || st["last_error"] != "" {
			t.Fatalf("running: state %v connected %v last_error %q", st["state"], st["connected"], st["last_error"])
		}
		for _, k := range []string{"aprs_is_server", "aprs_is_verified", "aprs_is_banner", "position_beacons", "last_beacon_at"} {
			if _, has := st[k]; has {
				t.Errorf("mode kiss answers %s", k)
			}
		}
		if s := g.Status(); s.Mode != APRSModeKISS || s.State != APRSStateConnected || !s.Connected || s.APRSISVerified != nil {
			t.Fatalf("Status: %+v", s)
		}

		// The TNC goes away: the read worker reopens the link, and the state
		// reads connecting with the reason.
		tnc.Close()
		deadline := time.Now().Add(5 * time.Second)
		for g.GetAPRSStatus()["state"] != APRSStateConnecting {
			if time.Now().After(deadline) {
				t.Fatalf("state %v after the TNC closed, want connecting", g.GetAPRSStatus()["state"])
			}
			time.Sleep(5 * time.Millisecond)
		}
		if s := g.Status(); s.LastError == "" || s.Connected {
			t.Fatalf("while reconnecting: %+v", s)
		}
		_ = g.Stop()
		if s := g.Status(); s.State != APRSStateDisconnected {
			t.Fatalf("after stop: %q", s.State)
		}
	})

	t.Run("is", func(t *testing.T) {
		setAPRSISKnobs(t, 200*time.Millisecond)
		f := newFakeAPRSIS(t, logrespVerified)
		g := newISGateway(t, f, nil)
		if st := g.GetAPRSStatus(); st["mode"] != APRSModeIS || st["state"] != APRSStateDisconnected || st["connected"] != false {
			t.Fatalf("before start: %v", st)
		}
		startGateway(t, g)
		waitISState(t, g, APRSStateConnected, 5*time.Second)
		st := g.GetAPRSStatus()
		want := map[string]interface{}{
			"connected": true, "mode": APRSModeIS, "state": APRSStateConnected, "last_error": "",
			"callsign": "N0CALL-10", "aprs_is_server": f.addr(), "aprs_is_verified": true,
			"aprs_is_banner": fakeISBanner, "position_beacons": int64(0), "last_beacon_at": "",
		}
		for k, v := range want {
			if st[k] != v {
				t.Errorf("%s = %v, want %v", k, st[k], v)
			}
		}
		for _, k := range []string{"kiss_addr", "kiss_up", "frequency_mhz", "receive_state", "tnc_serial"} {
			if _, has := st[k]; has {
				t.Errorf("mode is answers %s", k)
			}
		}
		s := g.Status()
		if !s.Connected || s.Mode != APRSModeIS || s.State != APRSStateConnected || s.APRSISVerified == nil || !*s.APRSISVerified ||
			s.APRSISServer != f.addr() || s.APRSISBanner != fakeISBanner || s.PositionBeacons == nil {
			t.Fatalf("Status: %+v", s)
		}
		f.dropClient()
		waitISState(t, g, APRSStateDisconnected, 3*time.Second)
		if s := g.Status(); s.Connected || s.LastError == "" {
			t.Fatalf("after the server closed: %+v", s)
		}
		_ = g.Stop()
		if s := g.Status(); s.State != APRSStateDisconnected {
			t.Fatalf("after stop: %q", s.State)
		}
	})

	t.Run("gateway list", func(t *testing.T) {
		setAPRSISKnobs(t, 200*time.Millisecond)
		f := newFakeAPRSIS(t, logrespVerified)
		m := NewManager(newTestDB(t), nil)
		t.Cleanup(m.Stop)
		begin := time.Now()
		cfg := fmt.Sprintf(`{"mode":"is","callsign":"N0CALL","ssid":10,"aprs_is_server":%q,"aprs_is_passcode":"13023"}`, f.addr())
		if err := m.Configure(context.Background(), "aprs", true, cfg); err != nil {
			t.Fatalf("configure: %v", err)
		}
		if d := time.Since(begin); d > 2*time.Second {
			t.Fatalf("PUT in mode is took %s: Start must return at once", d)
		}
		deadline := time.Now().Add(5 * time.Second)
		var resp *GatewayStatusResponse
		for {
			var err error
			if resp, err = m.GetSingleStatus("aprs"); err != nil {
				t.Fatal(err)
			}
			if resp.State == APRSStateConnected {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("state %q after 5 s", resp.State)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if resp.Mode != APRSModeIS || !resp.Connected || resp.APRSISVerified == nil || !*resp.APRSISVerified {
			t.Fatalf("GET /api/gateways/aprs: %+v", resp)
		}
		body, _ := json.Marshal(resp)
		for _, part := range []string{`"state":"connected"`, `"mode":"is"`, `"aprs_is_verified":true`, `"aprs_is_passcode":"****"`, `"position_beacon":false`, `"aprs_is_filter_km":100`} {
			if !strings.Contains(string(body), part) {
				t.Errorf("response lacks %s: %s", part, body)
			}
		}
		found := false
		for _, row := range m.GetStatus() {
			if row.Type == "aprs" && row.State == APRSStateConnected && row.Mode == APRSModeIS {
				found = true
			}
		}
		if !found {
			t.Fatal("GET /api/gateways lacks the aprs row's state")
		}
	})
}
