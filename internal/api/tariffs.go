package api

import (
	"encoding/json"
	"net/http"

	"meshsat/internal/database"
)

// Delivery-intent tariffs (HeMB-NG r3 D8, B9): what one submission on a
// paid interface costs. A paid route with no tariff is never priced at
// zero; it is skipped (skipped_no_price). [MESHSAT-1584]

type tariffRequest struct {
	InterfaceID string `json:"interface_id"`
	UnitKind    string `json:"unit_kind"`           // e.g. sms, imt_credit, sbd_credit
	UnitBytes   int    `json:"unit_bytes"`          // bytes per unit; 0 = one unit per submission
	MinUnits    int    `json:"min_units,omitempty"` // at least this many per submission (default 1, at most 10000)
	PriceMinor  int64  `json:"price_minor"`         // per unit, in minor units (cents), at most 1000000
	Currency    string `json:"currency,omitempty"`  // EUR, the default and the only one this release prices in
	Free        bool   `json:"free,omitempty"`      // the interface costs nothing per submission
}

// handleListTariffs lists every tariff version.
// @Summary List delivery tariffs
// @Description Every tariff version per interface, newest first. The newest
// @Description valid one prices a delivery intent's paid route at admission
// @Description and again before each send.
// @Tags deliveries
// @Produce json
// @Success 200 {array} database.Tariff
// @Failure 500 {object} map[string]string
// @Router /api/delivery/tariffs [get]
func (s *Server) handleListTariffs(w http.ResponseWriter, r *http.Request) {
	ts, err := s.db.ListTariffs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ts == nil {
		ts = []database.Tariff{}
	}
	writeJSON(w, http.StatusOK, ts)
}

// handleCreateTariff adds a new version of an interface's tariff.
// @Summary Set a delivery tariff
// @Description Adds a new version of an interface's tariff, valid now; the
// @Description earlier versions stay for the record. A submission costs
// @Description max(min_units, ceil(bytes / unit_bytes)) units (one unit per
// @Description submission with unit_bytes 0), each at price_minor. EUR only:
// @Description the intents' cap is in EUR and nothing is converted. Bounds:
// @Description price_minor at most 1000000, min_units at most 10000,
// @Description unit_bytes at most 1048576.
// @Tags deliveries
// @Accept json
// @Produce json
// @Param body body tariffRequest true "Tariff"
// @Success 201 {object} database.Tariff
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/delivery/tariffs [post]
func (s *Server) handleCreateTariff(w http.ResponseWriter, r *http.Request) {
	var req tariffRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Currency == "" {
		req.Currency = "EUR"
	}
	if req.MinUnits == 0 {
		req.MinUnits = 1
	}
	switch {
	case req.InterfaceID == "":
		writeError(w, http.StatusBadRequest, "interface_id is required")
		return
	case req.UnitKind == "":
		writeError(w, http.StatusBadRequest, "unit_kind is required")
		return
	case req.UnitBytes < 0 || req.MinUnits < 1 || req.PriceMinor < 0:
		writeError(w, http.StatusBadRequest, "unit_bytes and price_minor may not be negative, min_units is at least 1")
		return
	case !req.Free && req.PriceMinor == 0:
		writeError(w, http.StatusBadRequest, "a paid tariff needs a price above zero; set free for an interface that costs nothing")
		return
	}
	// The engine's own rule for a usable tariff (EUR only, bounded, no
	// cost past int64; B9SMS-R3, R5). [MESHSAT-1584]
	if err := (database.Tariff{UnitKind: req.UnitKind, UnitBytes: req.UnitBytes, MinUnits: req.MinUnits,
		PriceMinor: req.PriceMinor, Currency: req.Currency, Free: req.Free}).Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.db.GetInterface(req.InterfaceID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown interface "+req.InterfaceID)
		return
	}
	t, err := s.db.InsertTariff(database.Tariff{InterfaceID: req.InterfaceID, UnitKind: req.UnitKind, UnitBytes: req.UnitBytes,
		MinUnits: req.MinUnits, PriceMinor: req.PriceMinor, Currency: req.Currency, Free: req.Free})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}
