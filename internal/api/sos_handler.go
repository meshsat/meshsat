package api

import (
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// SOSState tracks an active SOS alert.
type SOSState struct {
	mu       sync.Mutex
	active   bool
	startAt  time.Time
	cancelFn context.CancelFunc
	sends    int
}

// @Summary Activate SOS alert
// @Description Triggers an SOS emergency alert that sends via mesh and satellite (3x at 30s intervals)
// @Tags sos
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 409 {object} map[string]string "already active"
// @Router /api/sos/activate [post]
func (s *Server) handleSOSActivate(w http.ResponseWriter, r *http.Request) {
	if s.sos == nil {
		s.sos = &SOSState{}
	}

	s.sos.mu.Lock()
	if s.sos.active {
		s.sos.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"status": "already_active"})
		return
	}
	s.sos.active = true
	s.sos.startAt = time.Now()
	s.sos.sends = 0
	ctx, cancel := context.WithCancel(context.Background())
	s.sos.cancelFn = cancel
	s.sos.mu.Unlock()

	go s.sosWorker(ctx)

	log.Warn().Msg("SOS ACTIVATED")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "activated",
		"started_at": s.sos.startAt.UTC().Format(time.RFC3339),
	})
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
	}

	writeJSON(w, http.StatusOK, resp)
}

// sosWorker sends SOS messages 3 times with 30s intervals via all available transports.
func (s *Server) sosWorker(ctx context.Context) {
	for i := 0; i < 3; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Send via mesh (broadcast)
		sosText := "SOS - EMERGENCY ALERT - Requesting immediate assistance"
		req := transport.SendRequest{
			Text: sosText,
		}
		if err := s.mesh.SendMessage(ctx, req); err != nil {
			log.Error().Err(err).Int("attempt", i+1).Msg("SOS mesh send failed")
		} else {
			log.Warn().Int("attempt", i+1).Msg("SOS sent via mesh")
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
