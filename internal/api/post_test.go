package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	cpost "meshsat/internal/post"
)

// The Community Post's operator API [MESHSAT-1733]: 503 while the post is
// not configured, the queue, approve and refuse, the settings.

type postTestQueue struct {
	db   *database.DB
	rows []string
}

func (q *postTestQueue) QueueDirectSendKeyed(source, key, fp, iface, text string, opts engine.DirectSendOptions) (int64, string, bool, error) {
	id, err := q.db.InsertDelivery(database.MessageDelivery{Channel: iface, Status: "queued", MsgRef: "ref-" + key, TextPreview: text, Class: opts.Class, MaxRetries: 3})
	if err != nil {
		return 0, "", false, err
	}
	q.rows = append(q.rows, text)
	return id, "ref-" + key, false, nil
}

func newPostAPIEnv(t *testing.T, configured bool) (*chi.Mux, *cpost.Service, *postTestQueue) {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{db: db}
	var svc *cpost.Service
	q := &postTestQueue{db: db}
	if configured {
		svc = cpost.New(cpost.Config{Listen: "0.0.0.0:8081", Operator: "Test Post"}, cpost.Deps{DB: db, Queue: q,
			Bearer: func() (string, bool) { return "iridium_0", true }})
		s.SetPost(svc)
	}
	mux := chi.NewRouter()
	mux.Get("/api/post/config", s.handleGetPostConfig)
	mux.Put("/api/post/config", s.handleSetPostConfig)
	mux.Get("/api/post/queue", s.handleGetPostQueue)
	mux.Get("/api/post/{code}", s.handleGetPost)
	mux.Post("/api/post/{code}/approve", s.handleApprovePost)
	mux.Post("/api/post/{code}/refuse", s.handleRefusePost)
	return mux, svc, q
}

func postAPI(t *testing.T, mux http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestPostAPIAnswers503UntilConfigured(t *testing.T) {
	mux, _, _ := newPostAPIEnv(t, false)
	for _, c := range []struct{ method, path string }{{"GET", "/api/post/config"}, {"GET", "/api/post/queue"}, {"POST", "/api/post/ABCDEF/approve"}} {
		if code, _ := postAPI(t, mux, c.method, c.path, ""); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, code)
		}
	}
}

func TestPostAPIQueueApproveRefuseAndSettings(t *testing.T) {
	mux, svc, q := newPostAPIEnv(t, true)
	code, body := postAPI(t, mux, "GET", "/api/post/config", "")
	if code != 200 || body["enabled"] != true || body["auto_approve"] != false || body["operator"] != "Test Post" || body["posts_per_hour"].(float64) != 3 {
		t.Fatalf("config = %d %v", code, body)
	}
	a, err := svc.Create("ip:10.44.0.11", "en", cpost.Submission{Recipient: "+31612345678", Text: "water", Name: "Maria"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create("ip:10.44.0.12", "en", cpost.Submission{Recipient: "ops@example.org", Text: "fuel", Emergency: true})
	if err != nil {
		t.Fatal(err)
	}
	code, body = postAPI(t, mux, "GET", "/api/post/queue", "")
	if code != 200 || len(body["pending"].([]any)) != 2 || body["counts"].(map[string]any)["pending_approval"].(float64) != 2 {
		t.Fatalf("queue = %d %v", code, body)
	}
	if code, _ := postAPI(t, mux, "POST", "/api/post/"+strings.ToLower(a.Code)+"/approve", ""); code != 200 {
		t.Fatalf("approve = %d", code)
	}
	if len(q.rows) != 1 || q.rows[0] != "@+31612345678 Maria: water [#"+a.Code+"]" {
		t.Fatalf("queued %v", q.rows)
	}
	if code, body := postAPI(t, mux, "POST", "/api/post/"+b.Code+"/refuse", ""); code != 200 || body["post"].(map[string]any)["status"] != "refused" {
		t.Fatalf("refuse = %d %v", code, body)
	}
	if code, _ := postAPI(t, mux, "POST", "/api/post/"+b.Code+"/approve", ""); code != http.StatusConflict {
		t.Fatalf("approve a refused post = %d, want 409", code)
	}
	if code, _ := postAPI(t, mux, "POST", "/api/post/ZZZZZZ/approve", ""); code != http.StatusNotFound {
		t.Fatalf("unknown code = %d, want 404", code)
	}
	if code, _ := postAPI(t, mux, "GET", "/api/post/not-a-code", ""); code != http.StatusNotFound {
		t.Fatalf("GET unknown = %d", code)
	}
	code, body = postAPI(t, mux, "GET", "/api/post/"+a.Code, "")
	if code != 200 || body["post"].(map[string]any)["status"] != "queued" {
		t.Fatalf("get = %d %v", code, body)
	}
	code, body = postAPI(t, mux, "PUT", "/api/post/config", `{"auto_approve":true,"posts_per_hour":5,"posts_per_day":0,"operator":"  Gemeente  Leiden "}`)
	if code != 200 || body["auto_approve"] != true || body["posts_per_hour"].(float64) != 5 || body["operator"] != "Gemeente Leiden" {
		t.Fatalf("settings = %d %v", code, body)
	}
	if code, _ := postAPI(t, mux, "PUT", "/api/post/config", `{"posts_per_hour":-1}`); code != http.StatusBadRequest {
		t.Fatalf("a negative ceiling = %d, want 400", code)
	}
	// With auto-approve on, a new post leaves at once.
	if _, err := svc.Create("ip:10.44.0.13", "en", cpost.Submission{Recipient: "+31612345678", Text: "now"}); err != nil {
		t.Fatal(err)
	}
	if len(q.rows) != 2 {
		t.Fatalf("auto-approve did not send: %v", q.rows)
	}
}
