package api

import (
	"net/http"
	"strconv"
	"time"

	"meshsat/internal/spectrum"
)

// Spectrum history endpoints. Thin shims over the HistoryStore on the
// DB — the monitor writes during scans, these handlers read for the
// main-page prefill (minutes= form) and the per-band detail view
// (from=/to= range form). Both always return the rows newest-first so
// the UI can treat the list as a drop-in replacement for its live SSE
// ring. [MESHSAT-650]

// handleGetSpectrumHistory returns stored scan samples for one band.
//
// Two forms:
//   - `?band=<name>&minutes=<n>` — last N minutes, used by the main
//     page to seed its 5-minute waterfall ring on load.
//   - `?band=<name>&from=<ts_ms>&to=<ts_ms>&max_rows=<n>` — explicit
//     time range, used by the detail view. Server caps rows at 5000
//     (and defaults to 2000) so an operator asking for a 7-day window
//     still gets a response that fits in a browser buffer; the UI
//     down-samples further for render.
//
// @Summary Stored spectrum scan history
// @Description Replay persisted scan rows — seeds the waterfall on page load and powers the detail view
// @Tags spectrum
// @Param band query string true "Band name (lora_868, aprs_144, gps_l1, lte_b20_dl, lte_b8_dl)"
// @Param minutes query int false "Last N minutes (mutually exclusive with from/to)"
// @Param from query int false "Range start, unix milliseconds"
// @Param to query int false "Range end, unix milliseconds"
// @Param max_rows query int false "Row cap (default 2000, max 5000)"
// @Success 200 {object} map[string]interface{}
// @Router /api/spectrum/history [get]
func (s *Server) handleGetSpectrumHistory(w http.ResponseWriter, r *http.Request) {
	band := r.URL.Query().Get("band")
	if band == "" {
		http.Error(w, "band query parameter required", http.StatusBadRequest)
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"band": band, "rows": []interface{}{}})
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	// minutes= form — fast path for the page-load prefill
	if minStr := q.Get("minutes"); minStr != "" {
		mins, err := strconv.Atoi(minStr)
		if err != nil || mins <= 0 {
			http.Error(w, "invalid minutes value", http.StatusBadRequest)
			return
		}
		// Cap minutes at the max retention window so an operator asking
		// for "minutes=99999" doesn't race against retention trim —
		// LoadScansRange is more appropriate for wide windows anyway.
		if mins > spectrum.MaxRetentionHours*60 {
			mins = spectrum.MaxRetentionHours * 60
		}
		rows, err := s.db.LoadScansByMinutes(ctx, band, mins)
		if err != nil {
			http.Error(w, "load history: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"band": band,
			"rows": rows,
		})
		return
	}

	// from/to range form — detail view
	from, to, ok := parseRangeMs(q.Get("from"), q.Get("to"))
	if !ok {
		http.Error(w, "minutes= OR from= and to= query params required (unix ms)", http.StatusBadRequest)
		return
	}
	maxRows := 2000
	if mr := q.Get("max_rows"); mr != "" {
		if v, err := strconv.Atoi(mr); err == nil && v > 0 {
			maxRows = v
			if maxRows > 5000 {
				maxRows = 5000
			}
		}
	}
	rows, err := s.db.LoadScansRange(ctx, band, from, to, maxRows)
	if err != nil {
		http.Error(w, "load history: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"band":     band,
		"from_ms":  from.UnixMilli(),
		"to_ms":    to.UnixMilli(),
		"max_rows": maxRows,
		"rows":     rows,
	})
}

// handleGetSpectrumTransitions returns stored state-change events for
// one band within a time range. The UI overlays these as vertical
// markers on the detail-view waterfall (red for jamming, amber for
// interference). Always range-bounded because a long quiet kit can
// accumulate hundreds of transitions over a week and the UI only
// needs the ones visible in the current window.
//
// @Summary Stored spectrum state transitions (alert markers)
// @Tags spectrum
// @Param band query string true "Band name"
// @Param from query int true "Range start, unix milliseconds"
// @Param to query int true "Range end, unix milliseconds"
// @Success 200 {object} map[string]interface{}
// @Router /api/spectrum/transitions [get]
func (s *Server) handleGetSpectrumTransitions(w http.ResponseWriter, r *http.Request) {
	band := r.URL.Query().Get("band")
	if band == "" {
		http.Error(w, "band query parameter required", http.StatusBadRequest)
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"band": band, "rows": []interface{}{}})
		return
	}
	from, to, ok := parseRangeMs(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if !ok {
		http.Error(w, "from= and to= query params required (unix ms)", http.StatusBadRequest)
		return
	}
	rows, err := s.db.LoadTransitionsRange(r.Context(), band, from, to)
	if err != nil {
		http.Error(w, "load transitions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"band":    band,
		"from_ms": from.UnixMilli(),
		"to_ms":   to.UnixMilli(),
		"rows":    rows,
	})
}

// parseRangeMs parses unix-millisecond strings into [from, to]. Rejects
// reversed ranges and windows narrower than one second — both are
// almost certainly operator typos and would return empty data anyway.
func parseRangeMs(fromStr, toStr string) (time.Time, time.Time, bool) {
	if fromStr == "" || toStr == "" {
		return time.Time{}, time.Time{}, false
	}
	fromMs, err1 := strconv.ParseInt(fromStr, 10, 64)
	toMs, err2 := strconv.ParseInt(toStr, 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, false
	}
	if toMs-fromMs < 1000 {
		return time.Time{}, time.Time{}, false
	}
	return time.UnixMilli(fromMs), time.UnixMilli(toMs), true
}
