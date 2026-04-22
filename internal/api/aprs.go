package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"crypto/sha256"
)

// handleGetAPRSStatus returns aggregated APRS gateway status.
// @Summary Get APRS gateway status
// @Description Returns connection state, callsign, frequency, uptime, counters, packet type breakdown, and MESHSAT-661 encryption state (enabled/transforms/key fingerprint).
// @Tags aprs
// @Success 200 {object} map[string]interface{}
// @Router /api/aprs/status [get]
func (s *Server) handleGetAPRSStatus(w http.ResponseWriter, r *http.Request) {
	agw := s.gwManager.GetAPRSGateway()
	if agw == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"connected":  false,
			"encryption": aprsEncryptionState(s, "aprs_0"),
		})
		return
	}
	status := agw.GetAPRSStatus()
	status["encryption"] = aprsEncryptionState(s, "aprs_0")
	writeJSON(w, http.StatusOK, status)
}

// aprsEncryptionState builds the dashboard/crypto-state block for the
// given interface ID. Reads the interfaces row + keystore; returns a
// self-contained map that the UI can render without additional fetches.
// Values are best-effort — a partial read (e.g. interface exists but
// key doesn't) produces the fields it can and leaves the rest empty
// rather than error-ing.
func aprsEncryptionState(s *Server, ifaceID string) map[string]interface{} {
	out := map[string]interface{}{
		"enabled": false,
	}
	if s == nil || s.db == nil {
		return out
	}
	iface, err := s.db.GetInterface(ifaceID)
	if err != nil || iface == nil {
		return out
	}

	transforms := iface.EgressTransforms
	if transforms == "" {
		transforms = "[]"
	}
	out["egress_transforms"] = transforms
	out["ingress_transforms"] = iface.IngressTransforms

	if !strings.Contains(transforms, "\"encrypt\"") {
		return out
	}
	out["enabled"] = true

	// Parse the transform chain to surface a human-readable summary
	// (e.g. "smaz2 → AES-256-GCM → base64") plus the encrypt stage's
	// key_ref so the UI can show what keystore entry is in use.
	type spec struct {
		Type   string            `json:"type"`
		Params map[string]string `json:"params"`
	}
	var specs []spec
	_ = json.Unmarshal([]byte(transforms), &specs)
	summary := make([]string, 0, len(specs))
	keyRef := ""
	for _, sp := range specs {
		switch sp.Type {
		case "encrypt":
			summary = append(summary, "AES-256-GCM")
			if sp.Params != nil {
				if kr := sp.Params["key_ref"]; kr != "" {
					keyRef = kr
				}
			}
		case "smaz2", "base64", "zstd", "llamazip", "msvqsc", "fec":
			summary = append(summary, sp.Type)
		default:
			if sp.Type != "" {
				summary = append(summary, sp.Type)
			}
		}
	}
	out["summary"] = strings.Join(summary, " → ")
	out["key_ref"] = keyRef

	// Fingerprint = first 8 hex chars of SHA-256(raw key). Mirrors the
	// `key_preview` convention used by /api/keys. Only produced if a
	// keystore is available AND the key_ref resolves — otherwise the
	// UI shows "no key" without leaking a misleading value.
	if s.keyStore != nil && keyRef != "" {
		parts := strings.SplitN(keyRef, ":", 2)
		if len(parts) == 2 {
			if raw, _, kerr := s.keyStore.GetKey(parts[0], parts[1]); kerr == nil && len(raw) > 0 {
				h := sha256.Sum256(raw)
				out["key_fingerprint"] = hex.EncodeToString(h[:8])
			}
		}
	}

	return out
}

// handleGetAPRSHeard returns the heard station table.
// @Summary Get APRS heard stations
// @Description Returns all stations heard by the APRS gateway with last position and distance
// @Tags aprs
// @Success 200 {array} gateway.HeardStation
// @Router /api/aprs/heard [get]
func (s *Server) handleGetAPRSHeard(w http.ResponseWriter, r *http.Request) {
	agw := s.gwManager.GetAPRSGateway()
	if agw == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	writeJSON(w, http.StatusOK, agw.Tracker().GetHeardStations())
}

// handleGetAPRSActivity returns RX/TX packets per minute for the last 30 minutes.
// @Summary Get APRS packet activity
// @Description Returns RX/TX packets per minute for the last 30 minutes
// @Tags aprs
// @Success 200 {object} map[string]interface{}
// @Router /api/aprs/activity [get]
func (s *Server) handleGetAPRSActivity(w http.ResponseWriter, r *http.Request) {
	agw := s.gwManager.GetAPRSGateway()
	if agw == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"buckets":      []interface{}{},
			"recent_paths": []interface{}{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"buckets":      agw.Tracker().GetActivity(),
		"recent_paths": agw.Tracker().GetRecentPaths(),
	})
}
