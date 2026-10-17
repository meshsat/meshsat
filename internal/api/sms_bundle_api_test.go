package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// The bundle's end through the API: valid_days from now, expires_at
// exactly, "" forgets it, bad values 400. [MESHSAT-1584]
func TestSMSBundlePut_Expiry(t *testing.T) {
	s := newTestServerWithDB(t)
	s.smsBudget = gateway.NewSMSBudget(s.db, "kitA", gateway.SMSBudgetOptions{}, nil)
	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/cellular/bundle", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		s.Router().ServeHTTP(w, req)
		return w
	}
	before := time.Now()
	if w := put(`{"size":250,"sent":1,"valid_days":31}`); w.Code != http.StatusOK {
		t.Fatalf("top-up with valid_days: %d %s", w.Code, w.Body.String())
	}
	st := s.smsBudget.Status()
	if st.ExpiresAt == nil || st.ExpiresAt.Before(before.Add(31*24*time.Hour-time.Minute)) || st.ExpiresAt.After(time.Now().Add(31*24*time.Hour)) {
		t.Fatalf("valid_days 31: %+v", st)
	}
	if st.ExpiresAt.Nanosecond() != 0 {
		t.Fatalf("valid_days end %s; want whole seconds, as stored (B9EXP-R1)", st.ExpiresAt.Format(time.RFC3339Nano))
	}
	var got struct {
		Bundle struct {
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"bundle"`
	}
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("GET", "/api/cellular/bundle", nil))
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil || !got.Bundle.ExpiresAt.Equal(*st.ExpiresAt) {
		t.Fatalf("GET returned %v (%v); want the stored end %v", got.Bundle.ExpiresAt, err, st.ExpiresAt)
	}
	if w := put(`{"expires_at":"2026-11-17T01:33:59Z"}`); w.Code != http.StatusOK {
		t.Fatalf("expires_at: %d %s", w.Code, w.Body.String())
	}
	if st := s.smsBudget.Status(); !st.ExpiresAt.Equal(time.Date(2026, 11, 17, 1, 33, 59, 0, time.UTC)) || st.Sent != 1 {
		t.Fatalf("expires_at alone: %+v; want the end corrected, the counts kept", st)
	}
	for _, body := range []string{`{"expires_at":"next week"}`, `{"valid_days":0}`, `{"valid_days":400}`, `{"expires_at":"2026-11-17T01:33:59Z","valid_days":31}`} {
		if w := put(body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d; want 400", body, w.Code)
		}
	}
	if w := put(`{"expires_at":""}`); w.Code != http.StatusOK || s.smsBudget.Status().ExpiresAt != nil {
		t.Fatalf("clear: %d %+v", w.Code, s.smsBudget.Status())
	}
}
