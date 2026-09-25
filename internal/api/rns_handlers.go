package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/rns"
)

// rnsStatusResponse is the /api/rns/status body.
type rnsStatusResponse struct {
	Enabled     bool      `json:"enabled"`
	TransportID string    `json:"transport_id"`
	Paths       int       `json:"paths"`
	Links       int       `json:"links"`
	Stats       rns.Stats `json:"stats"`
}

// rnsLinkResponse describes one link.
type rnsLinkResponse struct {
	ID        string  `json:"id"`
	Dest      string  `json:"dest"`
	State     string  `json:"state"`
	Initiator bool    `json:"initiator"`
	Interface string  `json:"interface"`
	RTTMs     float64 `json:"rtt_ms"`
	MTU       int     `json:"mtu"`
	Remote    string  `json:"remote_identity,omitempty"`
}

func parseHash16(s string) ([16]byte, bool) {
	var out [16]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return out, false
	}
	copy(out[:], b)
	return out, true
}

// handleRNSStatus reports the Reticulum node state.
// @Summary Reticulum node status
// @Tags rns
// @Produce json
// @Success 200 {object} rnsStatusResponse
// @Router /api/rns/status [get]
func (s *Server) handleRNSStatus(w http.ResponseWriter, r *http.Request) {
	if s.rnsNode == nil {
		writeJSON(w, http.StatusOK, rnsStatusResponse{Enabled: false})
		return
	}
	tid := s.rnsNode.IdentityHash()
	writeJSON(w, http.StatusOK, rnsStatusResponse{
		Enabled: true, TransportID: hex.EncodeToString(tid[:]),
		Paths: s.rnsNode.Paths().Count(), Links: len(s.rnsNode.Links().All()), Stats: s.rnsNode.Stats(),
	})
}

// handleRNSPaths lists the path table.
// @Summary List Reticulum paths
// @Tags rns
// @Produce json
// @Success 200 {array} rns.PathInfo
// @Router /api/rns/paths [get]
func (s *Server) handleRNSPaths(w http.ResponseWriter, r *http.Request) {
	if s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	entries := s.rnsNode.Paths().All()
	out := make([]rns.PathInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Info())
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRNSRequestPath sends a path request and waits up to 15 s.
// @Summary Request a path to a destination
// @Tags rns
// @Produce json
// @Param dest path string true "destination hash (32 hex)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Router /api/rns/paths/{dest}/request [post]
func (s *Server) handleRNSRequestPath(w http.ResponseWriter, r *http.Request) {
	if s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	dest, ok := parseHash16(chi.URLParam(r, "dest"))
	if !ok {
		writeError(w, http.StatusBadRequest, "dest must be 32 hex characters")
		return
	}
	found := s.rnsNode.RequestPath(r.Context(), dest, 15*time.Second)
	resp := map[string]interface{}{"dest": chi.URLParam(r, "dest"), "found": found}
	if e := s.rnsNode.Paths().Get(dest); e != nil {
		resp["path"] = e.Info()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRNSDeletePath forgets a path.
// @Summary Forget a Reticulum path
// @Tags rns
// @Param dest path string true "destination hash (32 hex)"
// @Success 204
// @Router /api/rns/paths/{dest} [delete]
func (s *Server) handleRNSDeletePath(w http.ResponseWriter, r *http.Request) {
	if s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	dest, ok := parseHash16(chi.URLParam(r, "dest"))
	if !ok {
		writeError(w, http.StatusBadRequest, "dest must be 32 hex characters")
		return
	}
	s.rnsNode.Paths().Remove(dest)
	if s.db != nil {
		_ = s.db.DeleteRNSPath(chi.URLParam(r, "dest"))
	}
	w.WriteHeader(http.StatusNoContent)
}

func rnsLinkView(l *rns.Link) rnsLinkResponse {
	out := rnsLinkResponse{ID: hex.EncodeToString(l.ID[:]), Dest: hex.EncodeToString(l.Dest[:]), State: l.State().String(),
		Initiator: l.Initiator, Interface: l.Iface, RTTMs: float64(l.RTT()) / float64(time.Millisecond), MTU: l.MTU()}
	if ri := l.RemoteIdentity(); ri != nil {
		out.Remote = hex.EncodeToString(ri)
	}
	return out
}

// handleRNSLinks lists pending and active links.
// @Summary List Reticulum links
// @Tags rns
// @Produce json
// @Success 200 {array} rnsLinkResponse
// @Router /api/rns/links [get]
func (s *Server) handleRNSLinks(w http.ResponseWriter, r *http.Request) {
	if s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	links := s.rnsNode.Links().All()
	out := make([]rnsLinkResponse, 0, len(links))
	for _, l := range links {
		out = append(out, rnsLinkView(l))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRNSOpenLink opens a link to a known destination.
// @Summary Open a Reticulum link
// @Tags rns
// @Accept json
// @Produce json
// @Param body body map[string]string true "{\"dest\": \"<32 hex>\"}"
// @Success 200 {object} rnsLinkResponse
// @Failure 400 {object} map[string]string
// @Router /api/rns/links [post]
func (s *Server) handleRNSOpenLink(w http.ResponseWriter, r *http.Request) {
	if s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	var body struct {
		Dest string `json:"dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	dest, ok := parseHash16(body.Dest)
	if !ok {
		writeError(w, http.StatusBadRequest, "dest must be 32 hex characters")
		return
	}
	l, err := s.rnsNode.Links().Initiate(dest)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rnsLinkView(l))
}

// handleRNSCloseLink tears a link down.
// @Summary Close a Reticulum link
// @Tags rns
// @Param id path string true "link id (32 hex)"
// @Success 204
// @Failure 404 {object} map[string]string
// @Router /api/rns/links/{id} [delete]
func (s *Server) handleRNSCloseLink(w http.ResponseWriter, r *http.Request) {
	if s.rnsNode == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	id, ok := parseHash16(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "id must be 32 hex characters")
		return
	}
	l := s.rnsNode.Links().Get(id)
	if l == nil {
		writeError(w, http.StatusNotFound, "no such link")
		return
	}
	s.rnsNode.Links().Teardown(l)
	w.WriteHeader(http.StatusNoContent)
}

// handleRNSAnnounce announces the bridge destination now.
// @Summary Announce the bridge destination now
// @Tags rns
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/rns/announce [post]
func (s *Server) handleRNSAnnounce(w http.ResponseWriter, r *http.Request) {
	if s.rnsNode == nil || s.routingID == nil {
		writeError(w, http.StatusServiceUnavailable, "rns node not running")
		return
	}
	d := s.rnsNode.LocalDestination(s.routingID.DestHash())
	if d == nil {
		writeError(w, http.StatusServiceUnavailable, "bridge destination not registered")
		return
	}
	if err := s.rnsNode.Announce(d, "", false); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "announced", "dest_hash": s.routingID.DestHashHex()})
}
