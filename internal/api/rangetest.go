package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleSendRangeTest sends a range test packet.
// @Summary Send range test
// @Description Sends a range test packet to the mesh (portnum 66)
// @Tags range_test
// @Accept json
// @Param body body object{text=string,to=uint32} false "Range test text and optional destination"
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/range-test/send [post]
func (s *Server) handleSendRangeTest(w http.ResponseWriter, r *http.Request) {
	if s.mesh == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh transport unavailable")
		return
	}

	var req struct {
		Text string `json:"text"`
		To   uint32 `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Text == "" {
		req.Text = fmt.Sprintf("RT %d", time.Now().Unix())
	}

	if err := s.mesh.SendRangeTest(r.Context(), req.Text, req.To); err != nil {
		writeError(w, http.StatusInternalServerError, "range test send failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "range test sent"})
}

// handleGetRangeTests returns range test history.
// @Summary Get range test history
// @Description Returns stored range test results
// @Tags range_test
// @Param limit query int false "Max records (default 100)"
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/range-test [get]
func (s *Server) handleGetRangeTests(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r.URL.Query().Get("limit"), 100)

	records, err := s.db.GetRangeTests(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to query range tests: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"range_tests": records,
	})
}
