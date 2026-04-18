package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestContactsGet_KindFilter covers the MESHSAT-542 addition: GET
// /api/contacts?kind=<bearer> returns only contacts that have at
// least one address of the given kind, via the existing
// database.GetContactsWithAddressType helper. Absence of the
// parameter preserves the previous "all contacts" behaviour.
func TestContactsGet_KindFilter(t *testing.T) {
	s := newTestServerWithDB(t)

	// Seed two contacts: Alice has SMS + mesh, Bob has mesh only.
	aliceID, err := s.db.CreateContact("Alice", "")
	if err != nil {
		t.Fatal(err)
	}
	bobID, err := s.db.CreateContact("Bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.AddContactAddress(aliceID, "sms", "+31611112222", "Mobile", "", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.AddContactAddress(aliceID, "mesh", "!aaaa1111", "", "", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.AddContactAddress(bobID, "mesh", "!bbbb2222", "", "", true, false); err != nil {
		t.Fatal(err)
	}

	router := s.Router()

	// No filter → both contacts.
	req := httptest.NewRequest("GET", "/api/contacts", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("no-filter status: %d body=%s", rr.Code, rr.Body.String())
	}
	var all []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode no-filter: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("no-filter count: got %d, want 2", len(all))
	}

	// kind=sms → only Alice.
	req = httptest.NewRequest("GET", "/api/contacts?kind=sms", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("kind=sms status: %d body=%s", rr.Code, rr.Body.String())
	}
	var smsOnly []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &smsOnly); err != nil {
		t.Fatalf("decode kind=sms: %v", err)
	}
	if len(smsOnly) != 1 {
		t.Fatalf("kind=sms count: got %d, want 1", len(smsOnly))
	}
	if name, _ := smsOnly[0]["display_name"].(string); name != "Alice" {
		t.Errorf("kind=sms name: got %q, want Alice", name)
	}

	// kind=mesh → both contacts.
	req = httptest.NewRequest("GET", "/api/contacts?kind=mesh", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	var meshContacts []map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &meshContacts)
	if len(meshContacts) != 2 {
		t.Errorf("kind=mesh count: got %d, want 2", len(meshContacts))
	}

	// kind=iridium → none.
	req = httptest.NewRequest("GET", "/api/contacts?kind=iridium", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	var iridium []map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &iridium)
	if len(iridium) != 0 {
		t.Errorf("kind=iridium count: got %d, want 0", len(iridium))
	}
}

// TestLegacySMSContacts_DeprecationHeaders asserts every legacy
// /api/cellular/contacts* handler emits RFC 8594 Sunset +
// Deprecation: true + Link rel="successor-version" headers so
// clients can migrate automatically.
func TestLegacySMSContacts_DeprecationHeaders(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	cases := []struct {
		method, path string
		body         string
	}{
		{"GET", "/api/cellular/contacts", ""},
		{"POST", "/api/cellular/contacts", `{"name":"DepCreate","phone":"+31600000001"}`},
	}
	for _, c := range cases {
		var req *http.Request
		if c.body != "" {
			req = httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req = httptest.NewRequest(c.method, c.path, nil)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if got := rr.Header().Get("Deprecation"); got != "true" {
			t.Errorf("%s %s: Deprecation header = %q, want 'true'", c.method, c.path, got)
		}
		if got := rr.Header().Get("Sunset"); got == "" {
			t.Errorf("%s %s: Sunset header missing", c.method, c.path)
		}
		if got := rr.Header().Get("Link"); !strings.Contains(got, `rel="successor-version"`) {
			t.Errorf("%s %s: Link header = %q, want successor-version", c.method, c.path, got)
		}
		if got := rr.Header().Get("X-Meshsat-Deprecation"); !strings.Contains(got, "MESHSAT-542") {
			t.Errorf("%s %s: X-Meshsat-Deprecation = %q, want MESHSAT-542 reference", c.method, c.path, got)
		}
	}
}
