package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/database"
)

func TestReviewB9TariffAPIBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"eur_control", `{"interface_id":"cellular_0","unit_kind":"sms","price_minor":3,"currency":"EUR"}`, 201},
		{"gbp_without_conversion", `{"interface_id":"cellular_0","unit_kind":"sms","price_minor":3,"currency":"GBP"}`, 400},
		{"overflowing_minimum_price", `{"interface_id":"cellular_0","unit_kind":"sms","price_minor":9223372036854775807,"min_units":2}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServerWithDB(t)
			_, _ = s.db.Exec(`DELETE FROM interfaces WHERE id = 'cellular_0'`)
			if err := s.db.InsertInterface(&database.Interface{ID: "cellular_0", ChannelType: "cellular", Label: "review", Enabled: true, Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/delivery/tariffs", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			s.Router().ServeHTTP(rr, req)
			t.Logf("HTTP=%d body=%s", rr.Code, rr.Body.String())
			if rr.Code != tc.want {
				t.Fatalf("HTTP=%d; want %d", rr.Code, tc.want)
			}
		})
	}
}
