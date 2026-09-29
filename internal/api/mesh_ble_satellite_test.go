package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"meshsat/internal/transport"
)

// satBLEMesh is a node over Bluetooth whose modem pipe the API reaches.
type satBLEMesh struct {
	transport.MeshTransport
	status   transport.BLEStatus
	stats    *transport.PipeStatsReading
	statsErr error
	set      []bool
}

func (m *satBLEMesh) BLEScan(context.Context, int) ([]transport.BLEDevice, error) { return nil, nil }
func (m *satBLEMesh) BLEConnect(context.Context, string) error                    { return nil }
func (m *satBLEMesh) BLEPair(string) error                                        { return nil }
func (m *satBLEMesh) BLEStatus() transport.BLEStatus                              { return m.status }
func (m *satBLEMesh) BLEForget(context.Context, bool) error                       { return nil }

func (m *satBLEMesh) BLESetSatellite(on bool) error {
	m.set = append(m.set, on)
	m.status.SatelliteEnabled = on
	return nil
}

func (m *satBLEMesh) BLESatelliteStats(context.Context) (*transport.PipeStatsReading, error) {
	return m.stats, m.statsErr
}

// plainBLEMesh is a node over Bluetooth with no satellite modem behind it.
type plainBLEMesh struct {
	transport.MeshTransport
	status transport.BLEStatus
}

func (m *plainBLEMesh) BLEScan(context.Context, int) ([]transport.BLEDevice, error) { return nil, nil }
func (m *plainBLEMesh) BLEConnect(context.Context, string) error                    { return nil }
func (m *plainBLEMesh) BLEPair(string) error                                        { return nil }
func (m *plainBLEMesh) BLEStatus() transport.BLEStatus                              { return m.status }
func (m *plainBLEMesh) BLEForget(context.Context, bool) error                       { return nil }

func decodeBody(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return out
}

// The status carries the switch, the owner and the broken link; PUT sets
// the switch; the node's report is answered with read_at, 404 without one.
// [MESHSAT-1391]
func TestBLESatellite_StatusSwitchAndStats(t *testing.T) {
	mesh := &satBLEMesh{status: transport.BLEStatus{Mode: "ready", Connected: true, SatellitePipe: true,
		SatelliteEnabled: true, SatelliteOwner: transport.PipeOwnerPhone}}
	s := &Server{mesh: mesh}

	w := serve(t, s, "GET", "/api/mesh/ble/status", "")
	st := decodeBody(t, w.Body.Bytes())
	if w.Code != http.StatusOK || st["satellite_enabled"] != true || st["satellite_owner"] != "phone" || st["satellite_link_broken"] != false {
		t.Fatalf("status %d %v", w.Code, st)
	}

	w = serve(t, s, "PUT", "/api/mesh/ble/satellite", `{"enabled": false}`)
	if w.Code != http.StatusOK || len(mesh.set) != 1 || mesh.set[0] || decodeBody(t, w.Body.Bytes())["satellite_enabled"] != false {
		t.Fatalf("switch off: %d %s %v", w.Code, w.Body.String(), mesh.set)
	}
	for _, body := range []string{`{}`, `{"enabled":"no"}`, `not json`} {
		if w := serve(t, s, "PUT", "/api/mesh/ble/satellite", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if len(mesh.set) != 1 {
		t.Fatalf("a bad body set the switch: %v", mesh.set)
	}

	mesh.statsErr = transport.ErrNoPipeStats
	if w := serve(t, s, "GET", "/api/mesh/ble/satellite/stats", ""); w.Code != http.StatusNotFound {
		t.Fatalf("no STATS: %d", w.Code)
	}
	mesh.statsErr = errors.New("the node did not report its modem's health")
	if w := serve(t, s, "GET", "/api/mesh/ble/satellite/stats", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable: %d", w.Code)
	}
	three := 3
	mesh.statsErr, mesh.stats = nil, &transport.PipeStatsReading{
		PipeStats: transport.PipeStats{Owner: transport.PipeOwnerNode, CSQ: &three, Sessions: 7, DaySessionsCap: 10},
		ReadAt:    time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
	}
	w = serve(t, s, "GET", "/api/mesh/ble/satellite/stats", "")
	got := decodeBody(t, w.Body.Bytes())
	if w.Code != http.StatusOK || got["owner"] != "node" || got["csq"] != 3.0 || got["sessions"] != 7.0 ||
		got["last_mo_status"] != nil || got["read_at"] != "2026-09-29T12:00:00Z" || got["day_sessions_cap"] != 10.0 {
		t.Fatalf("stats %d %v", w.Code, got)
	}
	if _, ok := got["flags"].(map[string]interface{}); !ok {
		t.Fatalf("no flags: %v", got)
	}

	// A mesh transport that carries no satellite modem: 404 on the switch
	// and on the report alike.
	plain := &Server{mesh: &plainBLEMesh{status: transport.BLEStatus{Mode: "ready", Connected: true}}}
	if w := serve(t, plain, "PUT", "/api/mesh/ble/satellite", `{"enabled": true}`); w.Code != http.StatusNotFound {
		t.Fatalf("switch without a satellite modem: %d %s", w.Code, w.Body.String())
	}
	if w := serve(t, plain, "GET", "/api/mesh/ble/satellite/stats", ""); w.Code != http.StatusNotFound {
		t.Fatalf("report without a satellite modem: %d %s", w.Code, w.Body.String())
	}

	off := &Server{mesh: &satBLEMesh{status: transport.BLEStatus{Mode: "off"}}}
	if w := serve(t, off, "PUT", "/api/mesh/ble/satellite", `{"enabled": true}`); w.Code != http.StatusConflict {
		t.Fatalf("switch on a serial node: %d", w.Code)
	}
	if w := serve(t, off, "GET", "/api/mesh/ble/satellite/stats", ""); w.Code != http.StatusConflict {
		t.Fatalf("stats on a serial node: %d", w.Code)
	}
}
