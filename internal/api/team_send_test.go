package api

import (
	"encoding/json"
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

// Team send (C8): one submission, one intent per member resolved at
// submission; a member no route admits closes failed with its reasons;
// 200 / 207 / 422; a retry under the key returns the same members.
// [MESHSAT-1584]
func TestSendToTeam(t *testing.T) {
	s := newTestServerWithDB(t)
	_, _ = s.db.Exec(`DELETE FROM interfaces`)
	_ = s.db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Label: "mesh", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"})
	d := engine.NewDispatcher(s.db, channel.NewRegistry(), nil, idMesh{})
	store := directory.NewSQLStore(s.db.DB)
	d.SetRecipientResolver(store)
	esc := engine.NewEscalator(s.db, d, engine.DefaultEscalatorConfig(), nil, true)
	d.SetEscalator(esc)
	esc.SetReady()
	s.SetDispatcher(d)
	router := s.Router()

	contact := func(id string, addrs ...directory.Address) string {
		if err := store.CreateContact(t.Context(), &directory.Contact{ID: id, DisplayName: id}); err != nil {
			t.Fatal(err)
		}
		for _, a := range addrs {
			a := a
			a.ContactID = id
			if err := store.AddAddress(t.Context(), &a); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	group := func(id string, kind directory.GroupKind, members ...string) {
		if err := store.CreateGroup(t.Context(), &directory.Group{ID: id, DisplayName: id, Kind: kind}); err != nil {
			t.Fatal(err)
		}
		for _, m := range members {
			if err := store.AddMember(t.Context(), id, m, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	m1 := contact("m1", directory.Address{Kind: directory.KindMeshtastic, Value: "!0000aaaa"})
	m2 := contact("m2", directory.Address{Kind: directory.KindMeshtastic, Value: "!0000bbbb"})
	m3 := contact("m3", directory.Address{Kind: directory.KindSMS, Value: "+31600000003"}) // no SMS gateway here
	m4 := contact("m4")                                                                    // no address at all
	group("team-1", directory.GroupTeam, m1, m2, m3, m4)
	group("team-2", directory.GroupTeam, m3, m4)
	group("team-3", directory.GroupTeam)
	group("list-1", directory.GroupList, m1)

	post := func(body, key string) (*httptest.ResponseRecorder, sendToContactResponse) {
		req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		var resp sendToContactResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
		return rr, resp
	}
	count := func(q string) int {
		var n int
		_ = s.db.QueryRow(q).Scan(&n)
		return n
	}

	rr, first := post(`{"group_id":"team-1","text":"move north"}`, "team-key")
	if rr.Code != http.StatusMultiStatus || first.SubmissionID == "" || len(first.Members) != 4 || len(first.IntentIDs) != 4 || !first.Queued {
		t.Fatalf("team send: %d %s", rr.Code, rr.Body.String())
	}
	byContact := map[string]teamMemberStatus{}
	for _, m := range first.Members {
		byContact[m.ContactID] = m
	}
	for _, id := range []string{m1, m2} {
		if m := byContact[id]; m.State != database.IntentActive || len(m.DeliveryIDs) != 1 || len(m.Reasons) != 0 {
			t.Errorf("reachable member %s: %+v", id, m)
		}
	}
	if m := byContact[m3]; m.State != database.IntentClosed || m.Outcome != database.OutcomeFailed ||
		len(m.Reasons) == 0 || !strings.Contains(strings.Join(m.Reasons, " "), "skipped_offline") || len(m.DeliveryIDs) != 0 {
		t.Errorf("member without a running route: %+v", m)
	}
	if m := byContact[m4]; m.State != database.IntentClosed || !strings.Contains(strings.Join(m.Reasons, " "), "skipped_no_address") {
		t.Errorf("member without an address: %+v", m)
	}
	if n := count(`SELECT COUNT(*) FROM delivery_submissions`); n != 1 {
		t.Errorf("%d submissions, want one for the team", n)
	}
	if n := count(`SELECT COUNT(*) FROM delivery_intents WHERE submission_id = '` + first.SubmissionID + `'`); n != 4 {
		t.Errorf("%d intents, want one per member", n)
	}
	if n := count(`SELECT COUNT(*) FROM message_deliveries WHERE intent_id IS NOT NULL AND intent_id != ''`); n != 2 {
		t.Errorf("%d attempts, want the two reachable members'", n)
	}

	// The members were frozen at submission: a later member does not join
	// it, and the retry under the key answers the same intents.
	contact("m5", directory.Address{Kind: directory.KindMeshtastic, Value: "!0000cccc"})
	if err := store.AddMember(t.Context(), "team-1", "m5", ""); err != nil {
		t.Fatal(err)
	}
	// The retry answers the first submission even when no member could be
	// reached any more: it is not resolved again.
	if _, err := s.db.Exec(`UPDATE interfaces SET enabled = 0 WHERE id = 'mesh_0'`); err != nil {
		t.Fatal(err)
	}
	rr, again := post(`{"group_id":"team-1","text":"move north"}`, "team-key")
	if _, err := s.db.Exec(`UPDATE interfaces SET enabled = 1 WHERE id = 'mesh_0'`); err != nil {
		t.Fatal(err)
	}
	if !again.Existing || again.SubmissionID != first.SubmissionID || len(again.Members) != 4 || fmt.Sprint(again.IntentIDs) != fmt.Sprint(first.IntentIDs) {
		t.Fatalf("retry: %d %s", rr.Code, rr.Body.String())
	}
	if rr, _ := post(`{"group_id":"team-2","text":"move north"}`, "team-key"); rr.Code != http.StatusConflict {
		t.Errorf("the key for another team: %d, want 409", rr.Code)
	}

	subs := count(`SELECT COUNT(*) FROM delivery_submissions`)
	for _, c := range []struct {
		body string
		want int
		has  string
	}{
		{`{"group_id":"team-2","text":"x"}`, http.StatusUnprocessableEntity, "skipped_no_address"},
		{`{"group_id":"team-3","text":"x"}`, http.StatusUnprocessableEntity, "no members"},
		{`{"group_id":"list-1","text":"x"}`, http.StatusBadRequest, "not a team"},
		{`{"group_id":"nope","text":"x"}`, http.StatusNotFound, "no such group"},
	} {
		if rr, _ := post(c.body, ""); rr.Code != c.want || !strings.Contains(rr.Body.String(), c.has) {
			t.Errorf("%s: %d %s, want %d with %q", c.body, rr.Code, rr.Body.String(), c.want, c.has)
		}
	}
	if count(`SELECT COUNT(*) FROM delivery_submissions`) != subs {
		t.Error("a refused team send wrote a submission")
	}

	// Every member reachable: 200.
	group("team-4", directory.GroupTeam, m1, m2)
	if rr, resp := post(`{"group_id":"team-4","text":"y"}`, ""); rr.Code != http.StatusOK || len(resp.Members) != 2 {
		t.Errorf("all reachable: %d %s", rr.Code, rr.Body.String())
	}

	// The failed member's intent reads closed failed on its status.
	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest("GET", "/api/deliveries/intents/"+byContact[m3].IntentID, nil))
	var view engine.IntentView
	_ = json.Unmarshal(get.Body.Bytes(), &view)
	if get.Code != http.StatusOK || view.State != database.IntentClosed || view.Outcome != database.OutcomeFailed {
		t.Errorf("failed member's status: %d %s", get.Code, get.Body.String())
	}
}
