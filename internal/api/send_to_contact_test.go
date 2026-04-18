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

// testServerWithDispatcher builds a Server wired to a directory-aware
// Dispatcher for the MESHSAT-545 endpoint tests.
func testServerWithDispatcher(t *testing.T, kinds ...directory.Kind) (*Server, *directory.SQLStore) {
	t.Helper()
	s := newTestServerWithDB(t)

	// Clear migration-seeded interfaces and re-insert one per kind.
	_, _ = s.db.Exec(`DELETE FROM interfaces`)
	for _, k := range kinds {
		chanType := kindChannelType(k)
		_ = s.db.InsertInterface(&database.Interface{
			ID: chanType + "_0", ChannelType: chanType, Label: string(k),
			Enabled: true, Config: "{}",
			IngressTransforms: "[]", EgressTransforms: "[]",
		})
	}
	reg := channel.NewRegistry()
	d := engine.NewDispatcher(s.db, reg, nil, nil)
	store := directory.NewSQLStore(s.db.DB)
	d.SetRecipientResolver(store)
	s.SetDispatcher(d)
	return s, store
}

// kindChannelType duplicates engine's private mapping so tests in
// this package stay self-contained.
func kindChannelType(k directory.Kind) string {
	switch k {
	case directory.KindSMS:
		return "sms"
	case directory.KindMeshtastic:
		return "mesh"
	case directory.KindAPRS:
		return "aprs"
	case directory.KindIridiumSBD:
		return "iridium"
	case directory.KindIridiumIMT:
		return "iridium_imt"
	case directory.KindCellular:
		return "cellular"
	case directory.KindTAK:
		return "tak"
	}
	return ""
}

// --- MESHSAT-545 S2-02 tests -----------------------------------------

func TestSendToContact_HappyPath(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS, directory.KindMeshtastic)
	router := s.Router()

	c := &directory.Contact{DisplayName: "Alice"}
	_ = store.CreateContact(t.Context(), c)
	_ = store.AddAddress(t.Context(), &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+31612345678", PrimaryRank: 0,
	})
	_ = store.AddAddress(t.Context(), &directory.Address{
		ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!abcd1234", PrimaryRank: 1,
	})

	body := `{"contact_id":"` + c.ID + `","text":"hello","precedence":"Flash","strategy":"PRIMARY_ONLY"}`
	req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp sendToContactResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Queued {
		t.Error("queued: false")
	}
	if resp.Strategy != "PRIMARY_ONLY" {
		t.Errorf("strategy: %s", resp.Strategy)
	}
	if resp.Precedence != "Flash" {
		t.Errorf("precedence: %s", resp.Precedence)
	}
	if len(resp.PerBearer) != 1 || resp.PerBearer[0].Kind != "SMS" {
		t.Errorf("per_bearer: %+v", resp.PerBearer)
	}
}

func TestSendToContact_AllBearers(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS, directory.KindMeshtastic, directory.KindAPRS)
	router := s.Router()

	c := &directory.Contact{DisplayName: "Broadcast"}
	_ = store.CreateContact(t.Context(), c)
	for _, a := range []directory.Address{
		{ContactID: c.ID, Kind: directory.KindSMS, Value: "+316"},
		{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!m"},
		{ContactID: c.ID, Kind: directory.KindAPRS, Value: "X-1"},
	} {
		a := a
		_ = store.AddAddress(t.Context(), &a)
	}
	body := `{"contact_id":"` + c.ID + `","text":"to all","strategy":"ALL_BEARERS"}`
	req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	var resp sendToContactResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.PerBearer) != 3 {
		t.Errorf("expected 3 bearers, got %d: %+v", len(resp.PerBearer), resp.PerBearer)
	}
}

func TestSendToContact_MissingText(t *testing.T) {
	s, _ := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	req := httptest.NewRequest("POST", "/api/messages/send-to-contact",
		strings.NewReader(`{"contact_id":"x","text":""}`))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing text: got %d, want 400", rr.Code)
	}
}

func TestSendToContact_MissingTarget(t *testing.T) {
	s, _ := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	req := httptest.NewRequest("POST", "/api/messages/send-to-contact",
		strings.NewReader(`{"text":"hi"}`))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("no target: got %d", rr.Code)
	}
}

func TestSendToContact_InvalidPrecedence(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	c := &directory.Contact{DisplayName: "X"}
	_ = store.CreateContact(t.Context(), c)
	_ = store.AddAddress(t.Context(), &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+3100",
	})
	body := `{"contact_id":"` + c.ID + `","text":"x","precedence":"URGENT_NOW"}`
	req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad precedence: got %d", rr.Code)
	}
}

func TestSendToContact_ProsignPrecedence(t *testing.T) {
	s, store := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	c := &directory.Contact{DisplayName: "Z"}
	_ = store.CreateContact(t.Context(), c)
	_ = store.AddAddress(t.Context(), &directory.Address{
		ContactID: c.ID, Kind: directory.KindSMS, Value: "+3111111111",
	})
	body := `{"contact_id":"` + c.ID + `","text":"urgent","precedence":"Z"}`
	req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("prosign Z status: %d body=%s", rr.Code, rr.Body.String())
	}
	var resp sendToContactResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Precedence != "Flash" {
		t.Errorf("prosign Z → %s (want Flash)", resp.Precedence)
	}
}

func TestSendToContact_RawEscape(t *testing.T) {
	s, _ := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	body := `{"raw_interface_id":"sms_0","raw_address":"+31000","text":"adhoc"}`
	req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("raw escape: %d %s", rr.Code, rr.Body.String())
	}
	var resp sendToContactResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp.Queued || len(resp.PerBearer) != 1 {
		t.Errorf("raw escape: %+v", resp)
	}
}

func TestSendToContact_UnknownContact_503(t *testing.T) {
	s, _ := testServerWithDispatcher(t, directory.KindSMS)
	router := s.Router()
	body := `{"contact_id":"nonexistent","text":"x"}`
	req := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	// Zero-bearer outcome returns 503 with the per_bearer breakdown.
	if rr.Code != http.StatusServiceUnavailable && rr.Code != http.StatusInternalServerError {
		t.Errorf("unknown contact status: got %d, want 503 or 500", rr.Code)
	}
}
