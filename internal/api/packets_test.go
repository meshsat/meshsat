package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"meshsat/internal/engine"
	"meshsat/internal/gateway"
)

func packetsTestServer(t *testing.T) (*Server, *engine.PacketRing) {
	t.Helper()
	proc := engine.NewProcessor(nil, nil)
	ring := proc.Packets()
	now := time.Now()
	for i := 0; i < 3; i++ {
		ring.Add(gateway.PacketRecord{Time: now.Add(-time.Duration(i) * time.Second), Bearer: "lora", Dir: "rx", Iface: "mesh_0", From: "!00000001"})
	}
	ring.Add(gateway.PacketRecord{Time: now.Add(-2 * time.Minute), Bearer: "aprs", Dir: "tx", Iface: "aprs_0", From: "MSTESS-10"})
	ring.Add(gateway.PacketRecord{Time: now.Add(-10 * time.Minute), Bearer: "sms", Dir: "rx", Iface: "cellular_0", From: "+31600000000"})
	return &Server{processor: proc}, ring
}

func TestHandleGetPackets(t *testing.T) {
	s, _ := packetsTestServer(t)
	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantCount  int
		wantFirst  string
	}{
		// Newest = most recently added (arrival order), the sms record.
		{"default", "", http.StatusOK, 5, "+31600000000"},
		{"limit 2", "?limit=2", http.StatusOK, 2, "+31600000000"},
		{"limit above max is clamped", "?limit=9999", http.StatusOK, 5, "+31600000000"},
		{"bearer aprs", "?bearer=aprs", http.StatusOK, 1, "MSTESS-10"},
		{"dir tx", "?dir=tx", http.StatusOK, 1, "MSTESS-10"},
		{"bearer sms dir rx", "?bearer=sms&dir=rx", http.StatusOK, 1, "+31600000000"},
		{"bearer sms dir tx: empty", "?bearer=sms&dir=tx", http.StatusOK, 0, ""},
		{"bad bearer", "?bearer=ble", http.StatusBadRequest, 0, ""},
		{"bad dir", "?dir=sideways", http.StatusBadRequest, 0, ""},
		{"bad limit", "?limit=-1", http.StatusBadRequest, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/packets"+tc.query, nil)
			rr := httptest.NewRecorder()
			s.handleGetPackets(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var body struct {
				Packets []gateway.PacketRecord `json:"packets"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Packets == nil {
				t.Fatal("packets must be an array, never null")
			}
			if len(body.Packets) != tc.wantCount {
				t.Fatalf("count = %d, want %d", len(body.Packets), tc.wantCount)
			}
			if tc.wantCount > 0 && body.Packets[0].From != tc.wantFirst {
				t.Errorf("first from = %q, want %q", body.Packets[0].From, tc.wantFirst)
			}
		})
	}
}

func TestHandleGetPacketRates(t *testing.T) {
	s, _ := packetsTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/packets/rates", nil)
	rr := httptest.NewRecorder()
	s.handleGetPacketRates(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		WindowS int                           `json:"window_s"`
		Bearers map[string]engine.PacketRates `json:"bearers"`
		Long    struct {
			WindowS int                           `json:"window_s"`
			Bearers map[string]engine.PacketRates `json:"bearers"`
		} `json:"long"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.WindowS != 60 || body.Long.WindowS != 300 {
		t.Errorf("windows = %d / %d", body.WindowS, body.Long.WindowS)
	}
	for _, b := range []string{"lora", "aprs", "sms"} {
		if _, ok := body.Bearers[b]; !ok {
			t.Errorf("short window missing bearer %s", b)
		}
		if _, ok := body.Long.Bearers[b]; !ok {
			t.Errorf("long window missing bearer %s", b)
		}
	}
	if body.Bearers["lora"].RX != 3 || body.Bearers["aprs"].TX != 0 || body.Bearers["sms"].RX != 0 {
		t.Errorf("short window: %+v", body.Bearers)
	}
	if body.Long.Bearers["lora"].RX != 3 || body.Long.Bearers["aprs"].TX != 1 || body.Long.Bearers["sms"].RX != 0 {
		t.Errorf("long window: %+v", body.Long.Bearers)
	}
}

func TestPacketsHandlers_NilProcessor(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	s.handleGetPackets(rr, httptest.NewRequest(http.MethodGet, "/api/packets", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("packets status = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleGetPacketRates(rr, httptest.NewRequest(http.MethodGet, "/api/packets/rates", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("rates status = %d", rr.Code)
	}
}
