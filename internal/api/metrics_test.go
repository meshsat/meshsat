package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsEndpoint_ReturnsPrometheusFormat(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") && !strings.Contains(ct, "openmetrics") {
		t.Errorf("unexpected Content-Type: %s", ct)
	}

	body := w.Body.String()

	// System metrics always present (reads procfs)
	wantMetrics := []string{
		"meshsat_system_cpu_percent",
		"meshsat_system_memory_percent",
		"meshsat_system_disk_percent",
		"meshsat_system_uptime_seconds",
		// HeMB global stats always present (singleton)
		"meshsat_hemb_symbols_sent_total",
		"meshsat_hemb_generations_decoded_total",
		"meshsat_hemb_cost_usd_total",
		"meshsat_hemb_decode_latency_p50_ms",
		// Messages from DB
		"meshsat_messages_total",
		"meshsat_messages_today",
	}

	for _, m := range wantMetrics {
		if !strings.Contains(body, m) {
			t.Errorf("missing metric %q in output", m)
		}
	}
}

func TestMetricsEndpoint_NilSubsystems(t *testing.T) {
	// Server with DB but no gwManager, dispatcher, or transforms.
	// Metrics endpoint must still work — just omits those sections.
	s := newTestServerWithDB(t)
	router := s.Router()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Gateway metrics should NOT appear (no gwManager)
	if strings.Contains(body, "meshsat_gateway_messages_in_total{") {
		t.Error("gateway metrics should be absent with nil gwManager")
	}

	// FEC metrics should NOT appear (no transforms)
	if strings.Contains(body, "meshsat_fec_encode_ok_total ") {
		t.Error("FEC metrics should be absent with nil transforms")
	}

	// Delivery metrics should NOT appear (no dispatcher)
	if strings.Contains(body, "meshsat_delivery_hop_limit_drops_total ") {
		t.Error("delivery metrics should be absent with nil dispatcher")
	}

	// System + HeMB + message metrics still present
	if !strings.Contains(body, "meshsat_system_cpu_percent") {
		t.Error("system metrics should be present regardless")
	}
	if !strings.Contains(body, "meshsat_hemb_symbols_sent_total") {
		t.Error("HeMB metrics should be present regardless")
	}
}

func TestMetricsEndpoint_HELP_and_TYPE(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	body := w.Body.String()

	// Prometheus exposition format must include HELP and TYPE lines
	if !strings.Contains(body, "# HELP meshsat_system_cpu_percent") {
		t.Error("missing HELP for system_cpu_percent")
	}
	if !strings.Contains(body, "# TYPE meshsat_system_cpu_percent gauge") {
		t.Error("missing TYPE for system_cpu_percent")
	}
	if !strings.Contains(body, "# TYPE meshsat_hemb_symbols_sent_total counter") {
		t.Error("missing TYPE counter for hemb_symbols_sent_total")
	}
}
