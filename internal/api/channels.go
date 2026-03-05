package api

import "net/http"

func (s *Server) handleGetChannels(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusOK, []struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, s.registry.List())
}
