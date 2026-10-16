package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/database"
)

// The tariff API (B9): a new version per POST, refusals for what cannot be
// a price. [MESHSAT-1584]
func TestTariffAPI(t *testing.T) {
	s := newTestServerWithDB(t)
	_, _ = s.db.Exec(`DELETE FROM interfaces WHERE id IN ('cellular_0', 'iridium_imt_0')`)
	for _, i := range []struct{ id, ct string }{{"cellular_0", "cellular"}, {"iridium_imt_0", "iridium_imt"}} {
		if err := s.db.InsertInterface(&database.Interface{ID: i.id, ChannelType: i.ct, Label: i.id, Enabled: true,
			Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
			t.Fatal(err)
		}
	}
	router := s.Router()
	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/delivery/tariffs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rr, req)
		return rr
	}
	for _, bad := range []string{
		`{"unit_kind":"sms","price_minor":3}`,
		`{"interface_id":"cellular_0","price_minor":3}`,
		`{"interface_id":"cellular_0","unit_kind":"sms","price_minor":0}`,
		`{"interface_id":"cellular_0","unit_kind":"sms","price_minor":-1}`,
		`{"interface_id":"cellular_0","unit_kind":"sms","price_minor":3,"currency":"euro"}`,
		`{"interface_id":"nope_0","unit_kind":"sms","price_minor":3}`,
	} {
		if rr := post(bad); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d; want 400", bad, rr.Code)
		}
	}
	rr := post(`{"interface_id":"cellular_0","unit_kind":"sms","price_minor":3}`)
	var got database.Tariff
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if rr.Code != http.StatusCreated || got.Version != 1 || got.MinUnits != 1 || got.Currency != "EUR" || got.PriceMinor != 3 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := post(`{"interface_id":"iridium_imt_0","unit_kind":"imt_credit","unit_bytes":50,"price_minor":17}`); rr.Code != http.StatusCreated {
		t.Fatalf("imt: %d %s", rr.Code, rr.Body.String())
	}
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest("GET", "/api/delivery/tariffs", nil))
	var all []database.Tariff
	_ = json.Unmarshal(list.Body.Bytes(), &all)
	if list.Code != http.StatusOK || len(all) != 2 {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
}
