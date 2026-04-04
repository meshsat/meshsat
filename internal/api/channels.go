package api

import "net/http"

// @Summary List transport channels
// @Description Returns all registered transport channel types with capabilities
// @Tags interfaces
// @Produce json
// @Success 200 {array} object
// @Router /api/transport/channels [get]
func (s *Server) handleGetChannels(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusOK, []struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, s.registry.List())
}
