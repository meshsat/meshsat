package api

import (
	"net/http"
	"strconv"
	"time"
)

// @Summary Delivery outcome statistics of the delivery intents
// @Description What became of each attempt of a delivery intent (HeMB-NG
// @Description stage 3, S3-3), per bearer, interface and size bucket (wire
// @Description bytes: 1-50, 51-100, 101-160, 161-340, 341-1024, >1024), for
// @Description the attempts made in the window. Every attempt is in exactly
// @Description one stage, so each row's stages add up to its attempts, the
// @Description denominator: completed (by receipt kind, a receipt matched to
// @Description this attempt alone), accepted (by the bearer, no receipt yet),
// @Description unknown (may have been sent), not_sent (refused before it left),
// @Description withdrawn (superseded or cancelled), unresolved (still in the
// @Description ledger; unresolved_handed_off of them wait on the bearer), and
// @Description other (any other status, by name). retries_spent counts the
// @Description transmissions that ended not sent and were put back for another
// @Description go (a refusal before any transmission spends none). This is not
// @Description radio loss: a local
// @Description refusal never reached the air, and an unknown is not proven data
// @Description loss. intents counts the intents made in the window by outcome.
// @Description Sends made with MESHSAT_DELIVERY_INTENTS off are not here
// @Description (GET /api/deliveries/stats counts the legacy ledger).
// @Tags deliveries
// @Produce json
// @Param since query string false "Window start, RFC 3339 (default: until minus 24 h)"
// @Param until query string false "Window end, RFC 3339, exclusive (default: the next whole second)"
// @Param hours query int false "Window length in hours when since is not given (1 to 8760)"
// @Success 200 {object} database.DeliveryOutcomeStats
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/delivery/stats [get]
func (s *Server) handleDeliveryOutcomeStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// The ledger keeps whole seconds and the window's end is exclusive: by
	// default it ends at the next whole second, so what was written this
	// second is in it.
	until := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "until: "+err.Error())
			return
		}
		until = t
	}
	since := until.Add(-24 * time.Hour)
	if v := q.Get("hours"); v != "" {
		h, err := strconv.Atoi(v)
		if err != nil || h < 1 || h > 8760 {
			writeError(w, http.StatusBadRequest, "hours: 1 to 8760")
			return
		}
		since = until.Add(-time.Duration(h) * time.Hour)
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since: "+err.Error())
			return
		}
		since = t
	}
	if !since.Before(until) {
		writeError(w, http.StatusBadRequest, "since must be before until")
		return
	}
	stats, err := s.db.DeliveryOutcomeStatsBetween(since, until)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
