package api

import (
	"context"
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
	"meshsat/internal/transport"
)

type idMesh struct{ transport.MeshTransport }

func (idMesh) SendMessageID(context.Context, transport.SendRequest) (uint32, error) { return 1, nil }

func (idMesh) SendMessageWithID(context.Context, transport.SendRequest, uint32) error { return nil }

// The delivery-intent endpoints (HeMB-NG stage 1a, r3 D11 and B10):
// send-to-contact with an Idempotency-Key, the refusals that write nothing
// (400, 409, 413, 422), the status, the cancel. [MESHSAT-1581]
func TestSendToContact_DeliveryIntents(t *testing.T) {
	s := newTestServerWithDB(t)
	_, _ = s.db.Exec(`DELETE FROM interfaces`)
	for _, i := range []string{"mesh", "cellular"} {
		_ = s.db.InsertInterface(&database.Interface{ID: i + "_0", ChannelType: i, Label: i, Enabled: true,
			Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"})
	}
	d := engine.NewDispatcher(s.db, channel.NewRegistry(), nil, idMesh{})
	store := directory.NewSQLStore(s.db.DB)
	d.SetRecipientResolver(store)
	esc := engine.NewEscalator(s.db, d, engine.DefaultEscalatorConfig(), nil, true)
	d.SetEscalator(esc)
	esc.SetReady()
	s.SetDispatcher(d)
	router := s.Router()

	newContact := func(addrs ...directory.Address) string {
		c := &directory.Contact{DisplayName: "Bo"}
		_ = store.CreateContact(t.Context(), c)
		for _, a := range addrs {
			a := a
			a.ContactID = c.ID
			_ = store.AddAddress(t.Context(), &a)
		}
		return c.ID
	}
	post := func(path, body, key string) (*httptest.ResponseRecorder, sendToContactResponse) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
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
	submissions := func() int {
		var n int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM delivery_submissions`).Scan(&n)
		return n
	}

	mesh := newContact(directory.Address{Kind: directory.KindMeshtastic, Value: "!0000abcd"})
	body := fmt.Sprintf(`{"contact_id":%q,"text":"hello","strategy":"PRIMARY_ONLY"}`, mesh)
	rr, first := post("/api/messages/send-to-contact", body, "key-1")
	if rr.Code != http.StatusOK || len(first.IntentIDs) != 1 || first.SubmissionID == "" || first.Existing {
		t.Fatalf("first: %d %s", rr.Code, rr.Body.String())
	}
	rr, again := post("/api/messages/send-to-contact", body, "key-1")
	if rr.Code != http.StatusOK || !again.Existing || again.IntentIDs[0] != first.IntentIDs[0] {
		t.Fatalf("retry: %d %s", rr.Code, rr.Body.String())
	}
	if rr, _ := post("/api/messages/send-to-contact", strings.Replace(body, "hello", "changed", 1), "key-1"); rr.Code != http.StatusConflict {
		t.Fatalf("same key, other text: %d; want 409", rr.Code)
	}
	n := submissions()
	if rr, _ := post("/api/messages/send-to-contact", fmt.Sprintf(`{"contact_id":%q,"text":"x","strategy":"HEMB_BONDED"}`, mesh), ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("unsupported strategy: %d; want 400", rr.Code)
	}
	smsOnly := newContact(directory.Address{Kind: directory.KindSMS, Value: "+31600000001"})
	if rr, resp := post("/api/messages/send-to-contact", fmt.Sprintf(`{"contact_id":%q,"text":"x"}`, smsOnly), ""); rr.Code != http.StatusUnprocessableEntity ||
		len(resp.PerBearer) != 1 || !strings.Contains(resp.PerBearer[0].Error, "skipped_offline") { // SMS is a route since stage 1b; no SMS gateway runs here
		t.Fatalf("no usable route: %d %s; want 422 with the reason", rr.Code, rr.Body.String())
	}
	if rr, _ := post("/api/messages/send-to-contact", fmt.Sprintf(`{"contact_id":%q,"text":%q}`, mesh, strings.Repeat("a", 101)), ""); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too long for the mesh: %d; want 413", rr.Code)
	}
	if submissions() != n {
		t.Fatal("a refused request wrote a submission")
	}

	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest("GET", "/api/deliveries/intents/"+first.IntentIDs[0], nil))
	var view engine.IntentView
	_ = json.Unmarshal(get.Body.Bytes(), &view)
	if get.Code != http.StatusOK || view.Label != "sending" || len(view.Routes) != 1 || view.Routes[0].Destination != "!0000abcd" {
		t.Fatalf("status: %d %s", get.Code, get.Body.String())
	}
	rowID := view.Routes[0].Attempts[0].DeliveryID
	if rr, _ := post(fmt.Sprintf("/api/deliveries/%d/cancel", rowID), "", ""); rr.Code != http.StatusConflict {
		t.Fatalf("queue cancel of an intent's row: %d; want 409", rr.Code)
	}
	rr, _ = post("/api/deliveries/intents/"+first.IntentIDs[0]+"/cancel", `{"reason":"test"}`, "")
	_ = json.Unmarshal(rr.Body.Bytes(), &view)
	if rr.Code != http.StatusOK || view.Outcome != database.OutcomeCancelled || view.State != database.IntentClosed {
		t.Fatalf("cancel: %d %s", rr.Code, rr.Body.String())
	}
	if rr, _ := post("/api/deliveries/intents/"+first.IntentIDs[0]+"/cancel", "", ""); rr.Code != http.StatusConflict {
		t.Fatalf("second cancel: %d; want 409", rr.Code)
	}
	nf := httptest.NewRecorder()
	router.ServeHTTP(nf, httptest.NewRequest("GET", "/api/deliveries/intents/nope", nil))
	if nf.Code != http.StatusNotFound {
		t.Fatalf("unknown intent: %d; want 404", nf.Code)
	}
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest("GET", "/api/deliveries/intents?contact="+mesh, nil))
	var views []engine.IntentView
	_ = json.Unmarshal(list.Body.Bytes(), &views)
	if list.Code != http.StatusOK || len(views) != 1 {
		t.Fatalf("list: %d, %d intents", list.Code, len(views))
	}
}
