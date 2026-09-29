package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
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
	if s.sos == nil {
		s.sos = &SOSState{}
	}

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
	if s.sos == nil {
		s.sos = &SOSState{}
	}
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

	log.Warn().Str("trigger", trigger).Str("text", text).Msg("SOS ACTIVATED")
	return true
}

// handleSOSTest tells the Hub about a test of the alarm routes, as MeshSat
// Android does (HubReporter.publishSos with type "test", on the device's sos
// topic only): the Hub's live map shows it, its SOS detector never sees it, so
// nobody is paged. The mesh, satellite and SMS legs of a test are the apps'
// own ordinary sends. [MESHSAT-1397]
// @Summary Send an alarm test event to the Hub
// @Description Publishes a test event on the device's SOS topic; the Hub shows it and raises no alarm
// @Tags sos
// @Accept json
// @Produce json
// @Param body body object{message=string,latitude=number,longitude=number} false "The test's text and where it is from"
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]string "the Hub is not connected"
// @Router /api/sos/test [post]
func (s *Server) handleSOSTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message   string  `json:"message,omitempty"`
		Latitude  float64 `json:"latitude,omitempty"`
		Longitude float64 `json:"longitude,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	text := strings.TrimSpace(body.Message)
	if text == "" {
		text = "Test: checking the MeshSat alarm routes. No help needed."
	}
	if s.hubReporter == nil || !s.hubReporter.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "the Hub is not connected")
		return
	}
	lat, lon := body.Latitude, body.Longitude
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

// @Summary Cancel SOS alert
// @Description Cancels an active SOS emergency alert
// @Tags sos
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/sos/cancel [post]
func (s *Server) handleSOSCancel(w http.ResponseWriter, r *http.Request) {
	if s.sos == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "not_active"})
		return
	}

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
	if s.sos == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"active": false,
		})
		return
	}

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
					// SOS bypasses all queuing — send directly
					if err := gw.Forward(ctx, &transport.MeshMessage{
						PortNum:     1,
						DecodedText: sosText,
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
