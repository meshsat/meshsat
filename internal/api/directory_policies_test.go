package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/directory"
)

// GET /api/directory/policies (HeMB-NG r3 D12, stage 0b): every row with
// what a send does with it; ?unsupported=1 lists only the rows that name
// a strategy the bridge does not implement. [MESHSAT-1584]
func TestDirectoryPolicies_ListsWhatEachRowDoes(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	get := func(path string) policiesResponse {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		var out policiesResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}

	all := get("/api/directory/policies")
	if all.DeliveryIntents || strings.Join(all.SupportedStrategies, ",") != "PRIMARY_ONLY,ALL_BEARERS" || len(all.Policies) != 7 {
		t.Fatalf("fresh database: %+v", all)
	}
	for _, p := range all.Policies {
		if !p.Supported || !p.Seeded || p.RequestedStrategy != p.EffectiveStrategy || p.StrategyReason != "" {
			t.Errorf("seed %s:%s after v65: %+v", p.ScopeType, p.ScopeID, p)
		}
	}
	if got := get("/api/directory/policies?unsupported=1"); len(got.Policies) != 0 {
		t.Fatalf("nothing needs attention on a fresh database: %+v", got.Policies)
	}

	_ = store.UpsertPolicy(context.Background(), &directory.DispatchPolicy{ScopeType: directory.ScopeContact, ScopeID: "kim", Strategy: directory.StrategyHeMBBonded})
	got := get("/api/directory/policies?unsupported=1")
	if len(got.Policies) != 1 {
		t.Fatalf("unsupported rows: %+v; want the contact row only", got.Policies)
	}
	if p := got.Policies[0]; p.ScopeID != "kim" || p.RequestedStrategy != "HEMB_BONDED" || p.EffectiveStrategy != "ALL_BEARERS" ||
		p.StrategyReason != "legacy_compat_all_bearers" || p.Supported || p.Seeded {
		t.Fatalf("contact row: %+v", p)
	}
}

// S2, request half, through the API on the queue path: 400 before
// anything is queued; a policy send says which strategy it used and why.
func TestSendToContact_UnsupportedStrategy400(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS, directory.KindMeshtastic)
	router := s.Router()
	c := &directory.Contact{DisplayName: "Lu"}
	_ = store.CreateContact(context.Background(), c)
	_ = store.AddAddress(context.Background(), &directory.Address{ContactID: c.ID, Kind: directory.KindSMS, Value: "+31666666666"})
	_ = store.AddAddress(context.Background(), &directory.Address{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!0000abcd", PrimaryRank: 1})
	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rr, req)
		return rr
	}
	for _, st := range []string{"ANY_REACHABLE", "HEMB_BONDED", "ORDERED_FALLBACK"} {
		if rr := post(fmt.Sprintf(`{"contact_id":%q,"text":"x","strategy":%q}`, c.ID, st)); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s; want 400", st, rr.Code, rr.Body.String())
		}
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d deliveries queued by refused requests", n)
	}
	rr := post(fmt.Sprintf(`{"contact_id":%q,"text":"x","precedence":"Flash"}`, c.ID))
	var resp sendToContactResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != http.StatusOK || resp.Strategy != "ALL_BEARERS" || resp.RequestedStrategy != "ALL_BEARERS" || resp.StrategyReason != "policy:precedence" {
		t.Fatalf("Flash by policy: %d %s", rr.Code, rr.Body.String())
	}
}
