package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"meshsat/internal/timesync"
)

type tsIdentity struct{}

func (tsIdentity) DestHash() [timesync.DestHashLen]byte { return [timesync.DestHashLen]byte{1} }

// GET /api/timesync/peers answers with empty lists before routing is up, and
// with the per-interface schedule once the consensus is wired. [MESHSAT-778]
func TestHandleGetTimeSyncPeers(t *testing.T) {
	tests := []struct {
		name        string
		wire        bool
		wantEnabled bool
		wantIfaces  int
	}{
		{"no routing subsystem", false, false, 0},
		{"consensus wired", true, true, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{}
			if tt.wire {
				tc := timesync.NewMeshTimeConsensus(timesync.NewTimeService(nil), tsIdentity{}, func([]byte) {})
				tc.SetInterfaces(func() []string { return []string{"mesh_0", "tcp_0"} }, func(string, []byte) {})
				tc.SetDiscoveryInterval(15 * time.Minute)
				s.SetTimeConsensus(tc)
			}
			rec := httptest.NewRecorder()
			s.handleGetTimeSyncPeers(rec, httptest.NewRequest(http.MethodGet, "/api/timesync/peers", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			var got TimeSyncPeersResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Enabled != tt.wantEnabled || len(got.Interfaces) != tt.wantIfaces || got.Peers == nil {
				t.Errorf("got %+v", got)
			}
			if tt.wire && (got.DiscoveryIntervalSec != 900 || got.RequestIntervalSec != 30 || got.Interfaces[0].Mode != "discovery") {
				t.Errorf("schedule %+v", got)
			}
		})
	}
}
