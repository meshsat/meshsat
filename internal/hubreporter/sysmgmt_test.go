package hubreporter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSysMgmtHandlersRegistered confirms registerSysMgmtHandlers wires
// every MESHSAT-632 verb into the handler map.
func TestSysMgmtHandlersRegistered(t *testing.T) {
	ch := NewCommandHandler(nil, "test-bridge", func() BridgeHealth { return BridgeHealth{} })
	want := []string{
		"bt_status", "bt_devices", "bt_scan", "bt_pair", "bt_connect",
		"bt_disconnect", "bt_remove", "bt_power",
		"wifi_status", "wifi_scan", "wifi_saved", "wifi_connect", "wifi_disconnect",
	}
	for _, v := range want {
		if _, ok := ch.handlers[v]; !ok {
			t.Errorf("handler %q not registered", v)
		}
	}
}

// TestSysMgmtForwardsToLocalAPI exercises the bt_scan handler against
// a stub local API server; asserts it forwards the right path + query.
func TestSysMgmtForwardsToLocalAPI(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/api/system/bluetooth/scan") {
			t.Errorf("want /api/system/bluetooth/scan, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("duration") != "7" {
			t.Errorf("want duration=7, got %q", r.URL.Query().Get("duration"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer stub.Close()
	t.Setenv("MESHSAT_LOCAL_API_BASE", stub.URL)

	ch := NewCommandHandler(nil, "test-bridge", func() BridgeHealth { return BridgeHealth{} })
	h := ch.handlers["bt_scan"]
	cmd := Command{Payload: json.RawMessage(`{"duration":7}`)}
	out, err := h(cmd)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !strings.Contains(string(out), `"status":"ok"`) {
		t.Fatalf("want forwarded body, got %s", string(out))
	}
}

// TestSysMgmtSurfacesRESTError confirms a 4xx from the local API is
// turned into a command error, not a silent success.
func TestSysMgmtSurfacesRESTError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid MAC address"}`))
	}))
	defer stub.Close()
	t.Setenv("MESHSAT_LOCAL_API_BASE", stub.URL)

	ch := NewCommandHandler(nil, "test-bridge", func() BridgeHealth { return BridgeHealth{} })
	_, err := ch.handlers["bt_pair"](Command{Payload: json.RawMessage(`{"address":"nope"}`)})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "invalid MAC address") {
		t.Fatalf("want invalid-MAC-address in err, got %v", err)
	}
}
