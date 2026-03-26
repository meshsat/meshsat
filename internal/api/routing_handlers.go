package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/routing"
)

// handleGetRoutingIdentity returns the local routing identity.
// @Summary Get local routing identity
// @Tags routing
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/routing/identity [get]
func (s *Server) handleGetRoutingIdentity(w http.ResponseWriter, r *http.Request) {
	if s.routingID == nil {
		writeError(w, http.StatusServiceUnavailable, "routing not initialized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"dest_hash":      s.routingID.DestHashHex(),
		"signing_pub":    hex.EncodeToString(s.routingID.SigningPublicKey()),
		"encryption_pub": hex.EncodeToString(s.routingID.EncryptionPublicKey().Bytes()),
	})
}

// handleGetRoutingDestinations returns all known routing destinations.
// @Summary List known routing destinations
// @Tags routing
// @Produce json
// @Success 200 {array} routingDestinationResponse
// @Router /api/routing/destinations [get]
func (s *Server) handleGetRoutingDestinations(w http.ResponseWriter, r *http.Request) {
	if s.destTable == nil {
		writeError(w, http.StatusServiceUnavailable, "routing not initialized")
		return
	}
	dests := s.destTable.All()
	result := make([]routingDestinationResponse, 0, len(dests))
	for _, d := range dests {
		result = append(result, routingDestinationResponse{
			DestHash:      hex.EncodeToString(d.DestHash[:]),
			SigningPub:    hex.EncodeToString(d.SigningPub),
			EncryptionPub: hex.EncodeToString(d.EncryptionPub.Bytes()),
			HopCount:      d.HopCount,
			SourceIface:   d.SourceIface,
			FirstSeen:     d.FirstSeen,
			LastSeen:      d.LastSeen,
			AnnounceCount: d.AnnounceCount,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

type routingDestinationResponse struct {
	DestHash      string    `json:"dest_hash"`
	SigningPub    string    `json:"signing_pub"`
	EncryptionPub string    `json:"encryption_pub"`
	HopCount      int       `json:"hop_count"`
	SourceIface   string    `json:"source_iface"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	AnnounceCount int       `json:"announce_count"`
}

// handleCreateLink initiates a new link to a destination.
// @Summary Initiate a link to a remote destination
// @Tags routing
// @Accept json
// @Produce json
// @Param body body createLinkRequest true "Destination hash (hex)"
// @Success 201 {object} map[string]string
// @Router /api/links [post]
func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	if s.linkMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "routing not initialized")
		return
	}

	var req createLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	destBytes, err := hex.DecodeString(req.DestHash)
	if err != nil || len(destBytes) != routing.DestHashLen {
		writeError(w, http.StatusBadRequest, "invalid dest_hash: must be 32-char hex")
		return
	}

	var destHash [routing.DestHashLen]byte
	copy(destHash[:], destBytes)

	data, link, err := s.linkMgr.InitiateLink(destHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"link_id":      hex.EncodeToString(link.ID[:]),
		"state":        linkStateString(link.State),
		"request_size": len(data),
	})
}

type createLinkRequest struct {
	DestHash string `json:"dest_hash"`
}

// handleGetLinks returns all active links.
// @Summary List active links
// @Tags routing
// @Produce json
// @Success 200 {array} linkResponse
// @Router /api/links [get]
func (s *Server) handleGetLinks(w http.ResponseWriter, r *http.Request) {
	if s.linkMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "routing not initialized")
		return
	}

	links := s.linkMgr.ActiveLinks()
	result := make([]linkResponse, 0, len(links))
	for _, l := range links {
		result = append(result, linkResponse{
			LinkID:       hex.EncodeToString(l.ID[:]),
			DestHash:     hex.EncodeToString(l.DestHash[:]),
			State:        linkStateString(l.State),
			IsInitiator:  l.IsInitiator,
			CreatedAt:    l.CreatedAt,
			LastActivity: l.LastActivity,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

type linkResponse struct {
	LinkID       string    `json:"link_id"`
	DestHash     string    `json:"dest_hash"`
	State        string    `json:"state"`
	IsInitiator  bool      `json:"is_initiator"`
	CreatedAt    time.Time `json:"created_at"`
	LastActivity time.Time `json:"last_activity"`
}

// handleDeleteLink closes a link by ID.
// @Summary Close a link
// @Tags routing
// @Param id path string true "Link ID (hex)"
// @Success 204
// @Router /api/links/{id} [delete]
func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	if s.linkMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "routing not initialized")
		return
	}

	idHex := chi.URLParam(r, "id")
	idBytes, err := hex.DecodeString(idHex)
	if err != nil || len(idBytes) != routing.LinkIDLen {
		writeError(w, http.StatusBadRequest, "invalid link id")
		return
	}

	var linkID [routing.LinkIDLen]byte
	copy(linkID[:], idBytes)

	s.linkMgr.CloseLink(linkID)
	w.WriteHeader(http.StatusNoContent)
}

func linkStateString(s routing.LinkState) string {
	switch s {
	case routing.LinkStatePending:
		return "pending"
	case routing.LinkStateEstablished:
		return "established"
	case routing.LinkStateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// handleGetFloodable returns the floodable status of all Reticulum interfaces.
// @Summary Get interface flood control status
// @Tags routing
// @Produce json
// @Success 200 {array} floodableResponse
// @Router /api/routing/floodable [get]
func (s *Server) handleGetFloodable(w http.ResponseWriter, r *http.Request) {
	if s.ifaceRegistry == nil {
		writeError(w, http.StatusServiceUnavailable, "routing not initialized")
		return
	}
	ifaces := s.ifaceRegistry.All()
	result := make([]floodableResponse, 0, len(ifaces))
	for _, iface := range ifaces {
		result = append(result, floodableResponse{
			ID:        iface.ID(),
			Type:      string(iface.Type()),
			Cost:      iface.Cost(),
			MTU:       iface.MTU(),
			Online:    iface.IsOnline(),
			Floodable: iface.IsFloodable(),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

type floodableResponse struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Cost      float64 `json:"cost"`
	MTU       int     `json:"mtu"`
	Online    bool    `json:"online"`
	Floodable bool    `json:"floodable"`
}

// handleSetFloodable toggles the floodable flag on a Reticulum interface.
// Persists the override to system_config so it survives restarts.
// @Summary Toggle interface flood control
// @Tags routing
// @Accept json
// @Produce json
// @Param ifaceID path string true "Interface ID (e.g. iridium_0)"
// @Param body body setFloodableRequest true "Floodable flag"
// @Success 200 {object} floodableResponse
// @Router /api/routing/floodable/{ifaceID} [put]
func (s *Server) handleSetFloodable(w http.ResponseWriter, r *http.Request) {
	if s.ifaceRegistry == nil {
		writeError(w, http.StatusServiceUnavailable, "routing not initialized")
		return
	}

	ifaceID := chi.URLParam(r, "ifaceID")
	iface := s.ifaceRegistry.Get(ifaceID)
	if iface == nil {
		writeError(w, http.StatusNotFound, "interface not found: "+ifaceID)
		return
	}

	var req setFloodableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	iface.SetFloodable(req.Floodable)

	// Persist override to system_config
	overrides := s.loadFloodableOverrides()
	overrides[ifaceID] = req.Floodable
	s.saveFloodableOverrides(overrides)

	writeJSON(w, http.StatusOK, floodableResponse{
		ID:        iface.ID(),
		Type:      string(iface.Type()),
		Cost:      iface.Cost(),
		MTU:       iface.MTU(),
		Online:    iface.IsOnline(),
		Floodable: iface.IsFloodable(),
	})
}

type setFloodableRequest struct {
	Floodable bool `json:"floodable"`
}

const floodableConfigKey = "reticulum_floodable_overrides"

func (s *Server) loadFloodableOverrides() map[string]bool {
	raw, err := s.db.GetSystemConfig(floodableConfigKey)
	if err != nil || raw == "" {
		return make(map[string]bool)
	}
	var m map[string]bool
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return make(map[string]bool)
	}
	return m
}

func (s *Server) saveFloodableOverrides(m map[string]bool) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = s.db.SetSystemConfig(floodableConfigKey, string(data))
}
