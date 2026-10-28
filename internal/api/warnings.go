package api

import (
	"net/http"
	"strconv"

	"meshsat/internal/database"
)

// handleListWarnings lists the public warnings the Hub relayed here.
// @Summary List relayed public warnings
// @Description The official public warnings the Hub handed this Bridge for its area (signed digests verified with the pinned Hub directory key), newest first: id, message type, source, event, severity, when it expires, the text, how it arrived (hub or a satellite link), what it went out on (mesh ALERT_APP, mesh text, APRS bulletin) and whether a Cancel or an Update took it back. [MESHSAT-1734]
// @Tags warnings
// @Produce json
// @Param limit query int false "Most rows to answer (default 100, max 500)"
// @Success 200 {array} database.Warning
// @Failure 500 {object} map[string]string
// @Router /api/warnings [get]
func (s *Server) handleListWarnings(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	list, err := s.db.ListWarnings(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list warnings")
		return
	}
	if list == nil {
		list = []database.Warning{}
	}
	writeJSON(w, http.StatusOK, list)
}
