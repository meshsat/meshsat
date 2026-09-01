package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/database"
	"meshsat/internal/keystore"
	"meshsat/internal/oob"
)

// oobFakeKeys is a minimal KeyProvider for the handler tests.
type oobFakeKeys struct {
	mu   sync.Mutex
	keys map[string][]byte
}

func (f *oobFakeKeys) GetKey(ct, addr string) ([]byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[ct+":"+addr]
	if !ok {
		return nil, 0, errors.New("no key")
	}
	return k, 1, nil
}
func (f *oobFakeKeys) StoreKey(ct, addr string, raw []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[ct+":"+addr] = append([]byte{}, raw...)
	return 1, nil
}
func (f *oobFakeKeys) RevokeKey(ct, addr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, ct+":"+addr)
	return nil
}
func (f *oobFakeKeys) CreateBundleFromEntries(entries []keystore.BundleEntry) ([]byte, string, error) {
	return []byte("b"), "meshsat://key/test-" + entries[0].Address, nil
}

type oobTestEnv struct {
	srv   *Server
	mux   *chi.Mux
	sends []string
	mu    sync.Mutex
}

func newOOBTestEnv(t *testing.T) *oobTestEnv {
	t.Helper()
	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	env := &oobTestEnv{}
	svc := oob.New(oob.Config{Enabled: true, ReplyBudgetHour: 12}, oob.Deps{
		DB:   db,
		Keys: &oobFakeKeys{keys: map[string][]byte{}},
		Host: oob.NewHostClient(filepath.Join(t.TempDir(), "none.sock")),
		Send: func(ctx context.Context, iface, addr, text string) (int64, error) {
			env.mu.Lock()
			defer env.mu.Unlock()
			env.sends = append(env.sends, iface+"|"+addr+"|"+text)
			return int64(len(env.sends)), nil
		},
		LocalAlias: "tesseract",
	})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	env.srv = &Server{db: db, oob: svc}
	s := env.srv
	mux := chi.NewRouter()
	mux.Get("/api/oob/config", s.handleGetOOBConfig)
	mux.Put("/api/oob/config", s.handleSetOOBConfig)
	mux.Get("/api/oob/peers", s.handleListOOBPeers)
	mux.Post("/api/oob/peers", s.handleCreateOOBPeer)
	mux.Put("/api/oob/peers/{id}", s.handleUpdateOOBPeer)
	mux.Delete("/api/oob/peers/{id}", s.handleDeleteOOBPeer)
	mux.Post("/api/oob/peers/{id}/bundle", s.handleOOBPeerBundle)
	mux.Get("/api/oob/peers/{id}/bundle/qr", s.handleOOBPeerBundleQR)
	mux.Post("/api/oob/send", s.handleOOBSend)
	mux.Get("/api/oob/log", s.handleGetOOBLog)
	mux.Get("/api/oob/targets", s.handleGetOOBTargets)
	mux.Get("/api/oob/agent", s.handleGetOOBAgent)
	env.mux = mux
	return env
}

func (e *oobTestEnv) do(t *testing.T, method, path string, body any) (int, map[string]any, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.Bytes()
}

func TestOOBHandlers_Unavailable(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleGetOOBConfig(rec, httptest.NewRequest(http.MethodGet, "/api/oob/config", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestOOBHandlers_ConfigPeersSendLog(t *testing.T) {
	env := newOOBTestEnv(t)

	// Config round trip.
	code, out, _ := env.do(t, http.MethodGet, "/api/oob/config", nil)
	if code != 200 || out["enabled"] != true || out["reply_budget"].(float64) != 12 {
		t.Fatalf("config: %d %v", code, out)
	}
	code, out, _ = env.do(t, http.MethodPut, "/api/oob/config", map[string]any{"enabled": true, "reply_budget": 5, "host_socket": "/tmp/x.sock"})
	if code != 200 || out["reply_budget"].(float64) != 5 || out["host_socket"] != "/tmp/x.sock" {
		t.Fatalf("set config: %d %v", code, out)
	}

	// Create a peer, list it, update it.
	code, out, _ = env.do(t, http.MethodPost, "/api/oob/peers", map[string]any{
		"alias": "parallax", "role": "control", "addresses": map[string]string{"cellular_0": "+31653207829"},
	})
	if code != 201 || out["alias"] != "parallax" || out["role"] != "control" || out["local_role"] != "issuer" || out["key_source"] != "bundle" {
		t.Fatalf("create: %d %v", code, out)
	}
	peerID := int(out["peer_id"].(float64))
	if peerID == 0 {
		t.Fatal("peer id 0")
	}
	if code, _, raw := env.do(t, http.MethodPost, "/api/oob/peers", map[string]any{"alias": "parallax"}); code != 400 {
		t.Fatalf("duplicate alias: %d %s", code, raw)
	}
	code, _, raw := env.do(t, http.MethodGet, "/api/oob/peers", nil)
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil || code != 200 || len(list) != 1 {
		t.Fatalf("list: %d %s %v", code, raw, err)
	}
	if addrs, ok := list[0]["addresses"].(map[string]any); !ok || addrs["cellular_0"] != "+31653207829" {
		t.Fatalf("addresses view: %v", list[0]["addresses"])
	}
	code, out, _ = env.do(t, http.MethodPut, "/api/oob/peers/"+strconv.Itoa(peerID), map[string]any{"role": "readonly", "enabled": false, "enc_policy": map[string]bool{"aprs_0": false}})
	if code != 200 || out["role"] != "readonly" || out["enabled"] != false {
		t.Fatalf("update: %d %v", code, out)
	}
	if code, _, _ := env.do(t, http.MethodPut, "/api/oob/peers/65535", map[string]any{"role": "control"}); code != 404 {
		t.Fatalf("update missing: %d", code)
	}
	if code, _, _ := env.do(t, http.MethodPut, "/api/oob/peers/abc", nil); code != 400 {
		t.Fatalf("update bad id: %d", code)
	}

	// Bundle issue (URL and QR).
	code, out, _ = env.do(t, http.MethodPost, "/api/oob/peers/"+strconv.Itoa(peerID)+"/bundle", nil)
	if code != 200 || !strings.HasPrefix(out["url"].(string), "meshsat://key/test-tesseract") {
		t.Fatalf("bundle: %d %v", code, out)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/oob/peers/"+strconv.Itoa(peerID)+"/bundle/qr?issuer_alias=kitA", nil)
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatalf("qr: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	// Send a command: the frame goes to the peer's bearer address.
	env.do(t, http.MethodPut, "/api/oob/peers/"+strconv.Itoa(peerID), map[string]any{"enabled": true})
	code, out, _ = env.do(t, http.MethodPost, "/api/oob/send", map[string]any{
		"peer_id": peerID, "via": "cellular_0", "cmd": "reboot", "args": map[string]any{"delay": 20},
	})
	if code != 202 || out["delivery_id"].(float64) != 1 || out["address"] != "+31653207829" || !strings.HasPrefix(out["text"].(string), "MS:") {
		t.Fatalf("send: %d %v", code, out)
	}
	if code, _, raw := env.do(t, http.MethodPost, "/api/oob/send", map[string]any{"peer_id": peerID, "via": "cellular_0", "cmd": "shell"}); code != 400 {
		t.Fatalf("send unknown cmd: %d %s", code, raw)
	}
	if code, _, raw := env.do(t, http.MethodPost, "/api/oob/send", map[string]any{"peer_id": peerID, "via": "cellular_0", "cmd": "reset", "args": map[string]any{"target": "netplan"}}); code != 400 {
		t.Fatalf("send bad target: %d %s", code, raw)
	}

	// Log, tables, agent.
	code, _, raw = env.do(t, http.MethodGet, "/api/oob/log?limit=10", nil)
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil || code != 200 || len(entries) != 1 || entries[0]["kind"] != "request" || entries[0]["direction"] != "out" {
		t.Fatalf("log: %d %s %v", code, raw, err)
	}
	code, out, _ = env.do(t, http.MethodGet, "/api/oob/targets", nil)
	if code != 200 || out["commands"] == nil || out["targets"] == nil || out["units"] == nil {
		t.Fatalf("targets: %d %v", code, out)
	}
	code, out, _ = env.do(t, http.MethodGet, "/api/oob/agent", nil)
	if code != 200 || out["available"] != false {
		t.Fatalf("agent: %d %v", code, out)
	}

	// Delete.
	if code, _, _ := env.do(t, http.MethodDelete, "/api/oob/peers/"+strconv.Itoa(peerID), nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := env.do(t, http.MethodDelete, "/api/oob/peers/"+strconv.Itoa(peerID), nil); code != 404 {
		t.Fatalf("delete again: %d", code)
	}
}
