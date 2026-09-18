package api

import (
	"net/http"

	"meshsat/internal/timesync"
)

// TimeSyncPeersResponse is the body of GET /api/timesync/peers.
type TimeSyncPeersResponse struct {
	Enabled              bool                      `json:"enabled"`
	RequestIntervalSec   int                       `json:"request_interval_sec"`
	DiscoveryIntervalSec int                       `json:"discovery_interval_sec"`
	Peers                []timesync.PeerState      `json:"peers"`
	Interfaces           []timesync.InterfaceState `json:"interfaces"`
}

// handleGetTimeSyncPeers returns the bridge-to-bridge time sync state.
// @Summary Get time sync peers and per-interface request schedule
// @Description Lists the other bridges that answered this bridge's time sync requests (stratum, clock offset, last round trip, the interface it came in on) and, per free interface, whether requests go out every 30 s because a peer is there ("peer") or only once per discovery period ("discovery"). These are the 0x14/0x15 packets an operator sees on a KISS modem or the mesh. [MESHSAT-778]
// @Tags timesync
// @Produce json
// @Success 200 {object} TimeSyncPeersResponse
// @Router /api/timesync/peers [get]
func (s *Server) handleGetTimeSyncPeers(w http.ResponseWriter, r *http.Request) {
	if s.timeConsensus == nil {
		writeJSON(w, http.StatusOK, TimeSyncPeersResponse{
			Peers:      []timesync.PeerState{},
			Interfaces: []timesync.InterfaceState{},
		})
		return
	}
	writeJSON(w, http.StatusOK, TimeSyncPeersResponse{
		Enabled:              true,
		RequestIntervalSec:   int(timesync.RequestInterval.Seconds()),
		DiscoveryIntervalSec: int(s.timeConsensus.DiscoveryInterval().Seconds()),
		Peers:                s.timeConsensus.Peers(),
		Interfaces:           s.timeConsensus.Interfaces(),
	})
}
