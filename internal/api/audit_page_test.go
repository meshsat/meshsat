package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"meshsat/internal/database"
)

func getJSON(t *testing.T, s *Server, path string, into interface{}) int {
	t.Helper()
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if into != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
			t.Fatalf("GET %s: %v (%s)", path, err, w.Body.String())
		}
	}
	return w.Code
}

// TestAuditLog_CountAndPages: "N entries" above a page of the log, and a full copy
// read page by page with before=<oldest id so far>. [MESHSAT-1402]
func TestAuditLog_CountAndPages(t *testing.T) {
	s := newTestServerWithDB(t)
	mesh := "mesh_0"
	for i := 1; i <= 25; i++ {
		entry := &database.AuditLogEntry{Timestamp: fmt.Sprintf("2027-01-15T10:00:%02dZ", i), EventType: "dispatch", Detail: fmt.Sprintf("e%d", i)}
		if i%5 == 0 {
			entry.InterfaceID = &mesh
		}
		if _, err := s.db.InsertAuditLog(entry); err != nil {
			t.Fatal(err)
		}
	}

	var count map[string]int
	if code := getJSON(t, s, "/api/audit/count", &count); code != http.StatusOK || count["count"] != 25 {
		t.Fatalf("count: %d %v", code, count)
	}

	var page []database.AuditLogEntry
	getJSON(t, s, "/api/audit?limit=10", &page)
	if len(page) != 10 || page[0].ID != 25 || page[9].ID != 16 {
		t.Fatalf("first page: %d entries, %v", len(page), ids(page))
	}
	var next []database.AuditLogEntry
	getJSON(t, s, fmt.Sprintf("/api/audit?limit=10&before=%d", page[9].ID), &next)
	if len(next) != 10 || next[0].ID != 15 || next[9].ID != 6 {
		t.Fatalf("second page: %v", ids(next))
	}
	var last []database.AuditLogEntry
	getJSON(t, s, "/api/audit?limit=10&before=6", &last)
	if len(last) != 5 || last[4].ID != 1 {
		t.Fatalf("last page: %v", ids(last))
	}

	var byLink []database.AuditLogEntry
	getJSON(t, s, "/api/audit?interface_id=mesh_0&before=20", &byLink)
	if len(byLink) != 3 || byLink[0].ID != 15 {
		t.Fatalf("one link's page: %v", ids(byLink))
	}

	// An empty page is a list, not null.
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("GET", "/api/audit?before=1", nil))
	if w.Body.String() != "[]\n" && w.Body.String() != "[]" {
		t.Fatalf("an empty page: %q", w.Body.String())
	}
}

func ids(entries []database.AuditLogEntry) []int64 {
	out := make([]int64, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}
