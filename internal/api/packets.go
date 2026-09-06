package api

import (
	"net/http"
	"strconv"
	"time"

	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/transport"
)

// Live packet feed (TTC mode): the in-memory ring the engine fills from the
// LoRa, APRS and SMS hook points, exposed newest-first plus per-bearer rates
// so the SPA can show a packet ticker and animate a message crossing
// bearers. Purely in memory, no table. [MESHSAT-826]

const (
	packetsDefaultLimit = 200
	packetsMaxLimit     = engine.PacketRingSize
	packetsShortWindow  = 60 * time.Second
	packetsLongWindow   = 300 * time.Second
)

// packetRatesWindow is one rates window of the response.
type packetRatesWindow struct {
	WindowS int                           `json:"window_s"`
	Bearers map[string]engine.PacketRates `json:"bearers"`
}

// @Summary Live packet feed
// @Description Newest-first records of frames seen on the LoRa, APRS and SMS bearers (both directions) from the in-memory 500-record ring. Each record carries time, bearer, dir, iface, from, to, bytes, rssi, snr, hops, channel, portnum, portnum_name, text, raw, path and msg_ref.
// @Tags packets
// @Produce json
// @Param limit query integer false "Max records (default 200, max 500)"
// @Param bearer query string false "Filter by bearer: lora, aprs or sms"
// @Param dir query string false "Filter by direction: rx or tx"
// @Success 200 {object} map[string]interface{} "{\"packets\":[...]}"
// @Failure 400 {object} map[string]string
// @Router /api/packets [get]
func (s *Server) handleGetPackets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := packetsDefaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > packetsMaxLimit {
			n = packetsMaxLimit
		}
		limit = n
	}
	bearer := q.Get("bearer")
	switch bearer {
	case "", gateway.BearerLoRa, gateway.BearerAPRS, gateway.BearerSMS:
	default:
		writeError(w, http.StatusBadRequest, "bearer must be lora, aprs or sms")
		return
	}
	dir := q.Get("dir")
	switch dir {
	case "", gateway.DirRX, gateway.DirTX:
	default:
		writeError(w, http.StatusBadRequest, "dir must be rx or tx")
		return
	}

	packets := s.processor.Packets().Newest(limit, bearer, dir)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"packets": packets,
	})
}

// @Summary Live packet rates
// @Description Per-bearer rx/tx frame counts over the last 60 s and the last 300 s, computed from the in-memory packet ring. Every bearer (lora, aprs, sms) is present, zero-filled.
// @Tags packets
// @Produce json
// @Success 200 {object} map[string]interface{} "{\"window_s\":60,\"bearers\":{...},\"long\":{\"window_s\":300,\"bearers\":{...}}}"
// @Router /api/packets/rates [get]
func (s *Server) handleGetPacketRates(w http.ResponseWriter, r *http.Request) {
	ring := s.processor.Packets()
	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"window_s": int(packetsShortWindow / time.Second),
		"bearers":  ring.Rates(now, packetsShortWindow),
		"long": packetRatesWindow{
			WindowS: int(packetsLongWindow / time.Second),
			Bearers: ring.Rates(now, packetsLongWindow),
		},
	})
}

// recordMeshTX adds a LoRa tx record for a text the API just handed to the
// mesh radio (compose, preset, SOS, demo). Call only after SendMessage
// returned nil. Safe with a nil processor. [MESHSAT-826]
func (s *Server) recordMeshTX(req transport.SendRequest) {
	s.processor.Packets().Add(engine.MeshTXRecord(s.mesh, "mesh_0", req, ""))
}

// recordSMSTX adds an SMS tx record for a message the API sent straight
// through the modem (POST /api/cellular/sms/send). onAir is the text as
// sent (bytes), plain the operator's text (the decoded text, like the
// inbound side records). [MESHSAT-826]
func (s *Server) recordSMSTX(to, onAir, plain string) {
	s.processor.Packets().Add(gateway.PacketRecord{
		Time:   time.Now(),
		Bearer: gateway.BearerSMS,
		Dir:    gateway.DirTX,
		Iface:  "cellular_0",
		To:     to,
		Bytes:  len(onAir),
		Text:   plain,
	})
}
