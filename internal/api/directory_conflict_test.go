package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/engine"
)

// r20 F1 through the API: a Hub team member whose address a local contact
// holds is refused naming the holder; the operator removes that local
// address (a Hub one is refused with 409); the Hub's next snapshot gives
// the member its address and the team send goes to both. [MESHSAT-1584]
func TestDirectoryAddressConflictThroughTheAPI(t *testing.T) {
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
	ctx := t.Context()

	if err := store.CreateContact(ctx, &directory.Contact{ID: "l-peer", DisplayName: "peer (mine)"}); err != nil {
		t.Fatal(err)
	}
	local := &directory.Address{ContactID: "l-peer", Kind: directory.KindMeshtastic, Value: "!0000cccc"}
	if err := store.AddAddress(ctx, local); err != nil {
		t.Fatal(err)
	}
	snap := directory.HubSnapshot{TenantID: "t", Version: 3,
		Contacts: []directory.HubContact{
			{ID: "h-peer", DisplayName: "peer", Addresses: []directory.HubAddress{{ID: "ha-peer", Kind: "MESHTASTIC", Value: "!0000cccc"}}},
			{ID: "h-ok", DisplayName: "ok", Addresses: []directory.HubAddress{{ID: "ha-ok", Kind: "MESHTASTIC", Value: "!0000dddd"}}},
		},
		Groups: []directory.HubGroup{{ID: "h-team", Name: "team", Kind: "TEAM", MemberIDs: []string{"h-peer", "h-ok"}}},
	}
	all := func(directory.Strategy) bool { return true }
	if _, err := store.ApplyHubSnapshot(ctx, snap, all); err != nil {
		t.Fatal(err)
	}

	do := func(method, path, body, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	var st directoryState
	if rr := do("GET", "/api/directory/state", "", ""); rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &st) != nil ||
		len(st.AddressConflicts) != 1 || st.AddressConflicts[0].ContactID != "h-peer" || st.AddressConflicts[0].HolderContactID != "l-peer" {
		t.Fatalf("state %d %s", rr.Code, rr.Body)
	}

	rr := do("POST", "/api/messages/send-to-contact", `{"group_id":"h-team","text":"hi"}`, "k1")
	var resp sendToContactResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != http.StatusMultiStatus || len(resp.Members) != 2 {
		t.Fatalf("team send %d %s", rr.Code, rr.Body)
	}
	for _, m := range resp.Members {
		switch m.ContactID {
		case "h-peer":
			want := "MESHTASTIC: skipped_address_held_locally (!0000cccc, held by contact l-peer)"
			if m.Outcome != database.OutcomeFailed || len(m.Reasons) == 0 || m.Reasons[0] != want {
				t.Errorf("h-peer: %+v; want %q", m, want)
			}
		case "h-ok":
			if m.Outcome == database.OutcomeFailed || len(m.DeliveryIDs) == 0 {
				t.Errorf("h-ok: %+v", m)
			}
		}
	}

	for _, c := range []struct {
		path string
		code int
	}{
		{"/api/directory/contacts/h-ok/addresses/ha-ok", http.StatusConflict},         // the Hub's
		{"/api/directory/contacts/h-peer/addresses/" + local.ID, http.StatusNotFound}, // not that contact's
		{"/api/directory/contacts/l-peer/addresses/nope", http.StatusNotFound},
	} {
		if rr := do("DELETE", c.path, "", ""); rr.Code != c.code {
			t.Errorf("DELETE %s: %d %s; want %d", c.path, rr.Code, rr.Body, c.code)
		}
	}
	rr = do("DELETE", "/api/directory/contacts/l-peer/addresses/"+local.ID, "", "")
	var del deletedAddress
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &del) != nil || len(del.WaitingHubContacts) != 1 ||
		del.WaitingHubContacts[0].ContactID != "h-peer" {
		t.Fatalf("remove the local address: %d %s", rr.Code, rr.Body)
	}
	if c, err := store.GetContact(ctx, "l-peer"); err != nil || c.DisplayName != "peer (mine)" {
		t.Errorf("the local contact: %+v %v", c, err)
	}

	snap.Version = 4 // the Hub's next snapshot
	if rep, err := store.ApplyHubSnapshot(ctx, snap, all); err != nil || !rep.Applied || len(rep.Skipped) != 0 {
		t.Fatalf("next snapshot %+v %v", rep, err)
	}
	rr = do("POST", "/api/messages/send-to-contact", `{"group_id":"h-team","text":"hi again"}`, "k2")
	resp = sendToContactResponse{}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != http.StatusOK || len(resp.Members) != 2 {
		t.Fatalf("team send after the clean-up %d %s", rr.Code, rr.Body)
	}
	if rr := do("GET", "/api/directory/state", "", ""); json.Unmarshal(rr.Body.Bytes(), &st) != nil || len(st.AddressConflicts) != 0 {
		t.Errorf("conflicts left: %s", rr.Body)
	}
}
