package engine

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

func feedRec(bearer, dir string, at time.Time, n int) PacketRecord {
	return PacketRecord{
		Time:   at,
		Bearer: bearer,
		Dir:    dir,
		Iface:  bearer + "_0",
		From:   fmt.Sprintf("n%d", n),
		Bytes:  n,
	}
}

func TestPacketRing_NewestOrderAndCap(t *testing.T) {
	base := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		size      int
		adds      int
		limit     int
		wantLen   int
		wantFirst string // From of the newest record returned
		wantLast  string // From of the oldest record returned
	}{
		{"empty ring", 5, 0, 10, 0, "", ""},
		{"below capacity, newest first", 5, 3, 10, 3, "n3", "n1"},
		{"limit trims", 5, 3, 2, 2, "n3", "n2"},
		{"exactly full", 5, 5, 10, 5, "n5", "n1"},
		{"wrapped: oldest evicted", 5, 8, 10, 5, "n8", "n4"},
		{"limit zero means all", 5, 8, 0, 5, "n8", "n4"},
		{"wrapped twice", 3, 10, 10, 3, "n10", "n8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewPacketRing(tc.size, nil)
			for i := 1; i <= tc.adds; i++ {
				r.Add(feedRec(gateway.BearerLoRa, gateway.DirRX, base.Add(time.Duration(i)*time.Second), i))
			}
			if r.Len() != min(tc.adds, tc.size) {
				t.Fatalf("Len = %d, want %d", r.Len(), min(tc.adds, tc.size))
			}
			got := r.Newest(tc.limit, "", "")
			if len(got) != tc.wantLen {
				t.Fatalf("Newest returned %d records, want %d", len(got), tc.wantLen)
			}
			if tc.wantLen == 0 {
				return
			}
			if got[0].From != tc.wantFirst {
				t.Errorf("newest = %s, want %s", got[0].From, tc.wantFirst)
			}
			if got[len(got)-1].From != tc.wantLast {
				t.Errorf("oldest returned = %s, want %s", got[len(got)-1].From, tc.wantLast)
			}
			for i := 1; i < len(got); i++ {
				if got[i].Time.After(got[i-1].Time) {
					t.Errorf("record %d (%s) is newer than record %d (%s)", i, got[i].Time, i-1, got[i-1].Time)
				}
			}
		})
	}
}

func TestPacketRing_Filter(t *testing.T) {
	base := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	r := NewPacketRing(50, nil)
	// 4 lora rx, 3 lora tx, 2 aprs rx, 1 aprs tx, 5 sms rx, 0 sms tx
	mix := []struct {
		bearer, dir string
		n           int
	}{
		{gateway.BearerLoRa, gateway.DirRX, 4},
		{gateway.BearerLoRa, gateway.DirTX, 3},
		{gateway.BearerAPRS, gateway.DirRX, 2},
		{gateway.BearerAPRS, gateway.DirTX, 1},
		{gateway.BearerSMS, gateway.DirRX, 5},
	}
	i := 0
	for _, m := range mix {
		for k := 0; k < m.n; k++ {
			i++
			r.Add(feedRec(m.bearer, m.dir, base.Add(time.Duration(i)*time.Second), i))
		}
	}
	tests := []struct {
		name        string
		bearer, dir string
		limit       int
		want        int
	}{
		{"all", "", "", 0, 15},
		{"lora", gateway.BearerLoRa, "", 0, 7},
		{"lora rx", gateway.BearerLoRa, gateway.DirRX, 0, 4},
		{"lora tx", gateway.BearerLoRa, gateway.DirTX, 0, 3},
		{"aprs", gateway.BearerAPRS, "", 0, 3},
		{"aprs tx", gateway.BearerAPRS, gateway.DirTX, 0, 1},
		{"sms rx", gateway.BearerSMS, gateway.DirRX, 0, 5},
		{"sms tx: none", gateway.BearerSMS, gateway.DirTX, 0, 0},
		{"rx any bearer", "", gateway.DirRX, 0, 11},
		{"rx any bearer, limit 3", "", gateway.DirRX, 3, 3},
		{"unknown bearer", "ble", "", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Newest(tc.limit, tc.bearer, tc.dir)
			if len(got) != tc.want {
				t.Fatalf("got %d records, want %d", len(got), tc.want)
			}
			for _, rec := range got {
				if tc.bearer != "" && rec.Bearer != tc.bearer {
					t.Errorf("bearer %s leaked into %s filter", rec.Bearer, tc.bearer)
				}
				if tc.dir != "" && rec.Dir != tc.dir {
					t.Errorf("dir %s leaked into %s filter", rec.Dir, tc.dir)
				}
			}
		})
	}
	// Filtered results stay newest-first.
	got := r.Newest(0, gateway.BearerLoRa, "")
	if got[0].From != "n7" || got[len(got)-1].From != "n1" {
		t.Errorf("lora filter order: newest %s oldest %s", got[0].From, got[len(got)-1].From)
	}
}

func TestPacketRing_RatesWindows(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 5, 0, 0, time.UTC)
	r := NewPacketRing(100, nil)
	// age in seconds before now → bearer/dir
	adds := []struct {
		age         int
		bearer, dir string
	}{
		{5, gateway.BearerLoRa, gateway.DirRX},
		{30, gateway.BearerLoRa, gateway.DirRX},
		{59, gateway.BearerLoRa, gateway.DirTX},
		{61, gateway.BearerLoRa, gateway.DirRX}, // outside 60 s, inside 300 s
		{120, gateway.BearerAPRS, gateway.DirRX},
		{299, gateway.BearerAPRS, gateway.DirTX},
		{301, gateway.BearerAPRS, gateway.DirRX}, // outside both
		{10, gateway.BearerSMS, gateway.DirTX},
		{-5, gateway.BearerSMS, gateway.DirRX}, // future: ignored
	}
	for i, a := range adds {
		r.Add(feedRec(a.bearer, a.dir, now.Add(-time.Duration(a.age)*time.Second), i+1))
	}
	tests := []struct {
		name   string
		window time.Duration
		want   map[string]PacketRates
	}{
		{"60s", 60 * time.Second, map[string]PacketRates{
			gateway.BearerLoRa: {RX: 2, TX: 1},
			gateway.BearerAPRS: {RX: 0, TX: 0},
			gateway.BearerSMS:  {RX: 0, TX: 1},
		}},
		{"300s", 300 * time.Second, map[string]PacketRates{
			gateway.BearerLoRa: {RX: 3, TX: 1},
			gateway.BearerAPRS: {RX: 1, TX: 1},
			gateway.BearerSMS:  {RX: 0, TX: 1},
		}},
		{"1s: nothing", time.Second, map[string]PacketRates{
			gateway.BearerLoRa: {}, gateway.BearerAPRS: {}, gateway.BearerSMS: {},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Rates(now, tc.window)
			if len(got) != 4 {
				t.Fatalf("rates must list exactly the four bearers, got %v", got)
			}
			for b, want := range tc.want {
				if got[b] != want {
					t.Errorf("%s: got %+v, want %+v", b, got[b], want)
				}
			}
		})
	}
}

func TestPacketRing_AddEmitsPacketEvent(t *testing.T) {
	var events []transport.MeshEvent
	r := NewPacketRing(10, func(e transport.MeshEvent) { events = append(events, e) })
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	r.Add(PacketRecord{
		Bearer: gateway.BearerLoRa, Dir: gateway.DirRX, Iface: "mesh_0",
		From: "!a1b2c3d4", To: "broadcast", Bytes: 13, SNR: 6.5, RSSI: -71,
		PortNum: 1, PortNumName: "TEXT_MESSAGE_APP", Text: string(long),
		Raw: string(long) + string(long) + string(long),
	})
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != PacketEventType {
		t.Errorf("type = %q, want %q", ev.Type, PacketEventType)
	}
	if ev.Message == "" || ev.Time == "" {
		t.Errorf("summary and time must be set: %+v", ev)
	}
	if _, err := time.Parse(time.RFC3339Nano, ev.Time); err != nil {
		t.Errorf("event time not RFC3339Nano: %q", ev.Time)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(ev.Data, &got); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	for _, key := range []string{"time", "bearer", "dir", "iface", "from", "to", "bytes", "rssi", "snr",
		"hops", "channel", "portnum", "portnum_name", "text", "raw", "path", "msg_ref"} {
		if _, ok := got[key]; !ok {
			t.Errorf("packet JSON is missing %q", key)
		}
	}
	if got["from"] != "!a1b2c3d4" || got["rssi"].(float64) != -71 {
		t.Errorf("unexpected data: %v", got)
	}
	if len(got["text"].(string)) != gateway.PacketTextCap {
		t.Errorf("text not capped: %d", len(got["text"].(string)))
	}
	if len(got["raw"].(string)) != gateway.PacketRawHexCap {
		t.Errorf("raw not capped: %d", len(got["raw"].(string)))
	}
	// A record without a timestamp is stamped on insert.
	stored := r.Newest(1, "", "")
	if len(stored) != 1 || stored[0].Time.IsZero() {
		t.Errorf("stored record must carry a timestamp: %+v", stored)
	}
}

func TestPacketRing_NilSafe(t *testing.T) {
	var r *PacketRing
	r.Add(PacketRecord{Bearer: gateway.BearerSMS})
	if got := r.Newest(10, "", ""); len(got) != 0 {
		t.Errorf("nil ring Newest = %v", got)
	}
	if r.Len() != 0 {
		t.Errorf("nil ring Len = %d", r.Len())
	}
	rates := r.Rates(time.Now(), time.Minute)
	if len(rates) != 4 {
		t.Errorf("nil ring Rates must still list the bearers: %v", rates)
	}
	var p *Processor
	if p.Packets() != nil {
		t.Error("nil processor must yield a nil ring")
	}
	p.Packets().Add(PacketRecord{}) // must not panic
	r.Sink()(PacketRecord{})        // must not panic
}

// rssiMesh is a MeshTransport stub that only answers NodeRSSI.
type rssiMesh struct {
	transport.MeshTransport
	rssi map[uint32]int32
}

func (m *rssiMesh) NodeRSSI(num uint32) (int32, bool) {
	v, ok := m.rssi[num]
	return v, ok
}

func (m *rssiMesh) LocalNodeID() string { return "!0a0b0c0d" }

func TestMeshRXRecord(t *testing.T) {
	mesh := &rssiMesh{rssi: map[uint32]int32{0x11223344: -82}}
	text := "hello over lora"
	tests := []struct {
		name string
		msg  transport.MeshMessage
		want PacketRecord
	}{
		{
			name: "text message, direct, broadcast",
			msg: transport.MeshMessage{
				From: 0x11223344, To: 0xffffffff, Channel: 2, PortNum: int(transport.PortNumTextMessage),
				PortNumName: "TEXT_MESSAGE_APP", DecodedText: text, RawPayload: []byte(text),
				RxSNR: 7.25, HopLimit: 3, HopStart: 3,
			},
			want: PacketRecord{
				Bearer: "lora", Dir: "rx", Iface: "mesh_0", From: "!11223344", To: "broadcast",
				Bytes: len(text), RSSI: -82, SNR: 7.25, Hops: 0, Channel: 2, PortNum: 1,
				PortNumName: "TEXT_MESSAGE_APP", Text: text,
			},
		},
		{
			name: "telemetry after two hops, unknown node, no text",
			msg: transport.MeshMessage{
				From: 0xdeadbeef, To: 0x11223344, PortNum: 67, PortNumName: "TELEMETRY_APP",
				RawPayload: []byte{1, 2, 3, 4, 5, 6, 7}, HopLimit: 1, HopStart: 3,
			},
			want: PacketRecord{
				Bearer: "lora", Dir: "rx", Iface: "mesh_0", From: "!deadbeef", To: "!11223344",
				Bytes: 7, Hops: 2, PortNum: 67, PortNumName: "TELEMETRY_APP",
			},
		},
		{
			name: "via mqtt: rssi withheld",
			msg: transport.MeshMessage{
				From: 0x11223344, To: 0xffffffff, PortNum: 3, PortNumName: "POSITION_APP",
				RawPayload: []byte{9, 9}, ViaMqtt: true,
			},
			want: PacketRecord{
				Bearer: "lora", Dir: "rx", Iface: "mesh_0", From: "!11223344", To: "broadcast",
				Bytes: 2, PortNum: 3, PortNumName: "POSITION_APP",
			},
		},
		{
			name: "encrypted relay: bytes from the ciphertext",
			msg: transport.MeshMessage{
				From: 0x11223344, To: 0xffffffff, PortNumName: "ENCRYPTED_RELAY",
				EncryptedPayload: make([]byte, 40),
			},
			want: PacketRecord{
				Bearer: "lora", Dir: "rx", Iface: "mesh_0", From: "!11223344", To: "broadcast",
				Bytes: 40, RSSI: -82, PortNumName: "ENCRYPTED_RELAY",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := meshRXRecord(mesh, &tc.msg)
			if got.Time.IsZero() {
				t.Error("time must be stamped")
			}
			got.Time = time.Time{}
			if got != tc.want {
				t.Errorf("\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestMeshTXRecord(t *testing.T) {
	tests := []struct {
		name   string
		req    transport.SendRequest
		msgRef string
		wantTo string
	}{
		{"empty to is broadcast", transport.SendRequest{Text: "hi"}, "", "broadcast"},
		{"all-ones is broadcast", transport.SendRequest{Text: "hi", To: "!ffffffff"}, "20260906-1", "broadcast"},
		{"node id kept", transport.SendRequest{Text: "hi", To: "!a1b2c3d4", Channel: 1}, "ref-2", "!a1b2c3d4"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MeshTXRecord(nil, "", tc.req, tc.msgRef)
			if got.Bearer != "lora" || got.Dir != "tx" || got.Iface != "mesh_0" || got.From != "" {
				t.Errorf("bearer/dir/iface/from: %+v", got)
			}
			if withNode := MeshTXRecord(&rssiMesh{}, "mesh_0", tc.req, tc.msgRef); withNode.From != "!0a0b0c0d" {
				t.Errorf("from must come from the transport: %+v", withNode)
			}
			if got.To != tc.wantTo || got.Text != tc.req.Text || got.Bytes != len(tc.req.Text) ||
				got.Channel != tc.req.Channel || got.MsgRef != tc.msgRef || got.PortNum != 1 {
				t.Errorf("unexpected record %+v", got)
			}
		})
	}
}

// The rates endpoint zero-fills the satellite bearer like the others. [MESHSAT-962]
func TestPacketRing_RatesIncludeSat(t *testing.T) {
	r := NewPacketRing(8, nil)
	out := r.Rates(time.Now(), time.Minute)
	if _, ok := out[gateway.BearerSat]; !ok {
		t.Fatalf("sat missing from rates: %v", out)
	}
}
