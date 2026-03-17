package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/vpn"
)

// SetVPNManager sets the VPN manager for VPN tunnel endpoints.
func (s *Server) SetVPNManager(m *vpn.Manager) {
	s.vpnMgr = m
}

// vpnPeerResponse extends PeerInfo with config download URL.
type vpnPeerResponse struct {
	vpn.PeerInfo
	ConfigURL string `json:"config_url"`
	QRCodeURL string `json:"qr_code_url"`
}

func toVPNResponse(p vpn.PeerInfo) vpnPeerResponse {
	return vpnPeerResponse{
		PeerInfo:  p,
		ConfigURL: fmt.Sprintf("/api/vpn/peers/%d/config", p.DeviceID),
		QRCodeURL: fmt.Sprintf("/api/vpn/peers/%d/qrcode", p.DeviceID),
	}
}

// @Summary List VPN peers
// @Description Returns all WireGuard VPN peers with tunnel status.
// @Tags vpn
// @Produce json
// @Success 200 {array} vpnPeerResponse
// @Failure 503 {object} map[string]string "VPN not enabled"
// @Router /api/vpn/peers [get]
func (s *Server) handleGetVPNPeers(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "VPN not enabled")
		return
	}
	peers, err := s.vpnMgr.ListPeers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list VPN peers: "+err.Error())
		return
	}
	resp := make([]vpnPeerResponse, len(peers))
	for i := range peers {
		resp[i] = toVPNResponse(peers[i])
	}
	writeJSON(w, http.StatusOK, resp)
}

// @Summary Get VPN peer for device
// @Description Returns the VPN peer details and tunnel status for a specific device.
// @Tags vpn
// @Produce json
// @Param device_id path int true "Device ID"
// @Success 200 {object} vpnPeerResponse
// @Failure 404 {object} map[string]string "no VPN peer"
// @Failure 503 {object} map[string]string "VPN not enabled"
// @Router /api/vpn/peers/{device_id} [get]
func (s *Server) handleGetVPNPeer(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "VPN not enabled")
		return
	}
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	info, err := s.vpnMgr.GetPeerInfo(deviceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no VPN peer for this device")
		return
	}
	writeJSON(w, http.StatusOK, toVPNResponse(*info))
}

// @Summary Provision VPN peer for device
// @Description Creates a WireGuard peer for the given device. Idempotent — skips if already provisioned.
// @Tags vpn
// @Produce json
// @Param device_id path int true "Device ID"
// @Success 201 {object} vpnPeerResponse
// @Failure 400 {object} map[string]string "invalid device ID"
// @Failure 404 {object} map[string]string "device not found"
// @Failure 503 {object} map[string]string "VPN not enabled"
// @Router /api/vpn/peers/{device_id} [post]
func (s *Server) handleProvisionVPNPeer(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "VPN not enabled")
		return
	}
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	d, err := s.db.GetDevice(deviceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err := s.vpnMgr.ProvisionPeer(deviceID, d.Label); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to provision VPN peer: "+err.Error())
		return
	}
	info, err := s.vpnMgr.GetPeerInfo(deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "peer provisioned but failed to read back")
		return
	}
	writeJSON(w, http.StatusCreated, toVPNResponse(*info))
}

// @Summary Delete VPN peer for device
// @Description Removes the WireGuard peer for a device.
// @Tags vpn
// @Param device_id path int true "Device ID"
// @Success 204
// @Failure 503 {object} map[string]string "VPN not enabled"
// @Router /api/vpn/peers/{device_id} [delete]
func (s *Server) handleDeleteVPNPeer(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "VPN not enabled")
		return
	}
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if err := s.vpnMgr.RemovePeer(deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove VPN peer: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// @Summary Enable VPN peer
// @Description Re-enables a disabled WireGuard peer for a device.
// @Tags vpn
// @Param device_id path int true "Device ID"
// @Success 200 {object} vpnPeerResponse
// @Failure 503 {object} map[string]string "VPN not enabled"
// @Router /api/vpn/peers/{device_id}/enable [post]
func (s *Server) handleEnableVPNPeer(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "VPN not enabled")
		return
	}
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if err := s.vpnMgr.EnablePeer(deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enable VPN peer: "+err.Error())
		return
	}
	info, _ := s.vpnMgr.GetPeerInfo(deviceID)
	writeJSON(w, http.StatusOK, toVPNResponse(*info))
}

// @Summary Disable VPN peer
// @Description Disables a WireGuard peer for a device without removing it.
// @Tags vpn
// @Param device_id path int true "Device ID"
// @Success 200 {object} vpnPeerResponse
// @Failure 503 {object} map[string]string "VPN not enabled"
// @Router /api/vpn/peers/{device_id}/disable [post]
func (s *Server) handleDisableVPNPeer(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "VPN not enabled")
		return
	}
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	if err := s.vpnMgr.DisablePeer(deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to disable VPN peer: "+err.Error())
		return
	}
	info, _ := s.vpnMgr.GetPeerInfo(deviceID)
	writeJSON(w, http.StatusOK, toVPNResponse(*info))
}

// @Summary Get VPN peer config
// @Description Returns the WireGuard client configuration file for a device's peer.
// @Tags vpn
// @Produce text/plain
// @Param device_id path int true "Device ID"
// @Success 200 {string} string "WireGuard config INI"
// @Failure 503 {object} map[string]string "VPN not enabled"
// @Router /api/vpn/peers/{device_id}/config [get]
func (s *Server) handleGetVPNPeerConfig(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "VPN not enabled")
		return
	}
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	cfg, err := s.vpnMgr.GetPeerConfig(deviceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no VPN config: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=meshsat-device-%d.conf", deviceID))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(cfg))
}

// @Summary Get VPN peer QR code
// @Description Returns a QR code (base64-encoded PNG) for the WireGuard client configuration.
// @Tags vpn
// @Produce json
// @Param device_id path int true "Device ID"
// @Success 200 {object} map[string]string "qr_code_base64 field"
// @Failure 503 {object} map[string]string "VPN not enabled"
// @Router /api/vpn/peers/{device_id}/qrcode [get]
func (s *Server) handleGetVPNPeerQRCode(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "VPN not enabled")
		return
	}
	deviceID, err := strconv.ParseInt(chi.URLParam(r, "device_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device ID")
		return
	}
	cfg, err := s.vpnMgr.GetPeerConfig(deviceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no VPN config: "+err.Error())
		return
	}

	png, err := generateQRCode(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate QR code: "+err.Error())
		return
	}

	// Return as JSON with base64-encoded PNG for easy dashboard integration
	writeJSON(w, http.StatusOK, map[string]string{
		"qr_code_base64": base64.StdEncoding.EncodeToString(png),
		"config":         cfg,
	})
}

// @Summary VPN tunnel status
// @Description Returns overall VPN subsystem status: healthy, peer count, connected count.
// @Tags vpn
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/vpn/status [get]
func (s *Server) handleGetVPNStatus(w http.ResponseWriter, r *http.Request) {
	if s.vpnMgr == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":   false,
			"healthy":   false,
			"peers":     0,
			"connected": 0,
		})
		return
	}
	peers, _ := s.vpnMgr.ListPeers()
	connected := 0
	for _, p := range peers {
		if p.Connected {
			connected++
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":   true,
		"healthy":   s.vpnMgr.Healthy(),
		"peers":     len(peers),
		"connected": connected,
	})
}
