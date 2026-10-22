package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"meshsat/internal/database"
)

// TestMessagesSinceAnRFC3339TimeFindsTheDaysRows: GET /api/messages?since=<RFC 3339>, as a
// client sends back a created_at it was given, finds the rows; a bound that is no time is a 400,
// not an empty list. [MESHSAT-1640]
func TestMessagesSinceAnRFC3339TimeFindsTheDaysRows(t *testing.T) {
	s := newTestServerWithDB(t)
	if err := s.db.InsertMessage(&database.Message{FromNode: "!a1", DecodedText: "today", Direction: "rx", Transport: "iridium_imt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE messages SET created_at = '2026-10-22 09:00:00'"); err != nil {
		t.Fatal(err)
	}
	router := s.Router()
	get := func(q url.Values) (int, map[string]any) {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/messages?"+q.Encode(), nil))
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return rr.Code, body
	}
	code, body := get(url.Values{"since": {"2026-10-22T00:00:00Z"}, "transport": {"iridium_imt"}, "direction": {"rx"}})
	if code != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("since RFC 3339: HTTP %d total %v, want 200 and 1", code, body["total"])
	}
	// The created_at the API returns, sent back as a bound, finds its own row.
	created := body["messages"].([]any)[0].(map[string]any)["created_at"].(string)
	if code, body = get(url.Values{"since": {created}, "until": {created}}); code != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("since=until=%s: HTTP %d total %v, want 200 and 1", created, code, body["total"])
	}
	if code, _ = get(url.Values{"since": {"yesterday"}}); code != http.StatusBadRequest {
		t.Fatalf("since=yesterday: HTTP %d, want 400", code)
	}
}
