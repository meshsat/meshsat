package gateway

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"meshsat/internal/transport"
)

func TestBuildPositionEvent(t *testing.T) {
	ev := BuildPositionEvent("meshsat-test-001", "MESHSAT-001", 52.3676, 4.9041, 10.0, 300)

	if ev.Version != "2.0" {
		t.Errorf("version: got %q, want 2.0", ev.Version)
	}
	if ev.UID != "meshsat-test-001" {
		t.Errorf("uid: got %q", ev.UID)
	}
	if ev.Type != CotEventTypePosition {
		t.Errorf("type: got %q, want %q", ev.Type, CotEventTypePosition)
	}
	if ev.How != "m-g" {
		t.Errorf("how: got %q, want m-g", ev.How)
	}
	if ev.Point.Lat != 52.3676 {
		t.Errorf("lat: got %f, want 52.3676", ev.Point.Lat)
	}
	if ev.Point.Lon != 4.9041 {
		t.Errorf("lon: got %f, want 4.9041", ev.Point.Lon)
	}
	if ev.Detail == nil {
		t.Fatal("detail is nil")
	}
	if ev.Detail.Contact == nil || ev.Detail.Contact.Callsign != "MESHSAT-001" {
		t.Errorf("contact callsign: got %v", ev.Detail.Contact)
	}
	if ev.Detail.Group == nil || ev.Detail.Group.Name != "Cyan" {
		t.Errorf("group name: got %v", ev.Detail.Group)
	}

	// Verify stale is ~300s after time
	evTime, _ := time.Parse(cotTimeFormat, ev.Time)
	evStale, _ := time.Parse(cotTimeFormat, ev.Stale)
	staleDur := evStale.Sub(evTime)
	if staleDur < 299*time.Second || staleDur > 301*time.Second {
		t.Errorf("stale offset: got %v, want ~300s", staleDur)
	}
}

func TestBuildPositionEvent_XMLMarshal(t *testing.T) {
	ev := BuildPositionEvent("meshsat-test-001", "MESHSAT-001", 52.3676, 4.9041, 10.0, 300)
	data, err := xml.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)

	if !strings.Contains(s, `<event`) {
		t.Error("missing <event> element")
	}
	if !strings.Contains(s, `type="a-f-G-U-C"`) {
		t.Error("missing type attribute")
	}
	if !strings.Contains(s, `uid="meshsat-test-001"`) {
		t.Error("missing uid attribute")
	}
	if !strings.Contains(s, `<point`) {
		t.Error("missing <point> element")
	}
	if !strings.Contains(s, `callsign="MESHSAT-001"`) {
		t.Error("missing callsign")
	}
}

func TestBuildSOSEvent(t *testing.T) {
	ev := BuildSOSEvent("meshsat-sos-001", "MESHSAT-001", 52.3676, 4.9041, 0, 600, "Button pressed")

	if ev.Type != CotEventTypePosition {
		t.Errorf("type: got %q, want %q", ev.Type, CotEventTypePosition)
	}
	if ev.Detail == nil || ev.Detail.Emergency == nil {
		t.Fatal("emergency detail is nil")
	}
	if ev.Detail.Emergency.Type != "911 Alert" {
		t.Errorf("emergency type: got %q", ev.Detail.Emergency.Type)
	}
	if ev.Detail.Emergency.Text != "Button pressed" {
		t.Errorf("emergency text: got %q", ev.Detail.Emergency.Text)
	}

	data, err := xml.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `<emergency type="911 Alert">`) {
		t.Errorf("missing emergency element in XML: %s", s)
	}
}

func TestBuildDeadmanEvent(t *testing.T) {
	ev := BuildDeadmanEvent("meshsat-dm-001", "MESHSAT-001", 52.3676, 4.9041, 1800, 3600)

	if ev.Type != CotEventTypeAlarm {
		t.Errorf("type: got %q, want %q", ev.Type, CotEventTypeAlarm)
	}
	if ev.UID != "meshsat-dm-001-DEADMAN" {
		t.Errorf("uid: got %q", ev.UID)
	}
	if ev.How != "h-e" {
		t.Errorf("how: got %q, want h-e", ev.How)
	}
	if ev.Detail == nil || ev.Detail.Remarks == nil {
		t.Fatal("remarks is nil")
	}
	if !strings.Contains(ev.Detail.Remarks.Text, "3600s") {
		t.Errorf("remarks text: got %q", ev.Detail.Remarks.Text)
	}
}

func TestBuildTelemetryEvent(t *testing.T) {
	ev := BuildTelemetryEvent("meshsat-sensor-001", "MESHSAT-S1", 52.3676, 4.9041, 300, "temperature=22.5C humidity=65%")

	if ev.Type != CotEventTypeSensor {
		t.Errorf("type: got %q, want %q", ev.Type, CotEventTypeSensor)
	}
	if ev.Detail == nil || ev.Detail.Remarks == nil {
		t.Fatal("remarks is nil")
	}
	if !strings.Contains(ev.Detail.Remarks.Text, "temperature=22.5C") {
		t.Errorf("remarks: got %q", ev.Detail.Remarks.Text)
	}
}

func TestBuildChatEvent(t *testing.T) {
	ev := BuildChatEvent("meshsat-chat-001", "MESHSAT-001", "Hello from the field", 300)

	if ev.Type != CotEventTypeChat {
		t.Errorf("type: got %q, want %q", ev.Type, CotEventTypeChat)
	}
	if !strings.HasPrefix(ev.UID, "meshsat-chat-001-CHAT-") {
		t.Errorf("uid: got %q", ev.UID)
	}
	if ev.Detail == nil || ev.Detail.Remarks == nil {
		t.Fatal("remarks is nil")
	}
	if ev.Detail.Remarks.Text != "Hello from the field" {
		t.Errorf("remarks text: got %q", ev.Detail.Remarks.Text)
	}
}

func TestParseCotEvent(t *testing.T) {
	xmlData := `<event version="2.0" uid="TEST-001" type="a-f-G-U-C" how="m-g" time="2026-03-17T12:00:00Z" start="2026-03-17T12:00:00Z" stale="2026-03-17T12:05:00Z"><point lat="52.3676" lon="4.9041" hae="10" ce="10" le="10"/><detail><contact callsign="TEST"/><remarks>Hello</remarks></detail></event>`

	ev, err := ParseCotEvent([]byte(xmlData))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.UID != "TEST-001" {
		t.Errorf("uid: got %q", ev.UID)
	}
	if ev.Type != "a-f-G-U-C" {
		t.Errorf("type: got %q", ev.Type)
	}
	if ev.Point.Lat != 52.3676 {
		t.Errorf("lat: got %f", ev.Point.Lat)
	}
	if ev.Detail == nil || ev.Detail.Contact == nil {
		t.Fatal("contact is nil")
	}
	if ev.Detail.Contact.Callsign != "TEST" {
		t.Errorf("callsign: got %q", ev.Detail.Contact.Callsign)
	}
	if ev.Detail.Remarks == nil || ev.Detail.Remarks.Text != "Hello" {
		t.Errorf("remarks: got %v", ev.Detail.Remarks)
	}
}

func TestCotEventToInboundMessage(t *testing.T) {
	ev := &CotEvent{
		Type: "a-f-G-U-C",
		Point: CotPoint{
			Lat: 52.3676,
			Lon: 4.9041,
		},
		Detail: &CotDetail{
			Contact: &CotContact{Callsign: "ALPHA-1"},
			Remarks: &CotRemarks{Text: "Position report"},
		},
	}

	msg := CotEventToInboundMessage(ev)
	if msg.Source != "tak" {
		t.Errorf("source: got %q, want tak", msg.Source)
	}
	if !strings.Contains(msg.Text, "ALPHA-1") {
		t.Errorf("text should contain callsign: %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "52.367600") {
		t.Errorf("text should contain lat: %q", msg.Text)
	}
}

func TestMeshMessageToCotType(t *testing.T) {
	tests := []struct {
		portNum  int
		expected string
	}{
		{1, CotEventTypeChat},
		{3, CotEventTypePosition},
		{67, CotEventTypeSensor},
		{99, CotEventTypePosition}, // default
	}
	for _, tt := range tests {
		msg := &transport.MeshMessage{PortNum: tt.portNum}
		got := MeshMessageToCotType(msg)
		if got != tt.expected {
			t.Errorf("portnum %d: got %q, want %q", tt.portNum, got, tt.expected)
		}
	}
}

func TestMarshalUnmarshalRoundtrip(t *testing.T) {
	original := BuildSOSEvent("roundtrip-001", "RT-1", 51.5074, -0.1278, 30.0, 600, "Test SOS")
	data, err := MarshalCotEvent(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	parsed, err := ParseCotEvent(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.UID != original.UID {
		t.Errorf("uid: got %q, want %q", parsed.UID, original.UID)
	}
	if parsed.Type != original.Type {
		t.Errorf("type: got %q, want %q", parsed.Type, original.Type)
	}
	if parsed.Point.Lat != original.Point.Lat {
		t.Errorf("lat: got %f, want %f", parsed.Point.Lat, original.Point.Lat)
	}
	if parsed.Detail == nil || parsed.Detail.Emergency == nil {
		t.Fatal("emergency lost in roundtrip")
	}
	if parsed.Detail.Emergency.Type != "911 Alert" {
		t.Errorf("emergency type: got %q", parsed.Detail.Emergency.Type)
	}
}

func TestTAKConfigValidate(t *testing.T) {
	// Missing host
	cfg := DefaultTAKConfig()
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing host")
	}

	// Valid non-SSL
	cfg.Host = "tak.example.com"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// SSL without certs
	cfg.SSL = true
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for SSL without certs")
	}

	// SSL with certs
	cfg.CertFile = "/path/to/cert.pem"
	cfg.KeyFile = "/path/to/key.pem"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// SSL with credential_id (no file paths needed)
	cfg2 := DefaultTAKConfig()
	cfg2.Host = "tak.example.com"
	cfg2.SSL = true
	cfg2.CredentialID = "tak-enrolled-cert"
	if err := cfg2.Validate(); err != nil {
		t.Errorf("SSL with credential_id should be valid: %v", err)
	}

	// SSL with auto-enroll (no file paths or credential_id needed)
	cfg3 := DefaultTAKConfig()
	cfg3.Host = "tak.example.com"
	cfg3.SSL = true
	cfg3.AutoEnroll = true
	cfg3.EnrollURL = "https://tak-server:8446"
	cfg3.EnrollUsername = "meshsat"
	cfg3.EnrollPassword = "secret"
	if err := cfg3.Validate(); err != nil {
		t.Errorf("SSL with auto_enroll should be valid: %v", err)
	}

	// SSL with incomplete auto-enroll should fail
	cfg4 := DefaultTAKConfig()
	cfg4.Host = "tak.example.com"
	cfg4.SSL = true
	cfg4.AutoEnroll = true
	cfg4.EnrollURL = "https://tak-server:8446"
	// missing username/password
	if err := cfg4.Validate(); err == nil {
		t.Error("SSL with incomplete auto_enroll should fail")
	}
}

func TestTAKConfigHasEnrollmentConfig(t *testing.T) {
	cfg := DefaultTAKConfig()
	if cfg.HasEnrollmentConfig() {
		t.Error("default config should not have enrollment config")
	}

	cfg.AutoEnroll = true
	cfg.EnrollURL = "https://tak:8446"
	cfg.EnrollUsername = "user"
	cfg.EnrollPassword = "pass"
	if !cfg.HasEnrollmentConfig() {
		t.Error("config with all enrollment fields should have enrollment config")
	}

	cfg.EnrollPassword = ""
	if cfg.HasEnrollmentConfig() {
		t.Error("config with missing password should not have enrollment config")
	}
}

func TestTAKConfigParse(t *testing.T) {
	json := `{"tak_host":"tak.local","tak_port":8089,"tak_ssl":true,"cert_file":"/cert.pem","key_file":"/key.pem","callsign_prefix":"ALPHA","cot_stale_seconds":600}`
	cfg, err := ParseTAKConfig(json)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Host != "tak.local" {
		t.Errorf("host: got %q", cfg.Host)
	}
	if cfg.Port != 8089 {
		t.Errorf("port: got %d", cfg.Port)
	}
	if !cfg.SSL {
		t.Error("ssl should be true")
	}
	if cfg.CallsignPrefix != "ALPHA" {
		t.Errorf("callsign_prefix: got %q", cfg.CallsignPrefix)
	}
	if cfg.CotStaleSec != 600 {
		t.Errorf("cot_stale_seconds: got %d", cfg.CotStaleSec)
	}
}

func TestTAKConfigRedacted(t *testing.T) {
	cfg := TAKConfig{
		Host:           "tak.local",
		CertFile:       "/secret/cert.pem",
		KeyFile:        "/secret/key.pem",
		CAFile:         "/secret/ca.pem",
		EnrollPassword: "supersecret",
	}
	redacted := cfg.Redacted()
	if redacted.CertFile != "****" {
		t.Errorf("cert_file not redacted: %q", redacted.CertFile)
	}
	if redacted.KeyFile != "****" {
		t.Errorf("key_file not redacted: %q", redacted.KeyFile)
	}
	if redacted.CAFile != "****" {
		t.Errorf("ca_file not redacted: %q", redacted.CAFile)
	}
	if redacted.EnrollPassword != "****" {
		t.Errorf("enroll_password not redacted: %q", redacted.EnrollPassword)
	}
}

func TestTAKConfigParseWithEnrollment(t *testing.T) {
	j := `{"tak_host":"tak.local","tak_port":8089,"tak_ssl":true,"auto_enroll":true,"enroll_url":"https://tak:8446","enroll_username":"meshsat","enroll_password":"secret","callsign_prefix":"ALPHA"}`
	cfg, err := ParseTAKConfig(j)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.AutoEnroll {
		t.Error("auto_enroll should be true")
	}
	if cfg.EnrollURL != "https://tak:8446" {
		t.Errorf("enroll_url: got %q", cfg.EnrollURL)
	}
	if cfg.EnrollUsername != "meshsat" {
		t.Errorf("enroll_username: got %q", cfg.EnrollUsername)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("should validate with auto_enroll: %v", err)
	}
}
