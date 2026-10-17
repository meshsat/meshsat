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

// A retried send under the same Idempotency-Key queues nothing again, with
// delivery intents on or off, and when the flag changes between the
// requests (review B10-R4). [MESHSAT-1584]

func keyedServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := newTestServerWithDB(t)
	if _, err := s.db.Exec(`DELETE FROM interfaces`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.InsertInterface(&database.Interface{ID: "mesh_0", ChannelType: "mesh", Label: "mesh", Enabled: true,
		Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"}); err != nil {
		t.Fatal(err)
	}
	store := directory.NewSQLStore(s.db.DB)
	c := &directory.Contact{DisplayName: "Ann"}
	if err := store.CreateContact(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := store.AddAddress(t.Context(), &directory.Address{ContactID: c.ID, Kind: directory.KindMeshtastic, Value: "!00000001"}); err != nil {
		t.Fatal(err)
	}
	return s, c.ID
}

// withIntents gives s a dispatcher with delivery intents on or off, over
// the same database: a restart with the flag changed.
func withIntents(t *testing.T, s *Server, on bool) {
	t.Helper()
	d := engine.NewDispatcher(s.db, channel.NewRegistry(), nil, idMesh{})
	d.SetRecipientResolver(directory.NewSQLStore(s.db.DB))
	esc := engine.NewEscalator(s.db, d, engine.DefaultEscalatorConfig(), nil, on)
	d.SetEscalator(esc)
	esc.SetReady()
	s.SetDispatcher(d)
}

func keyedPost(t *testing.T, s *Server, body, key string) (int, sendToContactResponse) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/messages/send-to-contact", strings.NewReader(body))
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set("X-MeshSat-Client", "spa")
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	var resp sendToContactResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

func rowCount(t *testing.T, s *Server, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSendToContact_KeyHoldsWithIntentsOff(t *testing.T) {
	s, id := keyedServer(t)
	withIntents(t, s, false)
	body := fmt.Sprintf(`{"contact_id":%q,"text":"once","strategy":"PRIMARY_ONLY"}`, id)

	c1, r1 := keyedPost(t, s, body, "k-off")
	c2, r2 := keyedPost(t, s, body, "k-off")
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("codes %d, %d", c1, c2)
	}
	if n := rowCount(t, s, "message_deliveries"); n != 1 {
		t.Fatalf("%d delivery rows; the retry queued the message again", n)
	}
	if r1.SubmissionID == "" || r2.SubmissionID != r1.SubmissionID || r1.Existing || !r2.Existing {
		t.Fatalf("first %+v, retry %+v", r1, r2)
	}
	if len(r2.PerBearer) != 1 || len(r2.PerBearer[0].DeliveryIDs) != 1 || r2.PerBearer[0].DeliveryIDs[0] != r1.PerBearer[0].DeliveryIDs[0] {
		t.Fatalf("the retry did not answer with the first send's row: %+v vs %+v", r2.PerBearer, r1.PerBearer)
	}
	if c, _ := keyedPost(t, s, strings.Replace(body, "once", "another text", 1), "k-off"); c != http.StatusConflict {
		t.Fatalf("a different message under the key: %d, want 409", c)
	}
	// Without a key nothing changes: each request queues.
	keyedPost(t, s, body, "")
	keyedPost(t, s, body, "")
	if n := rowCount(t, s, "message_deliveries"); n != 3 {
		t.Fatalf("%d rows; want 3 (one keyed, two unkeyed)", n)
	}
}

func TestSendToContact_KeyHoldsAcrossAFlagChange(t *testing.T) {
	for _, first := range []bool{true, false} {
		t.Run(fmt.Sprintf("first_intents_%v", first), func(t *testing.T) {
			s, id := keyedServer(t)
			body := fmt.Sprintf(`{"contact_id":%q,"text":"across","strategy":"PRIMARY_ONLY"}`, id)
			withIntents(t, s, first)
			c1, r1 := keyedPost(t, s, body, "k-flag")
			rows, subs, intents := rowCount(t, s, "message_deliveries"), rowCount(t, s, "delivery_submissions"), rowCount(t, s, "delivery_intents")
			withIntents(t, s, !first)
			c2, r2 := keyedPost(t, s, body, "k-flag")
			if c1 != http.StatusOK || c2 != http.StatusOK {
				t.Fatalf("codes %d, %d", c1, c2)
			}
			if rowCount(t, s, "message_deliveries") != rows || rowCount(t, s, "delivery_submissions") != subs || rowCount(t, s, "delivery_intents") != intents {
				t.Fatal("the retry after the flag changed queued new work")
			}
			if subs != 1 || r2.SubmissionID != r1.SubmissionID || !r2.Existing {
				t.Fatalf("first %+v, retry %+v", r1, r2)
			}
			if first && (len(r2.IntentIDs) != 1 || r2.IntentIDs[0] != r1.IntentIDs[0]) {
				t.Fatalf("the retry did not answer with the first intent: %v vs %v", r2.IntentIDs, r1.IntentIDs)
			}
		})
	}
}

func TestSendToContact_KeyHoldsForARawSend(t *testing.T) {
	s, _ := keyedServer(t)
	withIntents(t, s, false)
	body := `{"raw_interface_id":"mesh_0","raw_address":"!00000002","text":"raw once"}`
	keyedPost(t, s, body, "k-raw")
	_, r2 := keyedPost(t, s, body, "k-raw")
	if n := rowCount(t, s, "message_deliveries"); n != 1 || !r2.Existing {
		t.Fatalf("%d rows, retry %+v", n, r2)
	}
}
