package gateway

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	pb "meshsat/internal/gateway/takproto"
	"meshsat/internal/transport"

	"google.golang.org/protobuf/proto"
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

func TestReadTakProtoMessage_CotEvent(t *testing.T) {
	// Build a protobuf-framed TakMessage with a CotEvent
	msg := &pb.TakMessage{
		CotEvent: &pb.CotEvent{
			Type:      "a-f-G-U-C",
			Uid:       "test-proto-001",
			How:       "m-g",
			SendTime:  uint64(time.Now().UnixMilli()),
			StartTime: uint64(time.Now().UnixMilli()),
			StaleTime: uint64(time.Now().Add(5 * time.Minute).UnixMilli()),
			Lat:       52.3676,
			Lon:       4.9041,
			Hae:       10.0,
			Ce:        10.0,
			Le:        10.0,
		},
	}
	frame, err := MarshalTakProto(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify magic byte
	if frame[0] != 0xBF {
		t.Fatalf("first byte: got 0x%02x, want 0xBF", frame[0])
	}

	// Read it back
	reader := bufio.NewReader(bytes.NewReader(frame))
	parsed, err := ReadTakProtoMessage(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if parsed.GetCotEvent().GetUid() != "test-proto-001" {
		t.Errorf("uid: got %q", parsed.GetCotEvent().GetUid())
	}
	if parsed.GetCotEvent().GetLat() != 52.3676 {
		t.Errorf("lat: got %f", parsed.GetCotEvent().GetLat())
	}
}

func TestReadTakProtoMessage_TakControl(t *testing.T) {
	// Build a protobuf-framed TakMessage with TakControl (version negotiation)
	msg := &pb.TakMessage{
		TakControl: &pb.TakControl{
			MinProtoVersion: 0,
			MaxProtoVersion: 1,
			ContactUid:      "tak-server-001",
		},
	}
	frame, err := MarshalTakProto(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	reader := bufio.NewReader(bytes.NewReader(frame))
	parsed, err := ReadTakProtoMessage(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Should have TakControl, not CotEvent
	if parsed.GetCotEvent() != nil {
		t.Error("expected no CotEvent in version negotiation message")
	}
	tc := parsed.GetTakControl()
	if tc == nil {
		t.Fatal("expected TakControl")
	}
	if tc.GetMinProtoVersion() != 0 {
		t.Errorf("minProto: got %d", tc.GetMinProtoVersion())
	}
	if tc.GetMaxProtoVersion() != 1 {
		t.Errorf("maxProto: got %d", tc.GetMaxProtoVersion())
	}
	if tc.GetContactUid() != "tak-server-001" {
		t.Errorf("contactUid: got %q", tc.GetContactUid())
	}
}

func TestProtoToCotEvent_Roundtrip(t *testing.T) {
	original := BuildPositionEvent("proto-rt-001", "ALPHA-1", 51.5074, -0.1278, 30.0, 300)

	takMsg, err := CotEventToProto(original)
	if err != nil {
		t.Fatalf("to proto: %v", err)
	}

	ev, err := ProtoToCotEvent(takMsg)
	if err != nil {
		t.Fatalf("from proto: %v", err)
	}

	if ev.UID != "proto-rt-001" {
		t.Errorf("uid: got %q", ev.UID)
	}
	if ev.Type != CotEventTypePosition {
		t.Errorf("type: got %q", ev.Type)
	}
	if ev.Point.Lat != 51.5074 {
		t.Errorf("lat: got %f", ev.Point.Lat)
	}
	if ev.Detail == nil || ev.Detail.Contact == nil {
		t.Fatal("detail/contact lost")
	}
	if ev.Detail.Contact.Callsign != "ALPHA-1" {
		t.Errorf("callsign: got %q", ev.Detail.Contact.Callsign)
	}
}

func TestMixedModeRead(t *testing.T) {
	// Simulate a stream with both XML and protobuf messages
	var buf bytes.Buffer

	// Write XML CoT event
	xmlEv := `<event version="2.0" uid="xml-001" type="a-f-G-U-C" how="m-g" time="2026-03-17T12:00:00Z" start="2026-03-17T12:00:00Z" stale="2026-03-17T12:05:00Z"><point lat="52.0" lon="4.0" hae="0" ce="10" le="10"/></event>` + "\n"
	buf.WriteString(xmlEv)

	// Write protobuf CotEvent
	pbMsg := &pb.TakMessage{
		CotEvent: &pb.CotEvent{
			Type:      "a-f-G-U-C",
			Uid:       "proto-001",
			How:       "m-g",
			SendTime:  1000000,
			StartTime: 1000000,
			StaleTime: 2000000,
			Lat:       53.0,
			Lon:       5.0,
		},
	}
	frame, _ := MarshalTakProto(pbMsg)
	buf.Write(frame)

	// Write another XML event
	xmlEv2 := `<event version="2.0" uid="xml-002" type="b-m-p-s-m" how="m-g" time="2026-03-17T12:01:00Z" start="2026-03-17T12:01:00Z" stale="2026-03-17T12:06:00Z"><point lat="54.0" lon="6.0" hae="0" ce="10" le="10"/></event>` + "\n"
	buf.WriteString(xmlEv2)

	reader := bufio.NewReaderSize(&buf, 256*1024)
	var events []*CotEvent

	for i := 0; i < 3; i++ {
		firstByte, err := reader.Peek(1)
		if err != nil {
			t.Fatalf("peek %d: %v", i, err)
		}

		if firstByte[0] == 0xBF {
			msg, err := ReadTakProtoMessage(reader)
			if err != nil {
				t.Fatalf("read proto %d: %v", i, err)
			}
			ev, err := ProtoToCotEvent(msg)
			if err != nil {
				t.Fatalf("convert proto %d: %v", i, err)
			}
			events = append(events, ev)
		} else {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				t.Fatalf("read xml %d: %v", i, err)
			}
			ev, err := ParseCotEvent(bytes.TrimSpace(line))
			if err != nil {
				t.Fatalf("parse xml %d: %v", i, err)
			}
			events = append(events, ev)
		}
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].UID != "xml-001" {
		t.Errorf("event 0 uid: got %q", events[0].UID)
	}
	if events[1].UID != "proto-001" {
		t.Errorf("event 1 uid: got %q", events[1].UID)
	}
	if events[2].UID != "xml-002" {
		t.Errorf("event 2 uid: got %q", events[2].UID)
	}
}

func TestParseCotEvent_TakControl(t *testing.T) {
	// Verify XML version negotiation response is parsed with TakControl
	xmlData := `<event version="2.0" uid="server-takp" type="t-x-takp-r" how="m-g" time="2026-03-17T12:00:00Z" start="2026-03-17T12:00:00Z" stale="2026-03-17T12:00:30Z"><point lat="0" lon="0" hae="0" ce="999999" le="999999"/><detail><TakControl minProtoVersion="0" maxProtoVersion="1"/></detail></event>`

	ev, err := ParseCotEvent([]byte(xmlData))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Type != "t-x-takp-r" {
		t.Errorf("type: got %q", ev.Type)
	}
	if ev.Detail == nil || ev.Detail.TakControl == nil {
		t.Fatal("TakControl not parsed from XML")
	}
	if ev.Detail.TakControl.MinProtoVersion != 0 {
		t.Errorf("minProto: got %d", ev.Detail.TakControl.MinProtoVersion)
	}
	if ev.Detail.TakControl.MaxProtoVersion != 1 {
		t.Errorf("maxProto: got %d", ev.Detail.TakControl.MaxProtoVersion)
	}
}

func TestUseProtobuf(t *testing.T) {
	g := &TAKGateway{}

	// Default: XML (no negotiation yet)
	g.config.Protocol = ""
	if g.useProtobuf() {
		t.Error("expected XML when no negotiation and no config override")
	}

	// Config override: protobuf
	g.config.Protocol = "protobuf"
	if !g.useProtobuf() {
		t.Error("expected protobuf when config says protobuf")
	}

	// Config override: xml (even if negotiated protobuf)
	g.config.Protocol = "xml"
	g.negotiatedProto.Store(1)
	if g.useProtobuf() {
		t.Error("expected XML when config says xml, even with negotiated protobuf")
	}

	// Auto (negotiated protobuf)
	g.config.Protocol = ""
	g.negotiatedProto.Store(1)
	if !g.useProtobuf() {
		t.Error("expected protobuf when negotiated proto=1")
	}

	// Auto (negotiated XML)
	g.negotiatedProto.Store(0)
	if g.useProtobuf() {
		t.Error("expected XML when negotiated proto=0")
	}
}

func TestMarshalTakProto_TakControl(t *testing.T) {
	// Verify TakControl-only message serializes correctly
	msg := &pb.TakMessage{
		TakControl: &pb.TakControl{
			MinProtoVersion: 0,
			MaxProtoVersion: 1,
			ContactUid:      "meshsat-takp",
		},
	}

	frame, err := MarshalTakProto(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if frame[0] != 0xBF {
		t.Fatalf("magic: got 0x%02x", frame[0])
	}

	// Verify it round-trips
	reader := bufio.NewReader(bytes.NewReader(frame))
	parsed, err := ReadTakProtoMessage(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if parsed.GetCotEvent() != nil {
		t.Error("should not have CotEvent")
	}
	tc := parsed.GetTakControl()
	if tc == nil {
		t.Fatal("no TakControl")
	}
	if tc.GetMaxProtoVersion() != 1 {
		t.Errorf("maxProto: got %d", tc.GetMaxProtoVersion())
	}
}

func TestReadTakProtoMessage_InvalidMagic(t *testing.T) {
	// Should fail if first byte is not 0xBF
	data := []byte{0x00, 0x05, 0x01, 0x02, 0x03, 0x04, 0x05}
	reader := bufio.NewReader(bytes.NewReader(data))
	_, err := ReadTakProtoMessage(reader)
	if err == nil {
		t.Error("expected error for invalid magic byte")
	}
	if !strings.Contains(err.Error(), "invalid magic byte") {
		t.Errorf("error: got %q", err)
	}
}

func TestReadTakProtoMessage_PayloadTooLarge(t *testing.T) {
	// Construct a frame claiming 512KB payload
	var buf bytes.Buffer
	buf.WriteByte(0xBF)
	// Varint for 512*1024 = 524288
	val := uint64(512 * 1024)
	for val >= 0x80 {
		buf.WriteByte(byte(val) | 0x80)
		val >>= 7
	}
	buf.WriteByte(byte(val))

	reader := bufio.NewReader(&buf)
	_, err := ReadTakProtoMessage(reader)
	if err == nil {
		t.Error("expected error for oversized payload")
	}
	if !strings.Contains(err.Error(), "payload too large") {
		t.Errorf("error: got %q", err)
	}
}

func TestBuildSpectrumJammingEvent(t *testing.T) {
	ev := BuildSpectrumJammingEvent(
		"meshsat-001", "MESHSAT-001", 52.3676, 4.9041,
		"lte_b20_dl", "LTE Band 20 DL (800)",
		804500000, 807500000,
		"jamming", "clear",
		-42.5, -30.1, -88.7,
		600,
	)
	if ev.Type != CotEventTypeJamming {
		t.Errorf("type: got %q, want %q", ev.Type, CotEventTypeJamming)
	}
	if ev.UID != "meshsat-001-SPECTRUM-lte_b20_dl" {
		t.Errorf("uid: got %q", ev.UID)
	}
	if ev.How != "m-g" {
		t.Errorf("how: got %q, want m-g", ev.How)
	}
	if ev.Point.Lat != 52.3676 || ev.Point.Lon != 4.9041 {
		t.Errorf("point: got lat=%f lon=%f", ev.Point.Lat, ev.Point.Lon)
	}
	if ev.Detail == nil || ev.Detail.Remarks == nil {
		t.Fatal("remarks is nil")
	}
	rem := ev.Detail.Remarks.Text
	// Text must carry the state, band, centre frequency, and the
	// jamming delta (power minus baseline) — those are what an
	// operator scans in ATAK to triage the source.
	for _, want := range []string{"JAMMING", "lte_b20_dl", "806.000 MHz", "+46.2 dB", "Prev=clear"} {
		if !strings.Contains(rem, want) {
			t.Errorf("remarks missing %q, got %q", want, rem)
		}
	}
	// Each band gets its own callsign suffix so ATAK tracks them as
	// distinct items.
	if ev.Detail.Contact == nil || ev.Detail.Contact.Callsign != "MESHSAT-001/lte_b20_dl" {
		t.Errorf("callsign: got %+v", ev.Detail.Contact)
	}
}

// Suppress unused import warnings
var _ = proto.Marshal
