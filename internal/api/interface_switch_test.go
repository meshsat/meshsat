package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/transport"
)

// switchServer is a Server with an interface manager and a running
// dispatcher over two links, mesh_0 and iridium_0, both on.
func switchServer(t *testing.T) (*Server, *engine.Dispatcher) {
	t.Helper()
	s := newTestServerWithDB(t)
	_, _ = s.db.Exec(`DELETE FROM interfaces`)
	for _, iface := range []database.Interface{
		{ID: "mesh_0", ChannelType: "mesh", Label: "Meshtastic LoRa", Enabled: true, Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"},
		{ID: "iridium_0", ChannelType: "iridium", Label: "Iridium SBD", Enabled: true, Config: "{}", IngressTransforms: "[]", EgressTransforms: "[]"},
	} {
		iface := iface
		if err := s.db.InsertInterface(&iface); err != nil {
			t.Fatal(err)
		}
	}
	mgr := engine.NewInterfaceManager(s.db)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	reg := channel.NewRegistry()
	channel.RegisterDefaults(reg)
	d := engine.NewDispatcher(s.db, reg, nil, nil)
	d.Start(ctx)
	t.Cleanup(func() {
		cancel()
		mgr.Stop()
		d.Wait()
	})
	s.SetInterfaceManager(mgr)
	s.SetDispatcher(d)
	return s, d
}

func post(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	return w
}

func listedEnabled(t *testing.T, s *Server, id string) bool {
	t.Helper()
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("GET", "/api/interfaces", nil))
	var list []struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("GET /api/interfaces: %v (%s)", err, w.Body.String())
	}
	for _, iface := range list {
		if iface.ID == id {
			return iface.Enabled
		}
	}
	t.Fatalf("%s not in the interface list", id)
	return false
}

// TestInterfaceSwitch_ListedAtOnce: a link switched off shows as off in the
// interface list at once, and on again after it is switched on. The switch
// used to reach the database only and showed after a restart. [MESHSAT-1401]
func TestInterfaceSwitch_ListedAtOnce(t *testing.T) {
	s, _ := switchServer(t)
	if w := post(t, s, "/api/interfaces/iridium_0/disable", ""); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if listedEnabled(t, s, "iridium_0") {
		t.Fatal("iridium_0 is still listed as on after it was switched off")
	}
	if w := post(t, s, "/api/interfaces/iridium_0/enable", ""); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	if !listedEnabled(t, s, "iridium_0") {
		t.Fatal("iridium_0 is listed as off after it was switched on")
	}
	if w := post(t, s, "/api/interfaces/nothing_0/disable", ""); w.Code != http.StatusNotFound {
		t.Fatalf("an unknown link: %d, want 404", w.Code)
	}
}

// TestInterfaceSwitch_StopsAndHoldsDeliveries: switching a link off stops its
// delivery worker and holds what waits for it; switching it on starts the
// worker again and releases what was held.
func TestInterfaceSwitch_StopsAndHoldsDeliveries(t *testing.T) {
	s, d := switchServer(t)
	if !d.HasWorker("iridium_0") {
		t.Fatal("no delivery worker for iridium_0 at start")
	}
	// A delivery waiting for the satellite, far in the future so the worker leaves it alone.
	next := time.Now().Add(time.Hour)
	id, err := s.db.InsertDelivery(database.MessageDelivery{MsgRef: "m-1", Channel: "iridium_0", Status: "retry", TextPreview: "wait", MaxRetries: 10, NextRetry: &next, QoSLevel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if w := post(t, s, "/api/interfaces/iridium_0/disable", ""); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if d.HasWorker("iridium_0") {
		t.Fatal("the delivery worker of a switched-off link still runs")
	}
	if del, _ := s.db.GetDelivery(id); del == nil || del.Status != "held" {
		t.Fatalf("the waiting delivery is %v, want held", del)
	}
	// The link's device coming online must not start its worker while it is off.
	d.StartWorker(context.Background(), "iridium_0", "iridium")
	if d.HasWorker("iridium_0") {
		t.Fatal("a worker was started for a switched-off link")
	}
	if w := post(t, s, "/api/interfaces/iridium_0/enable", ""); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	if !d.HasWorker("iridium_0") {
		t.Fatal("no delivery worker after the link was switched on")
	}
	if del, _ := s.db.GetDelivery(id); del == nil || del.Status == "held" {
		t.Fatalf("the delivery is still held after the link was switched on: %v", del)
	}
}

type sendRecorder struct {
	transport.MeshTransport
	sent []transport.SendRequest
}

func (m *sendRecorder) SendMessage(_ context.Context, req transport.SendRequest) error {
	m.sent = append(m.sent, req)
	return nil
}

// TestSendMessage_RefusedWhileTheMeshIsSwitchedOff: a direct send on the mesh
// is refused, in words, while every mesh link is switched off.
func TestSendMessage_RefusedWhileTheMeshIsSwitchedOff(t *testing.T) {
	s, _ := switchServer(t)
	mesh := &sendRecorder{}
	s.mesh = mesh
	s.processor = engine.NewProcessor(nil, nil) // the packet feed a mesh send is recorded in
	if w := post(t, s, "/api/interfaces/mesh_0/disable", ""); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	w := post(t, s, "/api/messages/send", `{"text":"hello"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Mesh is switched off") {
		t.Fatalf("send with the mesh off: %d %s", w.Code, w.Body.String())
	}
	if len(mesh.sent) != 0 {
		t.Fatalf("%d texts went out on a switched-off mesh", len(mesh.sent))
	}
	post(t, s, "/api/interfaces/mesh_0/enable", "")
	if w := post(t, s, "/api/messages/send", `{"text":"hello"}`); w.Code != http.StatusOK {
		t.Fatalf("send with the mesh on: %d %s", w.Code, w.Body.String())
	}
	if len(mesh.sent) != 1 {
		t.Fatalf("%d texts went out, want 1", len(mesh.sent))
	}
}
