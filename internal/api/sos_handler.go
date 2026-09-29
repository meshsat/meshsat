package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
	"meshsat/internal/types"
)

// SOSState tracks an active SOS alert.
type SOSState struct {
	mu       sync.Mutex
	active   bool
	startAt  time.Time
	cancelFn context.CancelFunc
	sends    int
	// text is what goes out on every route: the caller's words (the apps send
	// SosMessages' sentence with the person's name and position), or the fixed
	// sentence below when the caller gave none. [MESHSAT-1397]
	text    string
	trigger string
}

// sosDefaultText is the SOS text when the caller gives none.
const sosDefaultText = "SOS - EMERGENCY ALERT - Requesting immediate assistance"

// @Summary Activate SOS alert
// @Description Triggers an SOS emergency alert that sends via mesh and satellite (3x at 30s intervals).
// @Description When the TAK gateway runs and this Bridge knows its position, the SOS also goes to TAK
// @Description as a CoT emergency (a-f-G-U-C with a 911 Alert), once, through the gateway's outputs.
// @Tags sos
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 409 {object} map[string]string "already active"
// @Router /api/sos/activate [post]
func (s *Server) handleSOSActivate(w http.ResponseWriter, r *http.Request) {
	// Best-effort trigger capture so the signed audit-log entry can
	// record whether the activation came from the 3-s hold, the
	// double-tap, or an external caller (CLI, TAK, HeMB). Unknown =
	// "manual". [MESHSAT-562]. The message is the caller's words for every
	// route (the apps' "SOS: <name> needs help. At <position> at <time>.");
	// without one the fixed sentence goes. [MESHSAT-1397]
	var body struct {
		Trigger string `json:"trigger,omitempty"`
		Message string `json:"message,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	trigger := body.Trigger
	if trigger == "" {
		trigger = "manual"
	}

	s.touchOperatorActivity()

	if !s.TriggerSOSWithText(trigger, sosTextOf(body.Message)) {
		writeJSON(w, http.StatusConflict, map[string]string{"status": "already_active"})
		return
	}

	s.sos.mu.Lock()
	startedAt := s.sos.startAt
	s.sos.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "activated",
		"started_at": startedAt.UTC().Format(time.RFC3339),
		"trigger":    trigger,
	})
}

// TriggerSOS starts the SOS burst and reports whether it did. It is the single
// way an SOS begins: the button on the dashboard and the dead man's switch both
// arrive here, so both get the already-active guard and the signed audit entry.
//
// Before MESHSAT-996 the guard and the audit entry lived in the HTTP handler and
// the burst lived in sosWorker, so anything that reached sosWorker directly ran
// without either. A dead man's switch wired straight to the worker could have
// started a second burst on top of a manual one and corrupted the send counter.
//
// Returns false when an SOS is already running, in which case nothing is
// started and the existing burst continues.
func (s *Server) TriggerSOS(trigger string) bool {
	return s.TriggerSOSWithText(trigger, sosDefaultText)
}

// sosTextOf is the text an SOS goes out with: the caller's, trimmed and capped
// at what one mesh packet carries, or the fixed sentence. [MESHSAT-1397]
func sosTextOf(message string) string {
	text := strings.TrimSpace(message)
	if text == "" {
		return sosDefaultText
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// TriggerSOSWithText is TriggerSOS with the words that go out on every route.
func (s *Server) TriggerSOSWithText(trigger, text string) bool {
	if text == "" {
		text = sosDefaultText
	}

	s.sos.mu.Lock()
	if s.sos.active {
		s.sos.mu.Unlock()
		log.Warn().Str("trigger", trigger).Msg("SOS requested while one is already active, ignoring")
		return false
	}
	s.sos.active = true
	s.sos.startAt = time.Now()
	s.sos.sends = 0
	s.sos.text = text
	s.sos.trigger = trigger
	ctx, cancel := context.WithCancel(context.Background())
	s.sos.cancelFn = cancel
	startedAt := s.sos.startAt
	s.sos.mu.Unlock()

	// Immutable audit-log entry — proves the SOS started at this moment and
	// what started it. Hash-chained by SigningService.
	if s.signing != nil {
		detail, _ := json.Marshal(map[string]interface{}{
			"trigger":    trigger,
			"started_at": startedAt.UTC().Format(time.RFC3339),
			"message":    text,
		})
		s.signing.AuditEvent("sos_activated", nil, nil, nil, nil, string(detail))
	}

	go s.sosWorker(ctx, text)

	// TAK clients see the alarm as well, as on MeshSat Android
	// (SosController). The dead man's switch comes through here too. [MESHSAT-1421]
	s.takOwnSOS(text)

	// The SOS replaces a test of the alarm that is still waiting, as on
	// MeshSat Android; after the legs above have started, so it never
	// holds them up. [MESHSAT-1430]
	s.cancelSatelliteTests()

	log.Warn().Str("trigger", trigger).Str("text", text).Msg("SOS ACTIVATED")
	return true
}

// sosActive reports whether an SOS is running.
func (s *Server) sosActive() bool {
	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()
	return s.sos.active
}

// cancelSatelliteTests cancels the alarm test's satellite leg wherever it
// has not gone out (queued, waiting for a retry, or held), on every link,
// as MeshSat Android's SosController.start does when a real SOS replaces a
// running test: during an emergency the test must neither spend a credit
// nor put a test position on the Hub. The rows end cancelled (dead,
// "cancelled"), by the queue's own cancel. A test on the modem at that
// moment cannot be stopped, but gets a deadline of now: if that send fails
// it is not tried again (it came back through the retry path minutes later,
// in the middle of the emergency). [MESHSAT-1430]
func (s *Server) cancelSatelliteTests() {
	if s.db == nil {
		return
	}
	s.sosTestMu.Lock()
	defer s.sosTestMu.Unlock()
	ids, err := s.db.WaitingDeliveryIDs(database.DeliveryClassHubUplink, sosTestSatellitePreview)
	if err != nil {
		log.Error().Err(err).Msg("SOS: could not look for a waiting alarm test")
	}
	for _, id := range ids {
		if err := s.db.CancelDelivery(id); err != nil {
			log.Warn().Err(err).Int64("delivery_id", id).Msg("SOS: the alarm test's satellite leg could not be cancelled")
			continue
		}
		log.Warn().Int64("delivery_id", id).Msg("SOS: the alarm test's satellite leg is cancelled, the SOS replaces it")
	}
	if n, err := s.db.EndSendingDeliveries(database.DeliveryClassHubUplink, sosTestSatellitePreview); err != nil {
		log.Error().Err(err).Msg("SOS: could not end the alarm test being sent")
	} else if n > 0 {
		log.Warn().Int64("deliveries", n).Msg("SOS: the alarm test's satellite leg is on the modem; if that send fails it is not tried again")
	}
}

// sosTestSatellitePreview is the alarm test's satellite leg in the delivery
// queue: the words MeshSat Android queues it with. [MESHSAT-1430]
const sosTestSatellitePreview = "Alarm test: position report to the Hub"

// satSourceGPS is the position frame's source byte for GPS, the one the
// satellite fallback writes for a GPS fix and MeshSat Android's positionFrame
// always writes. [MESHSAT-1430]
const satSourceGPS byte = 1

// sosTestSatelliteTTL is how long the alarm test's satellite leg may wait
// to go out. MeshSat Android stops a test after 30 minutes (SosController
// TEST_LIMIT_MS) and cancels what still waits; a frame sent later would
// spend a credit and put an old position on the Hub. [MESHSAT-1430]
const sosTestSatelliteTTL = 30 * time.Minute

// sosTestBody is what POST /api/sos/test takes. Satellite picks the leg: the
// Hub's MQTT link without it, the satellite with it. Latitude and longitude
// are pointers so the satellite leg can tell a coordinate left out from a
// coordinate of 0. [MESHSAT-1430]
type sosTestBody struct {
	Message   string   `json:"message,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Altitude  float64  `json:"altitude,omitempty"`
	Satellite bool     `json:"satellite,omitempty"`
}

// handleSOSTest tests one of the alarm routes to the Hub, as MeshSat Android
// does, one leg per call.
//
// Without satellite (what the apps sent before MESHSAT-1430): the test event
// on the device's sos topic only (HubReporter.publishSos with type "test"):
// the Hub's live map shows it, its SOS detector never sees it, so nobody is
// paged. [MESHSAT-1397] This leg reads its body as it always has: none, or
// one that does not decode, sends the default test.
//
// With satellite true: the satellite leg, a position report to the Hub
// (sosTestSatellite). It does not need the Hub's MQTT link: a phone with a
// modem and no internet still tests its satellite leg. The mesh and SMS legs
// of a test are the apps' own ordinary sends. [MESHSAT-1430]
// @Summary Test an alarm route to the Hub
// @Description One leg per call. Without "satellite" (or false): publishes a test event on the device's SOS topic over the Hub's MQTT link; the Hub shows it and raises no alarm. The position is latitude and longitude as given, or this Bridge's GPS fix when both are 0 or left out. 503 when the Hub is not connected, 502 when it did not take the event.
// @Description With "satellite": true: queues the satellite leg, a position report to the Hub as MeshSat Android sends it: the uplink frame of type 0x01 (magic "MS", the bridge id this Bridge reports to the Hub as, latitude and longitude as float32, altitude in whole metres cut toward zero and held to int16, source 1 = GPS, the time), on the Iridium link (the 9603 SBD gateway, else the 9704 IMT one), text preview "Alarm test: position report to the Hub". It goes as a Hub uplink frame: no egress rules, no transform chain, no encryption, so the Hub's uplink decoder reads the frame as it is and it never reaches the Hub's routing engine. It does not need the Hub's MQTT link.
// @Description It is queued at precedence Routine, priority 1, under the SBD credit budget, below everything an SOS queues, and with a 30 minute deadline: a test still waiting then expires and never goes out (one held while the link is down expires when the link is back; the hold does not stop the clock). The position is latitude and longitude, both given (0,0 included), or this Bridge's GPS fix when both are left out (the altitude then from the fix as well); one without the other is 400.
// @Description While a test is open on that link (queued, retrying, held or being sent) another call queues nothing and answers with that one ("existing": true, and its own status), so a retry after a lost answer costs no second credit. 409 while an SOS is active: the SOS replaces its test, and starting an SOS cancels a test that has not gone out. 400 for a body that names satellite and does not decode (a field of the wrong type, NaN, a cut-off body), without a position, or with one out of range; 503 without an Iridium gateway. GET /api/deliveries/{id} follows it (status, ack_status, expires_at).
// @Tags sos
// @Accept json
// @Produce json
// @Param body body object{message=string,latitude=number,longitude=number,altitude=number,satellite=bool} false "The test's text (Hub event only), where it is from, its altitude in metres (satellite leg only), and the leg: satellite true for the satellite leg"
// @Success 200 {object} map[string]interface{} "Hub event: status sent, message, sent_at. Satellite leg: status (queued, or the open test's own status), existing, delivery_id, msg_ref, message (the preview), interface, bridge_id"
// @Failure 400 {object} map[string]string "satellite leg: a body naming satellite that does not decode, latitude without longitude or the reverse, no position, or one out of range"
// @Failure 409 {object} map[string]string "satellite leg: an SOS is active"
// @Failure 500 {object} map[string]string "satellite leg: the delivery queue did not take it"
// @Failure 502 {object} map[string]string "Hub event: the Hub did not take it"
// @Failure 503 {object} map[string]string "Hub event: the Hub is not connected. Satellite leg: no Iridium gateway runs, or no bridge id"
// @Router /api/sos/test [post]
func (s *Server) handleSOSTest(w http.ResponseWriter, r *http.Request) {
	// The body is kept as it came, so that one which does not decode can
	// still be told apart: a body that names "satellite" is the satellite
	// leg's, refused rather than guessed at (a field of the wrong type, a NaN
	// from a client's float formatter, a cut-off body), where it used to
	// fall through to the Hub event. Any other is the Hub event's, read as
	// leniently as ever. [MESHSAT-1430]
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var body sosTestBody
	err := json.NewDecoder(bytes.NewReader(raw)).Decode(&body)
	if errors.Is(err, io.EOF) {
		err = nil // no body: the Hub event with its defaults, as ever
	}
	if err != nil && (body.Satellite || bytes.Contains(raw, []byte(`"satellite"`))) {
		writeError(w, http.StatusBadRequest, "the body does not decode: "+err.Error())
		return
	}
	if body.Satellite {
		s.sosTestSatellite(w, body)
		return
	}
	text := strings.TrimSpace(body.Message)
	if text == "" {
		text = "Test: checking the MeshSat alarm routes. No help needed."
	}
	if s.hubReporter == nil || !s.hubReporter.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "the Hub is not connected")
		return
	}
	// Where the test is from, as before MESHSAT-1430: the coordinates as
	// given, one of them 0 or left out included, or the GPS fix when both
	// are 0 or left out.
	var lat, lon float64
	if body.Latitude != nil {
		lat = *body.Latitude
	}
	if body.Longitude != nil {
		lon = *body.Longitude
	}
	if lat == 0 && lon == 0 && s.gpsReader != nil {
		if st := s.gpsReader.GetStatus(); st.Fix {
			lat, lon = st.Lat, st.Lon
		}
	}
	event := hubreporter.DeviceSOS{DeviceID: "bridge", Type: "test", Message: text, Lat: lat, Lon: lon, Timestamp: time.Now().UTC()}
	if err := s.hubReporter.PublishDeviceSOS(event); err != nil {
		writeError(w, http.StatusBadGateway, "the Hub did not take the test event: "+err.Error())
		return
	}
	if s.signing != nil {
		detail, _ := json.Marshal(map[string]interface{}{"message": text})
		s.signing.AuditEvent("sos_test", nil, nil, nil, nil, string(detail))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "sent", "message": text, "sent_at": event.Timestamp.Format(time.RFC3339)})
}

// satelliteTestInterface is the link the alarm test's satellite leg goes on:
// the SBD (9603) gateway's whenever it runs, else the IMT (9704) one's, ""
// without either, as the mailbox check picks. It asks the gateway manager,
// under the manager's lock. [MESHSAT-1430]
func (s *Server) satelliteTestInterface() string {
	if s.satTestIfaceFn != nil {
		return s.satTestIfaceFn()
	}
	ifaceID := s.gwManager.ResolveGatewayInterface("iridium")
	if ifaceID == "" {
		ifaceID = s.gwManager.ResolveGatewayInterface("iridium_imt")
	}
	return ifaceID
}

// sosTestSatellitePosition is where the satellite leg's frame says the test
// is from: latitude and longitude when both are given (0,0 included, a
// position the caller states), or this Bridge's GPS fix when both are left
// out (the altitude then from the fix too; a fix at exactly 0,0 counts as
// none). problem is why there is no position to send, "" when there is one.
// [MESHSAT-1430]
func (s *Server) sosTestSatellitePosition(body sosTestBody) (lat, lon, alt float64, problem string) {
	switch {
	case body.Latitude != nil && body.Longitude != nil:
		lat, lon, alt = *body.Latitude, *body.Longitude, body.Altitude
	case body.Latitude != nil || body.Longitude != nil:
		return 0, 0, 0, "send latitude and longitude together, or neither for this Bridge's GPS fix"
	default:
		var st transport.GPSStatus
		if s.gpsReader != nil {
			st = s.gpsReader.GetStatus()
		}
		if !st.Fix || (st.Lat == 0 && st.Lon == 0) {
			return 0, 0, 0, "no position: send latitude and longitude (this Bridge has no GPS fix)"
		}
		lat, lon, alt = st.Lat, st.Lon, st.AltM
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, 0, "latitude must be -90 to 90 and longitude -180 to 180"
	}
	return lat, lon, alt, ""
}

// sosTestSatellite queues the alarm test's satellite leg: a position report to
// the Hub, as MeshSat Android sends it (SosController: positionFrame on
// iridium_0, "Alarm test: position report to the Hub"). The frame crosses the
// modem, Rock7 or Cloudloop and the Hub's uplink decoder as the SOS frame
// does, and never reaches the Hub's routing engine, where a text could be
// relayed to real phones. The Hub publishes the position on the bridge's
// position topic and marks the bridge seen; it makes the frame the bridge's
// "last report" only while the bridge is not live over MQTT (an MQTT report
// in the last 10 minutes keeps the Fleet page on MQTT). The Linux app sent a
// text by POST /api/messages/send {gateway: iridium} until now, for want of
// this.
//
// Class hub_uplink, as the satellite fallback's frames: no egress rules, no
// transform chain, no encryption; the satellite gateway sends the bytes as
// they are. The bridge id is the fallback's too, so the Hub finds the bridge.
// The SBD (9603) gateway is used whenever it runs, the IMT (9704) one only
// without it, as the mailbox check picks.
//
// Android queues it at priority 0, as its SOS legs. Here it goes at Routine,
// priority 1, under the credit budget: at priority 0 and Priority precedence
// it sorted before the SOS's own Hub frame (Priority, 1 then) and could never
// be evicted, so a test queued minutes before an SOS would have gone first.
// It waits 30 minutes at most, is refused while an SOS runs, is cancelled by
// an SOS that starts while it waits, and a second call while one is open
// answers with the open one. [MESHSAT-1430]
func (s *Server) sosTestSatellite(w http.ResponseWriter, body sosTestBody) {
	// A first look, so a test during an SOS never waits on the gateway
	// manager; the one that counts is under sosTestMu below.
	if s.sosActive() {
		writeError(w, http.StatusConflict, "an SOS is active: the SOS replaces its test")
		return
	}
	if s.dispatcher == nil || s.gwManager == nil || s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "no iridium gateway: the delivery queue or the gateways are not running")
		return
	}
	// The link, the position and the frame are worked out before the lock:
	// the gateway lookup takes the gateway manager's lock, which a gateway
	// being stopped can hold for a whole modem session, and an SOS starting
	// meanwhile must not wait for it (cancelSatelliteTests takes sosTestMu).
	ifaceID := s.satelliteTestInterface()
	if ifaceID == "" {
		writeError(w, http.StatusServiceUnavailable, "no iridium gateway running")
		return
	}
	lat, lon, alt, problem := s.sosTestSatellitePosition(body)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	bridgeID := s.uplinkBridgeID()
	var frame []byte
	if bridgeID != "" {
		frame = hubreporter.EncodeSatPosition(bridgeID, lat, lon, float32(alt), satSourceGPS, s.clockNow().UTC())
	}

	// Under sosTestMu only what must not interleave with the start of an
	// SOS or with a second test: the SOS check, the look for a test still
	// open on this link (a second call, or a client's retry after a lost
	// answer, spends no second credit) and the queueing.
	var (
		inSOS             bool
		open              *database.MessageDelivery
		lookErr, queueErr error
		delID             int64
		msgRef            string
	)
	s.sosTestMu.Lock()
	if inSOS = s.sosActive(); !inSOS {
		open, lookErr = s.db.OpenDeliveryByPreview(ifaceID, database.DeliveryClassHubUplink, sosTestSatellitePreview)
		if lookErr == nil && open == nil && frame != nil {
			delID, msgRef, queueErr = s.dispatcher.QueueDirectSendTo(ifaceID, sosTestSatellitePreview, engine.DirectSendOptions{
				Precedence: string(types.PrecedenceRoutine),
				Class:      database.DeliveryClassHubUplink,
				Payload:    frame,
				TTLSeconds: int(sosTestSatelliteTTL / time.Second),
			})
		}
	}
	s.sosTestMu.Unlock()

	switch {
	case inSOS:
		writeError(w, http.StatusConflict, "an SOS is active: the SOS replaces its test")
		return
	case lookErr != nil:
		writeError(w, http.StatusInternalServerError, "could not look for an open test: "+lookErr.Error())
		return
	case open != nil:
		log.Info().Str("interface", ifaceID).Int64("delivery_id", open.ID).Str("status", open.Status).
			Msg("alarm test: a position report to the Hub is already open on the satellite, answering with it")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":      open.Status,
			"existing":    true,
			"delivery_id": open.ID,
			"msg_ref":     open.MsgRef,
			"message":     sosTestSatellitePreview,
			"interface":   open.Channel,
			"bridge_id":   sosTestFrameBridgeID(open.Payload),
		})
		return
	case frame == nil:
		writeError(w, http.StatusServiceUnavailable, "this Bridge has no bridge id to name itself by to the Hub")
		return
	case queueErr != nil:
		writeError(w, http.StatusInternalServerError, "queue failed: "+queueErr.Error())
		return
	}
	if s.signing != nil {
		detail, _ := json.Marshal(map[string]interface{}{"leg": "satellite", "message": sosTestSatellitePreview, "msg_ref": msgRef, "bytes": len(frame)})
		dir := "egress"
		s.signing.AuditEvent("sos_test", &ifaceID, &dir, &delID, nil, string(detail))
	}
	log.Info().Str("interface", ifaceID).Int64("delivery_id", delID).Str("bridge_id", bridgeID).Int("bytes", len(frame)).
		Msg("alarm test: position report to the Hub queued for the satellite")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "queued",
		"existing":    false,
		"delivery_id": delID,
		"msg_ref":     msgRef,
		"message":     sosTestSatellitePreview,
		"interface":   ifaceID,
		"bridge_id":   bridgeID,
	})
}

// sosTestFrameBridgeID is the bridge id inside a queued position frame, ""
// when the bytes are not one.
func sosTestFrameBridgeID(frame []byte) string {
	header, payload, err := hubreporter.DecodeSatUplink(frame)
	if err != nil || header.MsgType != hubreporter.SatMsgPosition {
		return ""
	}
	id, _, _, _, _, _, err := hubreporter.DecodeSatPosition(payload)
	if err != nil {
		return ""
	}
	return id
}

// @Summary Cancel SOS alert
// @Description Cancels an active SOS emergency alert
// @Tags sos
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/sos/cancel [post]
func (s *Server) handleSOSCancel(w http.ResponseWriter, r *http.Request) {
	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()

	if !s.sos.active {
		writeJSON(w, http.StatusOK, map[string]string{"status": "not_active"})
		return
	}

	s.sos.active = false
	if s.sos.cancelFn != nil {
		s.sos.cancelFn()
	}

	log.Warn().Msg("SOS CANCELLED")
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// @Summary Get SOS status
// @Description Returns current SOS alert status and send count
// @Tags sos
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/sos/status [get]
func (s *Server) handleSOSStatus(w http.ResponseWriter, r *http.Request) {
	s.sos.mu.Lock()
	defer s.sos.mu.Unlock()

	resp := map[string]interface{}{
		"active": s.sos.active,
	}
	if s.sos.active {
		resp["started_at"] = s.sos.startAt.UTC().Format(time.RFC3339)
		resp["sends"] = s.sos.sends
		resp["message"] = s.sos.text
		resp["trigger"] = s.sos.trigger
	}

	writeJSON(w, http.StatusOK, resp)
}

// sosWorker sends SOS messages 3 times with 30s intervals via all available transports.
func (s *Server) sosWorker(ctx context.Context, sosText string) {
	if sosText == "" {
		sosText = sosDefaultText
	}
	for i := 0; i < 3; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Send via mesh (broadcast)
		req := transport.SendRequest{
			Text: sosText,
		}
		// Guarded: this runs in a goroutine, so a nil transport here is not an
		// error return, it is an unrecovered panic that takes the bridge down
		// during an emergency. A kit whose mesh radio failed to start must
		// still get the satellite legs below. [MESHSAT-996]
		if s.mesh == nil {
			log.Error().Int("attempt", i+1).Msg("SOS: no mesh transport, skipping the mesh leg")
		} else if err := s.mesh.SendMessage(ctx, req); err != nil {
			log.Error().Err(err).Int("attempt", i+1).Msg("SOS mesh send failed")
		} else {
			log.Warn().Int("attempt", i+1).Msg("SOS sent via mesh")
			if s.processor != nil {
				s.recordMeshTX(req)
			}
		}

		// Send via satellite if available
		if s.gwManager != nil {
			sosPayload := encodeSOSPayload(0, 0, 0) // position will be 0 if GPS unavailable
			for _, gw := range s.gwManager.Gateways() {
				if gw.Type() == "iridium" {
					// SOS bypasses all queuing — send directly, and past the
					// credit budget: a used-up daily or monthly budget failed
					// this leg with "budget exceeded". [MESHSAT-1431]
					if err := gw.Forward(ctx, &transport.MeshMessage{
						PortNum:     1,
						DecodedText: sosText,
						Critical:    true,
					}); err != nil {
						log.Error().Err(err).Int("attempt", i+1).Msg("SOS satellite send failed")
					} else {
						log.Warn().Int("attempt", i+1).Msg("SOS sent via satellite")
					}
				}
			}
			_ = sosPayload // payload used for direct SBD if needed
		}

		// Hub uplink frame when the MQTT link is down: satellite first,
		// SMS to the Hub's number otherwise. The Hub decodes it from any of
		// its webhooks and raises the SOS. [MESHSAT-963]
		if s.satFallback != nil && i == 0 {
			var lat, lon float64
			if s.gpsReader != nil {
				if st := s.gpsReader.GetStatus(); st.Fix {
					lat, lon = st.Lat, st.Lon
				}
			}
			if err := s.satFallback.PublishSOS("bridge", lat, lon, sosText); err != nil {
				log.Error().Err(err).Msg("SOS hub uplink frame failed")
			}
		}

		s.sos.mu.Lock()
		s.sos.sends++
		s.sos.mu.Unlock()

		// Wait 30s between sends (unless cancelled)
		if i < 2 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}

	// Mark SOS as completed (all 3 sends done)
	s.sos.mu.Lock()
	s.sos.active = false
	s.sos.mu.Unlock()
	log.Warn().Msg("SOS sequence completed (3 sends)")
}

// encodeSOSPayload creates a compact SOS payload (15 bytes).
// Byte 0: 0x06 (MSG_TYPE_SOS)
// Byte 1: flags (0x01 = active)
// Bytes 2-5: latitude (int32 BE, *1e7)
// Bytes 6-9: longitude (int32 BE, *1e7)
// Bytes 10-11: altitude (uint16 BE)
// Bytes 12-15: timestamp (uint32 BE)
func encodeSOSPayload(lat, lon float64, alt int16) []byte {
	buf := make([]byte, 16)
	buf[0] = 0x06 // MSG_TYPE_SOS
	buf[1] = 0x01 // active

	binary.BigEndian.PutUint32(buf[2:6], uint32(int32(math.Round(lat*1e7))))
	binary.BigEndian.PutUint32(buf[6:10], uint32(int32(math.Round(lon*1e7))))
	binary.BigEndian.PutUint16(buf[10:12], uint16(alt))
	binary.BigEndian.PutUint32(buf[12:16], uint32(time.Now().Unix()))

	return buf
}
