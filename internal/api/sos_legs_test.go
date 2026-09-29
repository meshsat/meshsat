package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
)

// sosMesh is a mesh radio for the SOS tests: present, and it records what
// it was given to send.
type sosMesh struct {
	transport.MeshTransport
	mu   sync.Mutex
	sent []string
}

func (m *sosMesh) SendMessage(_ context.Context, req transport.SendRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, req.Text)
	return nil
}

// sosLegsServer is sosSatServer with a mesh radio: the database, a delivery
// queue whose workers never start (the legs stay queued, nothing is sent),
// the gateways given, and the SOS looking for its modem every 20 ms.
func sosLegsServer(t *testing.T, sbd, imt *apiSat) *Server {
	t.Helper()
	s := sosSatServer(t, sbd, imt)
	s.mesh = &sosMesh{}
	old := sosFollowEvery
	sosFollowEvery = 20 * time.Millisecond
	t.Cleanup(func() { sosFollowEvery = old })
	return s
}

type sosStatusAnswer struct {
	Active  bool          `json:"active"`
	ID      int64         `json:"id"`
	Sends   int           `json:"sends"`
	Message string        `json:"message"`
	Legs    []sosLegState `json:"legs"`
	Skipped []string      `json:"skipped"`
}

func sosStatus(t *testing.T, s *Server) sosStatusAnswer {
	t.Helper()
	w := get(t, s, "/api/sos/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	var st sosStatusAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status %s: %v", w.Body.String(), err)
	}
	return st
}

// waitSOS polls the status until ready says so, for 5 s at most.
func waitSOS(t *testing.T, s *Server, what string, ready func(sosStatusAnswer) bool) sosStatusAnswer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := sosStatus(t, s)
		if ready(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: never came; the status is %+v", what, st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func legOf(st sosStatusAnswer, route string) *sosLegState {
	for i := range st.Legs {
		if st.Legs[i].Route == route {
			return &st.Legs[i]
		}
	}
	return nil
}

func queued(route string) func(sosStatusAnswer) bool {
	return func(st sosStatusAnswer) bool {
		leg := legOf(st, route)
		return leg != nil && leg.DeliveryID != 0
	}
}

const sosWords = "SOS: Kyriakos needs help. At 52.16010, 4.49700 (within 12 m) at 14:03 UTC."

// An SOS takes every route set up, each a delivery in the queue at
// precedence Override and priority 0, tried until it goes: the text
// broadcast on the mesh (class sos, as given), and the SOS frame to the
// Hub on the satellite (class hub_uplink): the frame the Hub raises the
// alarm from, with the person's position and Android's frame words. The
// Hub is not set up here, and the status says so. [MESHSAT-1446]
func TestSOS_EveryRouteSetUpIsAQueuedLeg(t *testing.T) {
	s := sosLegsServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	t.Cleanup(stopSOS(s))
	w := post(t, s, "/api/sos/activate", `{"trigger":"hold","message":"`+sosWords+`","latitude":52.1601,"longitude":4.497}`)
	if w.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", w.Code, w.Body.String())
	}
	st := waitSOS(t, s, "both legs queued", func(st sosStatusAnswer) bool { return queued("mesh")(st) && queued("satellite")(st) })
	if !st.Active || st.Message != sosWords || st.Sends != 0 {
		t.Fatalf("status %+v", st)
	}
	if len(st.Skipped) != 1 || st.Skipped[0] != "Hub: not set up on this Bridge." {
		t.Fatalf("skipped %q", st.Skipped)
	}

	mesh := legOf(st, "mesh")
	del, err := s.db.GetDelivery(mesh.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if del.Channel != "mesh_0" || del.Class != database.DeliveryClassSOS || del.Priority != 0 || del.Precedence != "Override" ||
		del.MaxRetries != 0 || string(del.Payload) != sosWords || del.TextPreview != sosWords || mesh.Status != "queued" ||
		del.MsgRef != "sos-"+sosItoa(st.ID)+"-mesh" || del.ExpiresAt != nil {
		t.Fatalf("mesh leg: %+v (status %s)", del, mesh.Status)
	}

	sat := legOf(st, "satellite")
	del, err = s.db.GetDelivery(sat.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if del.Channel != "iridium_0" || del.Class != database.DeliveryClassHubUplink || del.Priority != 0 || del.Precedence != "Override" ||
		del.MaxRetries != 0 || del.MsgRef != "sos-"+sosItoa(st.ID)+"-satellite" || sat.Interface != "iridium_0" || del.ExpiresAt != nil {
		t.Fatalf("satellite leg: %+v (%+v)", del, sat)
	}
	header, payload, err := hubreporter.DecodeSatUplink(del.Payload)
	if err != nil || header.MsgType != hubreporter.SatMsgSOS {
		t.Fatalf("the satellite leg is not an SOS frame: %v", err)
	}
	bridge, device, lat, lon, words, ts, err := hubreporter.DecodeSatSOS(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bridge != "msa-flaneur" || device != "bridge" || math.Abs(lat-52.1601) > 1e-4 || math.Abs(lon-4.497) > 1e-4 ||
		words != "SOS: Kyriakos needs help" || ts.Unix() != st.ID/1000 {
		t.Fatalf("frame: %s %s %v %v %q %v", bridge, device, lat, lon, words, ts)
	}
}

// The satellite leg goes on the modem this Bridge has: a kit with a 9704
// only queues its frame on the IMT link, and the Hub's alert id names that
// satellite. The burst it replaces sent nothing by satellite there (it
// looked for gateways of type "iridium" only). [MESHSAT-1440, MESHSAT-1446]
func TestSOS_TheSatelliteLegGoesOnThe9704(t *testing.T) {
	s := sosLegsServer(t, nil, &apiSat{kind: "imt", connected: true})
	t.Cleanup(stopSOS(s))
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	st := waitSOS(t, s, "the satellite leg queued", queued("satellite"))
	if leg := legOf(st, "satellite"); leg.Interface != "iridium_imt_0" {
		t.Fatalf("the satellite leg waits on %q", leg.Interface)
	}
	run, _, _ := s.sosSnapshot(st.ID)
	alert, _ := s.sosHubAlert(run, false)
	if want := "sos-imt-msa-flaneur-" + sosItoa(st.ID/1000); alert.ID != want {
		t.Fatalf("the Hub's alert id %q, want %q", alert.ID, want)
	}
}

// A satellite leg the caller names waits for a modem that is not there
// yet: the status says "waiting", with no delivery, and the frame is
// queued as soon as a modem runs. Without a named route a Bridge that
// never had a modem skips the satellite. [MESHSAT-1446]
func TestSOS_ANamedSatelliteLegWaitsForAModem(t *testing.T) {
	modem := &apiSat{kind: "sbd", connected: true}
	s := newTestServerWithDB(t)
	mgr := gateway.NewManager(s.db, modem)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mgr.Stop()
		cancel()
	})
	s.gwManager = mgr
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	s.SetDispatcher(engine.NewDispatcher(s.db, reg, mgr, nil))
	s.SetHubBridgeID("msa-flaneur")
	s.mesh = &sosMesh{}
	old := sosFollowEvery
	sosFollowEvery = 20 * time.Millisecond
	t.Cleanup(func() { sosFollowEvery = old })
	t.Cleanup(stopSOS(s))
	w := post(t, s, "/api/sos/activate", `{"trigger":"hold","message":"`+sosWords+`","routes":["mesh","satellite"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", w.Code, w.Body.String())
	}
	st := waitSOS(t, s, "the mesh leg queued", queued("mesh"))
	sat := legOf(st, "satellite")
	if sat == nil || sat.Status != "waiting" || sat.DeliveryID != 0 || len(st.Skipped) != 0 {
		t.Fatalf("status %+v", st)
	}

	if err := mgr.ConfigureInstance(ctx, "iridium", "iridium_0", true, `{"mailbox_mode":"off","auto_receive":false}`); err != nil {
		t.Fatal(err)
	}
	st = waitSOS(t, s, "the satellite leg queued once the modem runs", queued("satellite"))
	if leg := legOf(st, "satellite"); leg.Interface != "iridium_0" || leg.Status != "queued" {
		t.Fatalf("satellite leg %+v", leg)
	}

	other := sosLegsServer(t, nil, nil)
	t.Cleanup(stopSOS(other))
	if !other.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	st = waitSOS(t, other, "the mesh leg queued", queued("mesh"))
	if legOf(st, "satellite") != nil || !containsText(st.Skipped, "Satellite: no satellite modem has been connected to this Bridge.") {
		t.Fatalf("without a modem: %+v", st)
	}
}

func containsText(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Cancelling stops every leg that has not gone out, where it waits, and a
// mesh leg that went out gets the cancellation, a leg of its own that the
// answer names. The send gate then refuses the SOS's legs (one being sent
// when it was cancelled comes back as a retry and is never sent again) and
// lets the cancellation and everything else through. [MESHSAT-1446]
func TestSOS_CancelStopsTheWaitingLegsAndTellsTheMesh(t *testing.T) {
	s := sosLegsServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	t.Cleanup(stopSOS(s))
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	st := waitSOS(t, s, "both legs queued", func(st sosStatusAnswer) bool { return queued("mesh")(st) && queued("satellite")(st) })
	mesh, sat := legOf(st, "mesh"), legOf(st, "satellite")
	setRow(t, s, mesh.DeliveryID, `status = 'sent'`)
	st = sosStatus(t, s)
	if st.Sends != 1 {
		t.Fatalf("sends %d after the mesh leg went", st.Sends)
	}

	w := post(t, s, "/api/sos/cancel", `{"message":"Alarm cancelled: Kyriakos is safe and needs no help now."}`)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	var answer struct {
		Status     string   `json:"status"`
		ID         int64    `json:"id"`
		CancelLegs []sosLeg `json:"cancel_legs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Status != "cancelled" || answer.ID != st.ID || len(answer.CancelLegs) != 1 || answer.CancelLegs[0].Route != "mesh" ||
		answer.CancelLegs[0].MsgRef != "sos-"+sosItoa(st.ID)+"-cancel:mesh" {
		t.Fatalf("cancel answer %s", w.Body.String())
	}
	if status, lastErr := rowStatus(t, s, sat.DeliveryID); status != "dead" || lastErr != "cancelled" {
		t.Fatalf("the waiting satellite leg is %s %q", status, lastErr)
	}
	cancelRow, err := s.db.GetDelivery(answer.CancelLegs[0].DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelRow.Channel != "mesh_0" || cancelRow.TextPreview != "Alarm cancelled: Kyriakos is safe and needs no help now." ||
		cancelRow.Class != database.DeliveryClassSOS || cancelRow.Priority != 0 || cancelRow.Status != "queued" {
		t.Fatalf("the cancellation: %+v", cancelRow)
	}
	if st := sosStatus(t, s); st.Active {
		t.Fatal("still active after the cancel")
	}

	// The gate
	satRow, _ := s.db.GetDelivery(sat.DeliveryID)
	if s.sosMayDeliver(*satRow) {
		t.Fatal("the gate lets a leg of the cancelled SOS go")
	}
	if !s.sosMayDeliver(*cancelRow) {
		t.Fatal("the gate holds the cancellation back")
	}
	if !s.sosMayDeliver(database.MessageDelivery{MsgRef: "20260929-214902-91805"}) {
		t.Fatal("the gate holds an ordinary delivery back")
	}

	// A new SOS: its legs go, the old one's never.
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("a new SOS after the cancel was refused")
	}
	st2 := waitSOS(t, s, "the new SOS's mesh leg", queued("mesh"))
	if st2.ID <= st.ID {
		t.Fatalf("the new SOS's id %d is not after %d", st2.ID, st.ID)
	}
	newRow, _ := s.db.GetDelivery(legOf(st2, "mesh").DeliveryID)
	if !s.sosMayDeliver(*newRow) || s.sosMayDeliver(*satRow) {
		t.Fatal("the gate mixes the two SOSs up")
	}
}

// Words the Hub would raise an alarm for are refused as a cancellation, and
// the SOS stays on.
func TestSOSCancel_RefusesAlarmWords(t *testing.T) {
	s := sosLegsServer(t, nil, nil)
	t.Cleanup(stopSOS(s))
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	w := post(t, s, "/api/sos/cancel", `{"message":"SOS cancelled, no emergency"}`)
	if w.Code != http.StatusBadRequest || !s.sosActive() {
		t.Fatalf("cancel with alarm words: %d %s, active %v", w.Code, w.Body.String(), s.sosActive())
	}
	if w := post(t, s, "/api/sos/cancel", ""); w.Code != http.StatusOK || s.sosActive() {
		t.Fatalf("cancel without words: %d %s", w.Code, w.Body.String())
	}
	run, _, _ := s.sosSnapshot(sosStatusID(s))
	if run.CancelText != sosCancelDefaultText || sosAlarmWord(sosCancelDefaultText) {
		t.Fatalf("the default cancellation %q", run.CancelText)
	}
}

func sosStatusID(s *Server) int64 {
	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()
	if s.sos.run == nil {
		return 0
	}
	return s.sos.run.ID
}

// A restart neither ends the SOS nor loses its legs: a new server on the
// same database has the SOS on, with its legs, and its gate lets them go.
// Cancelled there, the legs stop. [MESHSAT-1446]
func TestSOS_ARestartKeepsTheSOS(t *testing.T) {
	s := sosLegsServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	t.Cleanup(stopSOS(s))
	if !s.TriggerSOSWithText("hold", sosWords) {
		t.Fatal("refused")
	}
	st := waitSOS(t, s, "both legs queued", func(st sosStatusAnswer) bool { return queued("mesh")(st) && queued("satellite")(st) })
	stopSOS(s)()

	after := &Server{db: s.db, mesh: &sosMesh{}, gwManager: s.gwManager}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	after.SetDispatcher(engine.NewDispatcher(after.db, reg, after.gwManager, nil))
	after.SetHubBridgeID("msa-flaneur")
	after.RestoreSOS()
	t.Cleanup(stopSOS(after))
	// A second RestoreSOS starts no second worker: the first one's stop is kept.
	calls := 0
	after.sos.mu.Lock()
	first := after.sos.cancelFn
	after.sos.cancelFn = func() { calls++; first() }
	after.sos.mu.Unlock()
	after.RestoreSOS()
	after.sos.mu.Lock()
	after.sos.cancelFn()
	after.sos.mu.Unlock()
	if calls != 1 {
		t.Fatal("a second RestoreSOS replaced the worker's stop")
	}
	st2 := sosStatus(t, after)
	if !st2.Active || st2.ID != st.ID || st2.Message != sosWords || len(st2.Legs) != 2 {
		t.Fatalf("after the restart: %+v", st2)
	}
	sat, _ := after.db.GetDelivery(legOf(st2, "satellite").DeliveryID)
	if !after.sosMayDeliver(*sat) {
		t.Fatal("after the restart the gate holds the SOS's leg back")
	}
	if after.TriggerSOS("deadman") {
		t.Fatal("a second SOS started on top of the restored one")
	}
	if w := post(t, after, "/api/sos/cancel", ""); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if status, _ := rowStatus(t, after, sat.ID); status != "dead" {
		t.Fatalf("the satellite leg after the cancel: %s", status)
	}
}

// The activation's form: routes by name only, a position as both
// coordinates within range.
func TestSOSActivate_RefusesABadForm(t *testing.T) {
	s := sosLegsServer(t, nil, nil)
	t.Cleanup(stopSOS(s))
	for _, body := range []string{
		`{"routes":["mesh","sms"]}`,
		`{"latitude":52.1}`,
		`{"latitude":95,"longitude":4}`,
	} {
		if w := post(t, s, "/api/sos/activate", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, w.Code, w.Body.String())
		}
	}
	if s.sosActive() {
		t.Fatal("a refused activation started an SOS")
	}
	// A body that does not decode still starts the SOS, with the defaults.
	if w := post(t, s, "/api/sos/activate", `{"trigger":`); w.Code != http.StatusOK || !s.sosActive() {
		t.Fatalf("a cut-off body: %d %s", w.Code, w.Body.String())
	}
}

// The words the SOS frame carries: Android's frameMessage, the first
// sentence, ", position unknown" without a position, 64 bytes at most and
// no character cut in two.
func TestSOSFrameMessage(t *testing.T) {
	for _, tc := range []struct {
		text  string
		known bool
		want  string
	}{
		{sosWords, true, "SOS: Kyriakos needs help"},
		{"SOS: Kyriakos needs help. Position unknown.", false, "SOS: Kyriakos needs help, position unknown"},
		{sosDefaultText, false, "SOS - EMERGENCY ALERT - Requesting immediate assistance, positio"},
		{"SOS: Χρυσάνθη Παπαδοπούλου-Καραγιαννοπούλου needs help. At 1, 2.", true, "SOS: Χρυσάνθη Παπαδοπούλου-Καραγιαν"},
	} {
		got := sosFrameMessage(tc.text, tc.known)
		if got != tc.want {
			t.Errorf("%q: %q, want %q", tc.text, got, tc.want)
		}
		if len(got) > 64 || !utf8Valid(got) {
			t.Errorf("%q: %d bytes, valid %v", got, len(got), utf8Valid(got))
		}
	}
}

// The Hub leg: the alert the Hub's detector pages on, with the id the Hub
// files the satellite frame under, and its cancellation.
func TestSOS_TheHubAlert(t *testing.T) {
	s := sosLegsServer(t, &apiSat{kind: "sbd", connected: true}, nil)
	run := sosRun{ID: 1790000000123, Text: sosWords, Bearer: "sbd", HasPosition: true, Lat: 52.1601, Lon: 4.497,
		CancelText: "Alarm cancelled: Kyriakos is safe and needs no help now."}
	alert, event := s.sosHubAlert(run, false)
	if alert.ID != "sos-sbd-msa-flaneur-1790000000" || alert.IMEI != "msa-flaneur" || !alert.SOS || alert.Text != sosWords ||
		alert.Channel != "mqtt" || alert.Source != "bridge_sos" || alert.Lat == nil || *alert.Lat != 52.1601 ||
		event.Type != "triggered" || event.DeviceID != "msa-flaneur" || event.Message != sosWords {
		t.Fatalf("alert %+v event %+v", alert, event)
	}
	alert, event = s.sosHubAlert(run, true)
	if alert.ID != "sos-sbd-msa-flaneur-1790000000-cancelled" || alert.SOS || alert.Text != run.CancelText ||
		event.Type != "cancelled" || sosAlarmWord(alert.Text) {
		t.Fatalf("cancellation %+v %+v", alert, event)
	}
}

func TestParseSOSRef(t *testing.T) {
	for _, tc := range []struct {
		ref        string
		id         int64
		cancel, ok bool
	}{
		{"sos-1790000000123-mesh", 1790000000123, false, true},
		{"sos-1790000000123-cancel:mesh", 1790000000123, true, true},
		{"sos-1790000000123-hubsms", 1790000000123, false, true},
		{"20260929-214902-91805", 0, false, false},
		{"sos-x-mesh", 0, false, false},
		{"sos-", 0, false, false},
	} {
		id, cancel, ok := parseSOSRef(tc.ref)
		if id != tc.id || cancel != tc.cancel || ok != tc.ok {
			t.Errorf("%q: %d %v %v", tc.ref, id, cancel, ok)
		}
	}
}

func sosItoa(n int64) string { return strconv.FormatInt(n, 10) }

func utf8Valid(s string) bool { return utf8.ValidString(s) }

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}
