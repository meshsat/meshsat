package transport

import (
	"testing"
)

// ============================================================================
// Frame Layer Tests
// ============================================================================

func TestFindStartMarker(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected int
	}{
		{"at start", []byte{0x94, 0xC3, 0x00, 0x05}, 0},
		{"offset", []byte{0xFF, 0xFF, 0x94, 0xC3, 0x00, 0x05}, 2},
		{"not found", []byte{0x94, 0x00, 0xC3}, -1},
		{"empty", []byte{}, -1},
		{"single byte", []byte{0x94}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStartMarker(tt.data)
			if got != tt.expected {
				t.Errorf("findStartMarker(%v) = %d, want %d", tt.data, got, tt.expected)
			}
		})
	}
}

func TestExtractFrame(t *testing.T) {
	reader := &meshFrameReader{}

	// Incomplete frame — should return nil
	reader.accum = []byte{0x94, 0xC3, 0x00, 0x05, 0x01, 0x02}
	payload := reader.extractFrame()
	if payload != nil {
		t.Errorf("expected nil for incomplete frame, got %v", payload)
	}

	// Complete frame
	reader.accum = []byte{0x94, 0xC3, 0x00, 0x03, 0xAA, 0xBB, 0xCC}
	payload = reader.extractFrame()
	if payload == nil || len(payload) != 3 {
		t.Fatalf("expected 3-byte payload, got %v", payload)
	}
	if payload[0] != 0xAA || payload[1] != 0xBB || payload[2] != 0xCC {
		t.Errorf("wrong payload content: %v", payload)
	}

	// Garbage before frame
	reader.accum = []byte{0xFF, 0xFF, 0x94, 0xC3, 0x00, 0x02, 0x01, 0x02}
	payload = reader.extractFrame()
	if payload == nil || len(payload) != 2 {
		t.Fatalf("expected 2-byte payload after garbage, got %v", payload)
	}

	// Zero-length frame — should be skipped
	reader.accum = []byte{0x94, 0xC3, 0x00, 0x00, 0x94, 0xC3, 0x00, 0x01, 0x42}
	payload = reader.extractFrame()
	if payload == nil || len(payload) != 1 || payload[0] != 0x42 {
		t.Errorf("expected 1-byte payload after zero-length skip, got %v", payload)
	}
}

// ============================================================================
// Protobuf Builder Tests
// ============================================================================

func TestBuildWantConfigID(t *testing.T) {
	data := buildWantConfigID(0x12345678)
	if len(data) == 0 {
		t.Fatal("empty result")
	}
	// want_config_id is field 3 (varint), so it's tag 0x18 + varint(0x12345678)
	// Verify by parsing back: first byte should be tag for field 3 varint
	if data[0] != 0x18 {
		t.Errorf("expected tag 0x18, got 0x%x", data[0])
	}
	// Read the varint value after the tag
	val, n := readVarint(data, 1)
	if n <= 0 {
		t.Fatal("failed to read varint")
	}
	if uint32(val) != 0x12345678 {
		t.Errorf("config ID = 0x%x, want 0x12345678", val)
	}
}

func TestBuildTextMessage(t *testing.T) {
	msg := buildTextMessage("Hello", 0xFFFFFFFF, 0)
	if len(msg) == 0 {
		t.Fatal("empty result")
	}
	// Should contain "Hello" as a length-delimited field
	hello := []byte("Hello")
	found := false
	for i := 0; i < len(msg)-len(hello); i++ {
		match := true
		for j := 0; j < len(hello); j++ {
			if msg[i+j] != hello[j] {
				match = false
				break
			}
		}
		if match {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("text 'Hello' not found in output: %x", msg)
	}
}

func TestBuildRawPacket(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03}
	pkt := buildRawPacket(payload, PortNumTextMessage, 0xFFFFFFFF, 0, true)
	if len(pkt) == 0 {
		t.Fatal("empty result")
	}
	// Should contain the payload bytes
	found := false
	for i := 0; i < len(pkt)-3; i++ {
		if pkt[i] == 0x01 && pkt[i+1] == 0x02 && pkt[i+2] == 0x03 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("payload not found in output: %x", pkt)
	}
}

func TestBuildAdminSetTime(t *testing.T) {
	data := buildAdminSetTime(1, 2, 1709000000)
	if len(data) == 0 {
		t.Fatal("empty result")
	}
	// Should be a valid ToRadio envelope
	// Verify it's parseable as a proto message
	if data[0] == 0 {
		t.Error("unexpected zero first byte")
	}
}

// ============================================================================
// Protobuf Parser Tests
// ============================================================================

func TestParseFromRadio_MyNodeInfo(t *testing.T) {
	// Build a minimal FromRadio with my_info field (field 3)
	// my_info contains MyNodeInfo with my_node_num (field 1)
	myInfo := appendVarintField(nil, 1, 42) // my_node_num = 42
	data := appendLengthDelimited(nil, 3, myInfo)

	fr, err := parseFromRadio(data)
	if err != nil {
		t.Fatalf("parseFromRadio failed: %v", err)
	}
	if fr.MyInfo == nil {
		t.Fatal("expected MyInfo to be set")
	}
	if fr.MyInfo.MyNodeNum != 42 {
		t.Errorf("MyNodeNum = %d, want 42", fr.MyInfo.MyNodeNum)
	}
}

func TestParseFromRadio_ConfigCompleteID(t *testing.T) {
	// Field 7 = config_complete_id (varint)
	data := appendVarintField(nil, 7, 12345)

	fr, err := parseFromRadio(data)
	if err != nil {
		t.Fatalf("parseFromRadio failed: %v", err)
	}
	if fr.ConfigCompleteID != 12345 {
		t.Errorf("ConfigCompleteID = %d, want 12345", fr.ConfigCompleteID)
	}
}

func TestParseMeshPacket(t *testing.T) {
	// Build a MeshPacket with from (field 1, fixed32), to (field 2, fixed32),
	// decoded (field 4, length-delimited), id (field 6, fixed32)
	var pkt []byte
	// from = 0x1234 (field 1, wire type 5 = fixed32)
	pkt = appendTag(pkt, 1, wireFixed32)
	pkt = appendFixed32(pkt, 0x1234)
	// to = 0xFFFFFFFF (field 2, wire type 5)
	pkt = appendTag(pkt, 2, wireFixed32)
	pkt = appendFixed32(pkt, 0xFFFFFFFF)
	// decoded is field 4 in MeshPacket (not field 3 — field 3 is channel)
	decoded := appendVarintField(nil, 1, PortNumTextMessage) // portnum
	decoded = appendLengthDelimited(decoded, 2, []byte("test"))
	pkt = appendLengthDelimited(pkt, 4, decoded)
	// id (field 6, fixed32)
	pkt = appendTag(pkt, 6, wireFixed32)
	pkt = appendFixed32(pkt, 999)

	mp, err := parseMeshPacket(pkt)
	if err != nil {
		t.Fatalf("parseMeshPacket failed: %v", err)
	}
	if mp.From != 0x1234 {
		t.Errorf("From = %d, want %d", mp.From, 0x1234)
	}
	if mp.To != 0xFFFFFFFF {
		t.Errorf("To = %d, want %d", mp.To, 0xFFFFFFFF)
	}
	if mp.ID != 999 {
		t.Errorf("ID = %d, want 999", mp.ID)
	}
	if mp.Decoded == nil {
		t.Fatal("Decoded is nil")
	}
	if mp.Decoded.PortNum != PortNumTextMessage {
		t.Errorf("PortNum = %d, want %d", mp.Decoded.PortNum, PortNumTextMessage)
	}
	if string(mp.Decoded.Payload) != "test" {
		t.Errorf("Payload = %q, want %q", mp.Decoded.Payload, "test")
	}
}

func TestParsePosition(t *testing.T) {
	// Build position: lat_i (field 1, sfixed32), lon_i (field 2, sfixed32),
	// altitude (field 3, int32), sats_in_view (field 11, uint32)
	var pos []byte
	// lat_i = 521620000 (52.162° * 1e7)
	pos = append(pos, 0x0D) // field 1, wire type 5
	pos = appendFixed32(pos, uint32(521620000))
	// lon_i = 45090000 (4.509° * 1e7)
	pos = append(pos, 0x15) // field 2, wire type 5
	pos = appendFixed32(pos, uint32(45090000))
	// altitude = 5 (field 3, varint)
	pos = appendVarintField(pos, 3, 5)
	// sats_in_view = 12 (field 19, varint)
	pos = appendVarintField(pos, 19, 12)

	p, err := parsePosition(pos)
	if err != nil {
		t.Fatalf("parsePosition failed: %v", err)
	}
	if p.LatitudeI != 521620000 {
		t.Errorf("LatitudeI = %d, want 521620000", p.LatitudeI)
	}
	if p.LongitudeI != 45090000 {
		t.Errorf("LongitudeI = %d, want 45090000", p.LongitudeI)
	}
	if p.Altitude != 5 {
		t.Errorf("Altitude = %d, want 5", p.Altitude)
	}
	if p.SatsInView != 12 {
		t.Errorf("SatsInView = %d, want 12", p.SatsInView)
	}
}

func TestParseUser(t *testing.T) {
	var user []byte
	user = appendLengthDelimited(user, 1, []byte("!abcd1234")) // id
	user = appendLengthDelimited(user, 2, []byte("Test Node")) // long_name
	user = appendLengthDelimited(user, 3, []byte("TST"))       // short_name
	user = appendVarintField(user, 5, 43)                      // hw_model (T_ECHO=43)

	u, err := parseUser(user)
	if err != nil {
		t.Fatalf("parseUser failed: %v", err)
	}
	if u.ID != "!abcd1234" {
		t.Errorf("ID = %q, want %q", u.ID, "!abcd1234")
	}
	if u.LongName != "Test Node" {
		t.Errorf("LongName = %q, want %q", u.LongName, "Test Node")
	}
	if u.ShortName != "TST" {
		t.Errorf("ShortName = %q, want %q", u.ShortName, "TST")
	}
	if u.HWModel != 43 {
		t.Errorf("HWModel = %d, want 43", u.HWModel)
	}
}

func TestPortNumName(t *testing.T) {
	tests := []struct {
		portnum int
		want    string
	}{
		{1, "TEXT_MESSAGE_APP"},
		{3, "POSITION_APP"},
		{4, "NODEINFO_APP"},
		{67, "TELEMETRY_APP"},
		{9999, "PORTNUM_9999"},
	}
	for _, tt := range tests {
		got := portNumName(tt.portnum)
		if got != tt.want {
			t.Errorf("portNumName(%d) = %q, want %q", tt.portnum, got, tt.want)
		}
	}
}

func TestHWModelName(t *testing.T) {
	tests := []struct {
		model int
		want  string
	}{
		{43, "HELTEC_V3"},
		{7, "T_ECHO"},
		{99999, "HW_MODEL_99999"},
	}
	for _, tt := range tests {
		got := hwModelName(tt.model)
		if got != tt.want {
			t.Errorf("hwModelName(%d) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestComputeSignalQuality(t *testing.T) {
	tests := []struct {
		rssi float64
		snr  float64
		want string // just the quality level
	}{
		{-80, 10.0, "GOOD"},
		{-120, -10.0, "FAIR"},
		{-130, -20.0, "BAD"},
	}
	for _, tt := range tests {
		q, _ := computeSignalQuality(tt.rssi, tt.snr)
		if q != tt.want {
			t.Errorf("computeSignalQuality(%.0f, %.1f) = %q, want %q", tt.rssi, tt.snr, q, tt.want)
		}
	}
}

// ============================================================================
// Proto Wire Encoding Tests
// ============================================================================

func TestAppendVarint(t *testing.T) {
	tests := []struct {
		val  uint64
		want []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7F}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xAC, 0x02}},
	}
	for _, tt := range tests {
		got := appendVarint(nil, tt.val)
		if len(got) != len(tt.want) {
			t.Errorf("appendVarint(%d) len = %d, want %d", tt.val, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("appendVarint(%d)[%d] = %x, want %x", tt.val, i, got[i], tt.want[i])
			}
		}
	}
}

func TestReadVarint(t *testing.T) {
	tests := []struct {
		data []byte
		want uint64
		size int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x01}, 1, 1},
		{[]byte{0x80, 0x01}, 128, 2},
		{[]byte{0xAC, 0x02}, 300, 2},
	}
	for _, tt := range tests {
		val, size := readVarint(tt.data, 0)
		if val != tt.want || size != tt.size {
			t.Errorf("readVarint(%x) = (%d, %d), want (%d, %d)", tt.data, val, size, tt.want, tt.size)
		}
	}
}

func TestRoundtripVarint(t *testing.T) {
	values := []uint64{0, 1, 127, 128, 255, 300, 16383, 16384, 1000000, 0xFFFFFFFF}
	for _, v := range values {
		encoded := appendVarint(nil, v)
		decoded, _ := readVarint(encoded, 0)
		if decoded != v {
			t.Errorf("roundtrip %d: encoded %x decoded %d", v, encoded, decoded)
		}
	}
}

func TestAppendFixed32(t *testing.T) {
	got := appendFixed32(nil, 0x12345678)
	want := []byte{0x78, 0x56, 0x34, 0x12} // little-endian
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %x, want %x", i, got[i], want[i])
		}
	}
}

func TestDecodeZigzag(t *testing.T) {
	tests := []struct {
		encoded uint64
		want    int64
	}{
		{0, 0},
		{1, -1},
		{2, 1},
		{3, -2},
		{4, 2},
	}
	for _, tt := range tests {
		got := decodeZigzag(tt.encoded)
		if got != tt.want {
			t.Errorf("decodeZigzag(%d) = %d, want %d", tt.encoded, got, tt.want)
		}
	}
}

func TestEncodeZigzag(t *testing.T) {
	tests := []struct {
		val  int64
		want uint64
	}{
		{0, 0},
		{-1, 1},
		{1, 2},
		{-2, 3},
		{2, 4},
	}
	for _, tt := range tests {
		got := encodeZigzag(tt.val)
		if got != tt.want {
			t.Errorf("encodeZigzag(%d) = %d, want %d", tt.val, got, tt.want)
		}
	}
}
