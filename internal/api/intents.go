package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/engine"
)

// Delivery intents: the status of a send-to-contact made with
// MESHSAT_DELIVERY_INTENTS on (architecture r3 D11). [MESHSAT-1581]

func (s *Server) escalator() *engine.Escalator {
	if s.dispatcher == nil {
		return nil
	}
	return s.dispatcher.Escalator()
}

type intentCancelRequest struct {
	Reason string `json:"reason,omitempty"`
}

// @Summary Get a delivery intent
// @Description One send-to-contact recipient's delivery intent: its outcome
// @Description in words (delivered to device, accepted by the recipient's
// @Description Reticulum node, accepted but not confirmed, may have been
// @Description sent, not sent, expired, cancelled), notes (possible
// @Description duplicate, may still be sent), the frozen route plan and,
// @Description per route, every attempt with its transmissions and the
// @Description evidence that came back. Exists only for sends made with
// @Description MESHSAT_DELIVERY_INTENTS on.
// @Tags deliveries
// @Produce json
// @Param id path string true "Intent ID"
// @Success 200 {object} engine.IntentView
// @Failure 404 {object} map[string]string
// @Router /api/deliveries/intents/{id} [get]
func (s *Server) handleGetIntent(w http.ResponseWriter, r *http.Request) {
	snap, err := s.db.LoadIntentSnapshot(chi.URLParam(r, "id"))
	if errors.Is(err, database.ErrIntentNotFound) {
		writeError(w, http.StatusNotFound, "delivery intent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, engine.BuildIntentView(snap))
}

// @Summary List delivery intents
// @Description The newest delivery intents, for one contact with
// @Description ?contact=<contact id>, each with its outcome in words.
// @Tags deliveries
// @Produce json
// @Param contact query string false "Contact ID"
// @Param limit query int false "Maximum number of intents (default 50, at most 500)"
// @Success 200 {array} engine.IntentView
// @Failure 500 {object} map[string]string
// @Router /api/deliveries/intents [get]
func (s *Server) handleListIntents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.db.ListIntents(r.URL.Query().Get("contact"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]engine.IntentView, 0, len(list))
	for _, in := range list {
		snap, err := s.db.LoadIntentSnapshot(in.ID)
		if err != nil {
			continue
		}
		out = append(out, engine.BuildIntentView(snap))
	}
	writeJSON(w, http.StatusOK, out)
}

// @Summary Cancel a delivery intent
// @Description Closes an active delivery intent: attempts not yet claimed
// @Description are cancelled and nothing more is sent. An attempt already
// @Description being sent finishes and is recorded; the status then says
// @Description it may still be sent. The outcome keeps the strongest
// @Description evidence (a confirmation stays a confirmation); cancelled
// @Description replaces only "not sent".
// @Tags deliveries
// @Accept json
// @Produce json
// @Param id path string true "Intent ID"
// @Param body body intentCancelRequest false "Reason"
// @Success 200 {object} engine.IntentView
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/deliveries/intents/{id}/cancel [post]
func (s *Server) handleCancelIntent(w http.ResponseWriter, r *http.Request) {
	esc := s.escalator()
	if esc == nil {
		writeError(w, http.StatusServiceUnavailable, "delivery intents are not running")
		return
	}
	var req intentCancelRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // the body is optional
	}
	id := chi.URLParam(r, "id")
	if _, err := esc.Cancel(id, req.Reason); err != nil {
		if errors.Is(err, database.ErrIntentNotFound) {
			writeError(w, http.StatusNotFound, "delivery intent not found")
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	snap, err := s.db.LoadIntentSnapshot(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, engine.BuildIntentView(snap))
}
