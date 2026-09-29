package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/hubreporter"
)

// androidTestFrame is MeshSat Android's SosMessages.positionFrame("msa-flaneur",
// fix 52.1601, 4.4970, altitude 3.9, 1790000000), written out by hand from
// the Kotlin algorithm (the field by field derivation is in
// internal/hubreporter/satuplink_positionframe_test.go).
const androidTestFrame = "4d5301010b6d73612d666c616e657572" + "4250a3f1" + "408fe76d" + "0003" + "01" + "6ab13b80"

// sosTestSatBody is the Linux app's satellite leg for that fix.
const sosTestSatBody = `{"satellite":true,"latitude":52.1601,"longitude":4.4970,"altitude":3.9}`

// sosSatServer is the Bridge behind the alarm test's satellite leg: the
// database, a delivery queue whose workers never start (nothing is sent, no
// satellite session opens), and a gateway manager running an SBD gateway on
// iridium_0 and/or an IMT gateway on iridium_imt_0 over modems that refuse
// every session. It names itself "msa-flaneur" to the Hub, and its clock
// stands at 1790000000.
func sosSatServer(t *testing.T, sbd, imt *apiSat) *Server {
	t.Helper()
	s, _ := mailboxServer(t, sbd, imt)
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	s.SetDispatcher(engine.NewDispatcher(s.db, reg, s.gwManager, nil))
	s.SetHubBridgeID("msa-flaneur")
	s.nowFn = func() time.Time { return time.Unix(1790000000, 0) }
	return s
}

type sosSatAnswer struct {
	Status     string `json:"status"`
	Existing   bool   `json:"existing"`
	DeliveryID int64  `json:"delivery_id"`
	MsgRef     string `json:"msg_ref"`
	Message    string `json:"message"`
	Interface  string `json:"interface"`
	BridgeID   string `json:"bridge_id"`
}

// postSatTest posts body to POST /api/sos/test, wants 200 and returns the
// answer and the delivery it queued.
func postSatTest(t *testing.T, s *Server, body string) (sosSatAnswer, *database.MessageDelivery) {
	t.Helper()
	w := post(t, s, "/api/sos/test", body)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/sos/test %s: %d %s", body, w.Code, w.Body.String())
	}
	var resp sosSatAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("answer %s: %v", w.Body.String(), err)
	}
	del, err := s.db.GetDelivery(resp.DeliveryID)
	if err != nil {
		t.Fatalf("answer %s: %v", w.Body.String(), err)
	}
	return resp, del
}

// testDeliveryCount counts the alarm test's satellite legs in the queue,
// leaving out what an SOS queues of its own. [MESHSAT-1446]
func testDeliveryCount(t *testing.T, s *Server) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries WHERE text_preview = ?`, sosTestSatellitePreview).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func deliveryCount(t *testing.T, s *Server) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// With satellite true, the alarm test's satellite leg is MeshSat Android's:
// its position frame byte for byte, queued on iridium_0 as a Hub uplink
// frame, with the text preview Android gives it. The caller's words are not
// in it: a frame for the Hub's uplink decoder, never a text its routing
// engine could relay. It needs no Hub link: the Hub reporter here exists
// and is not connected. Unlike Android's it waits at Routine, priority 1,
// below everything an SOS queues, and for 30 minutes at most. [MESHSAT-1430]
func TestSOSTest_SatelliteQueuesAndroidsPositionFrame(t *testing.T) {
	sbd := &apiSat{kind: "sbd", connected: true}
	s := sosSatServer(t, sbd, nil)
	s.SetHubReporter(hubreporter.NewHubReporter(hubreporter.ReporterConfig{BridgeID: "msa-flaneur"}, nil, nil)) // never started: MQTT down

	resp, del := postSatTest(t, s, `{"satellite":true,"latitude":52.1601,"longitude":4.4970,"altitude":3.9,`+
		`"message":"Test from Anna: checking the MeshSat alarm routes. No help needed."}`)
	if resp.Status != "queued" || resp.Existing || resp.DeliveryID == 0 || resp.MsgRef == "" || resp.Message != "Alarm test: position report to the Hub" ||
		resp.Interface != "iridium_0" || resp.BridgeID != "msa-flaneur" {
		t.Fatalf("answer %+v", resp)
	}

	want := hubreporter.EncodeSatPosition("msa-flaneur", 52.1601, 4.4970, float32(3.9), 1, time.Unix(1790000000, 0))
	if !bytes.Equal(del.Payload, want) {
		t.Fatalf("queued %x, want EncodeSatPosition's %x", del.Payload, want)
	}
	if got := hex.EncodeToString(del.Payload); got != androidTestFrame {
		t.Fatalf("queued %s, want Android's positionFrame %s", got, androidTestFrame)
	}
	if del.Channel != "iridium_0" || del.TextPreview != "Alarm test: position report to the Hub" || del.Priority != 1 ||
		del.Precedence != "Routine" || del.Class != database.DeliveryClassHubUplink || del.MsgRef != resp.MsgRef {
		t.Fatalf("delivery: channel %s, preview %q, priority %d, precedence %s, class %s, msg_ref %s",
			del.Channel, del.TextPreview, del.Priority, del.Precedence, del.Class, del.MsgRef)
	}
	if del.TTLSeconds != 1800 || del.ExpiresAt == nil {
		t.Fatalf("delivery: ttl_seconds %d, expires_at %v, want a 30 minute deadline", del.TTLSeconds, del.ExpiresAt)
	}
	// Class hub_uplink: the delivery worker applies no egress rules and no
	// transform chain, and the satellite gateway sends the bytes as they are
	// (engine TestHubUplinkFrame_SatelliteSkipsTheTransformChain runs that).
	if !database.DeliveryClassBypassesPolicy(del.Class) || !database.DeliveryClassVerbatim(del.Class) {
		t.Fatalf("class %s would pass through the link's chain", del.Class)
	}

	// What the Hub's uplink decoder reads.
	hdr, payload, err := hubreporter.DecodeSatUplink(del.Payload)
	if err != nil || hdr.MsgType != hubreporter.SatMsgPosition {
		t.Fatalf("header %+v, %v", hdr, err)
	}
	id, lat, lon, alt, source, ts, err := hubreporter.DecodeSatPosition(payload)
	if err != nil || id != "msa-flaneur" || math.Abs(lat-52.1601) > 1e-5 || math.Abs(lon-4.4970) > 1e-5 ||
		alt != 3 || source != 1 || ts.Unix() != 1790000000 {
		t.Fatalf("decoded %q %v %v %v %d %v (%v)", id, lat, lon, alt, source, ts, err)
	}

	// Queued only: nothing reached the modem.
	if del.Status != "queued" {
		t.Fatalf("delivery %s, want queued", del.Status)
	}
	if checks, polls := sbd.counts(); checks != 0 || polls != 0 {
		t.Fatalf("the modem saw %d checks and %d polls", checks, polls)
	}
}

// Without a position there is no frame: 400, nothing queued. Latitude and
// longitude both left out fall back on this Bridge's GPS fix, and there is
// none here (no GPS reader). One without the other is refused, 0 or null
// included, and so is a latitude or longitude out of range. [MESHSAT-1430]
func TestSOSTest_SatelliteNeedsAPosition(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	for _, tc := range []struct{ body, why string }{
		{`{"satellite":true}`, "no GPS fix"},
		{`{"satellite":true,"message":"Test","altitude":12}`, "no GPS fix"},
		{`{"satellite":true,"latitude":null,"longitude":null}`, "no GPS fix"},
		{`{"satellite":true,"latitude":52.1601}`, "together"},
		{`{"satellite":true,"longitude":4.4970,"altitude":3.9}`, "together"},
		{`{"satellite":true,"latitude":0}`, "together"},
		{`{"satellite":true,"latitude":52.1601,"longitude":null}`, "together"},
		{`{"satellite":true,"latitude":91,"longitude":4.497}`, "-90 to 90"},
		{`{"satellite":true,"latitude":52.1601,"longitude":-180.5}`, "-180 to 180"},
	} {
		if w := post(t, s, "/api/sos/test", tc.body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.why) {
			t.Errorf("%s: %d %s, want 400 (%s)", tc.body, w.Code, w.Body.String(), tc.why)
		}
	}
	if n := deliveryCount(t, s); n != 0 {
		t.Fatalf("%d deliveries queued without a position", n)
	}
}

// 0,0 given as latitude and longitude is a position the caller states and
// goes as it is; only coordinates left out fall back on the GPS fix. It was
// refused (400) while the handler could not tell 0 from left out.
// [MESHSAT-1430]
func TestSOSTest_SatelliteTakesAStatedZeroZero(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	_, del := postSatTest(t, s, `{"satellite":true,"latitude":0,"longitude":0,"altitude":3.9}`)
	_, payload, err := hubreporter.DecodeSatUplink(del.Payload)
	if err != nil {
		t.Fatal(err)
	}
	_, lat, lon, alt, _, _, err := hubreporter.DecodeSatPosition(payload)
	if err != nil || lat != 0 || lon != 0 || alt != 3 {
		t.Fatalf("the frame says %v, %v, %v m (%v), want 0, 0, 3 m", lat, lon, alt, err)
	}
}

// A body that names satellite and does not decode is refused, whatever went
// wrong in it: a field of the wrong type, the flag itself as a string, a NaN
// (what Python's json module writes for a float that is not a number), a
// body cut off. The error was ignored, and such a request became a Hub
// event or a frame from half a body. The Hub event keeps its old leniency
// (the Linux app 0.6.0 to 0.12.0 sends it): a body that does not decode
// still sends the default test, and one coordinate alone is still taken;
// here that is 503, the Hub not being connected. [MESHSAT-1430]
func TestSOSTest_SatelliteRefusesABodyThatDoesNotDecode(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	for _, body := range []string{
		`{"satellite":true,"latitude":"52.1601","longitude":4.4970}`,
		`{"satellite":"true","latitude":52.1601,"longitude":4.4970}`,
		`{"satellite":true,"latitude":NaN,"longitude":4.4970}`,
		`{"satellite":true,"latitude":52.1601,"longitude":4.49`,
	} {
		if w := post(t, s, "/api/sos/test", body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "does not decode") {
			t.Errorf("%s: %d %s, want 400", body, w.Code, w.Body.String())
		}
	}
	if n := deliveryCount(t, s); n != 0 {
		t.Fatalf("%d deliveries queued from bodies that do not decode", n)
	}
	for _, body := range []string{
		`{"message":"Test","latitude":NaN,"longitude":4.4970}`,
		`{"message":"Test","latitude":52.1601}`,
		`{"message":`,
	} {
		if w := post(t, s, "/api/sos/test", body); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "the Hub is not connected") {
			t.Errorf("Hub event %s: %d %s, want 503 the Hub is not connected", body, w.Code, w.Body.String())
		}
	}
}

// Without an Iridium gateway: 503, nothing queued. A Bridge without a
// delivery queue or gateways at all answers 503 as well.
func TestSOSTest_SatelliteNeedsAnIridiumGateway(t *testing.T) {
	s := sosSatServer(t, nil, nil) // a gateway manager with no satellite gateway
	w := post(t, s, "/api/sos/test", sosTestSatBody)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "no iridium gateway") {
		t.Fatalf("no gateway: %d %s, want 503", w.Code, w.Body.String())
	}
	if n := deliveryCount(t, s); n != 0 {
		t.Fatalf("%d deliveries queued without a gateway", n)
	}

	rec := httptest.NewRecorder()
	(&Server{}).handleSOSTest(rec, httptest.NewRequest(http.MethodPost, "/api/sos/test", strings.NewReader(sosTestSatBody)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no queue, no gateways: %d %s, want 503", rec.Code, rec.Body.String())
	}
}

// The SBD (9603) gateway carries the test whenever it runs, the IMT (9704)
// one only without it, as the mailbox check picks; the frame is the same.
func TestSOSTest_SatelliteOnTheIMTOnlyWithoutAnSBD(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sbd, imt *apiSat
		want     string
	}{
		{"SBD and IMT", &apiSat{kind: "sbd", connected: true}, &apiSat{kind: "imt", connected: true}, "iridium_0"},
		{"IMT only", nil, &apiSat{kind: "imt", connected: true}, "iridium_imt_0"},
	} {
		s := sosSatServer(t, tc.sbd, tc.imt)
		resp, del := postSatTest(t, s, sosTestSatBody)
		if resp.Interface != tc.want || del.Channel != tc.want {
			t.Fatalf("%s: answered %s, queued on %s, want %s", tc.name, resp.Interface, del.Channel, tc.want)
		}
		if got := hex.EncodeToString(del.Payload); got != androidTestFrame {
			t.Fatalf("%s: queued %s, want %s", tc.name, got, androidTestFrame)
		}
	}
}

// The frame names the bridge as the satellite fallback's frames do: the id
// set at start (main.go's hubBridgeID), whole, also past the 16 bytes
// Android cuts an id to (the kits' "nllei01tesseract01" is 18). Unset, the
// id the Hub reporter runs as; with neither, 503 and nothing queued. Each
// test is marked sent before the next call: while one is open, a call
// answers with it (TestSOSTest_ASecondCallAnswersWithTheOpenTest).
func TestSOSTest_SatelliteCarriesTheFallbacksBridgeID(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	for _, tc := range []struct{ set, reporter, want string }{
		{"nllei01tesseract01", "", "nllei01tesseract01"},
		{"nllei01tesseract01", "parallax", "nllei01tesseract01"},
		{"", "parallax", "parallax"},
	} {
		s.SetHubBridgeID(tc.set)
		s.hubReporter = nil
		if tc.reporter != "" {
			s.SetHubReporter(hubreporter.NewHubReporter(hubreporter.ReporterConfig{BridgeID: tc.reporter}, nil, nil))
		}
		resp, del := postSatTest(t, s, sosTestSatBody)
		_, payload, err := hubreporter.DecodeSatUplink(del.Payload)
		if err != nil {
			t.Fatal(err)
		}
		id, _, _, _, _, _, err := hubreporter.DecodeSatPosition(payload)
		if err != nil || id != tc.want || resp.BridgeID != tc.want || resp.Existing {
			t.Fatalf("set %q, reporter %q: the frame names %q (answer %q, existing %v), want %q (%v)", tc.set, tc.reporter, id, resp.BridgeID, resp.Existing, tc.want, err)
		}
		if err := s.db.SetDeliveryStatus(del.ID, "sent", "", ""); err != nil { // it went out
			t.Fatal(err)
		}
	}

	s.SetHubBridgeID("")
	s.hubReporter = nil
	before := deliveryCount(t, s)
	if w := post(t, s, "/api/sos/test", sosTestSatBody); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no bridge id: %d %s, want 503", w.Code, w.Body.String())
	}
	if n := deliveryCount(t, s); n != before {
		t.Fatalf("%d deliveries queued without a bridge id", n-before)
	}
}

// Without satellite, or with it false, the test is the Hub event as before
// (the Linux app 0.6.0 to 0.12.0 sends {message, latitude, longitude}): 503
// while the Hub is not connected, and nothing goes to the satellite although
// an Iridium gateway runs. [MESHSAT-1397]
func TestSOSTest_WithoutSatelliteIsTheHubEventOnly(t *testing.T) {
	s := sosSatServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	s.SetHubReporter(hubreporter.NewHubReporter(hubreporter.ReporterConfig{BridgeID: "msa-flaneur"}, nil, nil)) // never started: MQTT down
	for _, body := range []string{
		`{"message":"Test from Anna: checking the MeshSat alarm routes. No help needed.","latitude":52.1601,"longitude":4.4970}`,
		`{"message":"Test","latitude":52.1601,"longitude":4.4970,"altitude":3.9,"satellite":false}`,
		``,
	} {
		w := post(t, s, "/api/sos/test", body)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "the Hub is not connected") {
			t.Fatalf("%q: %d %s, want 503 the Hub is not connected", body, w.Code, w.Body.String())
		}
	}
	if n := deliveryCount(t, s); n != 0 {
		t.Fatalf("%d deliveries queued by the Hub event", n)
	}
}
