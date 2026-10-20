package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/directory"
)

// The Hub's directory on the bridge: an edit of a Hub contact survives the
// next snapshot, a local address too, and the groups and the sync state are
// readable. [MESHSAT-1584]
func TestDirectoryHubEndpoints(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rr
	}
	snap := directory.HubSnapshot{TenantID: "t-1", Version: 3,
		Contacts: []directory.HubContact{{ID: "c-a", DisplayName: "Alice", Addresses: []directory.HubAddress{{Kind: "SMS", Value: "+31600000001"}}}},
		Groups:   []directory.HubGroup{{ID: "g-1", Name: "North", Kind: "TEAM", MemberIDs: []string{"c-a"}}}}
	if _, err := store.ApplyHubSnapshot(t.Context(), snap, func(directory.Strategy) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if rr := do("PATCH", "/api/directory/contacts/c-a", `{"display_name":"Alice (lead)"}`); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Alice (lead)") {
		t.Fatalf("PATCH: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do("POST", "/api/directory/contacts/c-a/addresses", `{"kind":"APRS","value":"PD0ABC-7"}`); rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), `"origin":"local"`) {
		t.Fatalf("POST address: %d %s", rr.Code, rr.Body.String())
	}
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"PATCH", "/api/directory/contacts/nobody", `{"notes":"x"}`, http.StatusNotFound},
		{"PATCH", "/api/directory/contacts/c-a", `{"display_name":""}`, http.StatusBadRequest},
		{"PATCH", "/api/directory/contacts/c-a", `{"team":"x"}`, http.StatusBadRequest},
		{"POST", "/api/directory/contacts/c-a/addresses", `{"kind":"SMS","value":"+31600000001"}`, http.StatusConflict},
		{"POST", "/api/directory/contacts/c-a/addresses", `{"kind":"PIGEON","value":"x"}`, http.StatusBadRequest},
	} {
		if rr := do(c.method, c.path, c.body); rr.Code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.method, c.path, c.body, rr.Code, rr.Body.String(), c.want)
		}
	}
	snap.Version = 4
	snap.Contacts[0].DisplayName = "Alice K."
	if _, err := store.ApplyHubSnapshot(t.Context(), snap, func(directory.Strategy) bool { return true }); err != nil {
		t.Fatal(err)
	}
	c, err := store.GetContact(t.Context(), "c-a")
	if err != nil || c.DisplayName != "Alice (lead)" {
		t.Fatalf("after the next snapshot: %+v %v", c, err)
	}
	addrs, _ := store.ListAddresses(t.Context(), "c-a")
	if len(addrs) != 2 {
		t.Errorf("addresses after the next snapshot: %+v", addrs)
	}
	rr := do("GET", "/api/directory/groups", "")
	var groups []directory.Group
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &groups) != nil || len(groups) != 1 ||
		groups[0].Origin != "hub:t-1" || len(groups[0].Members) != 1 || groups[0].Kind != directory.GroupTeam {
		t.Errorf("groups: %d %s", rr.Code, rr.Body.String())
	}
	rr = do("GET", "/api/directory/state", "")
	var st struct {
		TenantID string `json:"tenant_id"`
		Version  int64  `json:"version"`
		KeyID    string `json:"key_id"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &st) != nil || st.TenantID != "t-1" || st.Version != 4 || st.KeyID != "" {
		t.Errorf("state: %d %s", rr.Code, rr.Body.String())
	}
}
