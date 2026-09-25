package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/lxmf"
)

// lxmfSendRequest is the body of POST /api/lxmf/send.
type lxmfSendRequest struct {
	To         string `json:"to"`         // lxmf.delivery hash, 32 hex
	Content    string `json:"content"`    // message text
	Title      string `json:"title"`      // optional
	Precedence string `json:"precedence"` // optional delivery precedence
}

// lxmfSendResponse is the reply of POST /api/lxmf/send.
type lxmfSendResponse struct {
	DeliveryID int64  `json:"delivery_id"`
	MsgRef     string `json:"msg_ref"`
	Channel    string `json:"channel"`
}

// handleLXMFIdentity returns this bridge's LXMF delivery destination.
// @Summary LXMF delivery identity
// @Tags lxmf
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/lxmf/identity [get]
func (s *Server) handleLXMFIdentity(w http.ResponseWriter, r *http.Request) {
	if s.lxmfRouter == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":   true,
		"dest_hash": s.lxmfRouter.HashHex(),
		"address":   "lxmf@" + s.lxmfRouter.HashHex(),
		"peers":     len(s.lxmfRouter.Peers()),
	})
}

// handleLXMFPeers lists known LXMF destinations.
// @Summary List known LXMF peers
// @Tags lxmf
// @Produce json
// @Success 200 {array} lxmf.Peer
// @Router /api/lxmf/peers [get]
func (s *Server) handleLXMFPeers(w http.ResponseWriter, r *http.Request) {
	if s.lxmfRouter == nil {
		writeError(w, http.StatusServiceUnavailable, "lxmf not running")
		return
	}
	peers := s.lxmfRouter.Peers()
	if peers == nil {
		peers = []lxmf.Peer{}
	}
	writeJSON(w, http.StatusOK, peers)
}

// handleLXMFSend queues an LXMF message through the delivery ledger.
// @Summary Send an LXMF message
// @Tags lxmf
// @Accept json
// @Produce json
// @Param body body lxmfSendRequest true "message"
// @Success 202 {object} lxmfSendResponse
// @Failure 400 {object} map[string]string
// @Router /api/lxmf/send [post]
func (s *Server) handleLXMFSend(w http.ResponseWriter, r *http.Request) {
	if s.lxmfRouter == nil || s.dispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "lxmf not running")
		return
	}
	var req lxmfSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if b, err := hex.DecodeString(req.To); err != nil || len(b) != lxmf.DestLen {
		writeError(w, http.StatusBadRequest, "to must be a 32-hex LXMF destination hash")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if len(req.Content) > lxmf.MaxTransferBytes {
		writeError(w, http.StatusBadRequest, "content larger than one resource segment")
		return
	}
	body, _ := json.Marshal(map[string]string{"content": req.Content, "title": req.Title})
	id, ref, err := s.dispatcher.QueueDirectSendTo("lxmf_0", req.Content, engine.DirectSendOptions{
		Precedence:  req.Precedence,
		Destination: req.To,
		Class:       database.DeliveryClassLXMF,
		MaxRetries:  5,
		Payload:     body,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, lxmfSendResponse{DeliveryID: id, MsgRef: ref, Channel: "lxmf_0"})
}

// handleLXMFAnnounce announces the LXMF destination now.
// @Summary Announce the LXMF destination now
// @Tags lxmf
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/lxmf/announce [post]
func (s *Server) handleLXMFAnnounce(w http.ResponseWriter, r *http.Request) {
	if s.lxmfRouter == nil {
		writeError(w, http.StatusServiceUnavailable, "lxmf not running")
		return
	}
	if err := s.lxmfRouter.Announce(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "announced", "dest_hash": s.lxmfRouter.HashHex()})
}
