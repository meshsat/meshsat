package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"meshsat/internal/cell"
)

// MESHSAT-1734: the operator's test send and the on-air list go through
// the cell broadcast client, its allow-list and the radio check, against
// a fake osmo-cbc.

type fakeECBE struct {
	mu      sync.Mutex
	posts   []map[string]any
	deletes []string
}

func (f *fakeECBE) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.posts = append(f.posts, body)
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete:
		f.deletes = append(f.deletes, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestCellCBSRoutes(t *testing.T) {
	ecbe := &fakeECBE{}
	srv := httptest.NewServer(ecbe)
	t.Cleanup(srv.Close)
	up := true
	c := cell.NewCBC(nil, cell.NewAllow(nil), func() (int, string, string) { return 873, "kit-test", "TEST" }, func() bool { return up })
	c.SetBase(srv.URL + "/api/ecbe/v1")
	s := &Server{}
	s.SetCellCBC(c)
	router := s.Router()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// The default channel is 4380, a test run, the TX log's purpose from the text.
	w := do(http.MethodPost, "/api/cell/cbs/test", `{"text":"TEST MeshSat relay on 4380, not an official alert","language":"en"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var b cell.Broadcast
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if b.Channel != 4380 || b.Severity != "test" || !b.Test || b.Pages != 1 || b.Count != cell.DefaultBroadcasts || b.DCS != 1 ||
		!strings.HasPrefix(b.Purpose, "test run: operator test send: TEST MeshSat relay on 4380,") {
		t.Fatalf("%+v", b)
	}
	if len(ecbe.posts) != 1 || ecbe.posts[0]["smscb_message"].(map[string]any)["message_id"].(float64) != 4380 {
		t.Fatalf("posts %v", ecbe.posts)
	}
	// A real identifier: 403, nothing posted.
	w = do(http.MethodPost, "/api/cell/cbs/test", `{"channel":4370,"text":"never"}`)
	if w.Code != http.StatusForbidden || len(ecbe.posts) != 1 || !strings.Contains(w.Body.String(), "government alert") {
		t.Fatalf("4370: %d %s", w.Code, w.Body.String())
	}
	// No text: 400. Bad JSON: 400.
	if w = do(http.MethodPost, "/api/cell/cbs/test", `{"channel":50}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no text: %d", w.Code)
	}
	if w = do(http.MethodPost, "/api/cell/cbs/test", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", w.Code)
	}
	// On air.
	w = do(http.MethodGet, "/api/cell/cbs", "")
	var list struct {
		Live    bool             `json:"live"`
		OnAir   []cell.Broadcast `json:"on_air"`
		Allowed struct {
			Ordinary string `json:"ordinary"`
			Test     []int  `json:"test"`
			Real     []int  `json:"real"`
		} `json:"allowed"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != http.StatusOK || !list.Live || len(list.OnAir) != 1 || list.OnAir[0].Channel != 4380 || list.Allowed.Ordinary != "1-4351" ||
		len(list.Allowed.Test) != 3 || len(list.Allowed.Real) != 0 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// Stop it.
	w = do(http.MethodDelete, "/api/cell/cbs/4380", "")
	if w.Code != http.StatusOK || len(ecbe.deletes) != 1 || ecbe.deletes[0] != "4380" {
		t.Fatalf("stop: %d %s deletes %v", w.Code, w.Body.String(), ecbe.deletes)
	}
	if w = do(http.MethodDelete, "/api/cell/cbs/x", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad channel: %d", w.Code)
	}
	w = do(http.MethodGet, "/api/cell/cbs", "")
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.OnAir) != 0 {
		t.Fatalf("still on air: %s", w.Body.String())
	}
	// The radio down: 409.
	up = false
	if w = do(http.MethodPost, "/api/cell/cbs/test", `{"channel":50,"text":"radio down"}`); w.Code != http.StatusConflict {
		t.Fatalf("radio down: %d %s", w.Code, w.Body.String())
	}
	// No cell at all: 503 on every route.
	s.SetCellCBC(nil)
	for _, c := range []struct{ method, path string }{{http.MethodPost, "/api/cell/cbs/test"}, {http.MethodGet, "/api/cell/cbs"}, {http.MethodDelete, "/api/cell/cbs/50"}} {
		if w = do(c.method, c.path, `{"text":"x"}`); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s without a cell: %d", c.method, c.path, w.Code)
		}
	}
}
