package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"meshsat/internal/database"
)

// GET /api/delivery/stats (S3-3): the window it answers for, its refusals,
// and attempts counted per route. [MESHSAT-1584]
func TestDeliveryOutcomeStatsAPI(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	in := database.DeliveryIntent{ID: "i1", ContactID: "c", Precedence: "Routine", Class: database.DeliveryClassMessage,
		Payload: []byte("hi"), PayloadSHA256: "x", EffectiveStrategy: "PRIMARY_ONLY", Currency: "EUR",
		RoutePlan: []database.RoutePlanEntry{{RouteIndex: 1, Kind: "MESHTASTIC", InterfaceID: "mesh_0", EstWireBytes: 40, Admitted: true}}}
	rows := []database.MessageDelivery{{MsgRef: "i1", Channel: "mesh_0", Priority: 1, MaxRetries: 3, QoSLevel: 1,
		Payload: []byte("hi"), IntentID: "i1", Round: 1, AttemptNo: 1, RouteIndex: 1}}
	if _, _, err := s.db.CreateSubmission(database.DeliverySubmission{ID: "sub-i1", Source: "api", RequestSHA256: "i1"},
		[]database.NewIntent{{Intent: in, FirstRows: rows}}); err != nil {
		t.Fatal(err)
	}
	get := func(q string) (*httptest.ResponseRecorder, database.DeliveryOutcomeStats) {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/delivery/stats"+q, nil))
		var st database.DeliveryOutcomeStats
		_ = json.Unmarshal(rr.Body.Bytes(), &st)
		return rr, st
	}

	rr, st := get("")
	if rr.Code != http.StatusOK || st.Unit != "attempt" || len(st.Routes) != 1 || st.Routes[0].Bearer != "MESHTASTIC" ||
		st.Routes[0].SizeBucket != "1-50" || st.Routes[0].Unresolved != 1 || st.Total.Attempts != 1 {
		t.Fatalf("default window: %d %s", rr.Code, rr.Body)
	}
	since, _ := time.Parse(time.RFC3339, st.Since)
	until, _ := time.Parse(time.RFC3339, st.Until)
	if d := until.Sub(since); d != 24*time.Hour {
		t.Errorf("default window %v, want 24h", d)
	}
	if _, st := get("?hours=1"); st.Total.Attempts != 1 {
		t.Errorf("last hour: %+v", st.Total)
	}
	old := "?since=2020-01-01T00:00:00Z&until=2020-01-02T00:00:00Z"
	if rr, st := get(old); rr.Code != http.StatusOK || st.Total.Attempts != 0 || len(st.Routes) != 0 || st.Since != "2020-01-01T00:00:00Z" {
		t.Errorf("a window with nothing in it: %d %s", rr.Code, rr.Body)
	}
	for _, q := range []string{"?since=yesterday", "?until=x", "?hours=0", "?hours=9000",
		"?since=2020-01-02T00:00:00Z&until=2020-01-01T00:00:00Z"} {
		if rr, _ := get(q); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, rr.Code)
		}
	}
}

// R23-R2 over HTTP: fractions and offsets in since/until keep the window's
// membership, and the answer states the bounds as requested; the coverage
// block is there. [MESHSAT-1584]
func TestDeliveryOutcomeStatsAPIBounds(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	in := database.DeliveryIntent{ID: "noon", ContactID: "c", Precedence: "Routine", Class: database.DeliveryClassMessage,
		Payload: []byte("hi"), PayloadSHA256: "x", EffectiveStrategy: "PRIMARY_ONLY", Currency: "EUR",
		RoutePlan: []database.RoutePlanEntry{{RouteIndex: 1, Kind: "MESHTASTIC", InterfaceID: "mesh_0", EstWireBytes: 40, Admitted: true}}}
	rows := []database.MessageDelivery{{MsgRef: "noon", Channel: "mesh_0", Priority: 1, MaxRetries: 3, QoSLevel: 1,
		Payload: []byte("hi"), IntentID: "noon", Round: 1, AttemptNo: 1, RouteIndex: 1}}
	if _, _, err := s.db.CreateSubmission(database.DeliverySubmission{ID: "sub-noon", Source: "api", RequestSHA256: "noon"},
		[]database.NewIntent{{Intent: in, FirstRows: rows}}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.db.Exec(`UPDATE message_deliveries SET created_at = '2026-10-20 12:00:00' WHERE intent_id = 'noon'`)
	_, _ = s.db.Exec(`UPDATE delivery_intents SET created_at = '2026-10-20 12:00:00' WHERE id = 'noon'`)
	for _, c := range []struct {
		since, until string
		want         int
		echoSince    string
	}{
		{"2026-10-20T11:59:59.5Z", "2026-10-20T12:00:00.5Z", 1, "2026-10-20T11:59:59.5Z"},
		{"2026-10-20T12:00:00.5Z", "2026-10-20T12:00:01.5Z", 0, "2026-10-20T12:00:00.5Z"},
		{"2026-10-20T12:00:00.2Z", "2026-10-20T12:00:00.7Z", 0, "2026-10-20T12:00:00.2Z"},
		{"2026-10-20T15:00:00%2B03:00", "2026-10-20T15:00:01%2B03:00", 1, "2026-10-20T12:00:00Z"},
		{"2026-10-20T11:59:59Z", "2026-10-20T12:00:00Z", 0, "2026-10-20T11:59:59Z"},
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/delivery/stats?since="+c.since+"&until="+c.until, nil))
		var st database.DeliveryOutcomeStats
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(rr.Body.Bytes(), &st)
		_ = json.Unmarshal(rr.Body.Bytes(), &raw)
		if rr.Code != http.StatusOK || st.Total.Attempts != c.want || st.Intents.Intents != c.want || st.Since != c.echoSince ||
			raw["coverage"] == nil || !st.Coverage.Complete {
			t.Errorf("[%s, %s): %d %s; want %d attempts, since %s", c.since, c.until, rr.Code, rr.Body, c.want, c.echoSince)
		}
	}
}
