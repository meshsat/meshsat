package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/compress"
)

// @Summary RockBLOCK webhook receiver
// @Description Receives MO (Mobile Originated) messages from the RockBLOCK webhook service.
// @Description Verifies shared secret, hex-decodes payload, attempts SMAZ2 decompression,
// @Description and publishes to MQTT hub namespace topics (mo/raw, mo/decoded, position).
// @Tags webhooks
// @Accept application/x-www-form-urlencoded
// @Produce json
// @Param imei formData string true "RockBLOCK IMEI"
// @Param momsn formData int true "MO message sequence number"
// @Param transmit_time formData string true "Transmission time (YY-MM-DD HH:MM:SS)"
// @Param iridium_latitude formData number true "Iridium satellite latitude"
// @Param iridium_longitude formData number true "Iridium satellite longitude"
// @Param iridium_cep formData number true "Circular error probable (km)"
// @Param data formData string true "Hex-encoded payload"
// @Success 200 {object} map[string]string "accepted"
// @Failure 400 {object} map[string]string "malformed payload"
// @Failure 401 {object} map[string]string "invalid secret"
// @Router /api/webhook/rockblock [post]
func (s *Server) handleRockBLOCKWebhook(w http.ResponseWriter, r *http.Request) {
	// Verify shared secret
	secret := os.Getenv("MESHSAT_ROCKBLOCK_SECRET")
	if secret != "" {
		provided := r.FormValue("secret")
		if provided == "" {
			provided = r.URL.Query().Get("secret")
		}
		if provided != secret {
			writeError(w, http.StatusUnauthorized, "invalid webhook secret")
			return
		}
	}

	// Parse required form fields
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	imei := r.FormValue("imei")
	momsnStr := r.FormValue("momsn")
	transmitTime := r.FormValue("transmit_time")
	iridiumLatStr := r.FormValue("iridium_latitude")
	iridiumLonStr := r.FormValue("iridium_longitude")
	iridiumCEPStr := r.FormValue("iridium_cep")
	dataHex := r.FormValue("data")

	if imei == "" || momsnStr == "" || transmitTime == "" {
		writeError(w, http.StatusBadRequest, "missing required fields: imei, momsn, transmit_time")
		return
	}

	momsn, err := strconv.Atoi(momsnStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid momsn: "+err.Error())
		return
	}

	iridiumLat, _ := strconv.ParseFloat(iridiumLatStr, 64)
	iridiumLon, _ := strconv.ParseFloat(iridiumLonStr, 64)
	iridiumCEP, _ := strconv.ParseFloat(iridiumCEPStr, 64)

	// Parse transmit time (RockBLOCK format: "YY-MM-DD HH:MM:SS")
	txTime, err := time.Parse("06-01-02 15:04:05", transmitTime)
	if err != nil {
		log.Warn().Err(err).Str("transmit_time", transmitTime).Msg("rockblock: failed to parse transmit_time")
		txTime = time.Now().UTC()
	}

	// Hex-decode the data payload
	var rawData []byte
	if dataHex != "" {
		rawData, err = hex.DecodeString(dataHex)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid hex data: "+err.Error())
			return
		}
	}

	// Attempt SMAZ2 decompression (try Meshtastic dict first, then default)
	var decodedText string
	var decompressed bool
	if len(rawData) > 0 {
		if text, err := compress.DecompressString(rawData, compress.DictMeshtastic); err == nil && isPrintable(text) {
			decodedText = text
			decompressed = true
		} else if text, err := compress.DecompressString(rawData, compress.DictDefault); err == nil && isPrintable(text) {
			decodedText = text
			decompressed = true
		} else {
			// Not SMAZ2 compressed — treat as raw text if printable
			if isPrintable(string(rawData)) {
				decodedText = string(rawData)
			}
		}
	}

	log.Info().
		Str("imei", imei).
		Int("momsn", momsn).
		Str("transmit_time", txTime.Format(time.RFC3339)).
		Int("data_len", len(rawData)).
		Bool("smaz2", decompressed).
		Msg("rockblock: MO message received")

	// Publish to MQTT hub namespace
	if s.gwManager != nil {
		mqttGW := s.gwManager.GetMQTTGateway()
		if mqttGW != nil {
			// mo/raw — base64-encoded raw payload (QoS 1)
			rawTopic := fmt.Sprintf("meshsat/%s/mo/raw", imei)
			rawPayload, _ := json.Marshal(map[string]interface{}{
				"imei":          imei,
				"momsn":         momsn,
				"transmit_time": txTime.Format(time.RFC3339),
				"data":          base64.StdEncoding.EncodeToString(rawData),
			})
			if err := mqttGW.PublishRaw(rawTopic, 1, false, rawPayload); err != nil {
				log.Warn().Err(err).Str("topic", rawTopic).Msg("rockblock: failed to publish mo/raw")
			}

			// mo/decoded — decoded message with position (QoS 1)
			decodedTopic := fmt.Sprintf("meshsat/%s/mo/decoded", imei)
			decodedPayload := map[string]interface{}{
				"imei":          imei,
				"momsn":         momsn,
				"transmit_time": txTime.Format(time.RFC3339),
				"text":          decodedText,
				"compressed":    decompressed,
				"data_hex":      dataHex,
			}
			if iridiumLat != 0 || iridiumLon != 0 {
				decodedPayload["iridium_latitude"] = iridiumLat
				decodedPayload["iridium_longitude"] = iridiumLon
				decodedPayload["iridium_cep"] = iridiumCEP
			}
			decodedJSON, _ := json.Marshal(decodedPayload)
			if err := mqttGW.PublishRaw(decodedTopic, 1, false, decodedJSON); err != nil {
				log.Warn().Err(err).Str("topic", decodedTopic).Msg("rockblock: failed to publish mo/decoded")
			}

			// position — if Iridium-derived coordinates available (QoS 1, retained)
			if iridiumLat != 0 || iridiumLon != 0 {
				posTopic := fmt.Sprintf("meshsat/%s/position", imei)
				posPayload, _ := json.Marshal(map[string]interface{}{
					"latitude":  iridiumLat,
					"longitude": iridiumLon,
					"cep":       iridiumCEP,
					"source":    "iridium",
					"timestamp": txTime.Format(time.RFC3339),
				})
				if err := mqttGW.PublishRaw(posTopic, 1, true, posPayload); err != nil {
					log.Warn().Err(err).Str("topic", posTopic).Msg("rockblock: failed to publish position")
				}
			}
		}
	}

	// Fire OnMO callback (device registry last_seen update)
	if s.onMOCallback != nil {
		s.onMOCallback(imei)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

// isPrintable returns true if the string contains only printable ASCII/UTF-8 characters.
func isPrintable(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}
