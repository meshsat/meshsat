package hubreporter

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"meshsat/internal/hubmsg"
)

// The C10 frames and events against the shared cases (HeMB-NG stage 2, MESHSAT-1584):
// internal/hembfixtures/testdata/v1/sos.json, the same file the Hub's and the phones'
// tests read.

type sosFixture struct {
	Frames []struct {
		Case    string `json:"case"`
		Hex     string `json:"hex"`
		Type    string `json:"type"`
		Valid   *bool  `json:"valid"`
		Decoded *struct {
			BridgeID   string `json:"bridge_id"`
			DeviceID   string `json:"device_id"`
			RunStartMs int64  `json:"run_start_ms"`
			SOSID      string `json:"sos_id"`
			Text       string `json:"text"`
			Reason     int    `json:"reason"`
		} `json:"decoded"`
	} `json:"frames"`
	LegacySOSID struct {
		BridgeID   string `json:"bridge_id"`
		RunStartMs int64  `json:"run_start_ms"`
		SOSID      string `json:"sos_id"`
	} `json:"legacy_sos_id"`
	MQTT []struct {
		Case  string          `json:"case"`
		JSON  json.RawMessage `json:"json"`
		Valid bool            `json:"valid"`
	} `json:"mqtt"`
}

func loadSOSFixture(t *testing.T) sosFixture {
	t.Helper()
	raw, err := os.ReadFile("../hembfixtures/testdata/v1/sos.json")
	if err != nil {
		t.Fatal(err)
	}
	var f sosFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Frames) == 0 || len(f.MQTT) == 0 {
		t.Fatal("the fixture has no frames or no MQTT cases")
	}
	return f
}

// The fixture's frames carry 52.16, 4.49; their times are in the bytes (the 0x02 body's
// last field, the cancel's field after run_start_ms).
const fixtureLat, fixtureLon = 52.16, 4.49

func TestC10_FramesAreTheFixtureBytes(t *testing.T) {
	f := loadSOSFixture(t)
	n := 0
	for _, fr := range f.Frames {
		if fr.Decoded == nil {
			continue
		}
		want, err := hex.DecodeString(fr.Hex)
		if err != nil {
			t.Fatalf("%s: %v", fr.Case, err)
		}
		d := fr.Decoded
		id, ok := hubmsg.UUIDBytes(d.SOSID)
		if !ok {
			t.Fatalf("%s: sos_id %q does not read", fr.Case, d.SOSID)
		}
		var got []byte
		switch fr.Type {
		case "0x07":
			ts := time.Unix(int64(binary.BigEndian.Uint32(want[len(want)-28:])), 0)
			got = EncodeSatSOSStart(d.BridgeID, d.DeviceID, fixtureLat, fixtureLon, d.Text, ts, id, d.RunStartMs)
		case "0x08":
			ts := time.Unix(int64(binary.BigEndian.Uint32(want[len(want)-5:])), 0)
			got = EncodeSatSOSCancel(d.BridgeID, id, d.RunStartMs, ts, byte(d.Reason))
		default:
			t.Fatalf("%s: type %s", fr.Case, fr.Type)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s:\n got %x\nwant %x", fr.Case, got, want)
		}
		if !IsSatSOS(got) {
			t.Errorf("%s: IsSatSOS false, so the SMS leg would not queue it at priority 0", fr.Case)
		}
		n++
	}
	if n < 6 {
		t.Fatalf("only %d C10 frames checked", n)
	}
}

// The 0x02 is unchanged, and the 0x07 is the 0x02 body with the run's identity after it.
func TestC10_TheStartIsTheOldBodyWithItsIdentity(t *testing.T) {
	f := loadSOSFixture(t)
	old, _ := hex.DecodeString(f.Frames[0].Hex)
	ts := time.Unix(int64(binary.BigEndian.Uint32(old[len(old)-4:])), 0)
	if got := EncodeSatSOS("kit-a", "", fixtureLat, fixtureLon, "Injured, need help", ts); !bytes.Equal(got, old) {
		t.Fatalf("0x02 changed:\n got %x\nwant %x", got, old)
	}
	id, _ := hubmsg.UUIDBytes(f.LegacySOSID.SOSID)
	start := EncodeSatSOSStart("kit-a", "", fixtureLat, fixtureLon, "Injured, need help", ts, id, f.LegacySOSID.RunStartMs)
	if start[3] != SatMsgSOSStart || !bytes.Equal(start[4:len(old)], old[4:]) || len(start) != len(old)+24 {
		t.Fatalf("0x07 is not header + the 0x02 body + 24 bytes: %x", start)
	}
}

func TestC10_IsSatSOS(t *testing.T) {
	for _, c := range []struct {
		typ  byte
		want bool
	}{{SatMsgPosition, false}, {SatMsgSOS, true}, {SatMsgHealthSummary, false}, {SatMsgSOSStart, true}, {SatMsgSOSCancel, true}, {0x09, false}} {
		if got := IsSatSOS([]byte{0x4D, 0x53, 0x01, c.typ, 0x00}); got != c.want {
			t.Errorf("type 0x%02x: IsSatSOS %v, want %v", c.typ, got, c.want)
		}
	}
	if IsSatSOS([]byte{0x4D, 0x53, 0x02, SatMsgSOSStart}) {
		t.Error("another version taken as an SOS")
	}
}

// The id rule the Hub applies, here too: the fixture says which frames name their run.
func TestC10_SOSIDValidAsTheFixtureSays(t *testing.T) {
	f := loadSOSFixture(t)
	n := 0
	for _, fr := range f.Frames {
		if fr.Decoded == nil || fr.Valid == nil {
			continue
		}
		d := fr.Decoded
		if got := hubmsg.SOSIDValid(d.SOSID, d.BridgeID, d.RunStartMs); got != *fr.Valid {
			t.Errorf("%s: valid %v, the fixture says %v", fr.Case, got, *fr.Valid)
		}
		n++
	}
	l := f.LegacySOSID
	if got := hubmsg.LegacySOSID(l.BridgeID, l.RunStartMs); got != l.SOSID {
		t.Errorf("legacy id %s, want %s", got, l.SOSID)
	}
	if n < 7 {
		t.Fatalf("only %d cases checked", n)
	}
}

func TestC10_NewSOSIDNamesItsRun(t *testing.T) {
	const start = 1792330000123
	id := hubmsg.NewSOSID(start)
	if !hubmsg.ValidV7(id) || !hubmsg.SOSIDValid(id, "kit-a", start) {
		t.Fatalf("%s does not name the run that started at %d", id, start)
	}
	if hubmsg.SOSIDValid(id, "kit-a", start+1) {
		t.Fatal("the id names another start too")
	}
	if hubmsg.NewSOSID(start) == id {
		t.Fatal("two runs at the same millisecond got the same id")
	}
}

// What the Bridge publishes on sos/event reads as the fixture's valid events, field for
// field, and always carries run_start_ms.
func TestC10_EventsAreTheFixtureJSON(t *testing.T) {
	f := loadSOSFixture(t)
	n := 0
	for _, c := range f.MQTT {
		var fix map[string]any
		if err := json.Unmarshal(c.JSON, &fix); err != nil {
			t.Fatal(err)
		}
		if !c.Valid {
			if _, has := fix["run_start_ms"]; has {
				t.Fatalf("%s: the invalid case has run_start_ms; the test no longer covers it", c.Case)
			}
			continue
		}
		var ev SOSEvent
		if err := json.Unmarshal(c.JSON, &ev); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(ev)
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		if !reflect.DeepEqual(got, fix) {
			t.Errorf("%s:\n got %s\nwant %s", c.Case, raw, c.JSON)
		}
		n++
	}
	raw, _ := json.Marshal(SOSEvent{V: 2, Kind: SOSEventStart, SOSID: "x"})
	if !strings.Contains(string(raw), `"run_start_ms":0`) {
		t.Errorf("run_start_ms left out: %s", raw)
	}
	if n != 2 {
		t.Fatalf("%d valid MQTT cases checked, want 2", n)
	}
}

func TestC10_ParseHubCaps(t *testing.T) {
	for _, c := range []struct {
		payload string
		sosV2   bool
		bad     bool
	}{
		{``, false, false},
		{`{"v":1,"caps":["sos_v2"]}`, true, false},
		{`{"v":1,"caps":[]}`, false, false},
		{`{"v":1,"caps":["other","sos_v2"]}`, true, false},
		{`{"v":2,"caps":["sos_v2"]}`, false, true},
		{`not json`, false, true},
	} {
		h, err := ParseHubCaps([]byte(c.payload))
		if (err != nil) != c.bad || h.Has(CapSOSV2) != c.sosV2 {
			t.Errorf("%q: caps %+v err %v; want sos_v2 %v, error %v", c.payload, h, err, c.sosV2, c.bad)
		}
	}
}

func TestC10_ParseSOSAck(t *testing.T) {
	good := `{"v":2,"sos_id":"01a14f31-66fb-7a7b-bc7d-7e7f80818283","kind":"start","state":"active","rev":1}`
	a, err := ParseSOSAck([]byte(good))
	if err != nil || a.Kind != SOSEventStart || a.State != "active" || a.Rev != 1 {
		t.Fatalf("%+v %v", a, err)
	}
	for _, bad := range []string{
		`{"v":1,"sos_id":"x","kind":"start"}`,
		`{"v":2,"kind":"start"}`,
		`{"v":2,"sos_id":"x","kind":"stop"}`,
		`[]`,
	} {
		if _, err := ParseSOSAck([]byte(bad)); err == nil {
			t.Errorf("%s taken as an ack", bad)
		}
	}
}

func TestC10_Topics(t *testing.T) {
	def := Topics{}
	if got := def.SOS("kit-a", "event"); got != "meshsat/bridge/kit-a/sos/event" {
		t.Error(got)
	}
	if got := def.SOS("kit-a", "ack"); got != "meshsat/bridge/kit-a/sos/ack" {
		t.Error(got)
	}
	if got := def.HubCaps("kit-a"); got != "meshsat/bridge/kit-a/hub/caps" {
		t.Error(got)
	}
	if got := def.SOS("a/b+", "event"); got != "meshsat/bridge/a%2Fb%2B/sos/event" {
		t.Errorf("a bridge id is one segment: %s", got)
	}
}

// deliver sends a QoS 0 PUBLISH to every session, as the Hub does on hub/caps and sos/ack.
func (b *fakeBroker) deliver(topic string, payload []byte) {
	body := binary.BigEndian.AppendUint16(nil, uint16(len(topic)))
	body = append(append(body, topic...), payload...)
	pkt := []byte{0x30}
	for n := len(body); ; {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		pkt = append(pkt, d)
		if n == 0 {
			break
		}
	}
	pkt = append(pkt, body...)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		_, _ = c.Write(pkt)
	}
}

// On a session: the reporter subscribes its hub/caps and sos/ack on connect, hands what
// arrives there to its handlers, and publishes an event on its own sos/event at QoS 1,
// not retained.
func TestC10_TheSessionCarriesCapsAcksAndEvents(t *testing.T) {
	b := newFakeBroker(t, "127.0.0.1:0")
	r := NewHubReporter(ReporterConfig{HubURL: "tcp://" + b.addr(), BridgeID: "kit-a"},
		func() BridgeBirth { return BridgeBirth{BridgeID: "kit-a"} },
		func() BridgeHealth { return BridgeHealth{BridgeID: "kit-a"} })
	caps := make(chan HubCaps, 4)
	acks := make(chan SOSAck, 4)
	r.SetSOSHandlers(func(h HubCaps) { caps <- h }, func(a SOSAck) { acks <- a })

	if err := r.PublishSOSEvent(SOSEvent{Kind: SOSEventStart, SOSID: "x"}, time.Second); err != ErrNotConnected {
		t.Fatalf("before the session: %v, want ErrNotConnected", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	want := map[string]bool{"meshsat/bridge/kit-a/hub/caps": false, "meshsat/bridge/kit-a/sos/ack": false}
	deadline := time.After(10 * time.Second)
	for !want["meshsat/bridge/kit-a/hub/caps"] || !want["meshsat/bridge/kit-a/sos/ack"] {
		select {
		case topic := <-b.subs:
			if _, ok := want[topic]; ok {
				want[topic] = true
			}
		case <-deadline:
			t.Fatalf("subscriptions made: %v", want)
		}
	}

	b.deliver("meshsat/bridge/kit-a/hub/caps", []byte(`{"v":1,"caps":["sos_v2"]}`))
	select {
	case h := <-caps:
		if !h.Has(CapSOSV2) {
			t.Fatalf("caps %+v", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hub/caps never reached the handler")
	}
	b.deliver("meshsat/bridge/kit-a/sos/ack", []byte(`{"v":2,"sos_id":"01a14f31-66fb-7a7b-bc7d-7e7f80818283","kind":"cancel","state":"cancelled","rev":2}`))
	select {
	case a := <-acks:
		if a.Kind != SOSEventCancel || a.State != "cancelled" || a.Rev != 2 {
			t.Fatalf("ack %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sos/ack never reached the handler")
	}

	start := int64(1792330000123)
	ev := SOSEvent{Kind: SOSEventStart, SOSID: "01a14f31-66fb-7a7b-bc7d-7e7f80818283", RunStartMs: start, Text: "Injured, need help"}
	if err := r.PublishSOSEvent(ev, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case p := <-b.pubs:
			if p.topic != "meshsat/bridge/kit-a/sos/event" {
				continue
			}
			var got SOSEvent
			if err := json.Unmarshal(p.payload, &got); err != nil {
				t.Fatal(err)
			}
			if p.qos != 1 || p.retain || got.V != 2 || got.RunStartMs != start || got.Kind != SOSEventStart || got.SOSID != ev.SOSID {
				t.Fatalf("published qos %d retain %v: %s", p.qos, p.retain, p.payload)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("the event never reached the broker")
		}
	}
}
