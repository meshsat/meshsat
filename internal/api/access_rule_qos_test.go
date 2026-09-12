package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A forwarding rule created without naming a QoS level must come out retryable.
//
// qos_level 0 means best effort, and the delivery worker makes the first failure
// of a QoS 0 delivery final: no retry, not even a retry counted. Every rule
// created through this endpoint used to land on 0, because the JSON decoded into
// a bare struct (Go zero value) and InsertAccessRule always writes the column, so
// the schema's DEFAULT 1 never applied. Nobody chose that. [MESHSAT-1061]
func TestCreateAccessRule_DefaultsToRetryableQoS(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"interface_id":"mesh_0","direction":"ingress","action":"forward","forward_to":"aprs_0","name":"booth relay"}`
	req := httptest.NewRequest("POST", "/api/access-rules", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		ID       int64 `json:"id"`
		QoSLevel int   `json:"qos_level"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.QoSLevel != 1 {
		t.Errorf("qos_level = %d, want 1: a rule that forwards must survive one failed attempt", got.QoSLevel)
	}

	rules, err := s.db.GetAccessRules("mesh_0", "ingress")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r.ID == got.ID && r.QoSLevel != 1 {
			t.Errorf("stored qos_level = %d, want 1", r.QoSLevel)
		}
	}
}

// An operator who deliberately asks for best effort still gets it. The default
// only fills in a value nobody supplied.
func TestCreateAccessRule_ExplicitQoSZeroIsHonoured(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	body := `{"interface_id":"mesh_0","direction":"ingress","action":"forward","forward_to":"aprs_0","qos_level":0}`
	req := httptest.NewRequest("POST", "/api/access-rules", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		QoSLevel int `json:"qos_level"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.QoSLevel != 0 {
		t.Errorf("qos_level = %d, want the explicit 0 to be kept", got.QoSLevel)
	}
}
