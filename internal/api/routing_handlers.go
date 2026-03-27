package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// ============================================================================
// Routing config — listen port, announce interval, peer management
// ============================================================================

const (
	peersConfigKey   = "reticulum_peers"
	routingConfigKey = "reticulum_config"
)

type routingConfig struct {
	ListenPort       int    `json:"listen_port"`
	AnnounceInterval int    `json:"announce_interval"`
	ListenAddr       string `json:"listen_addr"`
	Warning          string `json:"warning,omitempty"`
}

// handleGetRoutingConfig returns the current routing configuration.
// @Summary Get Reticulum routing configuration
// @Tags routing
// @Produce json
// @Success 200 {object} routingConfig
// @Router /api/routing/config [get]
func (s *Server) handleGetRoutingConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.loadRoutingConfig()
	// Fill in live values if not yet persisted
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 4242
	}
	if cfg.AnnounceInterval == 0 {
		cfg.AnnounceInterval = 300
	}
	if s.tcpIface != nil {
		cfg.ListenAddr = s.tcpIface.ListenAddr()
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleSetRoutingConfig updates routing configuration. Changes to listen_port
// require a container restart and matching Docker port exposure.
// @Summary Update Reticulum routing configuration
// @Tags routing
// @Accept json
// @Produce json
// @Param body body routingConfig true "Routing config"
// @Success 200 {object} routingConfig
// @Router /api/routing/config [put]
func (s *Server) handleSetRoutingConfig(w http.ResponseWriter, r *http.Request) {
	var req routingConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	prev := s.loadRoutingConfig()

	if req.ListenPort > 0 {
		prev.ListenPort = req.ListenPort
	}
	if req.AnnounceInterval > 0 {
		prev.AnnounceInterval = req.AnnounceInterval
	}

	s.saveRoutingConfig(prev)

	resp := prev
	if s.tcpIface != nil {
		resp.ListenAddr = s.tcpIface.ListenAddr()
	}
	// Warn if port changed — requires container restart + Docker config update
	if req.ListenPort > 0 && s.tcpIface != nil {
		currentAddr := s.tcpIface.ListenAddr()
		if currentAddr != "" && currentAddr != fmt.Sprintf(":%d", req.ListenPort) && currentAddr != fmt.Sprintf("0.0.0.0:%d", req.ListenPort) {
			resp.Warning = fmt.Sprintf(
				"Listen port changed to %d. This requires: (1) update MESHSAT_TCP_LISTEN in defaults.env, "+
					"(2) if not using host networking, update Docker port mapping, (3) restart the container. "+
					"Remote peers must also update their connect address.",
				req.ListenPort,
			)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) loadRoutingConfig() routingConfig {
	raw, err := s.db.GetSystemConfig(routingConfigKey)
	if err != nil || raw == "" {
		return routingConfig{}
	}
	var cfg routingConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return routingConfig{}
	}
	return cfg
}

func (s *Server) saveRoutingConfig(cfg routingConfig) {
	data, _ := json.Marshal(cfg)
	_ = s.db.SetSystemConfig(routingConfigKey, string(data))
}

// handleGetPeers returns the current TCP peer list (connected + configured).
// @Summary List Reticulum TCP peers
// @Tags routing
// @Produce json
// @Success 200 {array} routing.PeerInfo
// @Router /api/routing/peers [get]
func (s *Server) handleGetPeers(w http.ResponseWriter, r *http.Request) {
	if s.tcpIface == nil {
		writeJSON(w, http.StatusOK, []routing.PeerInfo{})
		return
	}
	writeJSON(w, http.StatusOK, s.tcpIface.ListPeers())
}

// handleAddPeer adds a dynamic outbound peer connection.
// @Summary Add Reticulum TCP peer
// @Tags routing
// @Accept json
// @Produce json
// @Param body body addPeerRequest true "Peer address (host:port)"
// @Success 201 {object} map[string]string
// @Router /api/routing/peers [post]
func (s *Server) handleAddPeer(w http.ResponseWriter, r *http.Request) {
	if s.tcpIface == nil {
		writeError(w, http.StatusServiceUnavailable, "TCP interface not active — set MESHSAT_TCP_LISTEN first")
		return
	}

	var req addPeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Address == "" {
		writeError(w, http.StatusBadRequest, "address is required (host:port)")
		return
	}

	if err := s.tcpIface.AddPeer(r.Context(), req.Address); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Persist to system_config
	peers := s.loadPeers()
	// Deduplicate
	for _, p := range peers {
		if p == req.Address {
			writeJSON(w, http.StatusCreated, map[string]string{"address": req.Address, "status": "already exists"})
			return
		}
	}
	peers = append(peers, req.Address)
	s.savePeers(peers)

	writeJSON(w, http.StatusCreated, map[string]string{"address": req.Address, "status": "connecting"})
}

type addPeerRequest struct {
	Address string `json:"address"`
}

// handleRemovePeer removes a dynamic outbound peer.
// @Summary Remove Reticulum TCP peer
// @Tags routing
// @Param addr path string true "Peer address (host:port)"
// @Success 204
// @Router /api/routing/peers/{addr} [delete]
func (s *Server) handleRemovePeer(w http.ResponseWriter, r *http.Request) {
	if s.tcpIface == nil {
		writeError(w, http.StatusServiceUnavailable, "TCP interface not active")
		return
	}

	addr := chi.URLParam(r, "addr")
	s.tcpIface.RemovePeer(addr)

	// Remove from persisted list
	peers := s.loadPeers()
	filtered := peers[:0]
	for _, p := range peers {
		if p != addr {
			filtered = append(filtered, p)
		}
	}
	s.savePeers(filtered)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) loadPeers() []string {
	raw, err := s.db.GetSystemConfig(peersConfigKey)
	if err != nil || raw == "" {
		return nil
	}
	var peers []string
	if err := json.Unmarshal([]byte(raw), &peers); err != nil {
		return nil
	}
	return peers
}

func (s *Server) savePeers(peers []string) {
	data, _ := json.Marshal(peers)
	_ = s.db.SetSystemConfig(peersConfigKey, string(data))
}

// ============================================================================
// Hub connection — MQTT credentials for bridge-to-hub uplink
// ============================================================================

const hubConfigKey = "hub_connection"

type hubConnectionConfig struct {
	URL         string `json:"url"`
	BridgeID    string `json:"bridge_id"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	HasPass     bool   `json:"has_password"`
	TLSCertPEM  string `json:"tls_cert_pem,omitempty"`
	TLSKeyPEM   string `json:"tls_key_pem,omitempty"`
	TLSCAPEM    string `json:"tls_ca_pem,omitempty"`
	HasCert     bool   `json:"has_cert"`
	TLSInsecure bool   `json:"tls_insecure,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

// handleGetHubConfig returns the current Hub connection config (password redacted).
// @Summary Get Hub connection config
// @Tags routing
// @Produce json
// @Success 200 {object} hubConnectionConfig
// @Router /api/routing/hub [get]
func (s *Server) handleGetHubConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.loadHubConfig()
	cfg.Password = ""   // never expose password in GET
	cfg.TLSCertPEM = "" // never expose cert PEM in GET
	cfg.TLSKeyPEM = ""  // never expose key PEM in GET
	cfg.TLSCAPEM = ""   // never expose CA PEM in GET
	writeJSON(w, http.StatusOK, cfg)
}

// handleSetHubConfig saves Hub MQTT credentials. Takes effect on next restart.
// @Summary Set Hub connection config
// @Tags routing
// @Accept json
// @Produce json
// @Param body body hubConnectionConfig true "Hub MQTT credentials"
// @Success 200 {object} hubConnectionConfig
// @Router /api/routing/hub [put]
func (s *Server) handleSetHubConfig(w http.ResponseWriter, r *http.Request) {
	var req hubConnectionConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	prev := s.loadHubConfig()

	if req.URL != "" {
		prev.URL = req.URL
	}
	if req.BridgeID != "" {
		prev.BridgeID = req.BridgeID
	}
	if req.Username != "" {
		prev.Username = req.Username
	}
	if req.Password != "" {
		prev.Password = req.Password
		prev.HasPass = true
	}
	if req.TLSCertPEM != "" {
		prev.TLSCertPEM = req.TLSCertPEM
	}
	if req.TLSKeyPEM != "" {
		prev.TLSKeyPEM = req.TLSKeyPEM
	}
	if req.TLSCAPEM != "" {
		prev.TLSCAPEM = req.TLSCAPEM
	}
	prev.HasCert = prev.TLSCertPEM != "" && prev.TLSKeyPEM != ""
	prev.TLSInsecure = req.TLSInsecure

	s.saveHubConfig(prev)

	resp := prev
	resp.Password = ""
	resp.Warning = "Hub connection config saved. Restart the bridge for changes to take effect."
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) loadHubConfig() hubConnectionConfig {
	raw, err := s.db.GetSystemConfig(hubConfigKey)
	if err != nil || raw == "" {
		return hubConnectionConfig{}
	}
	var cfg hubConnectionConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return hubConnectionConfig{}
	}
	return cfg
}

func (s *Server) saveHubConfig(cfg hubConnectionConfig) {
	data, _ := json.Marshal(cfg)
	_ = s.db.SetSystemConfig(hubConfigKey, string(data))
}
