package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshsat/internal/directory"
)

// The compose screen lists the directory's contacts, the ones send-to-contact
// resolves, with their addresses and never their keys. [MESHSAT-1584]
func TestListDirectoryContacts(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS, directory.KindIridiumIMT)
	router := s.Router()

	get := func(path string) []directory.Contact {
		t.Helper()
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), `"keys"`) || strings.Contains(rr.Body.String(), "c2VjcmV0") {
			t.Fatalf("GET %s carries keys: %s", path, rr.Body.String())
		}
		var out []directory.Contact
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := get("/api/directory/contacts"); got == nil || len(got) != 0 {
		t.Fatalf("empty directory: %v, want []", got)
	}

	bob := &directory.Contact{DisplayName: "Bob", Team: "red"}
	ann := &directory.Contact{DisplayName: "Ann", Team: "blue"}
	for _, c := range []*directory.Contact{bob, ann} {
		if err := store.CreateContact(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []directory.Address{
		{ContactID: ann.ID, Kind: directory.KindSMS, Value: "+31612345678"},
		{ContactID: ann.ID, Kind: directory.KindIridiumIMT, Value: "300000000000001", PrimaryRank: 1},
	} {
		a := a
		if err := store.AddAddress(t.Context(), &a); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddKey(t.Context(), &directory.ContactKey{ContactID: ann.ID, Kind: directory.KeyAES256GCMShared,
		Version: 1, Status: directory.KeyActive, PublicData: []byte("secret"), EncryptedPriv: []byte("secret")}); err != nil {
		t.Fatal(err)
	}

	all := get("/api/directory/contacts")
	if len(all) != 2 || all[0].DisplayName != "Ann" || all[1].DisplayName != "Bob" {
		t.Fatalf("contacts %+v; want Ann then Bob", all)
	}
	if all[0].ID != ann.ID || len(all[0].Addresses) != 2 || all[0].Addresses[1].Kind != directory.KindIridiumIMT {
		t.Fatalf("Ann %+v", all[0])
	}
	if red := get("/api/directory/contacts?team=red"); len(red) != 1 || red[0].ID != bob.ID {
		t.Fatalf("team red: %+v", red)
	}
}

// A team send is refused as unsupported, not answered 503 as if a bearer
// were down: the dispatcher has no team fan-out (review B10-R3).
// Without delivery intents a team send is refused (C8 is intents only),
// as is a request that names no recipient; nothing is queued. [MESHSAT-1584]
func TestSendToContact_TeamNeedsDeliveryIntents(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	c := &directory.Contact{DisplayName: "Ann", Team: "red"}
	if err := store.CreateContact(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"group_id":"red","text":"team message"}`, `{"text":"to nobody"}`} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s; want 400", body, rr.Code, rr.Body.String())
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM message_deliveries`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d deliveries written (%v)", n, err)
	}
}
