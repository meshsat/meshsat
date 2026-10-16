package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/engine"
)

// Reviewer's regression (B5-R1, stage 0b review of e2cacd2): a raw send
// with an unsupported explicit strategy is refused before anything is
// queued; a raw send without one still queues. [MESHSAT-1584]
func TestReviewB5RawStrategyRefusedBeforeQueue(t *testing.T) {
	for _, strategy := range []string{"", "HEMB_BONDED", "ANY_REACHABLE", "ORDERED_FALLBACK", "BOGUS"} {
		name := strategy
		if name == "" {
			name = "raw_without_strategy"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := testServerWithDispatcher(t, directory.KindSMS)
			body := fmt.Sprintf(`{"raw_interface_id":"cellular_0","raw_address":"+31600000000","text":"review fixture","strategy":%q}`, strategy)
			req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			s.Router().ServeHTTP(response, req)
			var queued int
			if err := s.db.Get(&queued, `SELECT COUNT(*) FROM message_deliveries`); err != nil {
				t.Fatal(err)
			}
			if strategy == "" {
				if response.Code != http.StatusOK || queued != 1 {
					t.Fatalf("raw control failed: status=%d rows=%d body=%s", response.Code, queued, response.Body.String())
				}
				return
			}
			if response.Code != http.StatusBadRequest || queued != 0 {
				t.Fatalf("unsupported raw strategy %s: HTTP %d, queued=%d, response=%s; want HTTP 400 and zero rows",
					strategy, response.Code, queued, response.Body.String())
			}
		})
	}
}

// B5-R1 with delivery intents on: a raw send is still not an intent, so
// raw ORDERED_FALLBACK (and the rest) is 400 with nothing written; a raw
// send without a strategy queues one row and creates no intent.
func TestSendToContact_RawStrategyWithIntentsOn(t *testing.T) {
	s := newTestServerWithDB(t)
	_, _ = s.db.Exec(`DELETE FROM interfaces`)
	_ = s.db.InsertInterface(&database.Interface{ID: "cellular_0", ChannelType: "cellular", Label: "cellular", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"})
	d := engine.NewDispatcher(s.db, channel.NewRegistry(), nil, idMesh{})
	d.SetRecipientResolver(directory.NewSQLStore(s.db.DB))
	esc := engine.NewEscalator(s.db, d, engine.DefaultEscalatorConfig(), nil, true)
	d.SetEscalator(esc)
	esc.SetReady()
	s.SetDispatcher(d)
	router := s.Router()
	count := func() (n [3]int) {
		for i, tbl := range []string{"message_deliveries", "delivery_submissions", "delivery_intents"} {
			_ = s.db.Get(&n[i], `SELECT COUNT(*) FROM `+tbl)
		}
		return n
	}
	post := func(strategy string) int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(fmt.Sprintf(
			`{"raw_interface_id":"cellular_0","raw_address":"+31600000000","text":"x","strategy":%q}`, strategy)))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rr, req)
		return rr.Code
	}
	for _, st := range []string{"ORDERED_FALLBACK", "ALL_BEARERS", "HEMB_BONDED", "ANY_REACHABLE", "BOGUS"} {
		if code := post(st); code != http.StatusBadRequest || count() != [3]int{} {
			t.Fatalf("raw %s with intents on: HTTP %d, rows %v; want 400 and nothing written", st, code, count())
		}
	}
	if code := post(""); code != http.StatusOK || count() != [3]int{1, 0, 0} {
		t.Fatalf("raw without a strategy: HTTP %d, rows %v; want one delivery, no intent", code, count())
	}
}
