package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"

	"meshsat/internal/database"
	"meshsat/internal/oob"
)

// OOB management frames API [MESHSAT-756]. Every handler answers 503 when
// the service is not wired (no keystore).

func (s *Server) oobReady(w http.ResponseWriter) bool {
	if s.oob == nil {
		writeError(w, http.StatusServiceUnavailable, "oob service not available")
		return false
	}
	return true
}

type oobConfigBody struct {
	Enabled     bool   `json:"enabled"`
	ReplyBudget int    `json:"reply_budget"`
	HostSocket  string `json:"host_socket"`
}

type oobPeerView struct {
	PeerID     uint16            `json:"peer_id"`
	PeerIDHex  string            `json:"peer_id_hex"`
	Alias      string            `json:"alias"`
	SignerID   string            `json:"signer_id,omitempty"`
	KeySource  string            `json:"key_source"`
	LocalRole  string            `json:"local_role"`
	Role       string            `json:"role"`
	Enabled    bool              `json:"enabled"`
	Addresses  map[string]string `json:"addresses"`
	EncPolicy  map[string]bool   `json:"enc_policy"`
	TxCounter  uint32            `json:"tx_counter"`
	RxHigh     uint32            `json:"rx_high"`
	LastSeenAt *string           `json:"last_seen_at,omitempty"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
}

func oobPeerToView(p database.OOBPeer) oobPeerView {
	v := oobPeerView{
		PeerID: p.PeerID, PeerIDHex: strconv.FormatUint(uint64(p.PeerID), 16), Alias: p.Alias, SignerID: p.SignerID,
		KeySource: p.KeySource, Role: p.Role, Enabled: p.Enabled, TxCounter: p.TxCounter, RxHigh: p.RxHigh,
		LastSeenAt: p.LastSeenAt, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Addresses: map[string]string{}, EncPolicy: map[string]bool{},
	}
	if p.LocalRole == int(oob.RoleImporter) {
		v.LocalRole = "importer"
	} else {
		v.LocalRole = "issuer"
	}
	_ = json.Unmarshal([]byte(p.Addresses), &v.Addresses)
	_ = json.Unmarshal([]byte(p.EncPolicy), &v.EncPolicy)
	return v
}

func oobPeerID(r *http.Request) (uint16, error) {
	n, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 16)
	if err != nil || n == 0 {
		return 0, errors.New("invalid peer id")
	}
	return uint16(n), nil
}

// handleGetOOBConfig returns the OOB management configuration.
// @Summary Get OOB management config
// @Description Enabled flag, per-peer reply budget per hour and the host agent socket path
// @Tags oob
// @Success 200 {object} oobConfigBody
// @Failure 503 {object} map[string]string
// @Router /api/oob/config [get]
func (s *Server) handleGetOOBConfig(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	c := s.oob.Config()
	writeJSON(w, http.StatusOK, oobConfigBody{Enabled: c.Enabled, ReplyBudget: c.ReplyBudgetHour, HostSocket: c.HostSocket})
}

// handleSetOOBConfig updates the OOB management configuration.
// @Summary Set OOB management config
// @Description Persists the enabled flag, reply budget and host agent socket path in system_config
// @Tags oob
// @Accept json
// @Param body body oobConfigBody true "Configuration"
// @Success 200 {object} oobConfigBody
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/oob/config [put]
func (s *Server) handleSetOOBConfig(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	var body oobConfigBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.oob.SetConfig(oob.Config{Enabled: body.Enabled, ReplyBudgetHour: body.ReplyBudget, HostSocket: body.HostSocket}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	c := s.oob.Config()
	writeJSON(w, http.StatusOK, oobConfigBody{Enabled: c.Enabled, ReplyBudget: c.ReplyBudgetHour, HostSocket: c.HostSocket})
}

// handleListOOBPeers lists management peers.
// @Summary List OOB peers
// @Tags oob
// @Success 200 {array} oobPeerView
// @Failure 503 {object} map[string]string
// @Router /api/oob/peers [get]
func (s *Server) handleListOOBPeers(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	peers, err := s.oob.ListPeers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]oobPeerView, 0, len(peers))
	for _, p := range peers {
		out = append(out, oobPeerToView(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateOOBPeer provisions a peer on the random-key or ECDH path.
// @Summary Create OOB peer
// @Description source "random" (default) stores a fresh key for a QR bundle; source "ecdh" derives the key from the trusted peer's announce (signer_id required)
// @Tags oob
// @Accept json
// @Param body body oob.PeerSpec true "Peer"
// @Success 201 {object} oobPeerView
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/oob/peers [post]
func (s *Server) handleCreateOOBPeer(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	var spec oob.PeerSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	p, err := s.oob.CreatePeer(spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, oobPeerToView(*p))
}

type oobPeerUpdateBody struct {
	oob.PeerSpec
	Enabled *bool `json:"enabled,omitempty"`
}

// handleUpdateOOBPeer updates role, enabled flag, addresses and encryption policy.
// @Summary Update OOB peer
// @Tags oob
// @Accept json
// @Param id path int true "Peer id"
// @Param body body oobPeerUpdateBody true "Fields to change"
// @Success 200 {object} oobPeerView
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/oob/peers/{id} [put]
func (s *Server) handleUpdateOOBPeer(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	id, err := oobPeerID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var body oobPeerUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	p, err := s.oob.UpdatePeer(id, body.PeerSpec, body.Enabled)
	if errors.Is(err, database.ErrOOBPeerNotFound) {
		writeError(w, http.StatusNotFound, "peer not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, oobPeerToView(*p))
}

// handleDeleteOOBPeer revokes the peer's key and removes it.
// @Summary Delete OOB peer
// @Tags oob
// @Param id path int true "Peer id"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/oob/peers/{id} [delete]
func (s *Server) handleDeleteOOBPeer(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	id, err := oobPeerID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.oob.DeletePeer(id); err != nil {
		if errors.Is(err, database.ErrOOBPeerNotFound) {
			writeError(w, http.StatusNotFound, "peer not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

type oobBundleBody struct {
	IssuerAlias string `json:"issuer_alias,omitempty"`
}

// handleOOBPeerBundle issues the peer's management key in a signed QR bundle.
// @Summary Issue OOB key bundle
// @Description Wraps the peer's key as a mgmt entry under this kit's alias; the other side imports it with POST /api/keys/import. Only bundle-sourced keys can be issued.
// @Tags oob
// @Accept json
// @Param id path int true "Peer id"
// @Param body body oobBundleBody false "Issuer alias override"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/oob/peers/{id}/bundle [post]
func (s *Server) handleOOBPeerBundle(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	id, err := oobPeerID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var body oobBundleBody
	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	url, err := s.oob.IssueBundle(id, body.IssuerAlias)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// handleOOBPeerBundleQR renders the peer's key bundle as a QR code.
// @Summary OOB key bundle QR
// @Tags oob
// @Produce png
// @Param id path int true "Peer id"
// @Param issuer_alias query string false "Issuer alias override"
// @Success 200 {file} binary
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/oob/peers/{id}/bundle/qr [get]
func (s *Server) handleOOBPeerBundleQR(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	id, err := oobPeerID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	url, err := s.oob.IssueBundle(id, r.URL.Query().Get("issuer_alias"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 256)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "qr encode: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

type oobSendBody struct {
	PeerID  uint16      `json:"peer_id"`
	Via     string      `json:"via"`
	Cmd     string      `json:"cmd"`
	Args    oob.ArgSpec `json:"args"`
	NoReply bool        `json:"noreply,omitempty"`
	Encrypt *bool       `json:"encrypt,omitempty"`
}

// handleOOBSend originates a management command to a peer over a bearer.
// @Summary Send OOB command
// @Description Frames the command, queues it through the delivery ledger to the peer's address on the bearer and returns the delivery id and the text form
// @Tags oob
// @Accept json
// @Param body body oobSendBody true "Command"
// @Success 202 {object} oob.SendResult
// @Failure 400 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/oob/send [post]
func (s *Server) handleOOBSend(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	var body oobSendBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	cmd, ok := oob.CommandByName(body.Cmd)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown command")
		return
	}
	args, err := oob.BuildArgs(cmd.Code, body.Args)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.oob.Send(r.Context(), oob.SendRequest{PeerID: body.PeerID, Via: body.Via, Cmd: cmd.Code, Args: args, NoReply: body.NoReply, Encrypt: body.Encrypt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

// handleGetOOBLog returns recent frame events, newest first.
// @Summary OOB log
// @Tags oob
// @Param limit query int false "Max rows (default 100)"
// @Param peer_id query int false "Filter by peer id (0 = Hub and local origins)"
// @Success 200 {array} database.OOBLogEntry
// @Failure 503 {object} map[string]string
// @Router /api/oob/log [get]
func (s *Server) handleGetOOBLog(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	peer := -1
	if v := r.URL.Query().Get("peer_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			peer = n
		}
	}
	entries, err := s.db.ListOOBLog(limit, peer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []database.OOBLogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleGetOOBTargets returns the command, target and log-unit tables.
// @Summary OOB tables
// @Description Commands with their minimum role, reset targets with the levels this kit can act on, and the LOG unit names
// @Tags oob
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]string
// @Router /api/oob/targets [get]
func (s *Server) handleGetOOBTargets(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"commands": oob.Commands,
		"targets":  s.oob.TargetsInfo(),
		"units":    oob.LogUnits,
		"reverts":  s.oob.PendingReverts(),
	})
}

// handleGetOOBAgent reports the host agent state.
// @Summary OOB host agent status
// @Tags oob
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]string
// @Router /api/oob/agent [get]
func (s *Server) handleGetOOBAgent(w http.ResponseWriter, r *http.Request) {
	if !s.oobReady(w) {
		return
	}
	available, version, socket := s.oob.AgentStatus()
	writeJSON(w, http.StatusOK, map[string]any{"available": available, "version": version, "socket": socket})
}
