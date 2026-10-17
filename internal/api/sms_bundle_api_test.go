package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/gateway"
)

// A top-up that cannot be stored answers 500 and changes nothing; a bad
// value stays a 400 (B9FIX-R4). [MESHSAT-1584]
func TestSMSBundlePut_StorageFailure(t *testing.T) {
	s := newTestServerWithDB(t)
	s.smsBudget = gateway.NewSMSBudget(s.db, "kitA", gateway.SMSBudgetOptions{}, nil)
	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/cellular/bundle", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		s.Router().ServeHTTP(w, req)
		return w
	}
	if w := put(`{"size":250,"sent":10}`); w.Code != http.StatusOK {
		t.Fatalf("top-up: %d %s", w.Code, w.Body.String())
	}
	if w := put(`{"size":-1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad size: %d; want 400", w.Code)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_size BEFORE INSERT ON system_config
		WHEN NEW.key = 'sms_bundle_size' BEGIN SELECT RAISE(ABORT, 'size write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if w := put(`{"size":500,"sent":0}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("unstored top-up: %d %s; want 500", w.Code, w.Body.String())
	}
	if st := s.smsBudget.Status(); st.Size != 250 || st.Sent != 10 {
		t.Fatalf("after the failed top-up: %+v; want 250 / 10", st)
	}
}
